package llm

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderRequestRecorderSanitizesHeadersBodyAndURL(t *testing.T) {
	rec := newProviderRequestRecorder()
	ctx := contextWithProviderRequestRecorder(context.Background(), rec)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://user:pass@api.example/v1/chat?api_key=secret&trace=ok", bytes.NewReader([]byte(`{
		"model": "gpt-x",
		"api_key": "body-secret",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AAAA"}}]}]
	}`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("authorization", "Bearer secret")
	req.Header.Set("content-type", "application/json")

	recordProviderRequest(ctx, req)
	got := rec.snapshot()
	if got.Method != "POST" {
		t.Fatalf("method = %q", got.Method)
	}
	if strings.Contains(got.URL, "secret") || strings.Contains(got.URL, "user:pass") || !strings.Contains(got.URL, "api_key=%5Bredacted%5D") {
		t.Fatalf("url not sanitized: %s", got.URL)
	}
	if strings.Contains(got.Header, "Bearer secret") || !strings.Contains(got.Header, "[redacted]") {
		t.Fatalf("headers not sanitized: %s", got.Header)
	}
	if strings.Contains(got.Body, "body-secret") || !strings.Contains(got.Body, `"api_key": "[redacted]"`) {
		t.Fatalf("body secret not sanitized: %s", got.Body)
	}
	if strings.Contains(got.Body, "AAAA") || !strings.Contains(got.Body, "[redacted base64") {
		t.Fatalf("body media not redacted: %s", got.Body)
	}
}

func TestProviderRequestRecorderRedactsLargeJSONBeforeTruncating(t *testing.T) {
	rec := newProviderRequestRecorder()
	ctx := contextWithProviderRequestRecorder(context.Background(), rec)
	largeBase64 := strings.Repeat("A", providerRequestBodyMaxBytes+2048)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.example/v1/chat", bytes.NewReader([]byte(`{"image":"data:image/png;base64,`+largeBase64+`"}`)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	recordProviderRequest(ctx, req)
	got := rec.snapshot()
	if strings.Contains(got.Body, strings.Repeat("A", 256)) {
		t.Fatalf("large base64 leaked into stored body")
	}
	if !strings.Contains(got.Body, "[redacted base64") {
		t.Fatalf("large base64 was not redacted: %s", got.Body)
	}
}

func TestDoProviderRequestRecorderKeepsBodyReadable(t *testing.T) {
	rec := newProviderRequestRecorder()
	ctx := contextWithProviderRequestRecorder(context.Background(), rec)
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		seen <- buf.String()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := doProviderRequest(ctx, ModelInfo{BaseURL: srv.URL, APIKey: "k"}, new(atomic.Bool), func(baseURL, apiKey string) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat", bytes.NewReader([]byte(`{"hello":"world"}`)))
	})
	if err != nil {
		t.Fatalf("doProviderRequest: %v", err)
	}
	resp.Body.Close()
	if got := <-seen; got != `{"hello":"world"}` {
		t.Fatalf("provider saw body %q", got)
	}
	if got := rec.snapshot().Body; !strings.Contains(got, `"hello": "world"`) {
		t.Fatalf("request body not captured: %s", got)
	}
}
