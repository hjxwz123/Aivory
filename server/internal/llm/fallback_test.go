package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestRetryableUpstreamFailure pins the fallback trigger: transport errors and
// ANY non-2xx status retry on the backup channel; only a 2xx and a caller
// cancellation do NOT (§fallback channel). 4xx is deliberately retryable:
// relay channels answer 400/402/404 for channel-side conditions (quota, model
// not enabled, region) that a backup relay serves fine.
func TestRetryableUpstreamFailure(t *testing.T) {
	resp := func(code int) *http.Response { return &http.Response{StatusCode: code} }
	cases := []struct {
		name string
		resp *http.Response
		err  error
		want bool
	}{
		{"transport error", nil, errors.New("dial tcp: connection refused"), true},
		{"user cancel", nil, context.Canceled, false},
		{"deadline", nil, context.DeadlineExceeded, false},
		{"200 ok", resp(200), nil, false},
		{"400 bad request (relay quota/model errors)", resp(400), nil, true},
		{"401 unauthorized", resp(401), nil, true}, // bad key on primary → other key may work
		{"402 payment required (relay balance)", resp(402), nil, true},
		{"403 forbidden", resp(403), nil, true},
		{"404 model not found on relay", resp(404), nil, true},
		{"429 rate limited", resp(429), nil, true},
		{"500 server error", resp(500), nil, true},
		{"503 unavailable", resp(503), nil, true},
		{"nil resp nil err", nil, nil, true}, // defensive: treat as failure
	}
	for _, c := range cases {
		if got := retryableUpstreamFailure(c.resp, c.err); got != c.want {
			t.Errorf("%s: retryableUpstreamFailure = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestProviderBaseURL(t *testing.T) {
	if got := providerBaseURL("", "https://api.openai.com"); got != "https://api.openai.com" {
		t.Errorf("empty base = %q, want vendor default", got)
	}
	if got := providerBaseURL("https://proxy.example.com/", "https://api.openai.com"); got != "https://proxy.example.com" {
		t.Errorf("trailing slash not trimmed: %q", got)
	}
}

// TestDoProviderRequestFallback drives the retry end-to-end against two test
// servers: a failing primary and a healthy fallback.
func TestDoProviderRequestFallback(t *testing.T) {
	ctx := context.Background()
	build := func(reqBody string) func(baseURL, apiKey string) (*http.Request, error) {
		return func(baseURL, apiKey string) (*http.Request, error) {
			r, e := http.NewRequestWithContext(ctx, "POST", providerBaseURL(baseURL, "https://x")+"/v1/chat", nil)
			if e != nil {
				return nil, e
			}
			r.Header.Set("authorization", "Bearer "+apiKey)
			return r, nil
		}
	}

	// Primary always 500s; fallback returns the key it saw so we can prove the
	// retry used the FALLBACK creds, not the primary's.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, r.Header.Get("authorization"))
	}))
	defer fallback.Close()

	// With a fallback configured, the 500 triggers the retry.
	flag := new(atomic.Bool)
	m := ModelInfo{BaseURL: primary.URL, APIKey: "primary-key",
		Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "fallback-key"}}
	resp, err := doProviderRequest(ctx, m, flag, build("x"))
	if err != nil {
		t.Fatalf("doProviderRequest err: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 (fallback should have served)", resp.StatusCode)
	}
	if string(body) != "Bearer fallback-key" {
		t.Errorf("fallback used the wrong key: %q", string(body))
	}
	if !flag.Load() {
		t.Error("FallbackUsed flag not set after fallback served the request")
	}

	// No fallback configured → the 500 is returned as-is, flag stays false.
	flag2 := new(atomic.Bool)
	m2 := ModelInfo{BaseURL: primary.URL, APIKey: "primary-key"}
	resp2, err2 := doProviderRequest(ctx, m2, flag2, build("x"))
	if err2 != nil {
		t.Fatalf("no-fallback err: %v", err2)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 500 {
		t.Errorf("without fallback, status = %d, want 500 passthrough", resp2.StatusCode)
	}
	if flag2.Load() {
		t.Error("FallbackUsed must stay false when no fallback is configured")
	}
}

