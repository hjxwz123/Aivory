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
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", Vision: true},
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
		Model:   ModelInfo{RequestID: "gpt-test", BaseURL: srv.URL, APIKey: "k", APIFormat: "responses", Vision: true},
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
		Model:   ModelInfo{RequestID: "claude-test", BaseURL: srv.URL, APIKey: "k", Vision: true},
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
		Model:        ModelInfo{RequestID: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k", Vision: true},
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
		{
			Role: "user",
			Blocks: []UnifiedBlock{
				{Kind: "text", Text: "look at this"},
				{Kind: "image", Data: "aW1n", MimeType: "image/png"},
				{Kind: "artifact", Data: "d2VicA==", MimeType: "image/webp"},
			},
			Attachments: []Attachment{
				{ID: "kind-image", Kind: "image", MimeType: "application/octet-stream"},
				{ID: "mime-image", Kind: "file", MimeType: "image/avif"},
				{ID: "document", Kind: "pdf", MimeType: "application/pdf"},
			},
		},
		{
			Role:        "assistant",
			Blocks:      []UnifiedBlock{{Kind: "image", Data: "cHVyZQ==", MimeType: "image/png"}},
			Attachments: []Attachment{{ID: "raw-image", Kind: "image", MimeType: "image/png"}},
			Raw:         json.RawMessage(`[{"role":"assistant","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cmF3"}}]}]`),
		},
		{
			Role:   "assistant",
			Blocks: []UnifiedBlock{{Kind: "text", Text: "sure"}},
			Raw:    json.RawMessage(`[{"role":"assistant","content":[{"type":"text","text":"sure"}]}]`),
		},
	}
	out := stripImageBlocks(base)

	// No image block, image MIME, or image attachment survives anywhere.
	for _, m := range out {
		for _, b := range m.Blocks {
			if unifiedBlockIsImage(b) {
				t.Fatalf("image block survived stripImageBlocks: %+v", m)
			}
		}
		for _, attachment := range m.Attachments {
			if attachmentIsImage(attachment) {
				t.Fatalf("image attachment survived stripImageBlocks: %+v", m)
			}
		}
	}
	// A mixed image/text turn keeps only its original text and non-image files.
	first := out[0].Blocks
	if len(first) != 1 || first[0].Kind != "text" || first[0].Text != "look at this" {
		t.Fatalf("original text block not preserved: %+v", first)
	}
	if len(out[0].Attachments) != 1 || out[0].Attachments[0].ID != "document" {
		t.Fatalf("non-image attachment not preserved: %+v", out[0].Attachments)
	}

	// An image-only turn becomes a provider-safe text turn and cannot replay Raw.
	if len(out[1].Blocks) != 1 || out[1].Blocks[0].Text != nonVisionImagePlaceholder {
		t.Fatalf("image-only message needs a safe placeholder: %+v", out[1])
	}
	if len(out[1].Raw) != 0 {
		t.Fatalf("image-affected message retained native Raw: %s", out[1].Raw)
	}

	// Image-free native tool/text history remains available for faithful replay.
	if len(out[2].Blocks) != 1 || out[2].Blocks[0].Text != "sure" || len(out[2].Raw) == 0 {
		t.Fatalf("image-free message must remain intact: %+v", out[2])
	}

	// The caller's nested slices remain independent from the filtered copy.
	out[0].Blocks[0].Text = "changed"
	out[0].Attachments[0].Filename = "changed.pdf"
	out[2].Raw[0] = '{'
	if len(base[0].Blocks) != 3 || base[0].Blocks[0].Text != "look at this" || base[0].Attachments[2].Filename != "" || base[2].Raw[0] != '[' {
		t.Fatalf("stripImageBlocks mutated the input history: %+v", base[0].Blocks)
	}
}

func TestNonVisionProvidersOmitUnifiedAndNativeImages(t *testing.T) {
	tests := []struct {
		name      string
		provider  Provider
		apiFormat string
		response  string
		nativeRaw json.RawMessage
		requestID string
	}{
		{
			name:      "openai_chat",
			provider:  &OpenAIProvider{},
			requestID: "gpt-test",
			response:  `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n",
			nativeRaw: json.RawMessage(`[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,cmF3"}}]}]`),
		},
		{
			name:      "openai_responses",
			provider:  &OpenAIProvider{},
			apiFormat: "responses",
			requestID: "gpt-test",
			response:  `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n",
			nativeRaw: json.RawMessage(`[{"type":"message","role":"assistant","content":[{"type":"input_image","image_url":"data:image/png;base64,cmF3"}]}]`),
		},
		{
			name:      "anthropic",
			provider:  &AnthropicProvider{},
			requestID: "claude-test",
			response: strings.Join([]string{
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`data: {"type":"message_stop"}`,
			}, "\n\n") + "\n\n",
			nativeRaw: json.RawMessage(`[{"role":"assistant","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cmF3"}}]}]`),
		},
		{
			name:      "gemini",
			provider:  &GoogleProvider{},
			requestID: "gemini-test",
			response:  `data: {"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}` + "\n\n",
			nativeRaw: json.RawMessage(`[{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"cmF3"}}]}]`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var captured []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.response))
			}))
			defer srv.Close()

			history := []UnifiedMessage{
				{
					Role: "user",
					Blocks: []UnifiedBlock{
						{Kind: "text", Text: "keep this text"},
						{Kind: "image", Data: "aW1n", MimeType: "image/png"},
					},
					Attachments: []Attachment{{ID: "image-file", Kind: "file", MimeType: "image/avif"}},
				},
				{Role: "assistant", Blocks: []UnifiedBlock{{Kind: "text", Text: "prior answer"}}, Raw: test.nativeRaw},
				{Role: "user", Blocks: []UnifiedBlock{{Kind: "artifact", Data: "d2VicA==", MimeType: "image/webp"}}},
			}
			req := UnifiedChatRequest{
				SystemPrompt: "sys",
				Model: ModelInfo{
					RequestID: test.requestID,
					BaseURL:   srv.URL,
					APIKey:    "k",
					APIFormat: test.apiFormat,
					Vision:    false,
				},
				History: history,
			}
			if _, err := test.provider.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
				t.Fatal(err)
			}

			body := string(captured)
			for _, banned := range []string{
				`"image_url"`, `"input_image"`, `"type":"image"`, `"inlineData"`,
				`"image/png"`, `"image/avif"`, `"image/webp"`, "aW1n", "cmF3", "d2VicA==",
			} {
				if strings.Contains(body, banned) {
					t.Fatalf("non-vision request leaked image payload %s\nbody: %s", banned, body)
				}
			}
			if !strings.Contains(body, "keep this text") || !strings.Contains(body, nonVisionImagePlaceholder) {
				t.Fatalf("filtered history lost safe text/placeholder\nbody: %s", body)
			}
		})
	}
}
