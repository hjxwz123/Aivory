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

func TestAnthropicSamplingRestrictionMatchesClaudeModels(t *testing.T) {
	claudeIDs := []string{
		"claude-3-5-sonnet-20241022",
		"claude-3-7-sonnet-20250219",
		"claude-sonnet-4-20250514",
		"claude-opus-4-1",
		"claude-sonnet-5-20260401",
		"anthropic/claude-sonnet-5-20260401",
		"bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0",
	}
	nonClaudeIDs := []string{
		"",
		"gpt-4o",
		"gemini-2.5-pro",
		"llama-3.3-70b",
	}
	for _, id := range claudeIDs {
		if !anthropicModelRejectsSampling(id) {
			t.Errorf("anthropicModelRejectsSampling(%q) = false, want true", id)
		}
	}
	for _, id := range nonClaudeIDs {
		if anthropicModelRejectsSampling(id) {
			t.Errorf("anthropicModelRejectsSampling(%q) = true, want false", id)
		}
	}
}

func TestAnthropicDoesNotInjectThinkingByDefault(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode provider body: %v\n%s", err, string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextStream("ok")))
	}))
	defer srv.Close()

	p := &AnthropicProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "claude-sonnet-4-20250514", BaseURL: srv.URL, APIKey: "k"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	if _, has := captured["thinking"]; has {
		t.Fatalf("thinking was injected without param_controls: %#v", captured["thinking"])
	}
	if got := captured["max_tokens"]; got != float64(4096) {
		t.Fatalf("max_tokens = %#v, want default 4096", got)
	}
}

func TestAnthropicThinkingIsExplicitParamControl(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode provider body: %v\n%s", err, string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextStream("ok")))
	}))
	defer srv.Close()

	p := &AnthropicProvider{}
	req := UnifiedChatRequest{
		Model:           ModelInfo{RequestID: "claude-sonnet-4-20250514", BaseURL: srv.URL, APIKey: "k"},
		History:         []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		MaxOutputTokens: 1024,
		ParamOverrides:  map[string]any{"thinking": true},
		ParamControls: json.RawMessage(`[
			{
				"key": "thinking",
				"type": "toggle",
				"map": {
					"on": {
						"thinking": {"type": "enabled", "budget_tokens": 2048},
						"temperature": 0.7,
						"top_p": 0.9,
						"top_k": 40,
						"tool_choice": {"type": "tool", "name": "web_search"}
					}
				}
			}
		]`),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	cfg, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking not sent from explicit param_controls: %#v", captured["thinking"])
	}
	if cfg["type"] != "enabled" || cfg["budget_tokens"] != float64(2048) {
		t.Fatalf("thinking = %#v, want enabled with budget_tokens 2048", cfg)
	}
	if got := captured["max_tokens"]; got != float64(4096) {
		t.Fatalf("max_tokens = %#v, want raised to 4096", got)
	}
	assertNoAnthropicSamplingParams(t, captured)
	if _, has := captured["tool_choice"]; has {
		t.Fatalf("forced tool_choice should be removed when Anthropic thinking is enabled: %#v", captured["tool_choice"])
	}
}

func TestAnthropicAdaptiveThinkingRemovesConflictingParams(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode provider body: %v\n%s", err, string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextStream("ok")))
	}))
	defer srv.Close()

	p := &AnthropicProvider{}
	req := UnifiedChatRequest{
		Model:           ModelInfo{RequestID: "claude-3-5-sonnet-20241022", BaseURL: srv.URL, APIKey: "k"},
		History:         []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		MaxOutputTokens: 1024,
		ParamOverrides:  map[string]any{"thinking": true},
		ParamControls: json.RawMessage(`[
			{
				"key": "thinking",
				"type": "toggle",
				"map": {
					"on": {
						"thinking": {"type": "adaptive", "display": "summarized"},
						"output_config": {"effort": "high"},
						"temperature": 0.7,
						"top_p": 0.9,
						"top_k": 40,
						"tool_choice": {"type": "any"}
					}
				}
			}
		]`),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	cfg, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking not sent from explicit param_controls: %#v", captured["thinking"])
	}
	if cfg["type"] != "adaptive" || cfg["display"] != "summarized" {
		t.Fatalf("thinking = %#v, want adaptive summarized", cfg)
	}
	oc, ok := captured["output_config"].(map[string]any)
	if !ok || oc["effort"] != "high" {
		t.Fatalf("output_config = %#v, want effort high", captured["output_config"])
	}
	if got := captured["max_tokens"]; got != float64(1024) {
		t.Fatalf("max_tokens = %#v, want unchanged 1024 for adaptive thinking", got)
	}
	assertNoAnthropicSamplingParams(t, captured)
	if _, has := captured["tool_choice"]; has {
		t.Fatalf("forced tool_choice should be removed when Anthropic adaptive thinking is enabled: %#v", captured["tool_choice"])
	}
}

