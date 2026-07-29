package llm

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"aivory/server/internal/store"
)

type channelFallbackUsageTools struct{}

func (channelFallbackUsageTools) List(string) []ToolDef { return nil }

func (channelFallbackUsageTools) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "", nil, nil
}

func TestOrchestratorFallbackSuccessLogsPrimaryErrorAndFallbackUsage(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"error":{"message":"primary chat failure"}}`+"\n\n")
	}))
	t.Cleanup(primary.Close)

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"fallback answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	t.Cleanup(fallback.Close)

	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "orchestrator-channel-fallback.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_groups(id,name,is_default) VALUES('ug_free','Free',1)`); err != nil {
		t.Fatalf("insert default group: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','fallback@example.test','hash','user')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	primaryChannel, err := store.CreateChannel(ctx, db, "Primary", "openai", "chat", primary.URL, "primary-key")
	if err != nil {
		t.Fatalf("create primary channel: %v", err)
	}
	fallbackChannel, err := store.CreateChannel(ctx, db, "Fallback", "openai", "chat", fallback.URL, "fallback-key")
	if err != nil {
		t.Fatalf("create fallback channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: primaryChannel.ID, FallbackChannelID: fallbackChannel.ID,
		Kind: "chat", RequestID: "fallback-chat-model", Label: "Fallback chat model",
		Enabled: true, Stream: true, ToolMode: "none",
		Currency: "USD",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := store.SetModelQuotas(ctx, db, model.ID, []store.ModelGroupQuota{{
		GroupID: store.DefaultGroupID, LimitType: "count", LimitValue: 0,
	}}); err != nil {
		t.Fatalf("grant model quota: %v", err)
	}
	conversation, err := store.CreateConversation(ctx, db, store.Conversation{
		ID: "c1", UserID: "u1", Title: "Fallback", ModelID: model.ID,
	})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	logger := log.New(io.Discard, "", 0)
	orchestrator := NewOrchestrator(db, NewRegistry(logger), channelFallbackUsageTools{}, nil, nil, nil, nil, nil, logger)
	events := []SseEvent{}
	result, err := orchestrator.Run(ctx, RunRequest{
		UserID: "u1", ConversationID: conversation.ID, ModelID: model.ID,
		UserText: "hello", ToolMode: ToolModeDisabled,
	}, func(event SseEvent) { events = append(events, event) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.AssistantMessage == nil {
		t.Fatalf("incomplete result: %+v", result)
	}

	rows, err := db.Query(`
		SELECT status, error, channel_id, fallback, input_tokens, output_tokens, cost, credits,
		       COALESCE(message_id,''), request_body
		FROM usage_logs WHERE user_id=? AND model_id=? AND purpose='chat' ORDER BY id`, "u1", model.ID)
	if err != nil {
		t.Fatalf("query usage rows: %v", err)
	}
	defer rows.Close()
	type usageRow struct {
		status, errorText, channelID, messageID, requestBody string
		fallback, inputTokens, outputTokens                  int
		cost, credits                                        float64
	}
	got := []usageRow{}
	for rows.Next() {
		var row usageRow
		if err := rows.Scan(&row.status, &row.errorText, &row.channelID, &row.fallback,
			&row.inputTokens, &row.outputTokens, &row.cost, &row.credits, &row.messageID, &row.requestBody); err != nil {
			t.Fatalf("scan usage row: %v", err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate usage rows: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("usage rows = %d, want primary error + fallback success: %+v; hits=%d/%d assistant=%+v events=%+v",
			len(got), got, primaryHits.Load(), fallbackHits.Load(), result.AssistantMessage, events)
	}
	if got[0].status != "error" || !strings.Contains(got[0].errorText, "primary chat failure") ||
		got[0].channelID != primaryChannel.ID || got[0].fallback != 0 {
		t.Fatalf("primary failure row = %+v", got[0])
	}
	if got[0].inputTokens != 0 || got[0].outputTokens != 0 || got[0].cost != 0 || got[0].credits != 0 {
		t.Fatalf("primary failure must be free: %+v", got[0])
	}
	if got[0].requestBody == "" {
		t.Fatal("primary failure must retain its sanitized request body")
	}
	if got[1].status != "ok" || got[1].errorText != "" || got[1].channelID != fallbackChannel.ID || got[1].fallback != 1 {
		t.Fatalf("fallback success row = %+v", got[1])
	}
	if got[1].inputTokens != 7 || got[1].outputTokens != 3 || got[1].cost != 0 || got[1].credits != 0 {
		t.Fatalf("fallback billing row = %+v", got[1])
	}
	if got[0].messageID == "" || got[0].messageID != got[1].messageID || got[0].messageID != result.AssistantMessage.ID {
		t.Fatalf("attempt message ids = %q/%q, assistant=%q", got[0].messageID, got[1].messageID, result.AssistantMessage.ID)
	}
}
