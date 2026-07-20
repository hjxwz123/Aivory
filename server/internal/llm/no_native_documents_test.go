package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func multimodalHistoryWithDocument() []UnifiedMessage {
	return []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{
			{Kind: "image", Data: "aW1n", MimeType: "image/png"},
			{Kind: "document", Data: "cGRm", MimeType: "application/pdf", Title: "scan.pdf"},
			{Kind: "text", Text: "Use the uploaded file."},
		}},
	}
}

func assertNoNativeDocumentPayload(t *testing.T, body []byte) {
	t.Helper()
	s := string(body)
	for _, banned := range []string{
		`"input_file"`,
		`"file_data"`,
		`"type":"file"`,
		`"type":"document"`,
		`"application/pdf"`,
		`"scan.pdf"`,
		`"cGRm"`,
	} {
		if strings.Contains(s, banned) {
			t.Fatalf("provider request leaked native document payload %s\nbody: %s", banned, s)
		}
	}
}

func TestOpenAIChatOmitsNativeDocumentBlocks(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"))
	}))
	defer srv.Close()

	p := &OpenAIProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", Vision: true},
		History: multimodalHistoryWithDocument(),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured), `"image_url"`) {
		t.Fatalf("image payload should still be sent\nbody: %s", string(captured))
	}
	assertNoNativeDocumentPayload(t, captured)
}

func TestOpenAIResponsesOmitsNativeDocumentBlocks(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n"))
	}))
	defer srv.Close()

	p := &OpenAIProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses", Vision: true},
		History: multimodalHistoryWithDocument(),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured), `"input_image"`) {
		t.Fatalf("image payload should still be sent\nbody: %s", string(captured))
	}
	assertNoNativeDocumentPayload(t, captured)
}

func TestAnthropicOmitsNativeDocumentBlocks(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`data: {"type":"message_stop"}`,
		}, "\n\n") + "\n\n"))
	}))
	defer srv.Close()

	p := &AnthropicProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "claude-test", BaseURL: srv.URL, APIKey: "k", Vision: true},
		History: multimodalHistoryWithDocument(),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured), `"type":"image"`) {
		t.Fatalf("image payload should still be sent\nbody: %s", string(captured))
	}
	assertNoNativeDocumentPayload(t, captured)
}
