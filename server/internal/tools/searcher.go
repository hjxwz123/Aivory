package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"aivory/server/internal/llm"
)

// Searcher is the pluggable web-search backend abstraction (§4.4). Swap Serper
// for Brave/Tavily/Bing/SearXNG by providing another implementation — the tool
// code is backend-agnostic.
type Searcher interface {
	Search(ctx context.Context, query string, topK int) (text string, citations []llm.Citation, err error)
}

// newSearcher builds the configured searcher. Serper/Brave/Tavily require a
// key and SearXNG a base URL; DuckDuckGo is the free keyless channel and needs
// no configuration at all.
func newSearcher(provider, apiKey, baseURL string, selectedEngines ...[]string) Searcher {
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
	case "tavily":
		if apiKey == "" {
			return nil
		}
		return &tavilySearcher{apiKey: apiKey}
	case "searxng":
		if baseURL == "" {
			return nil
		}
		return &searxngSearcher{baseURL: strings.TrimRight(baseURL, "/"), engines: firstEngineSelection(selectedEngines)}
	case "duckduckgo", "ddg":
		// Free, keyless scraping of DuckDuckGo's public HTML endpoints.
		return &duckduckgoSearcher{}
	case "", "auto":
		if apiKey != "" {
			return &serperSearcher{apiKey: apiKey}
		}
		if baseURL != "" {
			return &searxngSearcher{baseURL: strings.TrimRight(baseURL, "/"), engines: firstEngineSelection(selectedEngines)}
		}
		// DuckDuckGo deliberately stays OUT of auto: silently sending user
		// queries to a third-party scraper target is an admin privacy call,
		// and the free endpoints are unstable from datacenter IPs.
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
		snippetRaw, _ := rm["snippet"].(string)
		snippet := cleanSnippet(snippetRaw)
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
		snippet := cleanSnippet(r.Description)
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.Title, URL: r.URL, Snippet: snippet, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, r.Title, r.URL, snippet)
		if r.PageAge != "" {
			fmt.Fprintf(&out, "(date: %s)\n", r.PageAge)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// tavilySearcher hits https://api.tavily.com/search.
type tavilySearcher struct{ apiKey string }

func (t *tavilySearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	body, _ := json.Marshal(map[string]any{
		"query":          query,
		"max_results":    topK,
		"search_depth":   "basic",
		"include_answer": false,
		"topic":          "general",
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.tavily.com/search", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bd, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", nil, fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, string(bd))
	}
	var parsed struct {
		Results []struct {
			Title         string  `json:"title"`
			URL           string  `json:"url"`
			Content       string  `json:"content"`
			Score         float64 `json:"score"`
			PublishedDate string  `json:"published_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", nil, err
	}
	if len(parsed.Results) == 0 {
		return "No web results found for this query.", nil, nil
	}
	citations := []llm.Citation{}
	out := strings.Builder{}
	for i, r := range parsed.Results {
		snippet := cleanSnippet(r.Content)
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.Title, URL: r.URL, Snippet: snippet, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, r.Title, r.URL, snippet)
		if r.PublishedDate != "" {
			fmt.Fprintf(&out, "(date: %s)\n", r.PublishedDate)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// searxngSearcher queries a self-hosted SearXNG instance over JSON.
type searxngSearcher struct {
	baseURL string
	engines []string
}

func firstEngineSelection(selections [][]string) []string {
	if len(selections) == 0 || len(selections[0]) == 0 {
		return nil
	}
	return append([]string(nil), selections[0]...)
}

// parseSearchEngines accepts the compact value used by the administrator
// settings page. SearXNG accepts engine names and shortcuts (for example
// "bing" or "ddg"), so Aivory deliberately does not maintain a fixed engine
// catalog. An empty value leaves SearXNG's own enabled-engine set untouched.
func parseSearchEngines(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	seen := make(map[string]struct{}, len(parts))
	engines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = strings.ToLower(part)
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		engines = append(engines, part)
	}
	return engines
}

func (s *searxngSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	// baseURL is used verbatim (an instance may legitimately be MOUNTED under a
	// /search subpath, so stripping the suffix would break it); an admin who
	// pasted the endpoint by mistake gets a targeted hint on the resulting 404.
	params := url.Values{
		"q":          []string{query},
		"format":     []string{"json"},
		"safesearch": []string{"1"},
	}
	if len(s.engines) > 0 {
		params.Set("engines", strings.Join(s.engines, ","))
	}
	u := fmt.Sprintf("%s/search?%s", s.baseURL, params.Encode())
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
		snippet := cleanSnippet(r.Content)
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.Title, URL: r.URL, Snippet: snippet, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n", i+1, r.Title, r.URL, snippet)
		if r.PublishedAt != "" {
			fmt.Fprintf(&out, "(date: %s)\n", r.PublishedAt)
		}
		out.WriteString("\n")
	}
	return out.String(), citations, nil
}

// --- DuckDuckGo (free, keyless) -------------------------------------------
//
// DuckDuckGo has no public search API, only the two server-rendered HTML
// endpoints below:
//
//	html.duckduckgo.com/html/  — full result cards with snippets
//	lite.duckduckgo.com/lite/  — a minimal table fallback
//
// The html endpoint is tried first; when it is bot-challenged, errored, or
// parsed to zero rows (layout shift), the request falls through to lite, whose
// independent markup confirms whether the query genuinely has no results.

const (
	ddgHTMLBase = "https://html.duckduckgo.com/html/"
	ddgLiteBase = "https://lite.duckduckgo.com/lite/"
	// ddgUserAgent: DuckDuckGo's bot filter trips on library-flavoured UAs, so
	// present a plain desktop browser one.
	ddgUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
	ddgBodyCap   = 1 << 20 // result pages are ~100-400 KB; 1 MiB bounds a misbehaving endpoint
	// ddgMaxResults: one DDG result page carries at most 10 rows, so a larger
	// top_k cannot be served by the single request below.
	ddgMaxResults = 10
)

type duckduckgoSearcher struct {
	// Endpoint overrides exist for tests to serve fixtures; empty = the real
	// DuckDuckGo bases above.
	htmlEndpoint string
	liteEndpoint string
}

type ddgResult struct {
	title   string
	url     string
	snippet string
}

func (d *duckduckgoSearcher) html() string {
	if d.htmlEndpoint != "" {
		return d.htmlEndpoint
	}
	return ddgHTMLBase
}

func (d *duckduckgoSearcher) lite() string {
	if d.liteEndpoint != "" {
		return d.liteEndpoint
	}
	return ddgLiteBase
}

func (d *duckduckgoSearcher) Search(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	if topK > ddgMaxResults {
		topK = ddgMaxResults
	}
	htmlBody, htmlStatus, htmlErr := ddgGet(ctx, d.html(), query)
	htmlOK := htmlErr == nil && htmlStatus == http.StatusOK && !ddgBlockedPage(htmlBody)
	if htmlOK {
		if results := parseDDGHTML(htmlBody, topK); len(results) > 0 {
			return formatDDGResults(results)
		}
		// Zero parsed rows: confirm against lite (genuine miss or html layout shift).
	}

	liteBody, liteStatus, liteErr := ddgGet(ctx, d.lite(), query)
	liteOK := liteErr == nil && liteStatus == http.StatusOK && !ddgBlockedPage(liteBody)
	if liteOK {
		results := parseDDGLite(liteBody, topK)
		if len(results) == 0 {
			// An explicit empty message keeps the model from reading an empty
			// payload as a backend failure (same contract as SearXNG / Tavily).
			return "No web results found for this query.", nil, nil
		}
		return formatDDGResults(results)
	}

	// Both endpoints failed or were challenged. Surface why for each leg so the
	// admin sees the actual cause (403 vs challenge vs transport) plus the fix.
	reason := ddgEndpointProblem("lite.duckduckgo.com", liteStatus, liteBody, liteErr)
	if !htmlOK {
		reason += "; also " + ddgEndpointProblem("html.duckduckgo.com", htmlStatus, htmlBody, htmlErr)
	}
	return "", nil, fmt.Errorf(
		"duckduckgo: %s — DuckDuckGo's free endpoints rate-limit some server IPs (especially datacenter ranges); retry later or switch search_provider to Serper / Brave / Tavily / SearXNG",
		reason)
}

// ddgGet issues one keyless GET against an endpoint for the query. kl=wt-wt is
// DuckDuckGo's "no region preference" value.
func ddgGet(ctx context.Context, endpoint, query string) (body string, status int, err error) {
	params := url.Values{"q": []string{query}, "kl": []string{"wt-wt"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", ddgUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ddgBodyCap))
	if err != nil {
		return "", 0, err
	}
	return string(raw), resp.StatusCode, nil
}

// ddgBlockedPage reports whether a 200 page is actually DuckDuckGo's anti-bot
// interstitial (anomaly / Cloudflare challenge) rather than a results page. The
// markers are structural — the challenge's own class and script names — because
// result text is not evidence: a query like "captcha bypass" legitimately puts
// that word in the page, and a query like "anomaly detection" returns snippets
// that mention it.
func ddgBlockedPage(body string) bool {
	if strings.Contains(body, "anomaly-modal") ||
		strings.Contains(body, "challenge-platform") ||
		strings.Contains(body, "challenge-form") {
		return true
	}
	// Fallback for a re-marked-up challenge: bot-check wording counts only when
	// the page carries no result container at all.
	hasResults := strings.Contains(body, "results_links") || strings.Contains(body, "result-link")
	return !hasResults && strings.Contains(strings.ToLower(body), "captcha")
}

// ddgEndpointProblem renders one endpoint's failure as a short clause.
func ddgEndpointProblem(host string, status int, body string, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("%s unreachable: %v", host, err)
	case status == http.StatusForbidden, status == http.StatusTooManyRequests:
		return fmt.Sprintf("%s rate-limited this server (HTTP %d)", host, status)
	case ddgBlockedPage(body):
		return fmt.Sprintf("%s returned an anti-bot challenge page", host)
	case status >= 400:
		return fmt.Sprintf("%s returned HTTP %d", host, status)
	default:
		return fmt.Sprintf("%s returned an unrecognised page", host)
	}
}

var (
	// ddgBlockRe anchors on each html-endpoint result container; the captured
	// class tail lets ads (result--ad) be skipped before parsing their rows.
	ddgBlockRe   = regexp.MustCompile(`class="result results_links([^"]*)"`)
	ddgLinkRe    = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*?href="([^"]*)"[^>]*>(.*?)</a>`)
	ddgSnippetRe = regexp.MustCompile(`(?s)class="result__snippet"[^>]*>(.*?)</a>`)

	ddgLiteLinkRe    = regexp.MustCompile(`(?s)<a[^>]*href="([^"]*)"[^>]*class=['"]result-link['"][^>]*>(.*?)</a>`)
	ddgLiteSnippetRe = regexp.MustCompile(`(?s)class=['"]result-snippet['"][^>]*>(.*?)</td>`)

	ddgTagRe = regexp.MustCompile(`<[^>]*>`)
)

