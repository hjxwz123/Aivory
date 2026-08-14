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
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("provider status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func TestDoProviderParsedRequestDoesNotFallbackAfterVisiblePrimaryEvent(t *testing.T) {
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
	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
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
		observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible),
	)
	if !errors.Is(err, errPrimaryParse) {
		t.Fatalf("error = %v, want primary parse failure", err)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 0 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/0", primaryHits.Load(), fallbackHits.Load())
	}
	if flag.Load() {
		t.Fatal("FallbackUsed was set after primary output was already visible")
	}
	if !visible.Load() {
		t.Fatal("visible output was not committed by the primary text delta")
	}
	if len(events) != 2 || events[0].Text != "discarded-primary-prefix" || events[1].Text != "discarded-primary-thinking" {
		t.Fatalf("committed events = %+v, want both primary partial events", events)
	}
}

func TestDoProviderParsedRequestFallsBackBeforeVisibleOutput(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "primary")
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback")
	}))
	defer fallback.Close()

	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
	flag := new(atomic.Bool)
	var events []SseEvent
	err := doProviderParsedRequest(ctx, ModelInfo{
		BaseURL: primary.URL,
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
		if body == "primary" {
			emit(SseEvent{Type: "message_start", MessageID: "metadata-only"})
			emit(SseEvent{Type: "citation", Citation: &Citation{Title: "not committed"}})
			emit(SseEvent{Type: "text_delta"})
			emit(SseEvent{Type: "thinking_delta"})
			return errors.New("primary failed before visible output")
		}
		emit(SseEvent{Type: "text_delta", Text: "fallback answer"})
		return nil
	}, observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible))
	if err != nil {
		t.Fatalf("doProviderParsedRequest: %v", err)
	}
	if fallbackHits.Load() != 1 || !flag.Load() {
		t.Fatalf("fallback hits/flag = %d/%v, want 1/true", fallbackHits.Load(), flag.Load())
	}
	if len(events) != 1 || events[0].Text != "fallback answer" {
		t.Fatalf("events = %+v, want only fallback answer", events)
	}
}

func TestDoProviderParsedRequestSuppressedTextDoesNotCommitOutput(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "primary")
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback")
	}))
	defer fallback.Close()

	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
	ctx = contextWithProviderTextDeltaVisibility(ctx, false)
	var events []SseEvent
	downstream := observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible)
	suppressText := func(ev SseEvent) {
		if ev.Type != "text_delta" {
			downstream(ev)
		}
	}
	err := doProviderParsedRequest(ctx, ModelInfo{
		BaseURL: primary.URL,
		APIKey:  "primary-key",
		Fallback: &ChannelCreds{
			BaseURL: fallback.URL,
			APIKey:  "fallback-key",
		},
	}, new(atomic.Bool), providerParsedTestBuild(ctx), func(resp *http.Response, emit func(SseEvent)) error {
		body, readErr := providerParsedTestBody(resp)
		if readErr != nil {
			return readErr
		}
		if body == "primary" {
			emit(SseEvent{Type: "citation", Citation: &Citation{Title: "primary metadata"}})
			emit(SseEvent{Type: "text_delta", Text: "suppressed primary text"})
			return errors.New("primary failed after suppressed text")
		}
		emit(SseEvent{Type: "thinking_delta", Text: "fallback visible thought"})
		return nil
	}, suppressText)
	if err != nil {
		t.Fatalf("doProviderParsedRequest: %v", err)
	}
	if fallbackHits.Load() != 1 {
		t.Fatalf("fallback hits = %d, want 1", fallbackHits.Load())
	}
	if len(events) != 1 || events[0].Type != "thinking_delta" || events[0].Text != "fallback visible thought" {
		t.Fatalf("events = %+v, want only fallback thought", events)
	}
}

