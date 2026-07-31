package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type generationInterruptedProvider struct{}

func (*generationInterruptedProvider) ID() string { return "openai" }

func (*generationInterruptedProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	onEvent func(SseEvent),
) (*UnifiedResult, error) {
	onEvent(SseEvent{Type: "text_delta", Text: "partial answer"})
	onEvent(SseEvent{Type: "tool_start", Name: "lookup", ID: "tool_1"})
	return &UnifiedResult{
		Blocks: []UnifiedBlock{
			{Kind: "text", Text: "partial answer"},
			{Kind: "tool_call", ToolName: "lookup", ToolID: "tool_1", Input: json.RawMessage(`{"q":"docs"}`)},
		},
		Raw:        json.RawMessage(`[{"role":"assistant","content":"partial answer"}]`),
		StopReason: "error",
		Usage:      Usage{InputTokens: 11, OutputTokens: 4, CacheReadTokens: 2},
		Citations: []Citation{{
			ID: "cite_1", Title: "Documentation", URL: "https://example.test/docs", Source: "web",
		}},
	}, errors.New("upstream stream interrupted")
}

type generationInterruptedTools struct{}

func (generationInterruptedTools) List(string) []ToolDef { return nil }

func (generationInterruptedTools) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "", nil, nil
}

func TestOrchestratorPersistsGenerationInterruptionAndErrorCode(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "generation-interrupted.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','interrupted@example.test','hash','admin')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Interrupted", "openai", "chat", "https://example.invalid", "test-key")
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID, Kind: "chat", RequestID: "interrupted-model", Label: "Interrupted model",
		Enabled: true, Stream: true, ToolMode: "none", Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "Interrupted", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	logger := log.New(io.Discard, "", 0)
	registry := NewRegistry(logger)
	registry.Register(&generationInterruptedProvider{})
	orchestrator := NewOrchestrator(db, registry, generationInterruptedTools{}, nil, nil, nil, nil, nil, logger)
	var events []SseEvent
	_, runErr := orchestrator.Run(ctx, RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "hello", ToolMode: ToolModeDisabled,
	}, func(event SseEvent) { events = append(events, event) })
	if runErr == nil || !strings.Contains(runErr.Error(), "upstream stream interrupted") {
		t.Fatalf("Run error = %v, want upstream interruption", runErr)
	}

	var (
		messageID, blocksJSON, citationsJSON, rawJSON string
		stopReason, status, errorText                 string
		inputTokens, outputTokens, cacheReadTokens    int
	)
	if err := db.QueryRow(`
		SELECT id, blocks, citations, COALESCE(raw,''), stop_reason, status, error,
		       input_tokens, output_tokens, cache_read_tokens
		FROM messages WHERE conversation_id=? AND role='assistant' LIMIT 1`, conversation.ID).Scan(
		&messageID, &blocksJSON, &citationsJSON, &rawJSON, &stopReason, &status, &errorText,
		&inputTokens, &outputTokens, &cacheReadTokens,
	); err != nil {
		t.Fatalf("query interrupted assistant: %v", err)
	}
	if stopReason != "generation_interrupted" || status != "error" {
		t.Fatalf("persisted terminal state = %q/%q", stopReason, status)
	}
	if errorText != "The model provider returned an error. Please try again in a moment." {
		t.Fatalf("persisted safe error = %q", errorText)
	}
	if inputTokens != 11 || outputTokens != 4 || cacheReadTokens != 2 {
		t.Fatalf("persisted usage = %d/%d/%d", inputTokens, outputTokens, cacheReadTokens)
	}
	if rawJSON == "" {
		t.Fatal("provider-native partial result was not preserved")
	}
	var blocks []UnifiedBlock
	if err := json.Unmarshal([]byte(blocksJSON), &blocks); err != nil {
		t.Fatalf("decode blocks: %v", err)
	}
	assertPartialBlock(t, blocks, "text", "", "partial answer")
	assertPartialBlock(t, blocks, "tool_call", "lookup", "")
	var citations []Citation
	if err := json.Unmarshal([]byte(citationsJSON), &citations); err != nil {
		t.Fatalf("decode citations: %v", err)
	}
	if len(citations) != 1 || citations[0].ID != "cite_1" || citations[0].Index != 1 {
		t.Fatalf("persisted citations = %+v", citations)
	}

	var errorEvent *SseEvent
	for i := range events {
		if events[i].Type == "error" {
			errorEvent = &events[i]
		}
	}
	if errorEvent == nil || errorEvent.Code != "generation_interrupted" || errorEvent.MessageID != messageID {
		t.Fatalf("error event = %+v; all events = %+v", errorEvent, events)
	}
	wire, err := json.Marshal(errorEvent)
	if err != nil {
		t.Fatalf("marshal error event: %v", err)
	}
	if !strings.Contains(string(wire), `"code":"generation_interrupted"`) {
		t.Fatalf("error event wire JSON = %s", wire)
	}
}
