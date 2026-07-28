package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func providerParsedTestBuild(ctx context.Context) func(string, string) (*http.Request, error) {
	return func(baseURL, apiKey string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/stream", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("authorization", "Bearer "+apiKey)
		return req, nil
	}
}

func providerParsedTestBody(resp *http.Response) (string, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("provider status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func TestDoProviderParsedRequestDiscardsPrimaryEventsOnParsedFailure(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		_, _ = io.WriteString(w, "primary")
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback")
	}))
	defer fallback.Close()

	errPrimaryParse := errors.New("primary stream parse failed")
	flag := new(atomic.Bool)
	var events []SseEvent
	err := doProviderParsedRequest(
		context.Background(),
		ModelInfo{
			BaseURL: primary.URL,
			APIKey:  "primary-key",
			Fallback: &ChannelCreds{
				BaseURL: fallback.URL,
				APIKey:  "fallback-key",
			},
		},
		flag,
		providerParsedTestBuild(context.Background()),
		func(resp *http.Response, emit func(SseEvent)) error {
			body, readErr := providerParsedTestBody(resp)
			if readErr != nil {
				return readErr
			}
			switch body {
			case "primary":
				emit(SseEvent{Type: "text_delta", Text: "discarded-primary-prefix"})
				emit(SseEvent{Type: "thinking_delta", Text: "discarded-primary-thinking"})
				return errPrimaryParse
			case "fallback":
				emit(SseEvent{Type: "text_delta", Text: "fallback-answer"})
				return nil
			default:
				return fmt.Errorf("unexpected test body %q", body)
			}
		},
		func(ev SseEvent) { events = append(events, ev) },
	)
	if err != nil {
		t.Fatalf("doProviderParsedRequest: %v", err)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/1", primaryHits.Load(), fallbackHits.Load())
	}
	if !flag.Load() {
		t.Fatal("FallbackUsed was not set after the parsed primary failure")
	}
	if len(events) != 1 || events[0].Type != "text_delta" || events[0].Text != "fallback-answer" {
		t.Fatalf("committed events = %+v, want only the fallback answer", events)
	}
}

func TestDoProviderParsedRequestFallsBackOnPrimaryHTTPFailure(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		http.Error(w, "primary unavailable", http.StatusBadGateway)
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback-ok")
	}))
	defer fallback.Close()

	recorder := newProviderRequestRecorder()
	ctx := contextWithProviderRequestRecorder(context.Background(), recorder)
	flag := new(atomic.Bool)
	var events []SseEvent
	err := doProviderParsedRequest(
		ctx,
		ModelInfo{
			BaseURL: primary.URL,
			APIKey:  "primary-key",
			Fallback: &ChannelCreds{
				BaseURL: fallback.URL,
				APIKey:  "fallback-key",
			},
		},
		flag,
		providerParsedTestBuild(ctx),
		func(resp *http.Response, emit func(SseEvent)) error {
			body, readErr := providerParsedTestBody(resp)
			if readErr != nil {
				return readErr
			}
			emit(SseEvent{Type: "text_delta", Text: body})
			return nil
		},
		func(ev SseEvent) { events = append(events, ev) },
	)
	if err != nil {
		t.Fatalf("doProviderParsedRequest: %v", err)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/1", primaryHits.Load(), fallbackHits.Load())
	}
	if !flag.Load() {
		t.Fatal("FallbackUsed was not set after the primary HTTP failure")
	}
	if len(events) != 1 || events[0].Text != "fallback-ok" {
		t.Fatalf("committed events = %+v, want fallback response", events)
	}
	snapshots := recorder.snapshots()
	if len(snapshots) != 2 || snapshots[0].Fallback || !snapshots[1].Fallback {
		t.Fatalf("request channel attempts = %+v, want primary then fallback", snapshots)
	}
}

func TestDoProviderParsedRequestReturnsFallbackFailureWithoutThirdAttempt(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		_, _ = io.WriteString(w, "primary")
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback")
	}))
	defer fallback.Close()

	errPrimary := errors.New("primary parse failure")
	errFallback := errors.New("fallback parse failure")
	ctx := context.Background()
	flag := new(atomic.Bool)
	var events []SseEvent
	err := doProviderParsedRequest(
		ctx,
		ModelInfo{
			BaseURL: primary.URL,
			APIKey:  "primary-key",
			Fallback: &ChannelCreds{
				BaseURL: fallback.URL,
				APIKey:  "fallback-key",
			},
		},
		flag,
		providerParsedTestBuild(ctx),
		func(resp *http.Response, emit func(SseEvent)) error {
			body, readErr := providerParsedTestBody(resp)
			if readErr != nil {
				return readErr
			}
			switch body {
			case "primary":
				emit(SseEvent{Type: "text_delta", Text: "discarded-primary"})
				return errPrimary
			case "fallback":
				emit(SseEvent{Type: "text_delta", Text: "visible-fallback-partial"})
				return errFallback
			default:
				return fmt.Errorf("unexpected test body %q", body)
			}
		},
		func(ev SseEvent) { events = append(events, ev) },
	)
	if !errors.Is(err, errFallback) {
		t.Fatalf("error = %v, want fallback parse failure", err)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
		t.Fatalf("request counts primary/fallback = %d/%d, want exactly 1/1", primaryHits.Load(), fallbackHits.Load())
	}
	if !flag.Load() {
		t.Fatal("FallbackUsed must remain true when the fallback also fails")
	}
	if len(events) != 1 || events[0].Text != "visible-fallback-partial" {
		t.Fatalf("committed events = %+v, want only the live fallback partial", events)
	}
}

