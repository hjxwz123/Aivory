package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
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

// --- DuckDuckGo (free keyless channel) -------------------------------------

const ddgHTMLFixture = `<html><body>
<div class="result results_links results_links_deep web-result ">
  <div class="links_main links_deep result__body">
    <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fone&rut=deadbeef">First <b>Result</b></a></h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fone&rut=deadbeef">Snippet <b>one</b> with
      awkward   newlines</a>
  </div>
</div>
<div class="result results_links results_links_deep web-result result--ad ">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fad.test%2Fpromo">Sponsored</a>
  <a class="result__snippet" href="#">ad snippet</a>
</div>
<div class="result results_links results_links_deep web-result ">
  <a rel="nofollow" class="result__a" href="https://example.test/two?a=1&amp;b=2">Second &amp; direct</a>
  <a class="result__snippet" href="https://example.test/two">plain second snippet</a>
</div>
</body></html>`

const ddgLiteFixture = `<html><body><table>
<tr><td><a rel="nofollow" href="https://example.test/l1" class='result-link'>Lite <b>One</b></a></td></tr>
<tr><td class='result-snippet'>lite snippet one</td></tr>
<tr><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fl2" class='result-link'>Lite Two</a></td></tr>
<tr><td class='result-snippet'>lite snippet two</td></tr>
</table></body></html>`

func ddgHTMLResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestNewSearcherDuckDuckGoNeedsNoConfig(t *testing.T) {
	for _, name := range []string{"duckduckgo", "ddg", "DuckDuckGo"} {
		if _, ok := newSearcher(name, "", "").(*duckduckgoSearcher); !ok {
			t.Fatalf("provider %q should build a keyless duckduckgoSearcher", name)
		}
	}
	// auto must NOT silently fall through to DuckDuckGo — sending user queries
	// to a scraper target has to stay an explicit admin opt-in.
	if got := newSearcher("auto", "", ""); got != nil {
		t.Fatalf(`auto with no key/url must stay nil (placeholder), got %#v`, got)
	}
}

func TestDuckDuckGoParsesHTMLEndpoint(t *testing.T) {
	previous := toolHTTPClient
	var gotQuery, gotUA string
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotQuery = req.URL.Query().Get("q")
		gotUA = req.Header.Get("User-Agent")
		return ddgHTMLResponse(ddgHTMLFixture), nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := &duckduckgoSearcher{htmlEndpoint: "https://html.duckduckgo.com/html/", liteEndpoint: "https://lite.duckduckgo.com/lite/"}
	text, citations, err := s.Search(context.Background(), "example query", 5)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotQuery != "example query" {
		t.Fatalf("q = %q", gotQuery)
	}
	if !strings.Contains(gotUA, "Mozilla/5.0") {
		t.Fatalf("browser UA expected, got %q", gotUA)
	}
	if len(citations) != 2 {
		t.Fatalf("ad block must be skipped; citations = %d (%#v)", len(citations), citations)
	}
	if citations[0].ID != "w_1" || citations[0].URL != "https://example.test/one" {
		t.Fatalf("uddg redirect not unwrapped: %#v", citations[0])
	}
	if citations[0].Title != "First Result" {
		t.Fatalf("title not cleaned: %q", citations[0].Title)
	}
	if citations[0].Snippet != "Snippet one with awkward newlines" {
		t.Fatalf("snippet not collapsed: %q", citations[0].Snippet)
	}
	if citations[1].URL != "https://example.test/two?a=1&b=2" {
		t.Fatalf("entity in direct href not decoded: %q", citations[1].URL)
	}
	if !strings.Contains(text, "[1] First Result") || !strings.Contains(text, "https://example.test/one") {
		t.Fatalf("missing formatted result in %q", text)
	}
}

func TestDuckDuckGoFallsBackToLiteWhenHTMLBlocked(t *testing.T) {
	previous := toolHTTPClient
	var seen []string
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.Host+req.URL.Path)
		if strings.Contains(req.URL.Host, "html.") {
			// 200 + anti-bot interstitial — must not be parsed as results.
			return ddgHTMLResponse("<html><body><div class=\"anomaly-modal\">Please work the page</div></body></html>"), nil
		}
		return ddgHTMLResponse(ddgLiteFixture), nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := &duckduckgoSearcher{htmlEndpoint: "https://html.duckduckgo.com/html/", liteEndpoint: "https://lite.duckduckgo.com/lite/"}
	text, citations, err := s.Search(context.Background(), "anything", 5)
	if err != nil {
		t.Fatalf("lite fallback should succeed: %v", err)
	}
	if len(seen) != 2 || !strings.Contains(seen[1], "lite.") {
		t.Fatalf("expected html-then-lite requests, got %v", seen)
	}
	if len(citations) != 2 || citations[1].URL != "https://example.test/l2" {
		t.Fatalf("lite rows mis-parsed: %#v", citations)
	}
	if !strings.Contains(text, "[1] Lite One") {
		t.Fatalf("missing lite text in %q", text)
	}
}

func TestDuckDuckGoEmptyResultsIsNotAnError(t *testing.T) {
	previous := toolHTTPClient
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return ddgHTMLResponse("<html><body><div>Nothing here</div></body></html>"), nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := &duckduckgoSearcher{htmlEndpoint: "https://html.duckduckgo.com/html/", liteEndpoint: "https://lite.duckduckgo.com/lite/"}
	out, citations, err := s.Search(context.Background(), "obscure", 5)
	if err != nil {
		t.Fatalf("empty results must not error: %v", err)
	}
	if len(citations) != 0 || !strings.Contains(out, "No web results found") {
		t.Fatalf("expected the no-results message, got %d citations, %q", len(citations), out)
	}
}

