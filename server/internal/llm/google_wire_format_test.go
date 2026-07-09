package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGoogleProviderWireFormatCamelCase locks in the §4.10-8 (v2.80) wire
// contract: every key in the outbound Gemini request body uses canonical
// proto3 lowerCamelCase. Google's own endpoint accepts the snake_case aliases
// too (proto3 JSON parser rule), but relay gateways (one-api/new-api) re-parse
// the body into camelCase-only tagged Go structs before forwarding —
// snake_case keys are silently dropped there. A dropped
// "function_declarations" forwards an empty tools[0] object, which the
// upstream rejects with `tools[0].tool_type: required one_of 'tool_type' must
// have one initialized field`; a dropped "system_instruction" silently loses
// the system prompt; a dropped "inline_data" loses image/PDF input.
func TestGoogleProviderWireFormatCamelCase(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}` + "\n\n"))
	}))
	defer srv.Close()

	p := &GoogleProvider{}
	req := UnifiedChatRequest{
		SystemPrompt: "sys",
		Model:        ModelInfo{RequestID: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"},
		History:      multimodalHistoryWithDocument(),
		Tools:        []ToolDef{{Name: "web_search", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}

	body := string(captured)
	for _, want := range []string{`"systemInstruction"`, `"functionDeclarations"`, `"inlineData"`, `"mimeType"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing canonical key %s\nbody: %s", want, body)
		}
	}
	for _, banned := range []string{`"system_instruction"`, `"function_declarations"`, `"inline_data"`, `"mime_type"`} {
		if strings.Contains(body, banned) {
			t.Errorf("request body carries snake_case key %s — relays drop it (→ empty tools[0], tool_type proto error)", banned)
		}
	}
	if !strings.Contains(body, `"aW1n"`) {
		t.Fatalf("image inlineData should still be sent\nbody: %s", body)
	}
	assertNoNativeDocumentPayload(t, captured)

	// tools[0] must never reach the wire as an empty object.
	var parsed struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(captured, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Tools) != 1 || len(parsed.Tools[0]["functionDeclarations"]) == 0 {
		t.Fatalf("tools[0] must carry functionDeclarations, got: %s", body)
	}
}

// TestGoogleProviderNoToolsOmitsToolsKey guards the other trigger of the same
// upstream error: when no tools are enabled the request must omit "tools"
// entirely rather than send an empty array/object.
func TestGoogleProviderNoToolsOmitsToolsKey(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}` + "\n\n"))
	}))
	defer srv.Close()

	p := &GoogleProvider{}
	req := UnifiedChatRequest{
		SystemPrompt: "sys",
		Model:        ModelInfo{RequestID: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"},
		History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hi"}}}},
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(captured, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, has := parsed["tools"]; has {
		t.Fatalf("tool-less request must omit the tools key, got: %s", string(captured))
	}
}