func TestDoProviderParsedRequestVisibleCommitEventsDisableFallback(t *testing.T) {
	tests := []struct {
		name  string
		event SseEvent
	}{
		{name: "text delta", event: SseEvent{Type: "text_delta", Text: "visible text"}},
		{name: "thinking delta", event: SseEvent{Type: "thinking_delta", Text: "visible thought"}},
		{name: "tool start", event: SseEvent{Type: "tool_start", ID: "tool-1", Name: "lookup"}},
		{name: "tool input", event: SseEvent{Type: "tool_input", ID: "tool-1", PartialJson: `{"q":"x"}`}},
		{name: "tool result", event: SseEvent{Type: "tool_result", ID: "tool-1", Status: "complete"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "primary")
			}))
			defer primary.Close()

			var fallbackHits atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackHits.Add(1)
				_, _ = io.WriteString(w, "fallback")
			}))
			defer fallback.Close()

			visible := new(atomic.Bool)
			ctx := contextWithProviderVisibleOutput(context.Background(), visible)
			var events []SseEvent
			errPrimary := errors.New("primary failed after commit")
			err := doProviderParsedRequest(ctx, ModelInfo{
				BaseURL: primary.URL,
				APIKey:  "primary-key",
				Fallback: &ChannelCreds{
					BaseURL: fallback.URL,
					APIKey:  "fallback-key",
				},
			}, new(atomic.Bool), providerParsedTestBuild(ctx), func(resp *http.Response, emit func(SseEvent)) error {
				if _, readErr := providerParsedTestBody(resp); readErr != nil {
					return readErr
				}
				emit(tc.event)
				if len(events) != 1 {
					t.Fatalf("event was not streamed immediately: %+v", events)
				}
				return errPrimary
			}, observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible))
			if !errors.Is(err, errPrimary) {
				t.Fatalf("error = %v, want primary failure", err)
			}
			if fallbackHits.Load() != 0 {
				t.Fatalf("fallback hits = %d, want 0", fallbackHits.Load())
			}
			if !visible.Load() {
				t.Fatal("event did not commit visible output")
			}
		})
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
	visible := new(atomic.Bool)
	ctx := contextWithProviderRequestRecorder(context.Background(), recorder)
	ctx = contextWithProviderVisibleOutput(ctx, visible)
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
		observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible),
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
	if !strings.Contains(snapshots[0].Error, "primary unavailable") {
		t.Fatalf("primary failure was not captured: %+v", snapshots[0])
	}
	if snapshots[1].Error != "" {
		t.Fatalf("successful fallback was marked failed: %+v", snapshots[1])
	}
}

func TestDoProviderParsedRequestFallsBackOnEveryNon200Status(t *testing.T) {
	statuses := []int{
		http.StatusCreated,
		http.StatusAccepted,
		http.StatusNoContent,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	}
	for _, status := range statuses {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer primary.Close()

			var fallbackHits atomic.Int32
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fallbackHits.Add(1)
				_, _ = io.WriteString(w, "fallback-ok")
			}))
			defer fallback.Close()

			visible := new(atomic.Bool)
			ctx := contextWithProviderVisibleOutput(context.Background(), visible)
			var events []SseEvent
			err := doProviderParsedRequest(ctx, ModelInfo{
				BaseURL: primary.URL,
				APIKey:  "primary-key",
				Fallback: &ChannelCreds{
					BaseURL: fallback.URL,
					APIKey:  "fallback-key",
				},
			}, new(atomic.Bool), providerParsedTestBuild(ctx), func(resp *http.Response, emit func(SseEvent)) error {
				if statusErr := requireProviderSuccess(resp, "test"); statusErr != nil {
					return statusErr
				}
				body, readErr := io.ReadAll(resp.Body)
				if readErr != nil {
					return readErr
				}
				emit(SseEvent{Type: "text_delta", Text: string(body)})
				return nil
			}, observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible))
			if err != nil {
				t.Fatalf("doProviderParsedRequest: %v", err)
			}
			if fallbackHits.Load() != 1 {
				t.Fatalf("fallback hits = %d, want 1", fallbackHits.Load())
			}
			if len(events) != 1 || events[0].Text != "fallback-ok" {
				t.Fatalf("events = %+v, want fallback response", events)
			}
		})
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

