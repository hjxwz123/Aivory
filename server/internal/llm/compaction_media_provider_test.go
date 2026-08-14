package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIPromptModePreservesCompactionMedia(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		captured, _ = io.ReadAll(request.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"))
	}))
	t.Cleanup(server.Close)

	request := UnifiedChatRequest{
		Model: ModelInfo{RequestID: "gpt-test", BaseURL: server.URL, APIKey: "key", Vision: true},
	}
	_, err := (&OpenAIProvider{}).promptRunOnce(request)(context.Background(), []UnifiedMessage{{
		Role: "user",
		Blocks: []UnifiedBlock{
			{Kind: "text", Text: "continue from the compacted image"},
			{Kind: "image", MimeType: "image/png", Data: "Y29tcGFjdGVkLWltYWdl"},
		},
	}}, "system")
	if err != nil {
		t.Fatal(err)
	}
	body := string(captured)
	if !strings.Contains(body, `"type":"image_url"`) ||
		!strings.Contains(body, `data:image/png;base64,Y29tcGFjdGVkLWltYWdl`) {
		t.Fatalf("OpenAI prompt-mode request dropped compacted image: %s", body)
	}
}

func TestGeminiPromptModePreservesCompactionMedia(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		captured, _ = io.ReadAll(request.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	t.Cleanup(server.Close)

	request := UnifiedChatRequest{
		Model: ModelInfo{RequestID: "gemini-test", BaseURL: server.URL, APIKey: "key", Vision: true},
	}
	_, err := (&GoogleProvider{}).promptRunOnce(request)(context.Background(), []UnifiedMessage{{
		Role: "user",
		Blocks: []UnifiedBlock{
			{Kind: "text", Text: "continue from the compacted image"},
			{Kind: "image", MimeType: "image/png", Data: "Y29tcGFjdGVkLWltYWdl"},
		},
	}}, "system")
	if err != nil {
		t.Fatal(err)
	}
	body := string(captured)
	if !strings.Contains(body, `"inlineData"`) || !strings.Contains(body, `"mimeType":"image/png"`) ||
		!strings.Contains(body, `"data":"Y29tcGFjdGVkLWltYWdl"`) {
		t.Fatalf("Gemini prompt-mode request dropped compacted image: %s", body)
	}
}

func TestPromptModeCompactionMediaRemainsUserRoleOnly(t *testing.T) {
	history := []UnifiedMessage{{
		Role: "assistant",
		Blocks: []UnifiedBlock{
			{Kind: "text", Text: "assistant output"},
			{Kind: "image", MimeType: "image/png", Data: "Y29tcGFjdGVkLWltYWdl"},
		},
	}}
	tests := []struct {
		name     string
		provider func(UnifiedChatRequest) PromptToolRunner
		response string
	}{
		{
			name: "openai",
			provider: func(request UnifiedChatRequest) PromptToolRunner {
				return (&OpenAIProvider{}).promptRunOnce(request)
			},
			response: `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n",
		},
		{
			name: "gemini",
			provider: func(request UnifiedChatRequest) PromptToolRunner {
				return (&GoogleProvider{}).promptRunOnce(request)
			},
			response: `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var captured []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				captured, _ = io.ReadAll(request.Body)
				_, _ = w.Write([]byte(test.response))
			}))
			t.Cleanup(server.Close)

			request := UnifiedChatRequest{
				Model: ModelInfo{RequestID: "vision-test", BaseURL: server.URL, APIKey: "key", Vision: true},
			}
			if _, err := test.provider(request)(context.Background(), history, "system"); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(captured), "Y29tcGFjdGVkLWltYWdl") {
				t.Fatalf("prompt-mode request put compacted media on assistant role: %s", captured)
			}
		})
	}
}
