package llm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
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

func privateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "private.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('private-user','private@example.test','hash','admin')`); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]bool{"log_full_requests": true, "log_request_bodies": true, "log_errors_only": false} {
		if err := store.SetSetting(db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func assertPrivateStorage(t *testing.T, db *sql.DB, wantRows int) []store.AdminUsageRecord {
	t.Helper()
	for _, table := range []string{"conversations", "messages", "files", "documents", "artifacts"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("private data table %s: count=%d error=%v", table, count, err)
		}
	}
	rows, err := store.AdminUsageRecords(context.Background(), db, store.UsageFilter{}, 50, 0)
	if err != nil || len(rows) != wantRows {
		t.Fatalf("usage rows=%d error=%v, want %d", len(rows), err, wantRows)
	}
	for _, row := range rows {
		if row.ConversationID != "" || row.ConversationTitle != "匿名对话" || row.ConversationDeleted || row.RequestBody != "" || row.RequestHeaders != "" || row.RequestURL != "" || row.RequestMethod != "" {
			t.Fatalf("private usage retained conversation or diagnostics: %+v", row)
		}
	}
	return rows
}

func TestPrivateProvidersUseNativeInlineFormatsAndNeverPersist(t *testing.T) {
	tests := []struct {
		provider, format, path, reply, imageKey string
	}{
		{"openai", "chat", "/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"private reply\"}}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n", "image_url"},
		{"openai", "responses", "/v1/responses", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"private reply\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":7},\"output\":[]}}\n\n", "input_image"},
		{"anthropic", "", "/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"private reply\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "base64"},
		{"google", "", "/v1beta/models/private-model:streamGenerateContent", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"private reply\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":7}}\n\n", "inlineData"},
	}
	for _, test := range tests {
		t.Run(test.provider+"/"+test.format, func(t *testing.T) {
			db := privateTestDB(t)
			var requests atomic.Int32
			image := base64.StdEncoding.EncodeToString(testPNGBytes(8))
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != test.path {
					t.Errorf("path=%q want %q", r.URL.Path, test.path)
				}
				body, _ := io.ReadAll(r.Body)
				var payload map[string]any
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Error(err)
				}
				for _, key := range []string{"tools", "tool_choice", "mcp_servers", "container", "context_management", "cachedContent", "previous_response_id", "conversation", "background", "metadata", "user", "prompt_cache_key"} {
					if _, exists := payload[key]; exists {
						t.Errorf("private request contains %s", key)
					}
				}
				if test.provider == "openai" && payload["store"] != false {
					t.Error("OpenAI request must set store=false")
				}
				if strings.Contains(string(body), "cache_control") || strings.Contains(string(body), "private-user") {
					t.Error("private request retains explicit caching or user identifiers")
				}
				for _, expected := range []string{"private prompt", test.imageKey, image} {
					if !strings.Contains(string(body), expected) {
						t.Errorf("request missing native field/content %q", expected)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.reply)
			}))
			defer upstream.Close()
			channel, err := store.CreateChannel(context.Background(), db, "Private", test.provider, test.format, upstream.URL, "key")
			if err != nil {
				t.Fatal(err)
			}
			model, err := store.CreateModel(context.Background(), db, store.Model{ChannelID: channel.ID, Kind: "chat", RequestID: "private-model", Label: "Private model", Enabled: true, Vision: true, Stream: true, SystemPrompt: "Be helpful.", PriceInput: 1, PriceOutput: 2, Currency: "USD", ExtraParams: json.RawMessage(`{"store":true,"metadata":{"secret":"secret metadata"},"user":"private-user","prompt_cache_key":"private-user","cachedContent":"remote cache","container":"remote container","mcp_servers":[{"url":"https://tool.example"}],"tools":[{"type":"web_search"}],"context_management":{"edits":[]}}`)})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := log.New(&logs, "", 0)
			orchestrator := &Orchestrator{db: db, reg: NewRegistry(logger), logger: logger}
			var events []SseEvent
			err = orchestrator.RunPrivate(context.Background(), "private-user", model, []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "private prompt"}, {Kind: "image", Data: image, MimeType: "image/png"}}}}, func(event SseEvent) { events = append(events, event) })
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 || len(events) == 0 || events[len(events)-1].Type != "done" {
				t.Fatalf("requests=%d events=%+v", requests.Load(), events)
			}
			rows := assertPrivateStorage(t, db, 1)
			if rows[0].InputTokens != 11 || rows[0].OutputTokens != 7 || rows[0].Purpose != "chat" || rows[0].Cost <= 0 {
				t.Fatalf("missing private usage: %+v", rows[0])
			}
			if logs.Len() != 0 {
				t.Fatalf("private provider logged diagnostics: %s", logs.String())
			}
		})
	}
}

