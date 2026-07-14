package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// §4.3-B strip-thinking retry: a 400 on a Claude turn whose request carries a
// thinking param and/or a replayed thinking block is retried ONCE with all
// thinking removed (the non-genuine-upstream guard). Pure helpers first, then
// the end-to-end retry through p.Stream.

func TestIsClaudeModel(t *testing.T) {
	cases := map[string]bool{
		"claude-opus-4-8":          true,
		"claude-sonnet-4-20250514": true,
		"  Claude-Haiku-4-5  ":     true, // trimmed + case-insensitive
		"gpt-4o":                   false,
		"gemini-2.5-pro":           false,
		"deepseek-v3":              false,
		"":                         false,
	}
	for id, want := range cases {
		if got := isClaudeModel(id); got != want {
			t.Errorf("isClaudeModel(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestMessagesHaveThinking(t *testing.T) {
	// []map[string]any shape (built this run)
	built := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "hi"}}},
		{"role": "assistant", "content": []map[string]any{
			{"type": "thinking", "thinking": "", "signature": "sig"},
			{"type": "text", "text": "answer"},
		}},
	}
	if !messagesHaveThinking(built) {
		t.Error("messagesHaveThinking missed a thinking block in []map[string]any content")
	}
	// []any shape (raw replay)
	replay := []map[string]any{
		{"role": "assistant", "content": []any{
			map[string]any{"type": "redacted_thinking", "data": "x"},
			map[string]any{"type": "text", "text": "answer"},
		}},
	}
	if !messagesHaveThinking(replay) {
		t.Error("messagesHaveThinking missed a redacted_thinking block in []any content")
	}
	// none
	clean := []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "hi"}}},
		{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "answer"}}},
	}
	if messagesHaveThinking(clean) {
		t.Error("messagesHaveThinking false positive on thinking-free messages")
	}
}

func TestStripThinkingFromMessages(t *testing.T) {
	messages := []map[string]any{
		// history[0]: user
		{"role": "user", "content": []map[string]any{{"type": "text", "text": "q"}}},
		// history[1]: assistant with a thinking block + text + tool_use (raw-replay shape)
		{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "reason", "signature": "sig"},
			map[string]any{"type": "text", "text": "let me search"},
			map[string]any{"type": "tool_use", "id": "t1", "name": "web_search", "input": map[string]any{"query": "x"}},
		}},
		// history[2]: user tool_result
		{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "none"}}},
		// history[3]: a pathological thinking-ONLY assistant turn → must be dropped
		{"role": "assistant", "content": []map[string]any{{"type": "thinking", "thinking": "", "signature": "s2"}}},
	}
	historyLen := 4

	out, newHL := stripThinkingFromMessages(messages, historyLen)

	if messagesHaveThinking(out) {
		t.Fatalf("thinking blocks survived the strip: %+v", out)
	}
	// history[3] (thinking-only) dropped → 3 messages left, historyLen decremented.
	if len(out) != 3 {
		t.Fatalf("message count = %d, want 3 (thinking-only turn dropped)", len(out))
	}
	if newHL != 3 {
		t.Fatalf("historyLen = %d, want 3 (one dropped message was in the history prefix)", newHL)
	}
	// The surviving assistant turn keeps its text + tool_use, just not the thinking.
	asst, _ := out[1]["content"].([]any)
	if len(asst) != 2 {
		t.Fatalf("assistant content len = %d, want 2 (text + tool_use kept)", len(asst))
	}
	if blk, _ := asst[0].(map[string]any); blk["type"] != "text" {
		t.Errorf("first surviving block = %v, want text", blk["type"])
	}
	if blk, _ := asst[1].(map[string]any); blk["type"] != "tool_use" {
		t.Errorf("second surviving block = %v, want tool_use", blk["type"])
	}
}

// thinkingParamControls injects thinking:{type:adaptive} via the param-control
// merge, so the first request body carries a `thinking` field.
func thinkingParamControls() json.RawMessage {
	return json.RawMessage(`[{"key":"thinking","type":"toggle","map":{"on":{"thinking":{"type":"adaptive"}}}}]`)
}

func TestStreamStripsThinkingAndRetriesOn400(t *testing.T) {
	var calls int32
	var firstHadThinking, secondHadThinking, secondHadThinkingBlock bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		_, hasThinkingParam := body["thinking"]
		hasThinkingBlock := strings.Contains(string(raw), `"type":"thinking"`) ||
			strings.Contains(string(raw), `"type":"redacted_thinking"`)
		if n == 1 {
			firstHadThinking = hasThinkingParam || hasThinkingBlock
			// Simulate a non-genuine upstream rejecting the signed thinking replay.
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"bad thinking"}}`)
			return
		}
		secondHadThinking = hasThinkingParam
		secondHadThinkingBlock = hasThinkingBlock
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(anthropicTextStream("here is the answer")))
	}))
	defer srv.Close()

	// History carries a replayed assistant turn WITH a thinking block (raw path),
	// so the first request contains both a thinking param and a thinking block.
	replay, _ := json.Marshal([]map[string]any{
		{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "", "signature": "sig"},
			map[string]any{"type": "text", "text": "let me search"},
			map[string]any{"type": "tool_use", "id": "t1", "name": "web_search", "input": map[string]any{"query": "news"}},
		}},
		{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "No web results found."},
		}},
	})

	p := &AnthropicProvider{}
	req := UnifiedChatRequest{
		Model:          ModelInfo{RequestID: "claude-opus-4-8", BaseURL: srv.URL, APIKey: "k"},
		ParamControls:  thinkingParamControls(),
		ParamOverrides: map[string]any{"thinking": true},
		History: []UnifiedMessage{
			{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "today's news"}}},
			{Role: "assistant", Raw: replay},
		},
	}

	res, err := p.Stream(context.Background(), req, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream returned error, expected silent retry to succeed: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream call count = %d, want 2 (original 400 + stripped retry)", got)
	}
	if !firstHadThinking {
		t.Error("first request should have carried thinking (param and/or block)")
	}
	if secondHadThinking {
		t.Error("retry request must NOT carry a thinking param")
	}
	if secondHadThinkingBlock {
		t.Error("retry request must NOT carry any thinking/redacted_thinking block")
	}
	// The answer from the successful retry is returned.
	gotText := ""
	for _, b := range res.Blocks {
		if b.Kind == "text" {
			gotText += b.Text
		}
	}
	if !strings.Contains(gotText, "here is the answer") {
		t.Errorf("result text = %q, want the retry's answer", gotText)
	}
}

func TestStreamDoesNotRetryWhenNoThinking(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"genuinely malformed"}}`)
	}))
	defer srv.Close()

	// Claude model, 400, but NO thinking anywhere → must not strip-retry.
	p := &AnthropicProvider{}
	req := UnifiedChatRequest{
		Model:   ModelInfo{RequestID: "claude-opus-4-8", BaseURL: srv.URL, APIKey: "k"},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hi"}}}},
	}
	if _, err := p.Stream(context.Background(), req, nil, func(SseEvent) {}); err == nil {
		t.Fatal("expected the 400 to surface as an error, got nil")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream call count = %d, want 1 (no strip-retry without thinking)", got)
	}
}