// TestDoProviderRequestPrimarySuccess: a healthy primary is used directly, the
// fallback is never touched, and the flag stays false.
func TestDoProviderRequestPrimarySuccess(t *testing.T) {
	ctx := context.Background()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "primary")
	}))
	defer primary.Close()
	fallbackHit := false
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHit = true
		w.WriteHeader(200)
	}))
	defer fallback.Close()

	flag := new(atomic.Bool)
	m := ModelInfo{BaseURL: primary.URL, APIKey: "k",
		Fallback: &ChannelCreds{BaseURL: fallback.URL, APIKey: "k2"}}
	resp, err := doProviderRequest(ctx, m, flag, func(baseURL, apiKey string) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "POST", baseURL+"/x", nil)
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "primary" || flag.Load() || fallbackHit {
		t.Errorf("primary success should not touch fallback: body=%q flag=%v hit=%v", body, flag.Load(), fallbackHit)
	}
}

func TestProviderTTFTWatchdogStartsAtHTTPRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stalled := atomic.Bool{}
	watchdog := newProviderTTFTWatchdog(25*time.Millisecond, cancel, &stalled)
	defer watchdog.stop()
	ctx = contextWithProviderTTFTWatchdog(ctx, watchdog)

	// Local provider work before the upstream HTTP request must not count toward
	// fallback_ttft_sec.
	time.Sleep(40 * time.Millisecond)
	if stalled.Load() || ctx.Err() != nil {
		t.Fatalf("watchdog fired before provider HTTP request: stalled=%v err=%v", stalled.Load(), ctx.Err())
	}

	// Server never writes a body at all (not even a header) within the window —
	// this must still trip the watchdog even though headers alone would arrive
	// fast; only a body byte counts as "first byte" here.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := doProviderRequest(ctx, ModelInfo{BaseURL: srv.URL, APIKey: "k"}, nil, func(baseURL, apiKey string) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "POST", baseURL+"/slow", nil)
	})
	if err == nil {
		t.Fatal("doProviderRequest unexpectedly completed; watchdog should cancel the slow upstream request")
	}
	if !stalled.Load() {
		t.Fatal("watchdog did not mark the upstream request as stalled")
	}
}

// TestProviderTTFTWatchdogDisarmsOnFirstByte pins the behavior this test file's
// sibling above does not cover: ANY response byte disarms the watchdog, even
// one that carries no parseable content (e.g. an SSE keep-alive/framing byte
// sent well before the model's real answer). Regression coverage for the case
// where the watchdog fired despite the upstream — and its own relay's TTFT
// dashboard — having already responded (§ fallback_ttft_sec = first byte, not
// first parsed event).
func TestProviderTTFTWatchdogDisarmsOnFirstByte(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stalled := atomic.Bool{}
	watchdog := newProviderTTFTWatchdog(30*time.Millisecond, cancel, &stalled)
	defer watchdog.stop()
	ctx = contextWithProviderTTFTWatchdog(ctx, watchdog)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One meaningless byte well inside the window, then silence for far
		// longer than the watchdog's timeout — a stalled-after-first-byte
		// stream that the watchdog is NOT supposed to protect against.
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ":")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(150 * time.Millisecond)
	}))
	defer srv.Close()

	resp, err := doProviderRequest(ctx, ModelInfo{BaseURL: srv.URL, APIKey: "k"}, nil, func(baseURL, apiKey string) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "POST", baseURL+"/slow", nil)
	})
	if err != nil {
		t.Fatalf("doProviderRequest err: %v", err)
	}
	buf := make([]byte, 1)
	_, _ = resp.Body.Read(buf) // trigger the wrapped Read so first-byte fires
	resp.Body.Close()

	time.Sleep(60 * time.Millisecond) // well past the 30ms timeout
	if stalled.Load() {
		t.Fatal("watchdog fired after a byte had already arrived — first byte must permanently disarm it")
	}
}
