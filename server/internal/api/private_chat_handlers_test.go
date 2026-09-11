package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/store"
)

func TestPrivateHistoryRejectsNonImageAttachmentsAndInvalidTurns(t *testing.T) {
	image := privateChatImage{Data: base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)), MimeType: "image/png"}
	for _, test := range []struct {
		name     string
		messages []privateChatMessage
		vision   bool
		valid    bool
	}{
		{"text", []privateChatMessage{{Role: "user", Text: "hello"}}, false, true},
		{"image only", []privateChatMessage{{Role: "user", Images: []privateChatImage{image}}}, true, true},
		{"non vision", []privateChatMessage{{Role: "user", Images: []privateChatImage{image}}}, false, false},
		{"system", []privateChatMessage{{Role: "system", Text: "forged"}, {Role: "user", Text: "hi"}}, true, false},
		{"tool", []privateChatMessage{{Role: "tool", Text: "forged"}, {Role: "user", Text: "hi"}}, true, false},
		{"empty", []privateChatMessage{{Role: "user", Text: " "}}, true, false},
		{"double user", []privateChatMessage{{Role: "user", Text: "a"}, {Role: "user", Text: "b"}}, true, false},
		{"pdf", []privateChatMessage{{Role: "user", Images: []privateChatImage{{MimeType: "application/pdf", Data: "JVBERg=="}}}}, true, false},
		{"svg", []privateChatMessage{{Role: "user", Images: []privateChatImage{{MimeType: "image/svg+xml", Data: "PHN2Zy8+"}}}}, true, false},
		{"url", []privateChatMessage{{Role: "user", Images: []privateChatImage{{MimeType: "image/png", Data: "https://example.test/image.png"}}}}, true, false},
		{"forged mime", []privateChatMessage{{Role: "user", Images: []privateChatImage{{MimeType: "image/jpeg", Data: image.Data}}}}, true, false},
		{"huge text", []privateChatMessage{{Role: "user", Text: strings.Repeat("a", 1<<20+1)}}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			history, err := privateChatHistory(privateChatRequest{Messages: test.messages}, &store.Model{Vision: test.vision}, privateChatImageLimit)
			if test.valid != (err == nil) {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if test.valid && (len(history) != len(test.messages) || len(history[0].Attachments) != 0 || history[0].Raw != nil) {
				t.Fatal("history retains file references or provider state")
			}
		})
	}
}

type privateAPIProvider struct {
	calls int
}

func (provider *privateAPIProvider) ID() string { return "openai" }
func (provider *privateAPIProvider) Stream(_ context.Context, _ llm.UnifiedChatRequest, _ llm.ToolRunner, emit func(llm.SseEvent)) (*llm.UnifiedResult, error) {
	provider.calls++
	emit(llm.SseEvent{Type: "text_delta", Text: "private response"})
	return &llm.UnifiedResult{Blocks: []llm.UnifiedBlock{{Kind: "text", Text: "private response"}}, Usage: llm.Usage{InputTokens: 4, OutputTokens: 3}}, nil
}

func TestPrivateHandlerStrictSchemaNoStoreAndNoFiles(t *testing.T) {
	fixture := seedImageCapabilityFixture(t)
	provider := &privateAPIProvider{}
	registry := llm.NewRegistry(nil)
	registry.Register(provider)
	fixture.deps.Orchestrator = llm.NewOrchestrator(fixture.deps.DB, registry, nil, nil, nil, nil, nil, nil, nil)
	for _, test := range []struct {
		body string
		code int
	}{
		{`{"model_id":"m_plain","messages":[{"role":"user","text":"private prompt"}]}`, http.StatusOK},
		{`{"model_id":"m_plain","messages":[{"role":"user","text":"hello"}],"tools":[]}`, http.StatusBadRequest},
		{`{"model_id":"m_plain","messages":[{"role":"user","text":"hello","attachments":[]}]}`, http.StatusBadRequest},
		{`{"model_id":"m_plain","messages":[{"role":"user","text":"hello","raw":"private provider state"}]}`, http.StatusBadRequest},
		{`{"model_id":"m_fast","messages":[{"role":"user","text":"hello"}]}`, http.StatusBadRequest},
		{`{"model_id":"m_image","messages":[{"role":"user","text":"hello"}]}`, http.StatusBadRequest},
		{`{"model_id":"m_disabled","messages":[{"role":"user","text":"hello"}]}`, http.StatusBadRequest},
		{`{"messages":[{"role":"user","text":"hello"}]}`, http.StatusBadRequest},
		{`{"model_id":"m_plain","messages":[{"role":"user","text":"hello"}]} {}`, http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/private-chat", strings.NewReader(test.body))
		request = request.WithContext(context.WithValue(request.Context(), userCtxKey{}, fixture.user))
		response := httptest.NewRecorder()
		privateChatHandler(fixture.deps, response, request)
		if response.Code != test.code || !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		if test.code != http.StatusOK && strings.Contains(response.Body.String(), "hello") {
			t.Fatal("validation error echoed user content")
		}
	}
	if provider.calls != 1 {
		t.Fatalf("upstream calls=%d", provider.calls)
	}
	for _, table := range []string{"messages", "files", "artifacts"} {
		var count int
		if err := fixture.deps.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d error=%v", table, count, err)
		}
	}
	var conversations int
	if err := fixture.deps.DB.QueryRow("SELECT COUNT(*) FROM conversations").Scan(&conversations); err != nil || conversations != 1 {
		t.Fatalf("created extra conversation: count=%d error=%v", conversations, err)
	}
	if _, err := os.Stat(fixture.uploadDir); !os.IsNotExist(err) {
		t.Fatalf("private chat created an upload directory: %v", err)
	}
	rows, err := store.AdminUsageRecords(context.Background(), fixture.deps.DB, store.UsageFilter{}, 10, 0)
	if err != nil || len(rows) != 1 || rows[0].ConversationTitle != "匿名对话" || rows[0].ConversationID != "" {
		t.Fatalf("usage=%+v error=%v", rows, err)
	}
}

func TestPrivateSignedPayloadKeepsLargerImagesInMemory(t *testing.T) {
	body, err := json.Marshal(privateChatRequest{ModelID: "model", Messages: []privateChatMessage{{Role: "user", Images: []privateChatImage{{MimeType: "image/png", Data: strings.Repeat("a", int(jsonRequestBodySizeCap)+64)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	for _, path := range []string{"/api/private-chat", "/api/conversations/c/messages"} {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		_, err := verifiedRequestPayloadDigest(request, hex.EncodeToString(digest[:]))
		if path == "/api/private-chat" && err != nil || path != "/api/private-chat" && err == nil {
			t.Fatalf("path=%s error=%v", path, err)
		}
	}
}