func TestAnthropicLatestModelRemovesSamplingWithoutExplicitThinking(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode provider body: %v\n%s", err, string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextStream("ok")))
	}))
	defer srv.Close()

	p := &AnthropicProvider{}
	req := UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "claude-sonnet-5-20260401", BaseURL: srv.URL, APIKey: "k"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		ParamOverrides: map[string]any{"sampling": true},
		ParamControls: json.RawMessage(`[
			{
				"key": "sampling",
				"type": "toggle",
				"map": {
					"on": {"temperature": 0.7, "top_p": 0.9, "top_k": 40}
				}
			}
		]`),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	if _, has := captured["thinking"]; has {
		t.Fatalf("thinking should not be injected for latest model cleanup: %#v", captured["thinking"])
	}
	assertNoAnthropicSamplingParams(t, captured)
}

func TestGeminiDoesNotInjectThinkingByDefault(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode provider body: %v\n%s", err, string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(geminiTextStream("ok")))
	}))
	defer srv.Close()

	p := &GoogleProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	if cfg, has := captured["generationConfig"].(map[string]any); has {
		if _, hasThinking := cfg["thinkingConfig"]; hasThinking {
			t.Fatalf("thinkingConfig was injected without param_controls: %#v", cfg["thinkingConfig"])
		}
	}
}

func TestGeminiThinkingIsExplicitParamControl(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode provider body: %v\n%s", err, string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(geminiTextStream("ok")))
	}))
	defer srv.Close()

	p := &GoogleProvider{}
	req := UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"},
		History:        []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		ParamOverrides: map[string]any{"thinking": true},
		ParamControls: json.RawMessage(`[
			{
				"key": "thinking",
				"type": "toggle",
				"map": {
					"on": {
						"generationConfig": {
							"thinkingConfig": {"includeThoughts": true, "thinkingBudget": 1024}
						}
					}
				}
			}
		]`),
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err != nil {
		t.Fatal(err)
	}
	gc, ok := captured["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing from explicit param_controls: %#v", captured)
	}
	cfg, ok := gc["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("thinkingConfig not sent from explicit param_controls: %#v", gc["thinkingConfig"])
	}
	if cfg["includeThoughts"] != true || cfg["thinkingBudget"] != float64(1024) {
		t.Fatalf("thinkingConfig = %#v, want includeThoughts true with thinkingBudget 1024", cfg)
	}
}

func anthropicTextStream(text string) string {
	return strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + jsonQuote(text) + `}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n\n")
}

func geminiTextStream(text string) string {
	return `data: {"candidates":[{"content":{"parts":[{"text":` + jsonQuote(text) + `}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}` + "\n\n"
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func assertNoAnthropicSamplingParams(t *testing.T, body map[string]any) {
	t.Helper()
	for _, key := range []string{"temperature", "top_p", "topP", "top_k", "topK"} {
		if _, has := body[key]; has {
			t.Fatalf("%s should be removed from Anthropic request body: %#v", key, body[key])
		}
	}
}

// TestReadAnthropicStreamThinking verifies the official Claude thinking stream
// (content_block_delta → thinking_delta) is surfaced as thinking_delta SSE
// events and captured (with signature) for replay.
func TestReadAnthropicStreamThinking(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"work it out."}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG123"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Answer."}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	var thinkingDeltas string
	_, _, text, thinkingBlocks, _, _, err := readAnthropicStream(strings.NewReader(stream), func(ev SseEvent) {
		if ev.Type == "thinking_delta" {
			thinkingDeltas += ev.Text
		}
	})
	if err != nil {
		t.Fatalf("readAnthropicStream: %v", err)
	}
	if thinkingDeltas != "Let me work it out." {
		t.Errorf("thinking_delta events = %q, want %q", thinkingDeltas, "Let me work it out.")
	}
	if text != "Answer." {
		t.Errorf("text = %q, want %q", text, "Answer.")
	}
	if len(thinkingBlocks) != 1 || thinkingBlocks[0].Signature != "SIG123" || thinkingBlocks[0].Text != "Let me work it out." {
		t.Errorf("thinkingBlocks = %+v, want one block with signature SIG123", thinkingBlocks)
	}
}

// TestReadGeminiStreamThinking verifies the official Gemini thought parts
// ({text, thought:true}) are surfaced as thinking_delta and kept out of the
// visible answer.
func TestReadGeminiStreamThinking(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Pondering...","thought":true}]}}]}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Final answer."}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}}`,
		"",
	}, "\n")

	var thinkingDeltas, textDeltas string
	text, thinking, _, _, _, err := readGeminiStream(strings.NewReader(stream), func(ev SseEvent) {
		switch ev.Type {
		case "thinking_delta":
			thinkingDeltas += ev.Text
		case "text_delta":
			textDeltas += ev.Text
		}
	})
	if err != nil {
		t.Fatalf("readGeminiStream: %v", err)
	}
	if thinking != "Pondering..." || thinkingDeltas != "Pondering..." {
		t.Errorf("thinking = %q / deltas = %q, want %q", thinking, thinkingDeltas, "Pondering...")
	}
	if text != "Final answer." || textDeltas != "Final answer." {
		t.Errorf("text = %q / deltas = %q, want %q", text, textDeltas, "Final answer.")
	}
}
