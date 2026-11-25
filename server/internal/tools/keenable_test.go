package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

const keenableFixture = `{"query":"go","mode":"pro","results":[{"title":"Go release notes","url":"https://go.dev/doc/go1.26","description":"","snippet":"Release   summary","published_at":"2026-02-10"},{"title":"No link","url":"","snippet":"dropped"},{"title":"Docs","url":"https://go.dev/doc/","snippet":"","description":"Only a description."}]}`

func keenableRequestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body = %s: %v", raw, err)
	}
	return body
}

func TestKeenableKeylessRequest(t *testing.T) {
	// An explicitly resolved empty key must not reactivate an environment key.
	t.Setenv("KEENABLE_API_KEY", "unused-env-key")
	mockYouComHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.String() != keenableKeylessEndpoint {
			t.Errorf("keyless request = %s %s", r.Method, r.URL)
		}
		if r.Header.Get("X-API-Key") != "" {
			t.Error("keyless request must not send X-API-Key")
		}
		if got := r.Header.Get("X-Keenable-Title"); got != keenableClientTitle {
			t.Errorf("X-Keenable-Title = %q", got)
		}
		body := keenableRequestBody(t, r)
		if body["query"] != "go release" || body["max_results"] != float64(5) || body["disable_cards"] != true {
			t.Errorf("body = %v", body)
		}
		fmt.Fprint(w, keenableFixture)
	})
	text, citations, err := newSearcher("keenable", "", "", "", "").Search(t.Context(), "go release", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(citations) != 2 || citations[0].Snippet != "Release summary" || citations[1].Snippet != "Only a description." {
		t.Fatalf("citations = %#v", citations)
	}
	for _, want := range []string{"Go release notes", "(date: 2026-02-10)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestKeenableKeyedRequest(t *testing.T) {
	for name, tc := range map[string]struct{ fallback, env, want string }{
		"search_env":   {"search-env-key", "env-key", "search-env-key"},
		"keenable_env": {"", "env-key", "env-key"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("KEENABLE_API_KEY", tc.env)
			mockYouComHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.String() != keenableKeyedEndpoint {
					t.Errorf("keyed request = %s %s", r.Method, r.URL)
				}
				if got := r.Header.Get("X-API-Key"); got != tc.want {
					t.Errorf("X-API-Key = %q, want %q", got, tc.want)
				}
				if body := keenableRequestBody(t, r); body["max_results"] != float64(keenableMaxResults) {
					t.Errorf("max_results = %v, want clamp to %d", body["max_results"], keenableMaxResults)
				}
				fmt.Fprint(w, keenableFixture)
			})
			if _, _, err := newSettingsSearcher(nil, "keenable", tc.fallback, "").Search(t.Context(), "q", 500); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestKeenableSettingsKeyChangesApplyImmediately(t *testing.T) {
	t.Setenv("KEENABLE_API_KEY", "keenable-env-key")
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "keenable-settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(db, "search_provider", "keenable"); err != nil {
		t.Fatal(err)
	}
	searcher := newSettingsSearcher(db, "", "", "")
	var endpoint, key string
	mockYouComHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		endpoint, key = r.URL.String(), r.Header.Get("X-API-Key")
		fmt.Fprint(w, keenableFixture)
	})
	check := func(wantKey string) {
		t.Helper()
		if _, _, err := searcher.Search(t.Context(), "query", 1); err != nil {
			t.Fatal(err)
		}
		wantEndpoint := keenableKeyedEndpoint
		if wantKey == "" {
			wantEndpoint = keenableKeylessEndpoint
		}
		if endpoint != wantEndpoint || key != wantKey {
			t.Fatalf("endpoint=%s keyMatches=%t; want endpoint=%s", endpoint, key == wantKey, wantEndpoint)
		}
	}
	// No saved row: KEENABLE_API_KEY is the fallback.
	check("keenable-env-key")
	for _, savedKey := range []string{"saved-key", "", "new-saved-key"} {
		if err := store.SetSetting(db, "search_api_key", savedKey); err != nil {
			t.Fatal(err)
		}
		// Clearing the key switches to the keyless endpoint even with the env key set.
		check(savedKey)
	}
}

func TestKeenableErrorStatuses(t *testing.T) {
	t.Setenv("KEENABLE_API_KEY", "")
	for status, want := range map[int]string{
		http.StatusUnauthorized:        "API key was rejected",
		http.StatusForbidden:           "API key was rejected",
		http.StatusTooManyRequests:     "1,000 requests an hour per IP",
		http.StatusBadRequest:          "keenable: HTTP 400: bad request body",
		http.StatusInternalServerError: "keenable: HTTP 500",
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			mockYouComHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(status)
				fmt.Fprint(w, "bad request body")
			})
			_, _, err := newSearcher("keenable", "", "", "", "").Search(t.Context(), "q", 3)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q", err, want)
			}
		})
	}
}

func TestKeenableEmptyResults(t *testing.T) {
	mockYouComHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	})
	text, citations, err := newSearcher("keenable", "", "", "", "").Search(t.Context(), "q", 3)
	if err != nil || len(citations) != 0 || !strings.Contains(text, "No web results") {
		t.Fatalf("text=%q citations=%d err=%v", text, len(citations), err)
	}
}

func TestKeenableIsNotPickedByAuto(t *testing.T) {
	t.Setenv("KEENABLE_API_KEY", "env-key")
	if backend := newSettingsSearcher(nil, "auto", "", "").backend(); backend != nil {
		t.Fatalf("KEENABLE_API_KEY must not configure auto search; got %T", backend)
	}
}