type privateStubProvider struct {
	requests []UnifiedChatRequest
	fail     bool
	block    bool
	cancel   bool
}

func (provider *privateStubProvider) ID() string { return "openai" }

func (provider *privateStubProvider) Stream(ctx context.Context, request UnifiedChatRequest, _ ToolRunner, emit func(SseEvent)) (*UnifiedResult, error) {
	provider.requests = append(provider.requests, request)
	if provider.cancel {
		emit(SseEvent{Type: "text_delta", Text: "partial secret"})
		<-ctx.Done()
		return &UnifiedResult{Usage: Usage{InputTokens: 11, OutputTokens: 3}}, ctx.Err()
	}
	if provider.fail {
		return nil, errors.New("upstream echoed private prompt and image secret")
	}
	text := "private reply"
	if request.Model.RequestID == "moderation-model" {
		text = "ALLOW"
		if provider.block {
			text = "BLOCK"
		}
	}
	emit(SseEvent{Type: "text_delta", Text: text})
	return &UnifiedResult{Blocks: []UnifiedBlock{{Kind: "text", Text: text}}, Usage: Usage{InputTokens: 11, OutputTokens: 7}}, nil
}

func TestPrivateModerationAndErrorsNeverInvokeRoutingOrPersistContent(t *testing.T) {
	for _, mode := range []string{"allow", "block", "error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			db := privateTestDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			channel, err := store.CreateChannel(ctx, db, "Private", "openai", "chat", "https://example.test", "key")
			if err != nil {
				t.Fatal(err)
			}
			model, err := store.CreateModel(ctx, db, store.Model{ChannelID: channel.ID, Kind: "chat", RequestID: "chat-model", Enabled: true, Stream: true, ModerationEnabled: mode == "allow" || mode == "block", ModerationMode: "model", Currency: "USD"})
			if err != nil {
				t.Fatal(err)
			}
			moderator, err := store.CreateModel(ctx, db, store.Model{ChannelID: channel.ID, Kind: "chat", RequestID: "moderation-model", Enabled: true, Currency: "USD"})
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"task_model_id", "moderation_model_id", "title_model_id", "router_model_id", "verify_model_id", "summary_model_id"} {
				if err := store.SetSetting(db, key, moderator.ID); err != nil {
					t.Fatal(err)
				}
			}
			provider := &privateStubProvider{block: mode == "block", fail: mode == "error", cancel: mode == "cancel"}
			registry := NewRegistry(nil)
			registry.Register(provider)
			orchestrator := &Orchestrator{db: db, reg: registry, task: NewTaskLLM(db, registry, nil)}
			err = orchestrator.RunPrivate(ctx, "private-user", model, []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "private prompt"}}}}, func(SseEvent) {
				if mode == "cancel" {
					cancel()
				}
			})
			if mode == "allow" && err != nil || mode != "allow" && err == nil {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("upstream error leaked")
			}
			wantRequests, wantLogs := 1, 1
			if mode == "allow" {
				wantRequests, wantLogs = 2, 2
			} else if mode == "block" {
				wantLogs = 2
			}
			if len(provider.requests) != wantRequests {
				t.Fatalf("provider requests=%d want %d", len(provider.requests), wantRequests)
			}
			for _, request := range provider.requests {
				if !request.Private || request.UserID != "" || request.ConversationID != "" || request.MessageID != "" || len(request.Tools) != 0 || len(request.OfficialToolRequests) != 0 || request.ToolModePrompt || request.Model.Fallback != nil {
					t.Fatalf("unsafe private provider request: %+v", request)
				}
			}
			rows := assertPrivateStorage(t, db, wantLogs)
			for _, row := range rows {
				if strings.Contains(row.Error, "secret") || row.Purpose != "chat" && row.Purpose != "task.moderation" {
					t.Fatalf("unsafe usage row: %+v", row)
				}
			}
			if mode == "cancel" && (rows[0].InputTokens != 11 || rows[0].OutputTokens != 3 || rows[0].Status != "error") {
				t.Fatalf("canceled use not recorded: %+v", rows[0])
			}
		})
	}
}

