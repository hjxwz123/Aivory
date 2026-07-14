package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"aivory/server/internal/llm"
)

// Searcher is the pluggable web-search backend abstraction (§4.4). Swap Serper
// for Brave/Bing/SearXNG by providing another implementation — the tool code
// is backend-agnostic.
type Searcher interface {
	Search(ctx context.Context, query string, topK int) (text string, citations []llm.Citation, err error)
}

// newSearcher builds the configured searcher. SearXNG can run unauthenticated
// (apiKey is empty), but Serper/Brave require a key.
func newSearcher(provider, apiKey, baseURL string) Searcher {
	switch strings.ToLower(provider) {
	case "serper":
		if apiKey == "" {
			return nil
		}
		return &serperSearcher{apiKey: apiKey}
	case "brave":
		if apiKey == "" {
			return nil
		}
		return &braveSearcher{apiKey: apiKey}
	case "searxng":
		if baseURL == "" {
			return nil
		}
		return &searxngSearcher{baseURL: strings.TrimRight(baseURL, "/")}
	case "", "auto":
		if apiKey != "" {
			return &serperSearcher{apiKey: apiKey}
		}
		if baseURL != "" {
			return &searxngSearcher{baseURL: strings.TrimRight(baseURL, "/")}
		}
		return nil
	default:
		return nil
	}
}

// serperSearcher hits https://google.serper.dev/search.
type serperSearcher struct{ apiKey string }

