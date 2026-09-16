package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanSnippet(t *testing.T) {
	// A JS-heavy page's nav boilerplate arrives as a multi-line wall — collapse
	// the whitespace and cap the length so the model gets a tight one-liner.
	navDump := "News\nToday's news US Politics World\n\nWeather   Climate change\tScience"
	got := cleanSnippet(navDump)
	if strings.Contains(got, "\n") || strings.Contains(got, "  ") {
		t.Fatalf("snippet not collapsed to single spaces: %q", got)
	}
	if got != "News Today's news US Politics World Weather Climate change Science" {
		t.Fatalf("unexpected collapse: %q", got)
	}
	long := strings.Repeat("字", 500)
	capped := cleanSnippet(long)
	if r := []rune(capped); len(r) > 321 { // 320 + the ellipsis
		t.Fatalf("snippet not capped: %d runes", len(r))
	}
	if !strings.HasSuffix(capped, "…") {
		t.Fatalf("capped snippet should end with an ellipsis: %q", capped[len(capped)-6:])
	}
}

func TestFormatUnresponsiveEngines(t *testing.T) {
	got := formatUnresponsiveEngines([][]any{{"google", "timeout"}, {"bing", "CAPTCHA", false}, {"lonely"}})
	if want := "google (timeout), bing (CAPTCHA), lonely"; got != want {
		t.Fatalf("formatUnresponsiveEngines = %q, want %q", got, want)
	}
	if formatUnresponsiveEngines(nil) != "" {
		t.Fatal("nil entries should render empty")
	}
}

func TestParseSearchEngines(t *testing.T) {
	got := parseSearchEngines(" Bing, ddg bing\twikipedia ")
	want := []string{"bing", "ddg", "wikipedia"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseSearchEngines = %#v, want %#v", got, want)
	}
	if got := parseSearchEngines("   "); len(got) != 0 {
		t.Fatalf("blank selection = %#v, want empty", got)
	}
}

func TestSearxngSearchUsesSelectedEngines(t *testing.T) {
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.URL.Query().Get("engines")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Bing result","url":"https://example.test","content":"snippet"}]}`))
	}))
	defer srv.Close()

	s := &searxngSearcher{baseURL: srv.URL, engines: []string{"bing", "wikipedia"}}
	if _, _, err := s.Search(context.Background(), "anything", 5); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if received != "bing,wikipedia" {
		t.Fatalf("engines query = %q, want %q", received, "bing,wikipedia")
	}
}

// A 200 with empty results but failed engines is a real failure (self-hosted
// SearXNG's engines are routinely IP-blocked / rate-limited) — surface WHICH
// engines failed instead of a bland "no results" the model reads as a genuine
// empty query.
func TestSearxngEmptyResultsSurfacesFailedEngines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[["google","timeout"],["bing","CAPTCHA"]]}`))
	}))
	defer srv.Close()

	s := &searxngSearcher{baseURL: srv.URL}
	_, _, err := s.Search(context.Background(), "anything", 5)
	if err == nil {
		t.Fatal("expected an error when all engines failed")
	}
	if !strings.Contains(err.Error(), "google (timeout)") || !strings.Contains(err.Error(), "bing (CAPTCHA)") {
		t.Fatalf("error should name the failed engines, got: %v", err)
	}
}

// A 200 with empty results AND every engine responsive is a genuine empty query,
// not an error.
func TestSearxngGenuineEmptyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[],"unresponsive_engines":[]}`))
	}))
	defer srv.Close()

	s := &searxngSearcher{baseURL: srv.URL}
	out, _, err := s.Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("genuine empty must not error: %v", err)
	}
	if !strings.Contains(out, "No web results found") {
		t.Fatalf("expected the no-results message, got %q", out)
	}
}

func TestNewSearcherTavilyRequiresAPIKey(t *testing.T) {
	if got := newSearcher("tavily", "", ""); got != nil {
		t.Fatalf("tavily without key should be nil, got %#v", got)
	}
	if _, ok := newSearcher("tavily", "tvly-test", "").(*tavilySearcher); !ok {
		t.Fatal("tavily with key should build a tavilySearcher")
	}
}

func TestTavilySearchParsesResults(t *testing.T) {
	previous := toolHTTPClient
	var gotAuth, gotBody string
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotAuth = req.Header.Get("Authorization")
		raw, _ := io.ReadAll(req.Body)
		gotBody = string(raw)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"query": "example",
				"results": [
					{"title":"First","url":"https://example.test/1","content":"snippet  one","score":0.9,"published_date":"2024-01-02"},
					{"title":"Second","url":"https://example.test/2","content":"  another\nsnippet  "}
				]
			}`)),
			Request: req,
		}, nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := &tavilySearcher{apiKey: "tvly-secret"}
	text, citations, err := s.Search(context.Background(), "Example Query", 5)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotAuth != "Bearer tvly-secret" {
		t.Fatalf("Authorization = %q, want Bearer tvly-secret", gotAuth)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if payload["query"] != "Example Query" {
		t.Fatalf("query = %v", payload["query"])
	}
	if payload["max_results"] != float64(5) {
		t.Fatalf("max_results = %v, want 5", payload["max_results"])
	}
	if len(citations) != 2 {
		t.Fatalf("citations = %d, want 2", len(citations))
	}
	if citations[0].ID != "w_1" || citations[0].URL != "https://example.test/1" {
		t.Fatalf("unexpected first citation: %#v", citations[0])
	}
	if citations[0].Snippet != "snippet one" {
		t.Fatalf("snippet not cleaned: %q", citations[0].Snippet)
	}
	if !strings.Contains(text, "[1] First") || !strings.Contains(text, "(date: 2024-01-02)") {
		t.Fatalf("missing formatted result/date in %q", text)
	}
}

func TestTavilyEmptyResultsIsNotAnError(t *testing.T) {
	previous := toolHTTPClient
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"query":"x","results":[]}`)),
			Request:    req,
		}, nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := &tavilySearcher{apiKey: "tvly-secret"}
	out, citations, err := s.Search(context.Background(), "x", 5)
	if err != nil {
		t.Fatalf("empty results must not error: %v", err)
	}
	if len(citations) != 0 {
		t.Fatalf("empty results should have no citations, got %d", len(citations))
	}
	if !strings.Contains(out, "No web results found") {
		t.Fatalf("expected no-results message, got %q", out)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
