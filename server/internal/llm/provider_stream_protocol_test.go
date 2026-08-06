package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type providerProtocolReader func(io.Reader, func(SseEvent)) (string, error)

func TestProviderStreamReadersValidateProtocol(t *testing.T) {
	readers := []struct {
		name          string
		explicitError string
		errorMarker   string
		validStream   string
		validText     string
		read          providerProtocolReader
	}{
		{
			name:          "openai chat",
			explicitError: `data: {"error":{"message":"chat protocol failure"}}` + "\n\n",
			errorMarker:   "chat protocol failure",
			validText:     "chat ok",
			validStream: strings.Join([]string{
				`data: {"choices":[{"delta":{"content":"chat ok"}}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"),
			read: func(body io.Reader, emit func(SseEvent)) (string, error) {
				text, _, _, _, _, err := readOpenAIChatStream(body, emit)
				return text, err
			},
		},
		{
			name:          "openai responses",
			explicitError: `data: {"type":"response.failed","response":{"error":{"message":"responses protocol failure"}}}` + "\n\n",
			errorMarker:   "responses protocol failure",
			validText:     "responses ok",
			validStream: strings.Join([]string{
				`data: {"type":"response.output_text.delta","delta":"responses ok"}`,
				`data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":2},"output":[]}}`,
				`data: [DONE]`,
				``,
			}, "\n\n"),
			read: func(body io.Reader, emit func(SseEvent)) (string, error) {
				text, _, _, _, _, _, _, err := readOpenAIResponsesStream(body, emit)
				return text, err
			},
		},
		{
			name:          "anthropic",
			explicitError: `data: {"type":"error","error":{"type":"overloaded_error","message":"anthropic protocol failure"}}` + "\n\n",
			errorMarker:   "anthropic protocol failure",
			validText:     "anthropic ok",
			validStream: strings.Join([]string{
				`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"anthropic ok"}}`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n\n"),
			read: func(body io.Reader, emit func(SseEvent)) (string, error) {
				_, _, text, _, _, _, err := readAnthropicStream(body, emit)
				return text, err
			},
		},
		{
			name:          "gemini",
			explicitError: `data: {"error":{"code":429,"message":"gemini protocol failure","status":"RESOURCE_EXHAUSTED"}}` + "\n\n",
			errorMarker:   "gemini protocol failure",
			validText:     "gemini ok",
			validStream: strings.Join([]string{
				`data: {"candidates":[{"content":{"parts":[{"text":"gemini ok"}]},"finishReason":"STOP"}]}`,
				``,
			}, "\n\n"),
			read: func(body io.Reader, emit func(SseEvent)) (string, error) {
				text, _, _, _, _, err := readGeminiStream(body, emit)
				return text, err
			},
		},
	}

	for _, reader := range readers {
		reader := reader
		t.Run(reader.name, func(t *testing.T) {
			t.Run("explicit error", func(t *testing.T) {
				_, err := reader.read(strings.NewReader(reader.explicitError), func(SseEvent) {})
				if err == nil {
					t.Fatal("explicit provider error was accepted as a successful stream")
				}
				if !strings.Contains(err.Error(), reader.errorMarker) {
					t.Fatalf("error = %q, want upstream marker %q", err, reader.errorMarker)
				}
			})

			t.Run("invalid data JSON", func(t *testing.T) {
				_, err := reader.read(strings.NewReader("data: {not-json}\n\n"), func(SseEvent) {})
				if err == nil {
					t.Fatal("malformed data JSON was accepted as a successful stream")
				}
				if !strings.Contains(err.Error(), "invalid JSON") {
					t.Fatalf("error = %q, want an invalid JSON protocol error", err)
				}
			})

			t.Run("empty stream", func(t *testing.T) {
				_, err := reader.read(strings.NewReader(""), func(SseEvent) {})
				if err == nil {
					t.Fatal("empty response was accepted as a successful stream")
				}
				if !strings.Contains(err.Error(), "empty response") {
					t.Fatalf("error = %q, want an empty response protocol error", err)
				}
			})

			t.Run("valid terminal stream", func(t *testing.T) {
				var events []SseEvent
				text, err := reader.read(strings.NewReader(reader.validStream), func(ev SseEvent) {
					events = append(events, ev)
				})
				if err != nil {
					t.Fatalf("valid terminal stream: %v", err)
				}
				wantText := reader.validText
				if text != wantText {
					t.Fatalf("text = %q, want %q", text, wantText)
				}
				if len(events) != 1 || events[0].Type != "text_delta" || events[0].Text != wantText {
					t.Fatalf("events = %+v, want one text delta %q", events, wantText)
				}
			})

			t.Run("terminal response ignores trailing gateway noise", func(t *testing.T) {
				noisy := reader.validStream + "data: {not-json}\n\n" + reader.explicitError
				var events []SseEvent
				got, err := reader.read(strings.NewReader(noisy), func(ev SseEvent) {
					events = append(events, ev)
				})
				if err != nil {
					t.Fatalf("completed response was overturned by trailing noise: %v", err)
				}
				if got != reader.validText {
					t.Fatalf("text = %q, want completed text %q", got, reader.validText)
				}
				if len(events) != 1 || events[0].Text != reader.validText {
					t.Fatalf("events = %+v, want only the completed response", events)
				}
			})
		})
	}
}

func TestReadGeminiStreamConsumesCumulativeAndTrailingUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"first "}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2}}`,
		`data: {"promptFeedback":{"safetyRatings":[]},"candidates":[{"content":{"parts":[{"text":"second "}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":4}}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"third"}]} ,"finishReason":"STOP"}]}`,
		`data: {"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":6}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	var deltas strings.Builder
	text, _, _, _, usage, err := readGeminiStream(strings.NewReader(stream), func(ev SseEvent) {
		if ev.Type == "text_delta" {
			deltas.WriteString(ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("readGeminiStream: %v", err)
	}
	if text != "first second third" || deltas.String() != text {
		t.Fatalf("text/deltas = %q/%q, want complete multi-chunk answer", text, deltas.String())
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 6 {
		t.Fatalf("usage = %+v, want final cumulative usage 7/6", usage)
	}
}

func TestReadGeminiStreamUsageIsNotTerminal(t *testing.T) {
	stream := `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}` + "\n\n"

	text, _, _, _, usage, err := readGeminiStream(strings.NewReader(stream), func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "response ended before a terminal event") {
		t.Fatalf("error = %v, want premature EOF protocol error", err)
	}
	if text != "partial" || usage.InputTokens != 3 || usage.OutputTokens != 1 {
		t.Fatalf("partial text/usage = %q/%+v, want preserved partial response", text, usage)
	}
}

func TestReadAnthropicStreamContinuesPastMessageDelta(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":2}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`data: {"type":"error","error":{"type":"api_error","message":"stream failed before message_stop"}}`,
		``,
	}, "\n\n")

	_, _, _, _, _, usage, err := readAnthropicStream(strings.NewReader(stream), func(SseEvent) {})
	if err == nil || !strings.Contains(err.Error(), "stream failed before message_stop") {
		t.Fatalf("error = %v, want trailing Anthropic stream error", err)
	}
	if usage.InputTokens != 2 || usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want usage preserved before stream error", usage)
	}
}

func TestReadOpenAIChatStreamConsumesUsageAfterFinishReason(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"complete"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")

	text, _, _, finish, usage, err := readOpenAIChatStream(strings.NewReader(stream), func(SseEvent) {})
	if err != nil {
		t.Fatalf("readOpenAIChatStream: %v", err)
	}
	if text != "complete" || finish != "stop" {
		t.Fatalf("text/finish = %q/%q, want complete/stop", text, finish)
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want trailing usage 5/2", usage)
	}
}

func TestProvidersFallBackAfterExplicitSSEError(t *testing.T) {
	tests := []struct {
		name           string
		provider       Provider
		requestID      string
		apiFormat      string
		primaryStream  string
		fallbackStream string
	}{
		{
			name:          "openai chat",
			provider:      &OpenAIProvider{},
			requestID:     "gpt-test",
			primaryStream: `data: {"error":{"message":"chat primary failed"}}` + "\n\n",
			fallbackStream: strings.Join([]string{
				`data: {"choices":[{"delta":{"content":"fallback answer"}}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"),
		},
		{
			name:          "anthropic",
			provider:      &AnthropicProvider{},
			requestID:     "claude-test",
			primaryStream: `data: {"type":"error","error":{"type":"overloaded_error","message":"anthropic primary failed"}}` + "\n\n",
			fallbackStream: strings.Join([]string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":2}}}`,
				`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"fallback answer"}}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n\n"),
		},
		{
			name:          "gemini",
			provider:      &GoogleProvider{},
			requestID:     "gemini-test",
			primaryStream: `data: {"error":{"code":503,"message":"gemini primary failed","status":"UNAVAILABLE"}}` + "\n\n",
			fallbackStream: strings.Join([]string{
				`data: {"candidates":[{"content":{"parts":[{"text":"fallback answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}`,
				``,
			}, "\n\n"),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var primaryHits atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				primaryHits.Add(1)
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, tc.primaryStream)
			}))
			defer primary.Close()

			var fallbackHits atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackHits.Add(1)
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, tc.fallbackStream)
			}))
			defer fallback.Close()

			flag := new(atomic.Bool)
			visible := new(atomic.Bool)
			ctx := contextWithProviderVisibleOutput(context.Background(), visible)
			var events []SseEvent
			result, err := tc.provider.Stream(ctx, UnifiedChatRequest{
				Model: ModelInfo{
					RequestID: tc.requestID,
					BaseURL:   primary.URL,
					APIKey:    "primary-key",
					APIFormat: tc.apiFormat,
					Fallback: &ChannelCreds{
						BaseURL: fallback.URL,
						APIKey:  "fallback-key",
					},
				},
				History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
				FallbackUsed: flag,
			}, nil, observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible))
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
				t.Fatalf("request counts primary/fallback = %d/%d, want 1/1", primaryHits.Load(), fallbackHits.Load())
			}
			if !flag.Load() {
				t.Fatal("FallbackUsed was not set after the primary SSE error")
			}
			if text := providerProtocolResultText(result); text != "fallback answer" {
				t.Fatalf("result text = %q, want only the fallback answer; result=%+v", text, result)
			}
			if len(events) != 1 || events[0].Type != "text_delta" || events[0].Text != "fallback answer" {
				t.Fatalf("streamed events = %+v, want only the fallback answer", events)
			}
		})
	}
}

func providerProtocolResultText(result *UnifiedResult) string {
	if result == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range result.Blocks {
		if block.Kind == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}
