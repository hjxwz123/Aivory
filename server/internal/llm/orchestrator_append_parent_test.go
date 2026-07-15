package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type appendParentCaptureProvider struct{}

func (*appendParentCaptureProvider) ID() string { return "openai" }

func (*appendParentCaptureProvider) Stream(
	_ context.Context,
	_ UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "ok"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

type appendParentTestTools struct{}

func (appendParentTestTools) List(string) []ToolDef { return nil }

func (appendParentTestTools) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "", nil, nil
}

func TestFastModeRecoversDanglingActiveLeafBeforeSavingUserMessage(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "fast-append-parent.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','admin')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Test", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "fast-parent-test",
		Label:     "Fast parent test",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "native",
	})
	if err != nil {
		t.Fatalf("model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, model.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	conv, err := store.CreateConversation(ctx, db, store.Conversation{ID: "c1", UserID: "u1", Title: "Parent recovery"})
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	blocks := func(text string) json.RawMessage {
		b, _ := json.Marshal([]UnifiedBlock{{Kind: "text", Text: text}})
		return b
	}
	if _, err := store.CreateMessage(ctx, db, store.Message{
		ID: "first-branch-user", ConversationID: conv.ID, Role: "user", Blocks: blocks("original question"), CreatedAt: 10,
	}); err != nil {
		t.Fatalf("first branch user: %v", err)
	}
	if _, err := store.CreateMessage(ctx, db, store.Message{
		ID: "first-branch-assistant", ConversationID: conv.ID, ParentID: "first-branch-user", Role: "assistant", Blocks: blocks("stopped answer"), CreatedAt: 20,
	}); err != nil {
		t.Fatalf("first branch assistant: %v", err)
	}
	if _, err := store.CreateMessage(ctx, db, store.Message{
		ID: "edited-branch-user", ConversationID: conv.ID, Role: "user", Blocks: blocks("edited question"), CreatedAt: 30,
	}); err != nil {
		t.Fatalf("edited branch user: %v", err)
	}
	if _, err := store.CreateMessage(ctx, db, store.Message{
		ID: "edited-branch-assistant", ConversationID: conv.ID, ParentID: "edited-branch-user", Role: "assistant", Blocks: blocks("second branch answer"), CreatedAt: 40,
	}); err != nil {
		t.Fatalf("edited branch assistant: %v", err)
	}
	// Reproduce edit-resend before reconciliation: the branch picker persisted the
	// optimistic user id even though the server assigned that turn a msg_* id.
	if _, err := db.Exec(`UPDATE conversations SET active_leaf_id='m_optimistic-edited-user' WHERE id=?`, conv.ID); err != nil {
		t.Fatalf("corrupt active leaf: %v", err)
	}

	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	registry := NewRegistry(logger)
	registry.Register(&appendParentCaptureProvider{})
	orchestrator := NewOrchestrator(db, registry, appendParentTestTools{}, nil, nil, nil, nil, nil, logger)
	result, err := orchestrator.Run(ctx, RunRequest{
		UserID:         "u1",
		ConversationID: conv.ID,
		UserText:       "analyze the uploaded file",
		Fast:           true,
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result == nil || result.UserMessage == nil || result.AssistantMessage == nil {
		t.Fatalf("incomplete run result: %+v", result)
	}
	if got := result.UserMessage.ParentID; got != "edited-branch-assistant" {
		t.Fatalf("recovered user parent = %q, want edited-branch-assistant", got)
	}
	if !result.UserMessage.Fast || !result.AssistantMessage.Fast {
		t.Fatalf("recovered turn lost fast marker: user=%v assistant=%v", result.UserMessage.Fast, result.AssistantMessage.Fast)
	}
	if !strings.Contains(logs.String(), `recovered invalid active leaf`) ||
		!strings.Contains(logs.String(), `active_leaf="m_optimistic-edited-user"`) {
		t.Fatalf("recovery was not logged with the stale leaf id: %s", logs.String())
	}
	updated, err := store.GetConversation(ctx, db, conv.ID, "u1")
	if err != nil {
		t.Fatalf("reload conversation: %v", err)
	}
	if updated.ActiveLeafID != result.AssistantMessage.ID {
		t.Fatalf("active leaf = %q, want new assistant %q", updated.ActiveLeafID, result.AssistantMessage.ID)
	}
}
