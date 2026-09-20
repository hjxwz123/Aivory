package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const noulResponse = `{"model":"jev-1.13.0","answers":{"yes":{"type":"noul","noul":0}},"usage":{"input_tokens":100,"output_tokens":10}}`

func request() Request {
	return Request{State: "private user text", Questions: map[string]Question{"yes": NewNoul("Is this relevant?", nil)}}
}

func testClient(t *testing.T, handler http.HandlerFunc, adjust func(*Config)) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := Config{APIKey: "secret-key", BaseURL: srv.URL, Timeout: time.Second, RetryBaseDelay: time.Millisecond}
	if adjust != nil {
		adjust(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMixedQuestionsStructuredJSONAndVersion(t *testing.T) {
	var logs bytes.Buffer
	var record Record
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("unexpected request method/path/auth")
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("local-user")) || bytes.Contains(body, []byte("task.test")) {
			t.Error("local metadata leaked upstream")
		}
		var req Request
		if err := json.Unmarshal(body, &req); err != nil || req.Model != "jev-latest" || len(req.Questions) != 3 {
			t.Errorf("unexpected request: %s", body)
		}
		w.Header().Set("X-Request-ID", "req-123")
		fmt.Fprint(w, `{"model":"jev-1.13.0","answers":{
		 "route":{"type":"choice","choice":"none","confidence":0.8,"probabilities":{"none":0.9,"web":0.1}},
		 "quality":{"type":"score","score":0.2,"confidence":0.5,"probabilities":{"0":0.8,"1":0.2},"legend":{"0":{"description":"low"},"1":"high"}},
		 "yes":{"type":"noul","noul":0}
		},"usage":{"input_tokens":123,"output_tokens":45}}`)
	}, func(cfg *Config) {
		cfg.Model = "jev-latest"
		cfg.Logger = log.New(&logs, "", 0)
		cfg.Recorder = func(_ context.Context, r Record) error { record = r; return nil }
	})
	req := request()
	req.State = map[string]any{"message": "private user text", "count": 1}
	req.Questions["route"] = NewChoice(map[string]any{"question": "Which route?"}, map[string]any{"none": nil, "web": []string{"search"}})
	req.Questions["quality"] = NewScore("Quality?", []any{map[string]any{"description": "low"}, "high"})
	result, err := c.Evaluate(context.Background(), req, Options{Metadata: Metadata{UserID: "local-user", Purpose: "task.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != DefaultModel || result.RequestedModel != "jev-latest" || result.RequestID != "req-123" || *result.Answers["yes"].Noul != 0 || result.Answers["yes"].Confidence != nil {
		t.Fatalf("incorrect result: %+v", result)
	}
	if record.Attempts != 1 || !record.UsageKnown || record.Usage.InputTokens != 123 || record.ServedModel != DefaultModel {
		t.Fatalf("incorrect record: %+v", record)
	}
	for _, secret := range []string{"private user text", "secret-key", "Which route?", "Quality?"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("sensitive text in logs: %s", secret)
		}
	}
}

func TestInvalidRequestsNeverReachServer(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }, nil)
	tests := []Request{
		{State: nil, Questions: request().Questions},
		{State: 123, Questions: request().Questions},
		{State: "test"},
		{State: make(chan int), Questions: request().Questions},
		{State: "test", Questions: map[string]Question{"x": {Type: "invalid"}}},
		{State: "test", Questions: map[string]Question{"x": NewChoice("q", nil)}},
		{State: "test", Questions: map[string]Question{"x": NewScore("q", []any{"only one"})}},
		{State: "test", Questions: map[string]Question{"x": NewNoul("q", map[string]any{"wrong": "yes"})}},
	}
	for i, req := range tests {
		if _, err := c.Evaluate(context.Background(), req, Options{}); KindOf(err) != ErrValidation {
			t.Errorf("case %d: expected validation error, got %v", i, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid requests sent upstream")
	}
}

func TestResponseValidationRetainsUsage(t *testing.T) {
	tests := []struct {
		name, body string
		kind       ErrorKind
		usage      bool
	}{
		{"missing answer", strings.Replace(noulResponse, `"yes":`, `"wrong":`, 1), ErrResponse, true},
		{"missing value", strings.Replace(noulResponse, `,"noul":0`, ``, 1), ErrResponse, true},
		{"range", strings.Replace(noulResponse, `"noul":0`, `"noul":1.5`, 1), ErrResponse, true},
		{"wrong type", strings.Replace(noulResponse, `"type":"noul"`, `"type":"choice"`, 1), ErrResponse, true},
		{"wrong value type", strings.Replace(noulResponse, `"noul":0`, `"noul":"yes"`, 1), ErrResponse, true},
		{"version drift", strings.Replace(noulResponse, DefaultModel, "jev-1.14.0", 1), ErrModelVersion, true},
		{"missing usage", `{"model":"jev-1.13.0","answers":{}}`, ErrResponse, false},
		{"null token count", strings.Replace(noulResponse, `"input_tokens":100`, `"input_tokens":null`, 1), ErrResponse, false},
		{"negative usage", strings.Replace(noulResponse, `"input_tokens":100`, `"input_tokens":-1`, 1), ErrResponse, false},
		{"malformed JSON", `{`, ErrResponse, false},
		{"trailing JSON", noulResponse + `{}`, ErrResponse, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var record Record
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }, func(cfg *Config) {
				cfg.Recorder = func(_ context.Context, r Record) error { record = r; return nil }
			})
			out, err := c.Evaluate(context.Background(), request(), Options{})
			if KindOf(err) != tc.kind || record.UsageKnown != tc.usage || record.Attempts != 1 {
				t.Fatalf("err=%v record=%+v", err, record)
			}
			if out != nil && len(out.Answers) != 0 {
				t.Fatal("invalid answers remain available")
			}
		})
	}
}

