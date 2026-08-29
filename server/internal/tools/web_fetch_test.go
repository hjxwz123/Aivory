package tools

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// errTransport always fails the round trip, simulating an origin the server
// cannot reach at the network level (black-holed / filtered route).
type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: i/o timeout")
}

type waitForContextTransport struct{}

func (waitForContextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// statusTransport returns a fixed status + body for the direct attempt.
type statusTransport struct {
	code int
	body string
}

func (s statusTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: s.code,
		Status:     http.StatusText(s.code),
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
	}, nil
}

// newFakeReader returns an httptest server standing in for Jina Reader plus a
// counter of requests and the (decoded) request path of the last hit.
func newFakeReader(t *testing.T) (base string, hits *atomic.Int32, lastPath *string) {
	t.Helper()
	hits = new(atomic.Int32)
	var path string
	lastPath = &path
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		path = r.URL.Path
		w.Write([]byte("# Reader content\ncontents served by the fallback"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits, lastPath
}

// toolHittingFakeReader builds a webFetchTool whose direct attempt fails and
// whose reader hop lands on the fake Jina server.
func toolHittingFakeReader(t *testing.T, direct http.RoundTripper, reader http.RoundTripper) *webFetchTool {
	t.Helper()
	if direct == nil {
		direct = errTransport{}
	}
	if reader == nil {
		reader = http.DefaultTransport
	}
	return &webFetchTool{direct: direct, reader: reader}
}

func TestWebFetchJinaFallbackOnDirectNetworkFailure(t *testing.T) {
	base, hits, lastPath := newFakeReader(t)
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", "1")
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", base)

	tool := toolHittingFakeReader(t, errTransport{}, http.DefaultTransport)
	out, _, err := tool.Execute(context.Background(), []byte(`{"url":"http://1.1.1.1/some-page"}`), nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(out, "Reader content") {
		t.Fatalf("expected fallback content, got %q", out)
	}
	if hits.Load() != 1 {
		t.Fatalf("reader hits = %d, want 1", hits.Load())
	}
	if !strings.Contains(*lastPath, "http://1.1.1.1/some-page") {
		t.Fatalf("reader path %q does not carry the target", *lastPath)
	}
}

func TestWebFetchJinaFallbackAfterDirectAttemptTimesOut(t *testing.T) {
	base, hits, _ := newFakeReader(t)
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", "1")
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", base)

	tool := &webFetchTool{
		direct:        waitForContextTransport{},
		reader:        http.DefaultTransport,
		directTimeout: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, _, err := tool.Execute(ctx, []byte(`{"url":"http://1.1.1.1/slow"}`), nil)
	if err != nil {
		t.Fatalf("Execute failed after direct timeout: %v", err)
	}
	if !strings.Contains(out, "Reader content") || hits.Load() != 1 {
		t.Fatalf("fallback output/hits = %q/%d, want reader content/1", out, hits.Load())
	}
}

func TestWebFetchJinaFallbackOnDirectHTTPError(t *testing.T) {
	base, hits, _ := newFakeReader(t)
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", "1")
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", base)

	// A bot-block (403) or server error is exactly when a reader still works.
	tool := &webFetchTool{direct: statusTransport{code: http.StatusForbidden, body: "blocked"}, reader: http.DefaultTransport}
	out, _, err := tool.Execute(context.Background(), []byte(`{"url":"http://1.1.1.1/blocked"}`), nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(out, "Reader content") {
		t.Fatalf("expected fallback content, got %q", out)
	}
	if hits.Load() != 1 {
		t.Fatalf("reader hits = %d, want 1", hits.Load())
	}
}

func TestWebFetchDirectSuccessSkipsFallback(t *testing.T) {
	base, hits, _ := newFakeReader(t)
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", base)

	tool := &webFetchTool{
		direct: statusTransport{
			code: http.StatusOK,
			body: "<html><article><h1>Article title</h1><p>Readable body text.</p></article></html>",
		},
		reader: http.DefaultTransport,
	}
	out, _, err := tool.Execute(context.Background(), []byte(`{"url":"http://1.1.1.1/article"}`), nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(out, "Article title") || !strings.Contains(out, "Readable body text.") {
		t.Fatalf("expected direct extraction, got %q", out)
	}
	if hits.Load() != 0 {
		t.Fatalf("reader hits = %d, want 0 (direct success must not fall back)", hits.Load())
	}
}

func TestWebFetchJinaSkipsPrivateTargets(t *testing.T) {
	base, hits, _ := newFakeReader(t)
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", "1")
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", base)

	tool := toolHittingFakeReader(t, errTransport{}, http.DefaultTransport)
	// RFC1918 target: the SSRF guard must NOT hand it to the reader.
	_, _, err := tool.Execute(context.Background(), []byte(`{"url":"http://192.168.1.10/private"}`), nil)
	if err == nil {
		t.Fatal("expected error for private target")
	}
	if hits.Load() != 0 {
		t.Fatalf("reader hits = %d, want 0 (private target must not reach the reader)", hits.Load())
	}
}

func TestWebFetchJinaFallbackDisabled(t *testing.T) {
	base, hits, _ := newFakeReader(t)
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", "0")
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", base)

	tool := toolHittingFakeReader(t, errTransport{}, http.DefaultTransport)
	_, _, err := tool.Execute(context.Background(), []byte(`{"url":"http://1.1.1.1/x"}`), nil)
	if err == nil {
		t.Fatal("expected error when fallback is disabled")
	}
	if hits.Load() != 0 {
		t.Fatalf("reader hits = %d, want 0 (fallback disabled)", hits.Load())
	}
}

func TestJinaReaderURL(t *testing.T) {
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_URL_MODE", "escaped")
	got, err := jinaReaderURL("https://r.jina.ai", "https://example.com/a?b=c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The target must be path-escaped: a literal `?` would be parsed as the
	// READER's query rather than part of the target URL.
	if want := "https://r.jina.ai/https:%2F%2Fexample.com%2Fa%3Fb=c"; got != want {
		t.Fatalf("jinaReaderURL = %q, want %q", got, want)
	}
	// Trailing slash on the base is trimmed so no double slash appears.
	got, err = jinaReaderURL("https://reader.example/", "https://example.com/x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://reader.example/https:%2F%2Fexample.com%2Fx"; got != want {
		t.Fatalf("jinaReaderURL = %q, want %q", got, want)
	}
	if _, err := jinaReaderURL("ftp://reader.example", "https://example.com/x"); err == nil {
		t.Fatal("expected error for non-http reader base")
	}
}

func TestJinaReaderURLRawMode(t *testing.T) {
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_URL_MODE", "raw")
	got, err := jinaReaderURL("https://markdown.new/", "https://example.com/a?b=c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://markdown.new/https://example.com/a?b=c"; got != want {
		t.Fatalf("jinaReaderURL = %q, want %q", got, want)
	}
}

func TestJinaReaderURLUnknownModeFallsBackToEscaped(t *testing.T) {
	t.Setenv("AIVORY_TOOLS_WEB_FETCH_JINA_URL_MODE", "unsupported")
	got, err := jinaReaderURL("https://reader.example", "https://example.com/a?b=c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://reader.example/https:%2F%2Fexample.com%2Fa%3Fb=c"; got != want {
		t.Fatalf("jinaReaderURL = %q, want %q", got, want)
	}
}

func TestCapExtractedTextRuneSafe(t *testing.T) {
	// CJK input: a byte-slice cap would split the final rune; the rune-safe cap
	// must keep the truncation marker intact at a valid boundary.
	marker := "\n…[truncated]"
	long := strings.Repeat("界", 33000)
	out := capExtractedText(long)
	want := webFetchExtractedTextCharCap + utf8.RuneCountInString(marker)
	if got := utf8.RuneCountInString(out); got != want {
		t.Fatalf("capped length = %d, want %d", got, want)
	}
	if !strings.HasSuffix(out, marker) {
		t.Fatalf("missing truncation marker %q in %q", marker, out[len(out)-20:])
	}
}