func TestDoProviderParsedRequestDoesNotFallbackOnContextErrors(t *testing.T) {
	tests := []struct {
		name      string
		wantError error
		cancel    bool
	}{
		{name: "canceled", wantError: context.Canceled, cancel: true},
		{name: "deadline exceeded", wantError: context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var primaryHits atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				primaryHits.Add(1)
				_, _ = io.WriteString(w, "primary")
			}))
			defer primary.Close()

			var fallbackHits atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackHits.Add(1)
				_, _ = io.WriteString(w, "fallback")
			}))
			defer fallback.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			flag := new(atomic.Bool)
			var events []SseEvent
			err := doProviderParsedRequest(
				ctx,
				ModelInfo{
					BaseURL: primary.URL,
					APIKey:  "primary-key",
					Fallback: &ChannelCreds{
						BaseURL: fallback.URL,
						APIKey:  "fallback-key",
					},
				},
				flag,
				providerParsedTestBuild(ctx),
				func(resp *http.Response, emit func(SseEvent)) error {
					body, readErr := providerParsedTestBody(resp)
					if readErr != nil {
						return readErr
					}
					emit(SseEvent{Type: "text_delta", Text: body + "-partial"})
					if tc.cancel {
						cancel()
					}
					return tc.wantError
				},
				func(ev SseEvent) { events = append(events, ev) },
			)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v, want %v", err, tc.wantError)
			}
			if primaryHits.Load() != 1 || fallbackHits.Load() != 0 {
				t.Fatalf("request counts primary/fallback = %d/%d, want 1/0", primaryHits.Load(), fallbackHits.Load())
			}
			if flag.Load() {
				t.Fatal("FallbackUsed was set for a caller context error")
			}
			if len(events) != 1 || events[0].Text != "primary-partial" {
				t.Fatalf("committed events = %+v, want the primary partial to be flushed", events)
			}
		})
	}
}

func TestDoProviderParsedRequestKeepsFallbackStickyAcrossRounds(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		_, _ = io.WriteString(w, "primary")
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback")
	}))
	defer fallback.Close()

	errPrimary := errors.New("primary failed")
	ctx := context.Background()
	flag := new(atomic.Bool)
	model := ModelInfo{
		BaseURL: primary.URL,
		APIKey:  "primary-key",
		Fallback: &ChannelCreds{
			BaseURL: fallback.URL,
			APIKey:  "fallback-key",
		},
	}
	build := providerParsedTestBuild(ctx)
	var events []SseEvent
	consume := func(resp *http.Response, emit func(SseEvent)) error {
		body, readErr := providerParsedTestBody(resp)
		if readErr != nil {
			return readErr
		}
		if body == "primary" {
			return errPrimary
		}
		emit(SseEvent{Type: "text_delta", Text: body})
		return nil
	}
	emit := func(ev SseEvent) { events = append(events, ev) }

	if err := doProviderParsedRequest(ctx, model, flag, build, consume, emit); err != nil {
		t.Fatalf("first round: %v", err)
	}
	if err := doProviderParsedRequest(ctx, model, flag, build, consume, emit); err != nil {
		t.Fatalf("second round: %v", err)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 2 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/2", primaryHits.Load(), fallbackHits.Load())
	}
	if !flag.Load() {
		t.Fatal("FallbackUsed was cleared between sticky rounds")
	}
	if len(events) != 2 || events[0].Text != "fallback" || events[1].Text != "fallback" {
		t.Fatalf("events = %+v, want one fallback event from each round", events)
	}
}

func TestDoProviderParsedRequestFallsBackWhenPrimaryRequestCannotBeBuilt(t *testing.T) {
	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback-after-build-error")
	}))
	defer fallback.Close()

	ctx := context.Background()
	flag := new(atomic.Bool)
	var events []SseEvent
	err := doProviderParsedRequest(ctx, ModelInfo{
		BaseURL: "://invalid-primary-url",
		APIKey:  "primary-key",
		Fallback: &ChannelCreds{
			BaseURL: fallback.URL,
			APIKey:  "fallback-key",
		},
	}, flag, providerParsedTestBuild(ctx), func(resp *http.Response, emit func(SseEvent)) error {
		body, readErr := providerParsedTestBody(resp)
		if readErr != nil {
			return readErr
		}
		emit(SseEvent{Type: "text_delta", Text: body})
		return nil
	}, func(ev SseEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("doProviderParsedRequest: %v", err)
	}
	if fallbackHits.Load() != 1 || !flag.Load() {
		t.Fatalf("fallback hits/flag = %d/%v, want 1/true", fallbackHits.Load(), flag.Load())
	}
	if len(events) != 1 || events[0].Text != "fallback-after-build-error" {
		t.Fatalf("events = %+v, want fallback response", events)
	}
}