func (s *serperSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	body, _ := json.Marshal(map[string]any{"q": query, "num": topK})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://google.serper.dev/search", strings.NewReader(string(body)))
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("content-type", "application/json")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("serper: %s", string(b))
	}
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", nil, err
	}
	organic, _ := parsed["organic"].([]any)
	citations := []llm.Citation{}
	out := strings.Builder{}
	for i, r := range organic {
		rm, _ := r.(map[string]any)
		title, _ := rm["title"].(string)
		link, _ := rm["link"].(string)
		snippet, _ := rm["snippet"].(string)
		date, _ := rm["date"].(string)
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1, Title: title, URL: link, Snippet: snippet, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, title, link, snippet)
		if date != "" {
			fmt.Fprintf(&out, "(date: %s)\n", date)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// braveSearcher hits https://api.search.brave.com/res/v1/web/search.
type braveSearcher struct{ apiKey string }

func (b *braveSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d", url.QueryEscape(query), topK)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("X-Subscription-Token", b.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bd, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("brave: %s", string(bd))
	}
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", nil, err
	}
	citations := []llm.Citation{}
	out := strings.Builder{}
	for i, r := range parsed.Web.Results {
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.Title, URL: r.URL, Snippet: r.Description, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, r.Title, r.URL, r.Description)
		if r.PageAge != "" {
			fmt.Fprintf(&out, "(date: %s)\n", r.PageAge)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// searxngSearcher queries a self-hosted SearXNG instance over JSON.
type searxngSearcher struct{ baseURL string }

func (s *searxngSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	// baseURL is used verbatim (an instance may legitimately be MOUNTED under a
	// /search subpath, so stripping the suffix would break it); an admin who
	// pasted the endpoint by mistake gets a targeted hint on the resulting 404.
	u := fmt.Sprintf("%s/search?q=%s&format=json&safesearch=1", s.baseURL, url.QueryEscape(query))
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Accept", "application/json")
	// SearXNG's default bot limiter blocks user agents that match bot/crawler
	// patterns and requests without an Accept-Language — identify plainly but
	// without tripping either check.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Aivory/1.0)")
	req.Header.Set("Accept-Language", "en")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bd, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		body := string(bd)
		// Map the classic self-hosted misconfigurations to actionable errors
		// instead of an opaque HTML error page:
		//  403 + Cloudflare challenge markers → the domain sits behind a
		//        Cloudflare JS challenge no server-side client can pass;
		//  403 otherwise → the JSON output format is disabled (SearXNG ships
		//        with search.formats: [html] only);
		//  429 → the bot limiter is rejecting server-side requests.
		switch resp.StatusCode {
		case http.StatusForbidden:
			// Challenge detection keys on challenge-specific markers only — a
			// bare "Server: cloudflare" header just means the domain is proxied
			// and would misdiagnose the far more common formats-disabled 403.
			if strings.Contains(body, "challenges.cloudflare.com") || strings.Contains(body, "Just a moment") ||
				strings.EqualFold(resp.Header.Get("cf-mitigated"), "challenge") {
				return "", nil, fmt.Errorf("searxng: HTTP 403 — the domain is behind a Cloudflare challenge that server-side requests cannot pass; point search_base_url at the origin directly (internal address), set the DNS record to DNS-only, or add a Cloudflare WAF skip rule for this host")
			}
			return "", nil, fmt.Errorf("searxng: HTTP 403 — the instance likely has the JSON API disabled; add \"json\" to search.formats in settings.yml (formats: [html, json]) and restart SearXNG")
		case http.StatusTooManyRequests:
			return "", nil, fmt.Errorf("searxng: HTTP 429 — the instance's bot limiter is blocking server-side requests; disable the limiter or allowlist this server in limiter.toml")
		case http.StatusNotFound:
			return "", nil, fmt.Errorf("searxng: HTTP 404 — check that search_base_url points at the instance root (it should not include the /search path itself)")
		}
		return "", nil, fmt.Errorf("searxng: HTTP %d: %s", resp.StatusCode, body)
	}
	var parsed struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Content     string `json:"content"`
			PublishedAt string `json:"publishedDate"`
		} `json:"results"`
		// SearXNG always reports which engines failed to answer this query as
		// [[engine, reason], …]. When results are empty this is the real cause
		// (self-hosted instances routinely have their engines blocked/rate-
		// limited/misconfigured), so we surface it instead of a bland "no results".
		UnresponsiveEngines [][]any `json:"unresponsive_engines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		// A 200 with a non-JSON body means the instance answered with an HTML
		// page (JSON format disabled, or a reverse proxy error page).
		return "", nil, fmt.Errorf("searxng: response was not JSON (%v) — verify format=json is enabled on the instance (search.formats in settings.yml)", err)
	}
	if len(parsed.Results) > topK {
		parsed.Results = parsed.Results[:topK]
	}
	if len(parsed.Results) == 0 {
		// Empty results + failed engines = the engines that could have answered
		// didn't (blocked IP, rate limit, bad config) — a real failure, not a
		// query with genuinely no matches. Report which engines failed so the
		// admin can fix them (visible in /admin/usage error detail).
		if failed := formatUnresponsiveEngines(parsed.UnresponsiveEngines); failed != "" {
			return "", nil, fmt.Errorf("searxng: 0 results because its search engines did not respond: %s. The instance reached its engines but they failed — commonly the upstream engine (Google/Bing/…) blocks the server's IP, the engine is rate-limited, or it's misconfigured. Check the instance's outbound network and engine settings; try a different engine in settings.yml", failed)
		}
		// An explicit empty-result message keeps the model from reading an
		// empty tool payload as a backend failure.
		return "No web results found for this query.", nil, nil
	}
	citations := []llm.Citation{}
	out := strings.Builder{}
	for i, r := range parsed.Results {
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.Title, URL: r.URL, Snippet: r.Content, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, r.Title, r.URL, r.Content)
		if r.PublishedAt != "" {
			fmt.Fprintf(&out, "(date: %s)\n", r.PublishedAt)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// formatUnresponsiveEngines renders SearXNG's unresponsive_engines
// ([[engine, reason, …], …]) as "engine (reason), …". Returns "" when none
// failed. Tolerant of shape drift across SearXNG versions (2- or 3-element
// entries, string or bool members).
func formatUnresponsiveEngines(entries [][]any) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		if len(e) == 0 {
			continue
		}
		name := fmt.Sprintf("%v", e[0])
		if len(e) >= 2 {
			if reason := strings.TrimSpace(fmt.Sprintf("%v", e[1])); reason != "" {
				parts = append(parts, fmt.Sprintf("%s (%s)", name, reason))
				continue
			}
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}