func TestDoProviderParsedRequestStreamsFallbackPartialWithoutThirdAttempt(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "primary")
	}))
	defer primary.Close()

	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHits.Add(1)
		_, _ = io.WriteString(w, "fallback")
	}))
	defer fallback.Close()

	errPrimary := errors.New("primary failed before visible output")
	errFallback := errors.New("fallback failed after visible output")
	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
	flag := new(atomic.Bool)
	var events []SseEvent
	err := doProviderParsedRequest(ctx, ModelInfo{
		BaseURL: primary.URL,
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
		if body == "primary" {
			emit(SseEvent{Type: "citation", Citation: &Citation{Title: "discarded primary metadata"}})
			return errPrimary
		}
		emit(SseEvent{Type: "text_delta", Text: "visible fallback partial"})
		return errFallback
	}, observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible))
	if !errors.Is(err, errFallback) {
		t.Fatalf("error = %v, want fallback failure", err)
	}
	if fallbackHits.Load() != 1 || !flag.Load() {
		t.Fatalf("fallback hits/flag = %d/%v, want 1/true", fallbackHits.Load(), flag.Load())
	}
	if !visible.Load() {
		t.Fatal("fallback partial did not commit visible output")
	}
	if len(events) != 1 || events[0].Text != "visible fallback partial" {
		t.Fatalf("events = %+v, want only fallback partial", events)
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

			baseCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			recorder := newProviderRequestRecorder()
			visible := new(atomic.Bool)
			ctx := contextWithProviderRequestRecorder(baseCtx, recorder)
			ctx = contextWithProviderVisibleOutput(ctx, visible)
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
				observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible),
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
			for _, snapshot := range recorder.snapshots() {
				if snapshot.Error != "" {
					t.Fatalf("caller cancellation/deadline was logged as a channel error: %+v", snapshot)
				}
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
	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
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
	emit := observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible)

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

	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
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
	}, observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible))
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

func TestProviderDoesNotFallbackAfterVisibleToolRound(t *testing.T) {
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
	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
	result, err := (&OpenAIProvider{}).Stream(ctx, UnifiedChatRequest{
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
	}, runner, observeProviderVisibleOutput(func(SseEvent) {}, visible))
	if err == nil || !strings.Contains(err.Error(), "second primary round failed") {
		t.Fatalf("Stream error = %v, want second primary round failure", err)
	}
	if primaryHits.Load() != 2 || fallbackHits.Load() != 0 {
		t.Fatalf("primary/fallback requests = %d/%d, want 2/0", primaryHits.Load(), fallbackHits.Load())
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("tool executions = %d, want exactly 1", runner.calls.Load())
	}
	if flag.Load() {
		t.Fatal("FallbackUsed was set after a visible tool round")
	}
	if !visible.Load() {
		t.Fatal("tool events did not commit visible output")
	}
	if result == nil || len(result.Blocks) != 2 || result.Blocks[0].Kind != "tool_call" || result.Blocks[1].Kind != "tool_output" {
		t.Fatalf("result blocks = %+v, want the completed tool round as partial output", result)
	}
}

func TestOpenAIResponsesDoesNotFallbackAfterVisibleDelta(t *testing.T) {
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
	visible := new(atomic.Bool)
	ctx := contextWithProviderVisibleOutput(context.Background(), visible)
	var events []SseEvent
	result, err := (&OpenAIProvider{}).Stream(ctx, UnifiedChatRequest{
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
	}, nil, observeProviderVisibleOutput(func(ev SseEvent) { events = append(events, ev) }, visible))
	if err == nil || !strings.Contains(err.Error(), "primary stream failed") {
		t.Fatalf("Stream error = %v, want primary stream failure", err)
	}
	if primaryHits.Load() != 1 || fallbackHits.Load() != 0 {
		t.Fatalf("request counts primary/fallback = %d/%d, want 1/0", primaryHits.Load(), fallbackHits.Load())
	}
	if flag.Load() {
		t.Fatal("FallbackUsed was set after a visible response delta")
	}
	if result == nil || len(result.Blocks) != 1 || result.Blocks[0].Kind != "text" || result.Blocks[0].Text != "discarded primary" {
		t.Fatalf("result blocks = %+v, want the visible primary partial", result)
	}
	if len(events) != 1 || events[0].Type != "text_delta" || events[0].Text != "discarded primary" {
		t.Fatalf("streamed events = %+v, want only the primary partial", events)
	}
}