func TestProviderUsesFallbackWhenPrimaryKeyIsEmpty(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		http.Error(w, "primary must not be called", http.StatusUnauthorized)
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"fallback without primary key"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer fallback.Close()

	flag := new(atomic.Bool)
	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{
			RequestID: "gpt-test",
			BaseURL:   primary.URL,
			Fallback: &ChannelCreds{
				BaseURL: fallback.URL,
				APIKey:  "fallback-key",
			},
		},
		History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		FallbackUsed: flag,
	}, nil, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if primaryHits.Load() != 0 || fallbackHits.Load() != 1 || !flag.Load() {
		t.Fatalf("primary/fallback/flag = %d/%d/%v, want 0/1/true", primaryHits.Load(), fallbackHits.Load(), flag.Load())
	}
	if result == nil || len(result.Blocks) != 1 || result.Blocks[0].Text != "fallback without primary key" {
		t.Fatalf("result = %+v, want fallback answer", result)
	}
}

type countingFallbackToolRunner struct {
	calls atomic.Int32
}

func (r *countingFallbackToolRunner) Run(context.Context, string, []byte) (string, []Citation, error) {
	r.calls.Add(1)
	return "tool result", nil, nil
}

func TestProviderFallbackRetriesOnlyFailedRoundWithoutRepeatingTools(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		round := primaryHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		if round == 1 {
			_, _ = io.WriteString(w, strings.Join([]string{
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`,
				``,
			}, "\n\n"))
			return
		}
		_, _ = io.WriteString(w, `data: {"error":{"message":"second primary round failed"}}`+"\n\n")
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"fallback final answer"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer fallback.Close()

	flag := new(atomic.Bool)
	runner := &countingFallbackToolRunner{}
	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{
			RequestID: "gpt-test",
			BaseURL:   primary.URL,
			APIKey:    "primary-key",
			Fallback: &ChannelCreds{
				BaseURL: fallback.URL,
				APIKey:  "fallback-key",
			},
		},
		History: []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "use a tool"}}}},
		Tools: []ToolDef{{
			Name:        "lookup",
			Description: "lookup",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		FallbackUsed: flag,
	}, runner, func(SseEvent) {})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if primaryHits.Load() != 2 || fallbackHits.Load() != 1 {
		t.Fatalf("primary/fallback requests = %d/%d, want 2/1", primaryHits.Load(), fallbackHits.Load())
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("tool executions = %d, want exactly 1", runner.calls.Load())
	}
	if !flag.Load() {
		t.Fatal("FallbackUsed was not set for the failed second round")
	}
	if result == nil || len(result.Blocks) != 2 || result.Blocks[0].Kind != "tool_call" || result.Blocks[1].Text != "fallback final answer" {
		t.Fatalf("result blocks = %+v, want one tool round followed by fallback answer", result)
	}
}

func TestOpenAIResponsesFallsBackAfterResponseFailedEvent(t *testing.T) {
	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"discarded primary"}`,
			`data: {"type":"response.failed","response":{"error":{"message":"primary stream failed"}}}`,
			``,
		}, "\n\n"))
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"fallback answer"}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2},"output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"fallback answer"}]}]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n"))
	}))
	defer fallback.Close()

	flag := new(atomic.Bool)
	var events []SseEvent
	result, err := (&OpenAIProvider{}).Stream(context.Background(), UnifiedChatRequest{
		Model: ModelInfo{
			RequestID: "gpt-test",
			BaseURL:   primary.URL,
			APIKey:    "primary-key",
			APIFormat: "responses",
			Fallback: &ChannelCreds{
				BaseURL: fallback.URL,
				APIKey:  "fallback-key",
			},
		},
		History:      []UnifiedMessage{{Role: "user", Blocks: []UnifiedBlock{{Kind: "text", Text: "hello"}}}},
		FallbackUsed: flag,
	}, nil, func(ev SseEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 1 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/1", primaryHits.Load(), fallbackHits.Load())
	}
	if !flag.Load() {
		t.Fatal("FallbackUsed was not set after response.failed")
	}
	if result == nil || len(result.Blocks) != 1 || result.Blocks[0].Kind != "text" || result.Blocks[0].Text != "fallback answer" {
		t.Fatalf("result blocks = %+v, want only the fallback answer", result)
	}
	if len(events) != 1 || events[0].Type != "text_delta" || events[0].Text != "fallback answer" {
		t.Fatalf("streamed events = %+v, want only the fallback answer", events)
	}
}
