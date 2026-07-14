package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