func TestRetriesAndPermanentErrors(t *testing.T) {
	for _, status := range []int{429, 529, 502, 503, 504, 401, 403, 422, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			var record Record
			var logs bytes.Buffer
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":"private user text secret-key"}`)
					return
				}
				fmt.Fprint(w, noulResponse)
			}, func(cfg *Config) {
				cfg.MaxRetries = 1
				cfg.Logger = log.New(&logs, "", 0)
				cfg.Recorder = func(_ context.Context, r Record) error { record = r; return nil }
			})
			_, err := c.Evaluate(context.Background(), request(), Options{})
			want := int32(1)
			if retryable(status) {
				want = 2
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("expected HTTP error")
			}
			if calls.Load() != want || record.Attempts != int(want) {
				t.Fatalf("calls=%d record=%+v", calls.Load(), record)
			}
			if strings.Contains(logs.String()+fmt.Sprint(err), "private user text") || strings.Contains(logs.String()+fmt.Sprint(err), "secret-key") {
				t.Fatal("upstream error body leaked")
			}
		})
	}
}

func TestDeadlineBoundsBackoffAndBody(t *testing.T) {
	for _, slowBody := range []bool{false, true} {
		t.Run(fmt.Sprint(slowBody), func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !slowBody {
					w.Header().Set("Retry-After", "120")
					w.WriteHeader(429)
					return
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}, func(cfg *Config) { cfg.MaxRetries = 2 })
			started := time.Now()
			_, err := c.Evaluate(context.Background(), request(), Options{Timeout: 40 * time.Millisecond})
			if KindOf(err) != ErrTimeout || !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 || time.Since(started) > time.Second {
				t.Fatalf("deadline failure: %v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestCancellationRecordingErrorAndResponseLimit(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, noulResponse) }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Evaluate(ctx, request(), Options{}); !errors.Is(err, context.Canceled) || c.Stats().Attempts != 0 {
		t.Fatalf("pre-canceled request: %v", err)
	}
	recorderErr := errors.New("database unavailable")
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, noulResponse) }, func(cfg *Config) {
		cfg.Recorder = func(ctx context.Context, r Record) error { return recorderErr }
	})
	out, err := c.Evaluate(context.Background(), request(), Options{})
	if out == nil || KindOf(err) != ErrRecording || !errors.Is(err, recorderErr) || c.Stats().InputTokens != 100 || c.Stats().Attempts != 1 {
		t.Fatalf("recording failure lost result or caused retry: %v", err)
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, noulResponse) }, func(cfg *Config) { cfg.MaxResponseBytes = 10 })
	if _, err := c.Evaluate(context.Background(), request(), Options{}); KindOf(err) != ErrResponse {
		t.Fatalf("expected response limit error: %v", err)
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }, nil)
	if _, err := c.Evaluate(context.Background(), request(), Options{}); KindOf(err) != ErrHTTP || forwarded.Load() != 0 {
		t.Fatalf("redirect followed or not reported: %v", err)
	}
}

