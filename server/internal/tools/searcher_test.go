package tools

import (
	"context"
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