func TestDuckDuckGoBothEndpointsBlockedErrorsWithAdvice(t *testing.T) {
	previous := toolHTTPClient
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "html.") {
			return ddgHTMLResponse(`<div class="anomaly-modal">challenge</div>`), nil
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("blocked")),
		}, nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := &duckduckgoSearcher{htmlEndpoint: "https://html.duckduckgo.com/html/", liteEndpoint: "https://lite.duckduckgo.com/lite/"}
	_, _, err := s.Search(context.Background(), "anything", 5)
	if err == nil {
		t.Fatal("expected an error when both endpoints are blocked")
	}
	msg := err.Error()
	for want := range map[string]bool{
		"anti-bot challenge":                true,
		"HTTP 403":                          true,
		"retry later":                       true,
		"Serper / Brave / Tavily / SearXNG": true,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

// A live result page for the query "captcha" mentions the word in the search
// box, the title and the snippet, while carrying no challenge markup at all.
const ddgCaptchaQueryFixture = `<html><body>
<form><input name="q" value="captcha"></form>
<div class="result results_links results_links_deep web-result ">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fcaptcha">Captcha solver <b>captcha</b> guide</a>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.test%2Fcaptcha">How captcha works and why captcha is annoying.</a>
</div>
</body></html>`

func TestDuckDuckGoResultPageMentioningCaptchaIsNotABlock(t *testing.T) {
	previous := toolHTTPClient
	var liteHits int
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Host, "lite.") {
			liteHits++
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(strings.NewReader(`<div class="anomaly-modal">challenge</div>`)),
			}, nil
		}
		return ddgHTMLResponse(ddgCaptchaQueryFixture), nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := &duckduckgoSearcher{htmlEndpoint: "https://html.duckduckgo.com/html/", liteEndpoint: "https://lite.duckduckgo.com/lite/"}
	_, citations, err := s.Search(context.Background(), "captcha", 5)
	if err != nil {
		t.Fatalf("a page whose result text mentions captcha must not read as a block: %v", err)
	}
	if len(citations) != 1 || citations[0].URL != "https://example.test/captcha" {
		t.Fatalf("html results dropped: %#v", citations)
	}
	if liteHits != 0 {
		t.Fatalf("html results must be returned without a lite fallback, lite hits = %d", liteHits)
	}
}

func TestDDGBlockedPageMarkersAreStructural(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"anomaly modal", `<html><body><div class="anomaly-modal"><form id="challenge-form" action="//duckduckgo.com/anomaly.js"></form></div></body></html>`, true},
		{"cloudflare platform", `<html><head><script src="https://challenges.cloudflare.com/turnstile/v0/api.js?onload=challenge-platform"></script></head></html>`, true},
		{"result page mentioning captcha", `<html><body><div class="result results_links "><a class="result__a" href="https://example.test/">Solve a captcha</a></div></body></html>`, false},
		{"lite page mentioning captcha", `<html><body><table><tr><td><a class='result-link' href='https://example.test/'>captcha</a></td></tr></table></body></html>`, false},
		{"plain page without results", `<html><body><div>Nothing here</div></body></html>`, false},
	}
	for _, tc := range cases {
		if got := ddgBlockedPage(tc.body); got != tc.want {
			t.Fatalf("%s: ddgBlockedPage = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDDGResolveURLUnwrapsAndVetsTargets(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://example.test/a?b=1&c=2", "https://example.test/a?b=1&c=2"},
		{"//example.test/b", "https://example.test/b"},
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fok.test%2Fpath&rut=abc", "https://ok.test/path"},
		{"//duckduckgo.com/l/?uddg=javascript%3Aalert%281%29", ""},
		{"//duckduckgo.com/l/?uddg=%2Frelative", ""},
		{"//duckduckgo.com/l/?rut=abc", ""},
		{"/relative", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ddgResolveURL(tc.raw); got != tc.want {
			t.Fatalf("ddgResolveURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// The admin setting is the only thing that turns search on: with no env fallback
// and no key, DuckDuckGo must still reach the real backend, while a cleared
// provider keeps the not-configured placeholder instead of falling back to env.
func TestSettingsSearcherResolvesDuckDuckGoFromAdminSetting(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "settings-searcher.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}

	previous := toolHTTPClient
	toolHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return ddgHTMLResponse(ddgHTMLFixture), nil
	})}
	t.Cleanup(func() { toolHTTPClient = previous })

	s := newSettingsSearcher(db, "", "", "")

	if err := store.SetSetting(db, "search_provider", "duckduckgo"); err != nil {
		t.Fatal(err)
	}
	_, citations, err := s.Search(context.Background(), "example query", 5)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(citations) != 2 || citations[0].URL != "https://example.test/one" {
		t.Fatalf("admin-set DuckDuckGo did not reach the real backend: %#v", citations)
	}

	if err := store.SetSetting(db, "search_provider", ""); err != nil {
		t.Fatal(err)
	}
	text, citations, err := s.Search(context.Background(), "example query", 5)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(citations) != 1 || !strings.Contains(text, "Search not yet configured") {
		t.Fatalf("cleared provider must keep the placeholder, got %d citations: %q", len(citations), text)
	}
}