func parseDDGHTML(body string, topK int) []ddgResult {
	blocks := ddgBlockRe.FindAllStringSubmatchIndex(body, -1)
	results := make([]ddgResult, 0, len(blocks))
	for i, m := range blocks {
		if strings.Contains(body[m[2]:m[3]], "result--ad") {
			continue
		}
		segEnd := len(body)
		if i+1 < len(blocks) {
			segEnd = blocks[i+1][0]
		}
		seg := body[m[1]:segEnd]
		link := ddgLinkRe.FindStringSubmatch(seg)
		if link == nil {
			continue
		}
		r := ddgResult{
			title: ddgText(link[2]),
			url:   ddgResolveURL(htmlEntities.Replace(link[1])),
		}
		if snip := ddgSnippetRe.FindStringSubmatch(seg); snip != nil {
			r.snippet = cleanSnippet(ddgText(snip[1]))
		}
		if r.title == "" || r.url == "" {
			continue
		}
		results = append(results, r)
		if len(results) >= topK {
			break
		}
	}
	return results
}

func parseDDGLite(body string, topK int) []ddgResult {
	links := ddgLiteLinkRe.FindAllStringSubmatch(body, -1)
	snips := ddgLiteSnippetRe.FindAllStringSubmatch(body, -1)
	results := make([]ddgResult, 0, len(links))
	for i, link := range links {
		r := ddgResult{
			title: ddgText(link[2]),
			url:   ddgResolveURL(htmlEntities.Replace(link[1])),
		}
		if i < len(snips) {
			r.snippet = cleanSnippet(ddgText(snips[i][1]))
		}
		if r.title == "" || r.url == "" {
			continue
		}
		results = append(results, r)
		if len(results) >= topK {
			break
		}
	}
	return results
}

