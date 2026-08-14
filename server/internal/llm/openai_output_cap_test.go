package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIStrictOutputCapNormalizesFormatAliases(t *testing.T) {
	tests := []struct {
		name       string
		apiFormat  string
		extra      json.RawMessage
		stream     string
		wantField  string
		omitFields []string
	}{
		{
			name:       "chat completions uses configured completion-token field",
			apiFormat:  "chat",
			extra:      json.RawMessage(`{"max_tokens":9000,"max_completion_tokens":8000,"max_output_tokens":7000}`),
			stream:     `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n",
			wantField:  "max_completion_tokens",
			omitFields: []string{"max_tokens", "max_output_tokens"},
		},
		{
			name:       "chat completions keeps legacy max-tokens field",
			apiFormat:  "chat",
			extra:      json.RawMessage(`{"max_tokens":9000,"max_output_tokens":7000}`),
			stream:     `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n",
			wantField:  "max_tokens",
			omitFields: []string{"max_completion_tokens", "max_output_tokens"},
		},
		{
			name:      "responses uses only max-output-tokens",
			apiFormat: "responses",
			extra:     json.RawMessage(`{"max_tokens":9000,"max_completion_tokens":8000,"max_output_tokens":7000}`),
			stream: `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"output":[]}}` + "\n\n",
			wantField:  "max_output_tokens",
			omitFields: []string{"max_tokens", "max_completion_tokens"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var captured map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
					return
				}
				if err := json.Unmarshal(body, &captured); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(test.stream))
			}))
			defer server.Close()

			_, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
				Model: ModelInfo{
					RequestID: "reasoning-summary-model",
					BaseURL:   server.URL,
					APIKey:    "test-key",
					APIFormat: test.apiFormat,
				},
				History:               []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "summarize"}}}},
				ExtraParams:           test.extra,
				MaxOutputTokens:       600,
				StrictMaxOutputTokens: true,
			}, nil, func(SseEvent) {})
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			if got := captured[test.wantField]; got != float64(600) {
				t.Fatalf("%s = %#v, want 600; body=%#v", test.wantField, got, captured)
			}
			for _, field := range test.omitFields {
				if _, exists := captured[field]; exists {
					t.Fatalf("strict %s request retained conflicting %s: %#v", test.apiFormat, field, captured)
				}
			}
		})
	}
}

func TestOpenAIOrdinaryChatKeepsConfiguredOutputAliases(t *testing.T) {
	body := map[string]any{"max_tokens": 600}
	req := UnifiedChatRequest{
		ExtraParams:     json.RawMessage(`{"max_completion_tokens":8000,"max_output_tokens":7000}`),
		MaxOutputTokens: 600,
	}
	body = MergeRequestParams(body, req.ExtraParams, nil, nil)
	enforceOpenAIOutputTokenCap(body, req, false)

	if body["max_tokens"] != 600 || body["max_completion_tokens"] != float64(8000) || body["max_output_tokens"] != float64(7000) {
		t.Fatalf("ordinary chat parameters were changed: %#v", body)
	}
}