func TestPrivateChatUsesNormalCreditAdmissionAndSettlement(t *testing.T) {
	db := privateTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE users SET role='user' WHERE id='private-user'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "credits_per_usd", 100.0); err != nil {
		t.Fatal(err)
	}
	channel, err := store.CreateChannel(ctx, db, "Paid", "openai", "chat", "https://example.test", "key")
	if err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{ChannelID: channel.ID, Kind: "chat", RequestID: "paid-model", Enabled: true, PriceInput: 1, PriceOutput: 2, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &privateStubProvider{}
	registry := NewRegistry(nil)
	registry.Register(provider)
	orchestrator := &Orchestrator{db: db, reg: registry}
	history := []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "private paid prompt"}}}}
	if err := store.SetPermanentCredits(ctx, db, "private-user", 0); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RunPrivate(ctx, "private-user", model, history, func(SseEvent) {}); err == nil || len(provider.requests) != 0 {
		t.Fatalf("unfunded request reached provider: err=%v requests=%d", err, len(provider.requests))
	}
	if err := store.SetPermanentCredits(ctx, db, "private-user", 100); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.RunPrivate(ctx, "private-user", model, history, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	rows := assertPrivateStorage(t, db, 1)
	var credits float64
	if err := db.QueryRow(`SELECT credits FROM usage_logs WHERE id=?`, rows[0].ID).Scan(&credits); err != nil {
		t.Fatal(err)
	}
	if credits <= 0 || rows[0].Cost <= 0 {
		t.Fatalf("private call bypassed billing: %+v", rows[0])
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credit_reservations WHERE status='reserved'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("unsettled reservation: pending=%d error=%v", pending, err)
	}
}

type privateToolTrap struct{ calls int }

func (trap *privateToolTrap) Run(context.Context, string, []byte) (string, []Citation, error) {
	trap.calls++
	return "must not execute", nil, nil
}

func TestPrivateProvidersNeverExecuteUnsolicitedTools(t *testing.T) {
	for _, test := range []struct {
		provider, format, reply string
	}{
		{"openai", "chat", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"leak\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"},
		{"openai", "responses", "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fn_1\",\"type\":\"function_call\",\"call_id\":\"fn_1\",\"name\":\"leak\",\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fn_1\",\"arguments\":\"{}\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"fn_1\",\"type\":\"function_call\",\"call_id\":\"fn_1\",\"name\":\"leak\",\"arguments\":\"{}\"}]}}\n\n"},
		{"anthropic", "", "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"leak\",\"input\":{}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"google", "", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"leak\",\"args\":{}}}]},\"finishReason\":\"STOP\"}]}\n\n"},
	} {
		t.Run(test.provider+"/"+test.format, func(t *testing.T) {
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.reply)
			}))
			defer upstream.Close()
			provider, err := NewRegistry(nil).Get(test.provider)
			if err != nil {
				t.Fatal(err)
			}
			trap := &privateToolTrap{}
			_, err = provider.Stream(context.Background(), UnifiedChatRequest{Private: true, Model: ModelInfo{RequestID: "private-model", Provider: test.provider, BaseURL: upstream.URL, APIKey: "key", APIFormat: test.format}, History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}}}, trap, func(SseEvent) {})
			if !errors.Is(err, ErrPrivateTools) || trap.calls != 0 || requests.Load() != 1 {
				t.Fatalf("err=%v tools=%d requests=%d", err, trap.calls, requests.Load())
			}
		})
	}
}