func TestConcurrentUsageStats(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, noulResponse) }, nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := c.Evaluate(context.Background(), request(), Options{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	s := c.Stats()
	if s.Calls != 20 || s.Attempts != 20 || s.Failures != 0 || s.InputTokens != 2000 || s.OutputTokens != 200 {
		t.Fatalf("incorrect concurrent stats: %+v", s)
	}
}

func TestRetryAfterFormats(t *testing.T) {
	if retryDelay("2", 0, time.Millisecond) < 2*time.Second {
		t.Fatal("ignored seconds")
	}
	if retryDelay(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), 0, time.Millisecond) < 58*time.Second {
		t.Fatal("ignored HTTP date")
	}
	if retryDelay("9223372036854775807", 0, time.Millisecond) < time.Hour {
		t.Fatal("overflowed large delay")
	}
}

func TestChoiceAndScoreRejectMalformedAnswers(t *testing.T) {
	choice := `{"type":"choice","choice":"a","confidence":0.5,"probabilities":{"a":0.7,"b":0.3}}`
	score := `{"type":"score","score":0.3,"confidence":0.5,"probabilities":{"0":0.7,"1":0.3},"legend":{"0":"low","1":"high"}}`
	for _, tc := range []struct {
		name   string
		q      Question
		answer string
	}{
		{"unknown choice", NewChoice("q", map[string]any{"a": nil, "b": nil}), strings.Replace(choice, `"choice":"a"`, `"choice":"c"`, 1)},
		{"missing confidence", NewChoice("q", map[string]any{"a": nil, "b": nil}), strings.Replace(choice, `,"confidence":0.5`, ``, 1)},
		{"invalid confidence", NewChoice("q", map[string]any{"a": nil, "b": nil}), strings.Replace(choice, `"confidence":0.5`, `"confidence":-1`, 1)},
		{"null probability", NewChoice("q", map[string]any{"a": nil, "b": nil}), strings.Replace(choice, `"b":0.3`, `"b":null`, 1)},
		{"missing probability", NewChoice("q", map[string]any{"a": nil, "b": nil}), strings.Replace(choice, `,"b":0.3`, ``, 1)},
		{"invalid distribution", NewChoice("q", map[string]any{"a": nil, "b": nil}), strings.Replace(choice, `"b":0.3`, `"b":0.8`, 1)},
		{"score out of range", NewScore("q", []any{"low", "high"}), strings.Replace(score, `"score":0.3`, `"score":2`, 1)},
		{"missing legend", NewScore("q", []any{"low", "high"}), strings.Replace(score, `,"legend":{"0":"low","1":"high"}`, ``, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"model":"jev-1.13.0","answers":{"check":%s},"usage":{"input_tokens":10,"output_tokens":1}}`, tc.answer)
			}, nil)
			out, err := c.Evaluate(context.Background(), Request{State: "text", Questions: map[string]Question{"check": tc.q}}, Options{})
			if KindOf(err) != ErrResponse || out == nil || len(out.Answers) != 0 || out.Usage.InputTokens != 10 {
				t.Fatalf("out=%+v err=%v", out, err)
			}
		})
	}
}

func TestStateShapesAndExplicitVersionPolicy(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, noulResponse) }, nil)
	for _, state := range []any{"text", []string{"one", "two"}, map[string]string{"message": "text"}, struct {
		Message string `json:"message"`
	}{"text"}, json.RawMessage(`{"message":"text"}`)} {
		req := request()
		req.State = state
		if _, err := c.Evaluate(context.Background(), req, Options{}); err != nil {
			t.Fatal(err)
		}
	}
	req := request()
	req.Model = "jev-latest"
	if _, err := c.Evaluate(context.Background(), req, Options{ExpectedModel: "jev-1.14.0"}); KindOf(err) != ErrModelVersion {
		t.Fatalf("alias guard: %v", err)
	}
	if _, err := c.Evaluate(context.Background(), request(), Options{ExpectedModel: "jev-1.14.0"}); KindOf(err) != ErrValidation {
		t.Fatalf("conflicting pin: %v", err)
	}
}

func TestClientConfiguration(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{APIKey: "key", BaseURL: "file:///tmp/api"},
		{APIKey: "key", BaseURL: "https://user:pass@example.test"},
		{APIKey: "key", BaseURL: "https://example.test?secret=key"},
		{APIKey: "key", MaxRetries: 6},
		{APIKey: "key", Timeout: -time.Second},
		{APIKey: "key", MaxResponseBytes: -1},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	for _, base := range []string{"https://example.test", "https://example.test/v1/"} {
		c, err := New(Config{APIKey: "key", BaseURL: base})
		if err != nil || c.endpoint != "https://example.test/v1/systemone" {
			t.Fatalf("endpoint config: %v %v", c, err)
		}
	}
}