// ddgText reduces a result fragment (titles and snippets carry <b> search-term
// highlighting) to a single plain-text line.
func ddgText(fragment string) string {
	plain := htmlEntities.Replace(ddgTagRe.ReplaceAllString(fragment, ""))
	return strings.Join(strings.Fields(plain), " ")
}

// ddgResolveURL unwraps DuckDuckGo's redirect links
// (//duckduckgo.com/l/?uddg=<encoded target>&rut=…) into the real destination;
// direct http(s) links pass through. Anything else (tracking link without a
// target, relative junk) yields "" and the row is dropped.
func ddgResolveURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if strings.EqualFold(u.Host, "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		// The unwrapped target is re-parsed, so it faces the same scheme check
		// as a direct link.
		if u, err = url.Parse(strings.TrimSpace(u.Query().Get("uddg"))); err != nil {
			return ""
		}
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.String()
	}
	return ""
}

func formatDDGResults(results []ddgResult) (string, []llm.Citation, error) {
	citations := make([]llm.Citation, 0, len(results))
	out := strings.Builder{}
	for i, r := range results {
		citations = append(citations, llm.Citation{
			ID: fmt.Sprintf("w_%d", i+1), Index: i + 1,
			Title: r.title, URL: r.url, Snippet: r.snippet, Source: "web",
		})
		fmt.Fprintf(&out, "[%d] %s\n%s\n%s\n\n", i+1, r.title, r.url, r.snippet)
	}
	return out.String(), citations, nil
}

// cleanSnippet normalises a search result's snippet: it collapses every run of
// whitespace (incl. the newlines that turn a JS-heavy page's nav/boilerplate
// into a wall of text) to single spaces and caps the length. Noisy multi-line
// snippets are what make weaker models echo the raw result instead of
// synthesising; a tight one-line snippet is easier to reason over and cheaper.
func cleanSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const maxRunes = 320
	if r := []rune(s); len(r) > maxRunes {
		s = strings.TrimSpace(string(r[:maxRunes])) + "…"
	}
	return s
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
