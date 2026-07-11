package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"aurelia/server/internal/llm"
	"aurelia/server/internal/store"
)

// settingsSearcher is a thin Searcher that re-resolves provider + base URL +
// api key from admin settings on every call (§4.4). Mirrors settingsSandbox:
// admin edits take effect on the very next web_search invocation, no restart.
//
// Resolution priority:
//  1. admin settings (`search_provider` / `search_base_url` / `search_api_key`)
//  2. env-derived fallback values supplied to the constructor
//  3. nil — webSearchTool then returns its polite no-op placeholder
//
// SearXNG (self-hosted) needs only `search_base_url`; `search_api_key` is
// optional. Serper / Brave both require an API key. The shared `newSearcher`
// helper enforces those provider-specific requirements so we don't duplicate
// the rules here.
type settingsSearcher struct {
	db          *sql.DB
	fallbackPv  string
	fallbackKey string
	fallbackURL string
}

func newSettingsSearcher(db *sql.DB, provider, apiKey, baseURL string) *settingsSearcher {
	return &settingsSearcher{
		db:          db,
		fallbackPv:  provider,
		fallbackKey: apiKey,
		fallbackURL: baseURL,
	}
}

// Search resolves the live backend, delegates, and returns the placeholder
// when nothing is configured anywhere. Errors from the backend bubble up
// untouched.
func (s *settingsSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	provider := s.settingString("search_provider", s.fallbackPv)
	baseURL := s.settingString("search_base_url", s.fallbackURL)
	apiKey := s.settingString("search_api_key", s.fallbackKey)
	b := newSearcher(provider, apiKey, baseURL)
	if b == nil {
		// Mirror the original webSearchTool fallback so the model sees a
		// consistent "search not configured" reply regardless of which leg of
		// the resolver ran out of values.
		return "Search not yet configured. Reply based on training knowledge or ask the admin to configure search in the admin panel.",
			[]llm.Citation{{
				ID: "w1", Index: 1, Title: "Aurelia local-only mode",
				URL:     "https://example.com/aurelia-local-mode",
				Snippet: "No search backend configured. Set provider + base URL / api key in admin settings to enable real web_search results.",
				Source:  "web",
			}}, nil
	}
	return b.Search(ctx, query, topK)
}

func (s *settingsSearcher) settingString(key, fallback string) string {
	if s.db == nil {
		return fallback
	}
	raw, err := store.GetSetting(s.db, key)
	if err != nil {
		// Row absent → fall back to the boot-time env value. This is the
		// "admin never touched this key" path.
		return fallback
	}
	// Row PRESENT. Decode and honour whatever the admin saved, INCLUDING an
	// empty string — that's an explicit "clear / disable" gesture from the
	// UI, not a "use env" gesture. Without this distinction, deleting the
	// API key in the admin UI would silently keep the env key live.
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return fallback
	}
	return strings.TrimSpace(v)
}
