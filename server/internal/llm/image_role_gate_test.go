package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// historyWithAssistantOwnedImage models a conversation whose ASSISTANT turn owns
// an image block. In normal chat only the user turn carries attachments, but a
// share-import / fork / branch copies attachments verbatim onto assistant rows
// (share_handlers.go, conversations_handlers.go), and resolveAttachments used to
// inline them regardless of role. The assistant turn has no same-vendor Raw, so
// each serializer falls through to the block path — exactly the state that used
// to emit an image content part on a non-user role.
func historyWithAssistantOwnedImage() []UnifiedMessage {
	return []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "describe it"}}},
		{Role: "assistant", Blocks: []UnifiedBlock{
			{Kind: "image", Data: "aW1n", MimeType: "image/png"},
			{Kind: "text", Text: "here you go"},
		}},
		{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "and again?"}}},
	}
}

// assertNoImagePartOnAnyRole fails if the outbound request body carries any
// provider image content-part marker. With the only image sitting on the
// assistant turn, a correctly role-gated serializer emits none of these — the
// base64 payload "aW1n" must not reach the wire at all.
func assertNoImagePartOnAnyRole(t *testing.T, body []byte) {
	t.Helper()
	s := string(body)
	for _, banned := range []string{
		`"image_url"`,    // OpenAI chat
		`"input_image"`,  // OpenAI responses
		`"type":"image"`, // Anthropic
		`"inlineData"`,   // Gemini
		`"aW1n"`,         // the raw base64 image payload
	} {
		if strings.Contains(s, banned) {
			t.Fatalf("assistant-owned image leaked onto a non-user role via %s\nbody: %s", banned, s)
		}
	}
}

func TestOpenAIChatDropsAssistantOwnedImage(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"))
	}))
	defer srv.Close()

	p := &OpenAIProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k"},
		History: historyWithAssistantOwnedImage(),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	assertNoImagePartOnAnyRole(t, captured)
}

func TestOpenAIResponsesDropsAssistantOwnedImage(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n"))
	}))
	defer srv.Close()

	p := &OpenAIProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses"},
		History: historyWithAssistantOwnedImage(),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	assertNoImagePartOnAnyRole(t, captured)
}

func TestAnthropicDropsAssistantOwnedImage(t *testing.T) {
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
		Model:   ModelInfo{RequestID: "claude-test", BaseURL: srv.URL, APIKey: "k"},
		History: historyWithAssistantOwnedImage(),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	assertNoImagePartOnAnyRole(t, captured)
}

func TestGeminiDropsAssistantOwnedImage(t *testing.T) {
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
		History:      historyWithAssistantOwnedImage(),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	assertNoImagePartOnAnyRole(t, captured)

	// The prior USER turn's own images must still be sent — the gate is by role,
	// not a blanket image ban.
	var withUserImg []UnifiedMessage
	withUserImg = append(withUserImg, UnifiedMessage{Role: "user", Blocks: []UnifiedBlock{
		{Kind: "image", Data: "dXNy", MimeType: "image/png"},
		{Kind: "text", Text: "mine"},
	}})
	req.History = withUserImg
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(captured), `"dXNy"`) {
		t.Fatalf("user-owned image must still be sent\nbody: %s", string(captured))
	}
}

// TestStripImageBlocksForNonVisionFallback covers the §4.6-C TTFT model-fallback
// re-gate: when a turn falls back to a non-vision model, the images inlined for the
// (vision-capable) primary must be removed before the shared history is reused, or
// the text-only fallback upstream rejects them.
func TestStripImageBlocksForNonVisionFallback(t *testing.T) {
	base := []UnifiedMessage{
		{Role: "user", Blocks: []UnifiedBlock{
			{Kind: "text", Text: "look at this"},
			{Kind: "image", Data: "aW1n", MimeType: "image/png"},
		}},
		{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "sure"}}},
	}
	out := stripImageBlocks(base)

	// No image blocks survive anywhere.
	for _, m := range out {
		for _, b := range m.Blocks {
			if b.Kind == "image" {
				t.Fatalf("image block survived stripImageBlocks: %+v", m)
			}
		}
	}
	// The user text is preserved and a skip note is appended.
	first := out[0].Blocks
	if len(first) != 2 || first[0].Kind != "text" || first[0].Text != "look at this" {
		t.Fatalf("original text block not preserved: %+v", first)
	}
	if first[1].Kind != "text" || !strings.Contains(first[1].Text, "vision") {
		t.Fatalf("expected a vision-skip note, got: %+v", first[1])
	}
	// The image-free assistant message is untouched.
	if len(out[1].Blocks) != 1 || out[1].Blocks[0].Text != "sure" {
		t.Fatalf("image-free message must be untouched: %+v", out[1])
	}
	// The caller's slice is not mutated (shared read-only during the primary stream).
	if len(base[0].Blocks) != 2 || base[0].Blocks[1].Kind != "image" {
		t.Fatalf("stripImageBlocks mutated the input history: %+v", base[0].Blocks)
	}
}
