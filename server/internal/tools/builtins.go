package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "golang.org/x/image/webp"

	"aivory/server/internal/config"
	"aivory/server/internal/envcfg"
	"aivory/server/internal/fileguard"
	"aivory/server/internal/llm"
	"aivory/server/internal/sandbox"
	"aivory/server/internal/store"
	"aivory/server/internal/toolnames"
)

// Env-overridable defaults (see docs/config-reference.md). Each falls back to
// its documented default when the corresponding AIVORY_* variable is unset.
var (
	inTopK                       = envcfg.Int("AIVORY_TOOLS_IN_TOP_K", 5)
	webFetchResponseBodyReadCap  = envcfg.Int64("AIVORY_TOOLS_WEB_FETCH_RESPONSE_BODY_READ_CAP", 256*1024)
	webFetchExtractedTextCharCap = envcfg.Int("AIVORY_TOOLS_WEB_FETCH_EXTRACTED_TEXT_CHAR_CAP", 32000)
	// § web_fetch Jina fallback: when the origin is directly unreachable from
	// THIS server (filtered/black-holed network, no route to the target), retry
	// the read through a Jina Reader–compatible text-extraction endpoint, which
	// fetches the page from its own hosts. The direct attempt gets its own short
	// sub-timeout so a black-hole can't consume the whole tool budget. The base
	// and switch read env at call time (webFetchJinaFallbackOn / webFetchJinaBase)
	// so tests can override them.
	webFetchDirectAttemptTimeout           = envcfg.Dur("AIVORY_TOOLS_WEB_FETCH_DIRECT_TIMEOUT", 12*time.Second)
	pythonExecuteUploadStagingFileSize     = envcfg.Int64("AIVORY_TOOLS_PYTHON_EXECUTE_UPLOAD_STAGING_FILE_SIZE", 40*1024*1024)
	pythonExecuteStdoutStderrTruncationCap = envcfg.Int("AIVORY_TOOLS_PYTHON_EXECUTE_STDOUT_STDERR_TRUNCATION_CAP", 32*1024)
	inSize                                 = envcfg.Str("AIVORY_TOOLS_IN_SIZE", "")
	dailyImageLimitResetWindow             = envcfg.Dur("AIVORY_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW", 24*time.Hour)
	// 0 selects the provider/model-specific limit. A positive value remains an
	// operator override for gateways with a stricter custom boundary.
	imageImageInputImageCap     = envcfg.Int("AIVORY_TOOLS_IMAGE_IMAGE_INPUT_IMAGE_CAP", 0)
	fetchRemoteImageDownloadCap = envcfg.Int64("AIVORY_TOOLS_FETCHREMOTEIMAGE_DOWNLOAD_CAP", 32<<20)
	saveMemoryConfidence        = envcfg.F64("AIVORY_TOOLS_CONFIDENCE", 0.95)
)

const (
	webSearchBatchMaxItems = 5
	webFetchBatchMaxItems  = 4
	webBatchConcurrency    = 4
)

// webFetchJinaFallbackOn reports whether the Jina reader fallback is enabled.
// Read at call time (not package init) so tests can flip it with t.Setenv.
func webFetchJinaFallbackOn() bool {
	return envcfg.Bool("AIVORY_TOOLS_WEB_FETCH_JINA_FALLBACK", true)
}

// webFetchJinaBase returns the Jina Reader–compatible base URL (call-time read).
func webFetchJinaBase() string {
	return envcfg.Str("AIVORY_TOOLS_WEB_FETCH_JINA_BASE", "https://r.jina.ai")
}

// webSearchTool implements §4.4 via a pluggable Searcher. When no backend is
// configured it returns a polite placeholder so callers never crash.
type webSearchTool struct {
	cfg      config.Config
	searcher Searcher
}

func (t *webSearchTool) Name() string { return toolnames.AivoryWebSearch }
func (t *webSearchTool) Description() string {
	return "Search the public web for current information. Use query for one search or queries to batch several known searches into one tool call. Returns titled snippets with URLs."
}
func (t *webSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"One search query. Use either query or queries."},"queries":{"type":"array","items":{"type":"string"},"maxItems":5,"description":"Up to 5 independent search queries. Prefer this when several searches are known in advance."},"top_k":{"type":"integer","minimum":1,"maximum":10,"description":"Maximum results per query."}}}`)
}

type webSearchInput struct {
	Query   string   `json:"query"`
	Queries []string `json:"queries"`
	TopK    int      `json:"top_k"`
}

func (t *webSearchTool) Execute(ctx context.Context, input []byte, _ *llm.ToolContext) (string, []llm.Citation, error) {
	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", nil, &llm.ToolUserError{Message: "invalid search input"}
	}
	queries, err := mergeBatchStrings(in.Query, in.Queries, webSearchBatchMaxItems, "query or queries required", func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(value), " "))
	})
	if err != nil {
		return "", nil, err
	}
	if in.TopK <= 0 {
		in.TopK = inTopK
	}
	if in.TopK > 10 {
		return "", nil, &llm.ToolUserError{Message: "top_k must not exceed 10"}
	}
	if len(queries) == 1 {
		return t.searchOne(ctx, queries[0], in.TopK)
	}
	return t.searchBatch(ctx, queries, in.TopK)
}

func (t *webSearchTool) searchOne(ctx context.Context, query string, topK int) (string, []llm.Citation, error) {
	if t.searcher == nil {
		// Fallback "result" so the model can still respond gracefully.
		fake := []llm.Citation{
			{ID: "w1", Index: 1, Title: "Aivory local-only mode", URL: "https://example.com/aivory-local-mode", Snippet: "No SEARCH_API_KEY configured. Configure one to enable real aivory_web_search results.", Source: "web"},
		}
		return "Search not yet configured. Reply based on training knowledge or ask the user to configure SEARCH_API_KEY.", fake, nil
	}
	return t.searcher.Search(ctx, query, topK)
}

type webSearchBatchItem struct {
	Query           string `json:"query"`
	Status          string `json:"status"`
	Content         string `json:"content,omitempty"`
	CitationIndexes []int  `json:"citation_indexes,omitempty"`
	Error           string `json:"error,omitempty"`
}

type webSearchBatchResult struct {
	Status string               `json:"status"`
	Items  []webSearchBatchItem `json:"items"`
}

func (t *webSearchTool) searchBatch(ctx context.Context, queries []string, topK int) (string, []llm.Citation, error) {
	type searchResult struct {
		text      string
		citations []llm.Citation
		err       error
	}
	results := make([]searchResult, len(queries))
	runBoundedBatch(ctx, len(queries), func(index int) {
		defer func() {
			if recovered := recover(); recovered != nil {
				results[index].err = fmt.Errorf("batch search panicked: %v", recovered)
			}
		}()
		text, citations, err := t.searchOne(ctx, queries[index], topK)
		results[index] = searchResult{text: text, citations: citations, err: err}
	})

	items := make([]webSearchBatchItem, len(queries))
	allCitations := make([]llm.Citation, 0, len(queries)*topK)
	citationIndexes := make(map[string]int)
	successes := 0
	var firstErr error
	for index, result := range results {
		item := webSearchBatchItem{Query: queries[index]}
		if result.err != nil {
			item.Status = "error"
			item.Error = "search failed"
			if firstErr == nil {
				firstErr = result.err
			}
			items[index] = item
			continue
		}
		successes++
		item.Status = "success"
		var content strings.Builder
		for _, citation := range result.citations {
			key := canonicalBatchURL(citation.URL)
			citationIndex, exists := citationIndexes[key]
			if !exists {
				citationIndex = len(allCitations) + 1
				citation.Index = citationIndex
				citation.ID = fmt.Sprintf("w_%d", citationIndex)
				allCitations = append(allCitations, citation)
				citationIndexes[key] = citationIndex
			}
			item.CitationIndexes = append(item.CitationIndexes, citationIndex)
			fmt.Fprintf(&content, "[%d] %s\n%s\n%s\n\n", citationIndex, citation.Title, citation.URL, citation.Snippet)
		}
		if content.Len() > 0 {
			item.Content = strings.TrimSpace(content.String())
		} else {
			item.Content = strings.TrimSpace(result.text)
		}
		items[index] = item
	}
	if successes == 0 {
		if firstErr == nil {
			firstErr = errors.New("search returned no results")
		}
		return "", nil, firstErr
	}
	hasEvidence := len(allCitations) > 0
	if !hasEvidence {
		for _, item := range items {
			if strings.TrimSpace(item.Content) != "" {
				hasEvidence = true
				break
			}
		}
	}
	if !hasEvidence {
		return "", nil, nil
	}
	status := "complete"
	if successes != len(queries) {
		status = "partial"
	}
	encoded, err := json.Marshal(webSearchBatchResult{Status: status, Items: items})
	if err != nil {
		return "", nil, err
	}
	return string(encoded), allCitations, nil
}

// webFetchTool implements §4.4 with the SSRF guards. `direct`/`reader` round
// trippers are nil in production (each falls back to the SSRF-safe client) and
// only injected by tests to avoid real network I/O / port constraints.
type webFetchTool struct {
	direct http.RoundTripper
	reader http.RoundTripper
}

func (t *webFetchTool) directClient() *http.Client {
	if t.direct != nil {
		return &http.Client{Transport: t.direct}
	}
	return ssrfSafeClient()
}

func (t *webFetchTool) readerClient() *http.Client {
	if t.reader != nil {
		return &http.Client{Transport: t.reader}
	}
	return ssrfSafeClient()
}

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch the main text content of web pages. Use url for one page or urls to batch several known pages into one tool call. SSRF-guarded: internal IPs are blocked."
}
func (t *webFetchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"One web URL. Use either url or urls."},"urls":{"type":"array","items":{"type":"string"},"maxItems":4,"description":"Up to 4 web URLs to fetch in one tool call."}}}`)
}

type webFetchInput struct {
	URL  string   `json:"url"`
	URLs []string `json:"urls"`
}

func (t *webFetchTool) Execute(ctx context.Context, input []byte, _ *llm.ToolContext) (string, []llm.Citation, error) {
	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", nil, &llm.ToolUserError{Message: "invalid web fetch input"}
	}
	urls, err := mergeBatchStrings(in.URL, in.URLs, webFetchBatchMaxItems, "url or urls required", canonicalBatchURL)
	if err != nil {
		return "", nil, err
	}
	if len(urls) == 1 {
		text, err := t.fetchOne(ctx, urls[0])
		return text, nil, err
	}
	return t.fetchBatch(ctx, urls)
}

func (t *webFetchTool) fetchOne(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", &llm.ToolUserError{Message: "invalid URL"}
	}
	// Reject non-web ports up-front (defence in depth — the dialer re-checks
	// the resolved IP + port on every hop, defeating redirects/rebinding).
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return "", &llm.ToolUserError{Message: "blocked non-web port"}
	}

	// Direct attempt first, on its own short deadline: an unreachable origin
	// (black-holed/filtered network) otherwise hangs until the whole tool budget
	// is spent and leaves nothing for the Jina fallback below.
	directCtx, cancel := context.WithTimeout(ctx, webFetchDirectAttemptTimeout)
	text, err := t.attemptDirect(directCtx, u.String())
	cancel()
	if err == nil && strings.TrimSpace(text) != "" {
		return text, nil
	}

	// § web_fetch Jina fallback: retry through a reader service when the origin
	// can't be read from this server (network failure, bot-blocked 4xx, or a
	// JS-only page that yields no text). The target is re-validated for the
	// reader hop — see jinaTargetAllowed.
	if webFetchJinaFallbackOn() && jinaTargetAllowed(u) {
		text, err = t.attemptJina(ctx, u.String())
		if err == nil && strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	return "", err
}

type webFetchBatchItem struct {
	URL     string `json:"url"`
	Status  string `json:"status"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

type webFetchBatchResult struct {
	Status string              `json:"status"`
	Items  []webFetchBatchItem `json:"items"`
}

func (t *webFetchTool) fetchBatch(ctx context.Context, urls []string) (string, []llm.Citation, error) {
	type fetchResult struct {
		text string
		err  error
	}
	results := make([]fetchResult, len(urls))
	runBoundedBatch(ctx, len(urls), func(index int) {
		defer func() {
			if recovered := recover(); recovered != nil {
				results[index].err = fmt.Errorf("batch fetch panicked: %v", recovered)
			}
		}()
		text, err := t.fetchOne(ctx, urls[index])
		results[index] = fetchResult{text: text, err: err}
	})
	items := make([]webFetchBatchItem, len(urls))
	successes := 0
	var firstErr error
	for index, result := range results {
		item := webFetchBatchItem{URL: urls[index]}
		if result.err != nil || strings.TrimSpace(result.text) == "" {
			item.Status = "error"
			item.Error = "page fetch failed"
			if firstErr == nil {
				firstErr = result.err
				if firstErr == nil {
					firstErr = errors.New("page fetch returned no content")
				}
			}
		} else {
			successes++
			item.Status = "success"
			item.Content = result.text
		}
		items[index] = item
	}
	if successes == 0 {
		return "", nil, firstErr
	}
	status := "complete"
	if successes != len(urls) {
		status = "partial"
	}
	encoded, err := json.Marshal(webFetchBatchResult{Status: status, Items: items})
	if err != nil {
		return "", nil, err
	}
	return string(encoded), nil, nil
}

func mergeBatchStrings(single string, multiple []string, maxItems int, requiredMessage string, key func(string) string) ([]string, error) {
	values := make([]string, 0, len(multiple)+1)
	if strings.TrimSpace(single) != "" {
		values = append(values, single)
	}
	values = append(values, multiple...)
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		dedupeKey := key(value)
		if dedupeKey == "" {
			continue
		}
		if _, exists := seen[dedupeKey]; exists {
			continue
		}
		seen[dedupeKey] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, &llm.ToolUserError{Message: requiredMessage}
	}
	if len(result) > maxItems {
		return nil, &llm.ToolUserError{Message: fmt.Sprintf("batch accepts at most %d items", maxItems)}
	}
	return result, nil
}

func canonicalBatchURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimSpace(raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" || parsed.Scheme == "http" && port == "80" || parsed.Scheme == "https" && port == "443" {
		parsed.Host = host
		if strings.Contains(host, ":") {
			parsed.Host = "[" + host + "]"
		}
	} else {
		parsed.Host = net.JoinHostPort(host, port)
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	parsed.Fragment = ""
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "mc_cid" || lower == "mc_eid" {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func runBoundedBatch(ctx context.Context, count int, run func(index int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, webBatchConcurrency)
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				run(index)
			case <-ctx.Done():
				run(index) // the canceled context makes the underlying operation return immediately
			}
		}(index)
	}
	wg.Wait()
}

// attemptDirect fetches the origin HTML, extracts readable text, and fails on
// transport errors, non-2xx status, or an empty extraction.
func (t *webFetchTool) attemptDirect(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("user-agent", "AivoryBot/1.0")
	resp, err := t.directClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("origin returned status %d", resp.StatusCode)
	}
	// Truncate after 256 KB — keeps tokens bounded.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, webFetchResponseBodyReadCap))
	return capExtractedText(stripHTML(string(body))), nil
}

// attemptJina reads the page through the configured reader service (default
// https://r.jina.ai/<url>), which renders content server-side (JS included)
// and returns extracted text/markdown instead of raw HTML.
func (t *webFetchTool) attemptJina(ctx context.Context, target string) (string, error) {
	base := strings.TrimSpace(webFetchJinaBase())
	if base == "" {
		return "", errors.New("jina fallback disabled")
	}
	readerURL, err := jinaReaderURL(base, target)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", readerURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("user-agent", "AivoryBot/1.0")
	resp, err := t.readerClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("reader returned status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, webFetchResponseBodyReadCap))
	return capExtractedText(strings.TrimSpace(string(body))), nil
}

// capExtractedText clips text to the per-§4.4 character budget on a rune
// boundary (byte-slicing a Go string can split a UTF-8 sequence mid-rune).
func capExtractedText(text string) string {
	if r := []rune(text); len(r) > webFetchExtractedTextCharCap {
		return string(r[:webFetchExtractedTextCharCap]) + "\n…[truncated]"
	}
	return text
}

// jinaReaderURL builds the reader URL for a target, e.g.
// https://r.jina.ai/<url-encoded target>. The base is operator-configurable so
// a self-hosted Jina-compatible reader can be pointed at.
func jinaReaderURL(base, target string) (string, error) {
	b, err := url.Parse(base)
	if err != nil || b.Host == "" || (b.Scheme != "https" && b.Scheme != "http") {
		return "", fmt.Errorf("invalid reader base %q", base)
	}
	return strings.TrimRight(base, "/") + "/" + url.PathEscape(target), nil
}

// jinaTargetAllowed re-asserts SSRF safety for the reader hop. The reader
// resolves the target from ITS network, so the local dial-time public-IP guard
// never sees the final address — check it here instead to keep cloud-metadata,
// loopback and RFC1918 targets out of the fallback. The URL already passed the
// scheme/port checks; a LOCAL resolution failure is not a block (the reader may
// still resolve it), so unknown-but-plausible names still get a chance.
func jinaTargetAllowed(target *url.URL) bool {
	addrs, err := net.LookupHost(target.Hostname())
	if err != nil {
		return true
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && isPublicIP(ip) {
			return true
		}
	}
	return false
}

// scriptStyleRe removes <script>/<style>/<noscript>/<svg> blocks before tag
// stripping. Adding noscript+svg eliminates a large class of decorative noise
// that survives plain tag stripping.
var scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript|svg|nav|aside|header|footer|form|iframe)[^>]*>.*?</(script|style|noscript|svg|nav|aside|header|footer|form|iframe)>`)

// readabilityContainerRe extracts the inner HTML of the most likely "main
// content" container — <article>, <main>, or a <div role="main">. If one is
// found we restrict stripHTML to its body so the snippet stops including site
// chrome (navigation, sidebars, related-article lists).
var readabilityContainerRe = regexp.MustCompile(`(?is)<(article|main)[^>]*>(.*?)</(article|main)>`)

// htmlEntities are the handful of named entities worth decoding for readability.
var htmlEntities = strings.NewReplacer(
	"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"",
	"&#39;", "'", "&apos;", "'", "&mdash;", "—", "&ndash;", "–", "&hellip;", "…",
)

// stripHTML extracts readable text from a web page. We approximate the
// readability algorithm:
//  1. drop script/style/nav/aside/header/footer/form/iframe blocks
//  2. prefer the inner HTML of an <article> / <main> container when present
//  3. strip tags, decode entities, collapse whitespace
//
// Not a full DOM parser but a vast improvement over the old "strip every tag"
// path for the web_fetch tool — boilerplate (cookie banners, sidebars,
// "related articles") now disappears and the model sees a cleaner article.
func stripHTML(s string) string {
	s = scriptStyleRe.ReplaceAllString(s, " ")
	// Prefer the main article body when present.
	if m := readabilityContainerRe.FindStringSubmatch(s); len(m) >= 3 {
		s = m[2]
	}
	out := strings.Builder{}
	inTag := false
	for _, c := range s {
		switch c {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				out.WriteRune(c)
			}
		}
	}
	text := htmlEntities.Replace(out.String())
	// Collapse runs of blank lines / spaces.
	text = regexp.MustCompile(`[ \t]+`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\n[ \t]*\n[ \t\n]*`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// fetchImageTool downloads a public image into the current conversation's
// persistent sandbox. The Python runner itself remains network-isolated; all
// outbound access goes through the backend's SSRF-safe client.
type fetchImageTool struct {
	sandbox sandbox.Service
	logger  *log.Logger
	client  *http.Client
}

func (t *fetchImageTool) Name() string { return "fetch_image" }
func (t *fetchImageTool) Description() string {
	return "Download an image from a public HTTP(S) URL into this conversation's Python sandbox. Returns a stable path under /workspace/downloads/ that python_execute can open with Pillow or use in documents."
}
func (t *fetchImageTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"filename":{"type":"string"}},"required":["url"]}`)
}

type fetchImageInput struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
}

func (t *fetchImageTool) Execute(ctx context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	var in fetchImageInput
	_ = json.Unmarshal(input, &in)
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", nil, &llm.ToolUserError{Message: "invalid image URL"}
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return "", nil, &llm.ToolUserError{Message: "blocked non-web port"}
	}
	if fetchRemoteImageDownloadCap <= 0 {
		return "", nil, errors.New("image downloads are unavailable")
	}
	if t.sandbox == nil || !t.sandbox.Enabled() {
		return "", nil, &llm.ToolUserError{Message: "fetch_image requires the Python sandbox to be configured"}
	}
	if tc == nil || tc.DB == nil || strings.TrimSpace(tc.ConvID) == "" || strings.TrimSpace(tc.MessageID) == "" || strings.TrimSpace(tc.UserID) == "" {
		return "", nil, errors.New("fetch_image requires an active conversation")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", nil, &llm.ToolUserError{Message: "invalid image URL"}
	}
	req.Header.Set("user-agent", "AivoryBot/1.0")
	client := t.client
	if client == nil {
		client = ssrfSafeClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("download public image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", nil, &llm.ToolUserError{Message: fmt.Sprintf("image download returned HTTP %d", resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchRemoteImageDownloadCap+1))
	if err != nil {
		return "", nil, fmt.Errorf("read public image: %w", err)
	}
	if len(data) == 0 {
		return "", nil, &llm.ToolUserError{Message: "image URL returned an empty response"}
	}
	if int64(len(data)) > fetchRemoteImageDownloadCap {
		return "", nil, &llm.ToolUserError{Message: "downloaded image exceeds the configured size limit"}
	}
	mimeType := verifiedImageMIMEFromBytes(data)
	if mimeType == "" {
		return "", nil, &llm.ToolUserError{Message: "URL did not return a supported image"}
	}

	sessionID, err := t.ensureSession(ctx, tc)
	if err != nil {
		return "", nil, err
	}
	path := "/workspace/downloads/" + downloadedImageName(in.Filename, u, mimeType)
	if err := t.sandbox.PutFile(ctx, sessionID, path, data); err != nil {
		if !isSandboxSessionGone(err) {
			return "", nil, fmt.Errorf("stage downloaded image: %w", err)
		}
		sessionID, err = t.rebuildSession(ctx, tc, sessionID)
		if err != nil {
			return "", nil, err
		}
		if err := t.sandbox.PutFile(ctx, sessionID, path, data); err != nil {
			return "", nil, fmt.Errorf("stage downloaded image: %w", err)
		}
	}
	return fmt.Sprintf("Saved image (%d bytes, %s) to %s. Use this exact path with python_execute.", len(data), mimeType, path), nil, nil
}

func (t *fetchImageTool) ensureSession(ctx context.Context, tc *llm.ToolContext) (string, error) {
	unlock := lockConvSandbox(tc.ConvID)
	defer unlock()
	sessionID, _ := store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id")
	if sessionID != "" {
		return sessionID, nil
	}
	sessionID, err := t.sandbox.NewSession(ctx, tc.ConvID)
	if err != nil {
		if t.logger != nil {
			t.logger.Printf("fetch_image: sandbox NewSession failed: %v", err)
		}
		return "", fmt.Errorf("sandbox session: %w", err)
	}
	if err := store.SetConvProviderStateKeyForUser(ctx, tc.DB, tc.ConvID, tc.MessageID, tc.UserID, "sandbox_id", sessionID); err != nil {
		_ = t.sandbox.Release(ctx, sessionID)
		return "", fmt.Errorf("persist sandbox session: %w", err)
	}
	return sessionID, nil
}

func (t *fetchImageTool) rebuildSession(ctx context.Context, tc *llm.ToolContext, previous string) (string, error) {
	unlock := lockConvSandbox(tc.ConvID)
	defer unlock()
	current, _ := store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id")
	if current != "" && current != previous {
		return current, nil
	}
	sessionID, err := t.sandbox.NewSession(ctx, tc.ConvID)
	if err != nil {
		return "", fmt.Errorf("sandbox session (rebuild): %w", err)
	}
	if err := store.SetConvProviderStateKeyForUser(ctx, tc.DB, tc.ConvID, tc.MessageID, tc.UserID, "sandbox_id", sessionID); err != nil {
		_ = t.sandbox.Release(ctx, sessionID)
		return "", fmt.Errorf("persist sandbox session (rebuild): %w", err)
	}
	return sessionID, nil
}

func downloadedImageName(want string, u *url.URL, mimeType string) string {
	base := strings.TrimSpace(want)
	if base == "" {
		base = filepath.Base(u.Path)
	}
	base = strings.TrimSuffix(filepath.Base(base), filepath.Ext(base))
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, base)
	base = strings.Trim(base, "-_")
	if base == "" {
		base = "image"
	}
	if len(base) > 80 {
		base = base[:80]
	}
	digest := sha256.Sum256([]byte(u.String()))
	return fmt.Sprintf("%s-%x%s", base, digest[:4], imageExtensionForMIME(mimeType))
}

func imageExtensionForMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	case "image/avif":
		return ".avif"
	case "image/heif":
		return ".heif"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	case "image/jxl":
		return ".jxl"
	case "image/vnd.adobe.photoshop":
		return ".psd"
	case "image/svg+xml":
		return ".svg"
	default:
		return ".png"
	}
}

var sandboxImageExtensions = map[string]struct{}{
	".apng": {}, ".avif": {}, ".bmp": {}, ".cr2": {}, ".cur": {}, ".dng": {},
	".eps": {}, ".gif": {}, ".heic": {}, ".heif": {}, ".ico": {}, ".jfif": {},
	".jpe": {}, ".jpeg": {}, ".jpg": {}, ".jxl": {}, ".nef": {}, ".png": {},
	".psd": {}, ".raw": {}, ".svg": {}, ".tif": {}, ".tiff": {}, ".webp": {},
}

var sandboxSVGTagRe = regexp.MustCompile(`(?i)<svg(?:\s|>)`)

// isSandboxImageInput classifies an input using every trustworthy signal we
// have before it reaches sandbox.PutFile. Metadata alone is not enough: legacy
// rows and skill manifests may be wrong, so bytes are also MIME-sniffed and the
// text-based SVG / ISO-BMFF formats get explicit checks.
func isSandboxImageInput(filename, mimeType, kind string, data []byte) bool {
	if strings.EqualFold(strings.TrimSpace(kind), "image") {
		return true
	}
	mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	if strings.HasPrefix(mimeType, "image/") {
		return true
	}
	if _, ok := sandboxImageExtensions[strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))]; ok {
		return true
	}
	if len(data) == 0 {
		return false
	}
	return verifiedImageMIMEFromBytes(data) != ""
}

func verifiedImageMIMEFromBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if detected := strings.ToLower(http.DetectContentType(data)); strings.HasPrefix(detected, "image/") {
		return detected
	}
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	if len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) {
		brands := head[8:]
		for _, brand := range [][]byte{
			[]byte("avif"), []byte("avis"),
		} {
			if bytes.Contains(brands, brand) {
				return "image/avif"
			}
		}
		for _, brand := range [][]byte{
			[]byte("heic"), []byte("heix"), []byte("hevc"), []byte("hevx"),
			[]byte("heim"), []byte("heis"), []byte("mif1"), []byte("msf1"),
		} {
			if bytes.Contains(brands, brand) {
				return "image/heif"
			}
		}
	}
	if bytes.HasPrefix(head, []byte("8BPS")) {
		return "image/vnd.adobe.photoshop"
	}
	if bytes.HasPrefix(head, []byte{0xff, 0x0a}) || bytes.HasPrefix(head, []byte("\x00\x00\x00\x0cJXL \r\n\x87\n")) {
		return "image/jxl"
	}
	if sandboxSVGTagRe.Match(head) {
		return "image/svg+xml"
	}
	return ""
}

func readSandboxUpload(path string, storedSize, limit int64, roots ...string) ([]byte, error) {
	if limit <= 0 || storedSize < 0 || storedSize > limit {
		return nil, errors.New("sandbox upload exceeds the staging limit")
	}
	safePath, err := fileguard.ResolveExisting(path, roots...)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(safePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("sandbox upload exceeds the staging limit")
	}
	return data, nil
}

// convSandboxMu serialises sandbox-session provisioning per conversation so two
// concurrent python_execute calls in one turn don't each create a session and
// clobber the conversation's sandbox_id (leaking the orphaned container until
// the idle reaper). Keyed by conversation id; the per-conv mutex is held only
// across the cheap get→create→persist, never across exec/staging.
var convSandboxMu sync.Map // convID -> *sync.Mutex

func lockConvSandbox(convID string) func() {
	m, _ := convSandboxMu.LoadOrStore(convID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// pythonExecuteTool — design.md §4.5. Proxies to the configured sandbox
// service (the single dependency point). When no sandbox is configured it
// falls back to a safe-mode arithmetic evaluator so dev stays usable.
type pythonExecuteTool struct {
	sandbox     sandbox.Service
	uploadDir   string
	artifactDir string
	logger      *log.Logger
}

func (t *pythonExecuteTool) Name() string { return "python_execute" }
func (t *pythonExecuteTool) Description() string {
	return "Run Python in a persistent sandbox for math, data analysis, image editing, plotting, spreadsheet/CSV processing, editing existing PDF/Office documents, and generating downloadable files (PDF/PPTX/DOCX/XLSX/PNG). The session and its /workspace persist across calls AND across turns in this conversation, so call it several times in a row — inspect the inputs first, then edit or compute, and read again differently if the first attempt doesn't fit. Every conversation upload, including the original PDF/DOCX/PPTX/XLSX file, is staged without format conversion in /workspace/uploads/; prior image-generation outputs are staged there too, and public images fetched with fetch_image are stored in /workspace/downloads/. Run `import os; os.listdir('/workspace/uploads')` and inspect /workspace/downloads when needed, then use the real paths (for example python-docx/python-pptx/pypdf for documents, Pillow for images, and pandas for tables). Preserve the original file's layout and formatting when the user asks for a targeted edit. Write outputs, including edited images, plots, and documents, to /workspace/outputs to return them as downloadable artifacts. Produced files are attached to the assistant message automatically: refer to them by filename and never emit sandbox: or /workspace/outputs paths as download links. Stdout/stderr is returned."
}
func (t *pythonExecuteTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}},"required":["code"]}`)
}

type pyInput struct {
	Code string `json:"code"`
}

func (t *pythonExecuteTool) Execute(ctx context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	var in pyInput
	_ = json.Unmarshal(input, &in)
	if strings.TrimSpace(in.Code) == "" {
		return "", nil, &llm.ToolUserError{Message: "code required"}
	}

	// Safe-mode fallback when no sandbox backend is wired in.
	if t.sandbox == nil || !t.sandbox.Enabled() {
		// Log loudly: this is the usual reason the model says "I can't run code /
		// host a download". It means SANDBOX_BASE_URL is empty in the API
		// container (or the admin cleared sandbox_base_url).
		if t.logger != nil {
			t.logger.Printf("python_execute: SANDBOX NOT CONFIGURED — running in safe-mode (set SANDBOX_BASE_URL / Admin → settings sandbox_base_url)")
		}
		if answer := tryQuickArithmetic(in.Code); answer != "" {
			return "stdout:\n" + answer + "\n(local arithmetic evaluator; configure the sandbox in Admin → settings for real Python execution)", nil, nil
		}
		return "[python_execute is in safe-mode] Configure a sandbox URL + key in Admin settings to execute real Python.", nil, nil
	}

	// Reuse the conversation's persistent session (§4.5) so /workspace files
	// carry across calls; provision one on first use. The get→create→persist is
	// serialised per conversation: two concurrent python_execute calls in one
	// turn (the model can emit several tool calls, run via runToolsConcurrent)
	// would otherwise each see an empty sandbox_id, each NewSession(), and clobber
	// the other's id — leaking a container until the 30-min reaper.
	sessionID := ""
	hasConv := tc != nil && tc.DB != nil && tc.ConvID != ""
	var unlockConv func()
	if hasConv {
		unlockConv = lockConvSandbox(tc.ConvID)
	}
	if hasConv {
		sessionID, _ = store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id")
	}
	if sessionID == "" {
		archiveKey := ""
		if hasConv {
			archiveKey = tc.ConvID
		}
		sid, err := t.sandbox.NewSession(ctx, archiveKey)
		if err != nil {
			if unlockConv != nil {
				unlockConv()
			}
			// Reachability/auth problem talking to the sandbox sidecar — surface
			// it in the server log so it's diagnosable (the model only sees a
			// generic tool error otherwise).
			if t.logger != nil {
				t.logger.Printf("python_execute: sandbox NewSession failed: %v", err)
			}
			return "", nil, pythonSandboxPublicError(fmt.Errorf("sandbox session: %w", err))
		}
		sessionID = sid
		if hasConv {
			if perr := store.SetConvProviderStateKeyForUser(ctx, tc.DB, tc.ConvID, tc.MessageID, tc.UserID, "sandbox_id", sessionID); perr != nil {
				// We created a container but couldn't durably record its id, so the
				// next call would provision a SECOND session and leak this one until
				// the 30-min reaper. Release it now and fail fast.
				_ = t.sandbox.Release(ctx, sessionID)
				if unlockConv != nil {
					unlockConv()
				}
				if t.logger != nil {
					t.logger.Printf("python_execute: persist sandbox_id failed, released session: %v", perr)
				}
				return "", nil, fmt.Errorf("persist sandbox session: %w", perr)
			}
		}
	}
	if unlockConv != nil {
		unlockConv()
	}

	// Reset the persistent input namespaces, then stage every current
	// conversation upload into /workspace/uploads. Keeping the original bytes is
	// important for targeted edits to Office/PDF files where parsing and
	// regenerating content would discard layout and formatting. Reset prevents
	// stale inputs from surviving deletion or permission changes between calls.
	stageFiles := func(sid string) error {
		if err := t.sandbox.ResetInputs(ctx, sid); err != nil {
			return fmt.Errorf("reset sandbox inputs: %w", err)
		}
		if tc == nil || tc.DB == nil || tc.ConvID == "" {
			return nil
		}
		// Dedupe staged names so multiple files that share a basename (e.g. four
		// pasted "image.png") don't overwrite each other at the same path — every
		// upload must land distinctly so the model can use all of them.
		seen := map[string]bool{}
		uniqueName := func(name string) string {
			base := filepath.Base(name)
			if base == "" || base == "." || base == "/" {
				base = "file"
			}
			if !seen[base] {
				seen[base] = true
				return base
			}
			ext := filepath.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			for i := 2; ; i++ {
				cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
				if !seen[cand] {
					seen[cand] = true
					return cand
				}
			}
		}
		if files, err := store.ListFilesByConversation(ctx, tc.DB, tc.ConvID, tc.UserID); err == nil {
			for _, f := range files {
				data, err := readSandboxUpload(f.StoragePath, f.SizeBytes, pythonExecuteUploadStagingFileSize, t.uploadDir)
				if err != nil {
					continue
				}
				dest := "/workspace/uploads/" + uniqueName(f.Filename)
				if err := t.sandbox.PutFile(ctx, sid, dest, data); err != nil {
					return fmt.Errorf("stage conversation upload %q: %w", f.Filename, err)
				}
			}
		}
		// Generated images are artifacts rather than rows in files. Mount only
		// image_generate / hosted image-generation outputs so Python can edit the
		// image produced by a previous turn without exposing unrelated tool output.
		if artifacts, err := store.ListImageArtifactsByConversation(ctx, tc.DB, tc.ConvID, tc.UserID); err == nil {
			for _, artifact := range artifacts {
				if !reusableGeneratedImageSource(artifact.Source) {
					continue
				}
				data, err := readSandboxUpload(artifact.StoragePath, artifact.SizeBytes, pythonExecuteUploadStagingFileSize, t.artifactDir)
				if err != nil {
					continue
				}
				mimeType := verifiedImageMIMEFromBytes(data)
				if mimeType == "" {
					continue
				}
				name := uniqueName("generated-" + artifact.Filename)
				_ = t.sandbox.PutFile(ctx, sid, "/workspace/uploads/"+name, data)
			}
		}
		// Stage non-image skill assets too (§4.17) so use_skill can reference scripts/data
		// from /workspace/skills/<name>/. Scope to the skills bound to THIS model
		// (model_skills) — the same set use_skill can load and the index advertises.
		if tc.DB != nil && tc.ModelID != "" && tc.AllowsBuiltinTool("use_skill") {
			if skillIDs, err := store.SkillsForModel(ctx, tc.DB, tc.ModelID); err == nil {
				for _, sid2 := range skillIDs {
					if !tc.AllowsAdminSkill(sid2) {
						continue
					}
					sk, err := store.GetSkill(ctx, tc.DB, sid2)
					if err != nil || sk == nil || !sk.Enabled {
						continue
					}
					assets, err := store.ListSkillAssets(ctx, tc.DB, sk.ID)
					if err != nil {
						continue
					}
					// Sanitise both path components: a skill name / asset filename
					// containing "/" or ".." must not steer the dest outside
					// /workspace/skills/<name>/ (the sidecar confines to /workspace
					// regardless, but keep the path well-formed — defense in depth).
					skillDir := filepath.Base(sk.Name)
					if skillDir == "." || skillDir == "/" || skillDir == "" {
						skillDir = "skill"
					}
					for _, a := range assets {
						if isSandboxImageInput(a.Filename, a.MimeType, "", nil) {
							continue
						}
						safePath, err := fileguard.ResolveExisting(a.StoragePath, filepath.Join(t.uploadDir, "skill-assets"))
						if err != nil {
							continue
						}
						data, err := os.ReadFile(safePath)
						if err != nil {
							continue
						}
						if isSandboxImageInput(a.Filename, a.MimeType, "", data) {
							continue
						}
						assetName := filepath.Base(a.Filename)
						if assetName == "." || assetName == "/" || assetName == "" {
							assetName = "asset"
						}
						_ = t.sandbox.PutFile(ctx, sid, "/workspace/skills/"+skillDir+"/"+assetName, data)
					}
				}
			}
		}
		return nil
	}
	if err := stageFiles(sessionID); err != nil {
		// The reaper may remove an idle container while its durable sandbox_id is
		// still stored on the conversation. A canceled HTTP request can also leave
		// its synchronous sidecar handler holding the old session lock. Replace
		// either stale session once, then re-stage authoritative inputs.
		if contextEnded(ctx, err) {
			t.abandonPythonSession(ctx, tc, sessionID)
			return "", nil, err
		}
		if !isSandboxSessionRecoverable(err) {
			return "", nil, pythonSandboxPublicError(err)
		}
		rebuilt, rebuildErr := t.rebuildPythonSession(ctx, tc, sessionID)
		if rebuildErr != nil {
			return "", nil, pythonSandboxPublicError(rebuildErr)
		}
		sessionID = rebuilt
		if err := stageFiles(sessionID); err != nil {
			if contextEnded(ctx, err) {
				t.abandonPythonSession(ctx, tc, sessionID)
			}
			return "", nil, pythonSandboxPublicError(err)
		}
	}

	res, err := t.sandbox.Exec(ctx, sessionID, in.Code)
	if err != nil {
		// §4.5 reaper recovery: if the upstream reaped the session container
		// while we were idle, Exec returns 404. Provision a fresh session,
		// re-stage uploads + skills, and retry once before bubbling the error.
		if isSandboxSessionRecoverable(err) {
			rebuilt, rebuildErr := t.rebuildPythonSession(ctx, tc, sessionID)
			if rebuildErr != nil {
				return "", nil, pythonSandboxPublicError(rebuildErr)
			}
			sessionID = rebuilt
			// §4.5 workspace restore: if a prior run archived /workspace, the
			// sandbox-service auto-restores on session creation. We re-stage
			// uploads (always cheap) so the new container has user data.
			if stageErr := stageFiles(sessionID); stageErr != nil {
				return "", nil, pythonSandboxPublicError(stageErr)
			}
			res, err = t.sandbox.Exec(ctx, sessionID, in.Code)
		}
	}
	if err != nil {
		if contextEnded(ctx, err) {
			t.abandonPythonSession(ctx, tc, sessionID)
		}
		return "", nil, pythonSandboxPublicError(err)
	}

	// Persist produced files as artifacts + surface them to the orchestrator.
	for _, f := range res.Files {
		if _, err := saveArtifact(ctx, tc, t.artifactDir, f.Name, f.MimeType, store.ArtifactSourcePythonExecute, f.Data); err != nil {
			return "", nil, fmt.Errorf("persist artifact %q: %w", f.Name, err)
		}
	}

	// Pitfall A5: truncate sandbox output at 32KB so a huge stdout can't flood
	// the model context and blow up single-turn cost (§4.5).
	out := strings.Builder{}
	if res.Stdout != "" {
		out.WriteString("stdout:\n" + truncateOutput(res.Stdout, pythonExecuteStdoutStderrTruncationCap) + "\n")
	}
	if res.Stderr != "" {
		out.WriteString("stderr:\n" + truncateOutput(res.Stderr, pythonExecuteStdoutStderrTruncationCap) + "\n")
	}
	if len(res.Files) > 0 {
		out.WriteString("\nProduced files:\n")
		for _, f := range res.Files {
			fmt.Fprintf(&out, "- %s (%s)\n", f.Name, f.MimeType)
		}
		out.WriteString("Files are attached to this message automatically. Refer to them by filename; do not emit sandbox: or /workspace/outputs links.\n")
	}
	if out.Len() == 0 {
		out.WriteString("(no output)")
	}
	return out.String(), nil, nil
}

func (t *pythonExecuteTool) rebuildPythonSession(ctx context.Context, tc *llm.ToolContext, previousID string) (string, error) {
	hasConv := tc != nil && tc.DB != nil && tc.ConvID != ""
	if !hasConv {
		sid, err := t.sandbox.NewSession(ctx, "")
		if err != nil {
			return "", fmt.Errorf("sandbox session (rebuild): %w", err)
		}
		return sid, nil
	}

	unlock := lockConvSandbox(tc.ConvID)
	defer unlock()
	current, _ := store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id")
	if current != "" && current != previousID {
		return current, nil
	}

	sid, err := t.sandbox.NewSession(ctx, tc.ConvID)
	if err != nil {
		return "", fmt.Errorf("sandbox session (rebuild): %w", err)
	}
	if err := store.SetConvProviderStateKeyForUser(ctx, tc.DB, tc.ConvID, tc.MessageID, tc.UserID, "sandbox_id", sid); err != nil {
		_ = t.sandbox.Release(ctx, sid)
		return "", fmt.Errorf("persist sandbox session (rebuild): %w", err)
	}
	return sid, nil
}

// abandonPythonSession disconnects future calls from a session whose HTTP
// request was canceled. The FastAPI handler executes blocking Docker work in a
// worker thread and can continue holding the session lock after its client has
// gone away; keeping that id would make every later health check queue behind
// work that no longer has a consumer.
func (t *pythonExecuteTool) abandonPythonSession(ctx context.Context, tc *llm.ToolContext, sessionID string) {
	if tc == nil || tc.DB == nil || tc.ConvID == "" || sessionID == "" {
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	unlock := lockConvSandbox(tc.ConvID)
	defer unlock()
	current, err := store.GetConvProviderStateKey(persistCtx, tc.DB, tc.ConvID, "sandbox_id")
	if err != nil || current != sessionID {
		return
	}
	if err := store.SetConvProviderStateKeyForUser(persistCtx, tc.DB, tc.ConvID, tc.MessageID, tc.UserID, "sandbox_id", ""); err != nil && t.logger != nil {
		t.logger.Printf("python_execute: failed to abandon canceled sandbox session: %v", err)
	}
}

func contextEnded(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func pythonSandboxPublicError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var httpErr *sandbox.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests {
		return &llm.ToolUserError{Message: "Python sandbox is busy. Please wait a moment and try again."}
	}
	return err
}

func reusableGeneratedImageSource(source string) bool {
	switch strings.TrimSpace(source) {
	case "", store.ArtifactSourceImageGenerate, store.ArtifactSourceHostedImageGeneration:
		// Empty source is retained for artifacts created before source metadata was
		// introduced; explicit Python artifacts remain excluded.
		return true
	default:
		return false
	}
}

// saveArtifact writes a tool-produced file to ArtifactDir, records it, and
// notifies the orchestrator so it streams an artifact event + persists a block.
// Shared by python_execute (sandbox outputs) and image_generate.
func saveArtifact(ctx context.Context, tc *llm.ToolContext, artifactDir, name, mime, source string, data []byte) (*store.Artifact, error) {
	if tc == nil || tc.DB == nil || tc.MessageID == "" {
		return nil, errors.New("artifact context is incomplete")
	}
	dir := artifactDir
	if dir == "" {
		dir = "./data/artifacts"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	safe := filepath.Base(name)
	// Unique on-disk name: a single turn can produce several artifacts that share
	// a display name (e.g. three image_generate calls each emitting "image_1.png").
	// Keying the path only on messageID+name made them collide → every artifact
	// row pointed at the last file written. A random token guarantees distinct
	// files (the display Filename stays `safe`).
	tok := make([]byte, 6)
	_, _ = rand.Read(tok)
	path := filepath.Join(dir, tc.MessageID+"_"+hex.EncodeToString(tok)+"_"+safe)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	art, err := store.CreateArtifactForUser(ctx, tc.DB, store.Artifact{
		MessageID:   tc.MessageID,
		Filename:    safe,
		StoragePath: path,
		MimeType:    mime,
		SizeBytes:   int64(len(data)),
		Source:      source,
	}, tc.ConvID, tc.UserID)
	if err != nil || art == nil {
		_ = os.Remove(path)
		if err == nil {
			err = errors.New("artifact row was not created")
		}
		return nil, err
	}
	if tc.OnArtifact != nil {
		tc.OnArtifact(llm.ArtifactRef{
			ID: art.ID, Filename: safe, URL: "/api/artifacts/" + art.ID,
			MimeType: mime, Size: int64(len(data)),
		})
	}
	return art, nil
}

// tryQuickArithmetic returns the result of a single `print(expr)` line.
func tryQuickArithmetic(code string) string {
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, "print(") || !strings.HasSuffix(code, ")") {
		return ""
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(code, "print("), ")")
	if strings.ContainsAny(inner, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return ""
	}
	v, ok := evalArith(inner)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%g", v)
}

func evalArith(expr string) (float64, bool) {
	// Tiny shunting-yard for + - * / and parens.
	tokens := tokenizeArith(expr)
	out := []string{}
	ops := []string{}
	prec := map[string]int{"+": 1, "-": 1, "*": 2, "/": 2}
	for _, t := range tokens {
		switch {
		case isNumber(t):
			out = append(out, t)
		case t == "(":
			ops = append(ops, t)
		case t == ")":
			for len(ops) > 0 && ops[len(ops)-1] != "(" {
				out = append(out, ops[len(ops)-1])
				ops = ops[:len(ops)-1]
			}
			if len(ops) == 0 {
				return 0, false
			}
			ops = ops[:len(ops)-1]
		case prec[t] > 0:
			for len(ops) > 0 && prec[ops[len(ops)-1]] >= prec[t] {
				out = append(out, ops[len(ops)-1])
				ops = ops[:len(ops)-1]
			}
			ops = append(ops, t)
		default:
			return 0, false
		}
	}
	for len(ops) > 0 {
		out = append(out, ops[len(ops)-1])
		ops = ops[:len(ops)-1]
	}
	stack := []float64{}
	for _, t := range out {
		if isNumber(t) {
			var n float64
			fmt.Sscanf(t, "%f", &n)
			stack = append(stack, n)
			continue
		}
		if len(stack) < 2 {
			return 0, false
		}
		b := stack[len(stack)-1]
		a := stack[len(stack)-2]
		stack = stack[:len(stack)-2]
		switch t {
		case "+":
			stack = append(stack, a+b)
		case "-":
			stack = append(stack, a-b)
		case "*":
			stack = append(stack, a*b)
		case "/":
			if b == 0 {
				return 0, false
			}
			stack = append(stack, a/b)
		}
	}
	if len(stack) != 1 {
		return 0, false
	}
	return stack[0], true
}
func tokenizeArith(s string) []string {
	out := []string{}
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, c := range s {
		switch {
		case c == ' ' || c == '\t':
			flush()
		case c == '+' || c == '-' || c == '*' || c == '/' || c == '(' || c == ')':
			flush()
			out = append(out, string(c))
		case (c >= '0' && c <= '9') || c == '.':
			cur.WriteRune(c)
		default:
			flush()
		}
	}
	flush()
	return out
}
func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || c == '.') {
			return false
		}
	}
	return true
}

// imageGenerateTool — design.md §4.12. Dual-channel (Gemini generateContent /
// OpenAI Images API) image generation routed by the user's pre-selected image
// model. Implemented in full below.
type imageGenerateTool struct {
	db          *sql.DB
	uploadDir   string
	artifactDir string
	logger      *log.Logger
}

func (t *imageGenerateTool) Name() string { return "image_generate" }
func (t *imageGenerateTool) Description() string {
	return "Generate a new image or faithfully edit one existing image. You must explicitly choose action=generate or action=edit from the user's intent. Generate never sends conversation images to the image API. For edit, select exactly one authoritative base_image: previous_generation for the nearest generated image on the active branch, or current_attachment plus its 1-based base_image_index for an image uploaded this turn. Other current-turn images become edit references. Do not invent file ids."
}
func (t *imageGenerateTool) InputSchema() json.RawMessage {
	// Image ids stay server-side. The chat model selects a semantic source and a
	// 1-based current-attachment position instead of copying opaque file ids.
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"The requested new image or the exact edit instruction. For edits, describe only the requested change and preserve everything else."},"action":{"type":"string","enum":["generate","edit"],"description":"Use generate only when the user wants a new image. Use edit only when the user wants to modify an existing image."},"base_image":{"type":"string","enum":["none","previous_generation","current_attachment"],"description":"For generate use none. For edit, choose previous_generation only when continuing the prior generated result, or current_attachment when an image uploaded this turn is the authoritative base."},"base_image_index":{"type":"integer","minimum":1,"description":"Required for edit with base_image=current_attachment when more than one image was uploaded this turn. This is the 1-based attachment position."},"n":{"type":"integer","default":1},"size":{"type":"string","description":"Optional explicit output size. OpenAI-format requests recognize the aspect ratio and the 1K, 2K, or 4K resolution tier from the exact user instruction; omitted resolution defaults to 2K. The final GPT Image 2 WIDTHxHEIGHT is normalized to legal multiples of 16. Edits preserve the selected base image's ratio unless the user requests another ratio. GPT Image 1.x is mapped to its supported fixed sizes."}},"required":["prompt","action","base_image"]}`)
}

type imgInput struct {
	Prompt         string   `json:"prompt"`
	UserPrompt     string   `json:"-"`
	Action         string   `json:"action"`
	BaseImage      string   `json:"base_image"`
	BaseImageIndex int      `json:"base_image_index"`
	N              int      `json:"n"`
	Size           string   `json:"size"`
	InputImages    []string `json:"input_images"`
}

func (t *imageGenerateTool) Execute(ctx context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	var in imgInput
	_ = json.Unmarshal(input, &in)
	var inputFields map[string]json.RawMessage
	_ = json.Unmarshal(input, &inputFields)
	_, nProvided := inputFields["n"]
	if strings.TrimSpace(in.Prompt) == "" {
		return "", nil, &llm.ToolUserError{Message: "prompt required"}
	}
	in.UserPrompt = strings.TrimSpace(in.Prompt)
	if tc != nil && strings.TrimSpace(tc.ImageUserPrompt) != "" {
		in.UserPrompt = strings.TrimSpace(tc.ImageUserPrompt)
	}
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	in.BaseImage = strings.ToLower(strings.TrimSpace(in.BaseImage))
	if err := validateImageOperation(in); err != nil {
		return "", nil, err
	}
	in.Size = strings.TrimSpace(in.Size)
	if in.Size == "" {
		in.Size = strings.TrimSpace(inSize)
	}
	imageRequestParams := map[string]any(nil)
	resolvedImageRequestParams := map[string]any(nil)
	if tc != nil {
		resolvedImageRequestParams = tc.ImageRequestParams
		imageRequestParams = sanitizeImageRequestParams(resolvedImageRequestParams)
		if configuredSize, ok := imageRequestParams["size"].(string); ok && strings.TrimSpace(configuredSize) != "" {
			in.Size = configuredSize
		}
	}

	// Resolve the image model: the user's pre-selected one (§4.12-B) first,
	// else the first enabled kind=image model.
	model, err := t.resolveImageModel(ctx, tc)
	if err != nil {
		return "", nil, err
	}
	// Drawing mode passes its already-selected mappings explicitly. Chat tool
	// calls historically passed nil here, silently dropping the selected image
	// model's quality/background defaults even though both paths used one model.
	if tc == nil || tc.ImageRequestParams == nil {
		picks := t.storedImageParamPicks(ctx, tc, model.ID)
		resolvedImageRequestParams = llm.MergeParamControls(nil, model.ParamControls, picks)
		imageRequestParams = sanitizeImageRequestParams(resolvedImageRequestParams)
	}
	if !nProvided {
		// n is a server-owned request boundary and is intentionally stripped by
		// sanitizeImageRequestParams before provider merging. Read it from the
		// validated param-control result first so chat tools inherit the same count
		// as direct drawing without allowing arbitrary provider fields through.
		in.N = llm.ImageGenerationCountFromParams(resolvedImageRequestParams)
	}
	in.N = llm.ClampImageGenerationCount(in.N)

	// §8.2 每用户每日图像张数限额（由 quota_ledger 原子预留）。 Resolve
	// n after model defaults so direct mode and chat tool calls project the same
	// quantity before either reaches the provider.
	var dailyReservation *store.QuotaReservation
	providerDelivered := false
	if tc != nil && tc.DB != nil {
		dailyReservation, err = t.checkDailyImageLimit(ctx, tc.UserID, in.N)
		if err != nil {
			if errors.Is(err, llm.ErrDailyImageLimitReached) {
				return "", nil, &llm.ToolRefusalError{Message: llm.ErrDailyImageLimitReached.Error()}
			}
			return "", nil, err
		}
		defer func() {
			if dailyReservation != nil && !providerDelivered {
				releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
				defer cancel()
				_ = store.ReleaseQuotaReservation(releaseCtx, t.db, dailyReservation.ID)
			}
		}()
	}
	channel, err := store.GetChannel(ctx, t.db, model.ChannelID)
	if err != nil {
		return "", nil, err
	}
	if channel.APIKey == "" {
		return "No API key on the image channel — ask an admin to configure it.", nil, nil
	}
	fallbackChannel := t.resolveImageFallbackChannel(ctx, model, channel)

	// §4.20 per-model image quota — shared across drawing mode and chat tool-call
	// (both log purpose='image' against this model id), enforced here so neither
	// path bypasses the other's usage.
	// §4.20 image quota. Drawing mode (tc.SkipImageQuota) already metered + charged
	// upstream → skip here. Otherwise (chat tool-call) run the SAME free→credits→
	// block decision via ImageBilling so it matches drawing mode; payImageCredits
	// is honored after generation. (Falls back to the legacy hard cap only if no
	// biller is wired, e.g. a non-orchestrator caller.)
	var imageBillingReservation *llm.ImageBillingReservation
	var legacyModelQuota *store.QuotaReservation
	if tc != nil && tc.DB != nil && !tc.SkipImageQuota {
		if tc.ImageBilling != nil {
			var allow bool
			var refuseMsg string
			var billingErr error
			imageBillingReservation, allow, refuseMsg, billingErr = tc.ImageBilling.ReserveImageBilling(
				ctx, tc.UserID, model, in.N, tc.NextImageBillingSourceID(),
			)
			if billingErr != nil {
				return "", nil, billingErr
			}
			if !allow {
				return "", nil, &llm.ToolRefusalError{Message: refuseMsg}
			}
			defer func() {
				if imageBillingReservation != nil && !providerDelivered {
					releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
					defer cancel()
					_ = tc.ImageBilling.ReleaseImageBilling(releaseCtx, imageBillingReservation)
				}
			}()
		} else {
			legacyModelQuota, err = t.checkModelImageQuota(ctx, tc.UserID, model, in.N)
			if err != nil {
				return "", nil, &llm.ToolRefusalError{Message: err.Error()}
			}
			defer func() {
				if legacyModelQuota != nil && !providerDelivered {
					releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
					defer cancel()
					_ = store.ReleaseQuotaReservation(releaseCtx, t.db, legacyModelQuota.ID)
				}
			}()
		}
	}

	// Image bytes are resolved only after the caller explicitly chooses edit and
	// its authoritative base. Merely having current attachments or an older image
	// on the branch must never turn a generation request into an edit request.
	inputLimit := imageInputImageLimit(channel.Type, model.RequestID)
	inputImgs, err := t.resolveImageOperationInputs(ctx, tc, in, inputLimit)
	if err != nil {
		return "", nil, err
	}
	// The exact user instruction is authoritative whenever this request becomes an
	// edit, including explicit previous-generation selection with no new attachment. A chat
	// or prompt-optimization model must not broaden a literal edit into a restyle.
	if len(inputImgs) > 0 && tc != nil && strings.TrimSpace(tc.ImageUserPrompt) != "" {
		in.Prompt = strings.TrimSpace(tc.ImageUserPrompt)
	}
	// §4.12-E 内容安全: screen the final provider prompt after edit resolution.
	if err := t.moderateImagePrompt(in.Prompt); err != nil {
		return "", nil, &llm.ToolRefusalError{Message: err.Error()}
	}
	providerInput := in
	if len(inputImgs) > 0 {
		providerInput.Prompt = faithfulImageEditPrompt(in.Prompt)
	}

	// §4.20 per-model image timeout: cap this single generation/edit request when
	// the admin set one (0 = no per-model cap; bounded only by the turn context).
	genCtx := ctx
	if model.ImageTimeoutSec > 0 {
		var cancel context.CancelFunc
		genCtx, cancel = context.WithTimeout(ctx, time.Duration(model.ImageTimeoutSec)*time.Second)
		defer cancel()
	}

	includeRequestBody := imageRequestBodyLoggingEnabled(t.db)
	captureSuccessRequest := imageSuccessRequestLoggingEnabled(t.db)
	runAttempt := func(attemptChannel *store.Channel) ([]imageBytes, llm.ProviderRequestDiagnostics, error) {
		var diagnostics llm.ProviderRequestDiagnostics
		capture := func(request *http.Request) {
			diagnostics = llm.CaptureProviderRequestDiagnostics(request, includeRequestBody)
		}
		var generated []imageBytes
		var attemptErr error
		switch imageChannelFamily(attemptChannel.Type) {
		case "gemini":
			generated, attemptErr = geminiGenerateImages(genCtx, attemptChannel.BaseURL, attemptChannel.APIKey, model.RequestID, providerInput, inputImgs, imageRequestParams, capture)
		case "openai":
			generated, attemptErr = openaiGenerateImages(genCtx, attemptChannel.BaseURL, attemptChannel.APIKey, model.RequestID, providerInput, inputImgs, imageRequestParams, capture)
		default:
			attemptErr = fmt.Errorf("image generation not supported for channel type %q", attemptChannel.Type)
		}
		if attemptErr == nil && len(generated) == 0 {
			if genCtx.Err() != nil {
				attemptErr = genCtx.Err()
			} else {
				attemptErr = errors.New("the image model returned no images")
			}
		}
		return generated, diagnostics, attemptErr
	}

	images, requestDiagnostics, err := runAttempt(channel)
	servedChannel := channel
	usedFallback := false
	if err != nil {
		t.logImageProviderFailure(ctx, tc, model, channel.ID, false, requestDiagnostics, err)
		if fallbackChannel != nil && imageFallbackAllowed(ctx, genCtx, err) {
			servedChannel = fallbackChannel
			usedFallback = true
			images, requestDiagnostics, err = runAttempt(fallbackChannel)
			if err != nil {
				t.logImageProviderFailure(ctx, tc, model, fallbackChannel.ID, true, requestDiagnostics, err)
			}
		}
	}
	if err != nil {
		return "", nil, err
	}
	// Treat the clamped request count as an output boundary too. A provider that
	// returns extra candidates must not bypass the daily/quota preflight, persist
	// more artifacts than requested, or debit more credits than were approved.
	if len(images) > in.N {
		images = images[:in.N]
	}

	// The bytes are in hand → persist the artifacts + meter on a DETACHED context
	// so a stop / timeout landing in this narrow window can't drop a delivered
	// image or skip its usage row (which feeds the daily limit + per-model quota).
	persistCtx := context.WithoutCancel(ctx)
	for i, img := range images {
		ext := extForMime(img.mime)
		name := fmt.Sprintf("image_%d%s", i+1, ext)
		if _, saveErr := saveArtifact(persistCtx, tc, t.artifactDir, name, img.mime, store.ArtifactSourceImageGenerate, img.data); saveErr != nil {
			return "", nil, fmt.Errorf("persist generated image %d: %w", i+1, saveErr)
		}
	}
	providerDelivered = true
	if dailyReservation != nil {
		if _, err := store.FinalizeQuotaReservation(persistCtx, t.db, dailyReservation.ID, float64(len(images))); err != nil {
			return "", nil, fmt.Errorf("finalize daily image quota: %w", err)
		}
	}
	imageCost := float64(len(images)) * model.PricePerImage
	if legacyModelQuota != nil {
		actual := imageCost
		if legacyModelQuota.LimitType == "count" {
			actual = float64(len(images))
		}
		if _, err := store.FinalizeQuotaReservation(persistCtx, t.db, legacyModelQuota.ID, actual); err != nil {
			return "", nil, fmt.Errorf("finalize image model quota: %w", err)
		}
	}

	// §4.20: if the image model's free allotment is exhausted, charge the image
	// cost in credits (same flow as drawing mode) via ImageBilling, and record the
	// full charge so the turn is not misclassified as free quota usage.
	var imageCredits float64
	usage := store.UsageLog{
		ModelID: model.ID, Purpose: "image", ImagesCount: len(images), Cost: imageCost, Currency: model.Currency,
		ChannelID: servedChannel.ID, Fallback: usedFallback,
	}
	if captureSuccessRequest {
		usage.RequestMethod = requestDiagnostics.Method
		usage.RequestURL = requestDiagnostics.URL
		usage.RequestHeaders = requestDiagnostics.Headers
		usage.RequestBody = requestDiagnostics.Body
	}
	if tc != nil {
		usage.UserID = tc.UserID
		usage.WorkspaceID = tc.WorkspaceID
		usage.ConversationID = tc.ConvID
		usage.MessageID = tc.MessageID
	}
	if tc != nil && tc.DB != nil {
		if err := store.RecordBillingUsage(persistCtx, t.db, usage); err != nil {
			return "", nil, fmt.Errorf("record image billing: %w", err)
		}
	}
	if imageBillingReservation != nil && tc != nil && tc.ImageBilling != nil {
		_, total, settleErr := tc.ImageBilling.SettleImageBilling(persistCtx, imageBillingReservation, len(images), imageCost)
		if settleErr != nil {
			return "", nil, fmt.Errorf("settle image credits: %w", settleErr)
		}
		imageCredits = total
		tc.AddImageCredits(total)
	}

	// Record cost (§8.3) — one usage row, images_count = N.
	if tc != nil && tc.DB != nil {
		usage.Credits = imageCredits
		if err := store.LogUsageAnalytics(persistCtx, t.db, usage); err != nil {
			return "", nil, fmt.Errorf("record image billing: %w", err)
		}
	}
	return fmt.Sprintf("Generated %d image(s) for: %s. They are attached as downloadable artifacts.", len(images), in.Prompt), nil, nil
}

func (t *imageGenerateTool) storedImageParamPicks(ctx context.Context, tc *llm.ToolContext, modelID string) map[string]any {
	if tc == nil || tc.DB == nil || tc.UserID == "" || modelID == "" {
		return nil
	}
	raw, err := store.GetUserSettingKey(ctx, tc.DB, tc.UserID, "image_model_params")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var saved struct {
		ModelID string         `json:"model_id"`
		Params  map[string]any `json:"params"`
	}
	if json.Unmarshal(raw, &saved) != nil || saved.ModelID != modelID {
		return nil
	}
	return saved.Params
}

// resolveImageModel picks the user's pre-selected image model, falling back to
// the first enabled kind=image model.
func (t *imageGenerateTool) resolveImageModel(ctx context.Context, tc *llm.ToolContext) (*store.Model, error) {
	if tc != nil && tc.ImageModelID != "" {
		if m, err := store.GetModel(ctx, t.db, tc.ImageModelID); err == nil && m.Enabled && m.Kind == "image" {
			return m, nil
		}
	}
	models, err := store.ListModels(ctx, t.db, "image", true)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, &llm.ToolUserError{Message: "no image model configured — an admin must add one (kind=image)"}
	}
	m := models[0]
	return &m, nil
}

type imageBytes struct {
	data []byte
	mime string
}

func imageChannelFamily(channelType string) string {
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case "openai":
		return "openai"
	case "google", "gemini":
		return "gemini"
	default:
		return ""
	}
}

func (t *imageGenerateTool) resolveImageFallbackChannel(ctx context.Context, model *store.Model, primary *store.Channel) *store.Channel {
	fallbackID := strings.TrimSpace(model.FallbackChannelID)
	if fallbackID == "" || fallbackID == primary.ID {
		return nil
	}
	fallback, err := store.GetChannel(ctx, t.db, fallbackID)
	if err != nil {
		if t.logger != nil {
			t.logger.Printf("image: model %q fallback channel %q not found — ignoring", model.ID, fallbackID)
		}
		return nil
	}
	sameFamily := imageChannelFamily(primary.Type) != "" && imageChannelFamily(primary.Type) == imageChannelFamily(fallback.Type)
	sameFormat := strings.EqualFold(strings.TrimSpace(primary.APIFormat), strings.TrimSpace(fallback.APIFormat))
	if !fallback.Enabled || !sameFamily || !sameFormat || strings.TrimSpace(fallback.APIKey) == "" {
		if t.logger != nil {
			t.logger.Printf("image: model %q fallback channel %q unusable (enabled=%v type=%q/%q format=%q/%q hasKey=%v) — ignoring",
				model.ID, fallback.ID, fallback.Enabled, fallback.Type, primary.Type, fallback.APIFormat, primary.APIFormat, fallback.APIKey != "")
		}
		return nil
	}
	return fallback
}

func imageFallbackAllowed(parentCtx, attemptCtx context.Context, err error) bool {
	if err == nil || parentCtx.Err() != nil || attemptCtx.Err() != nil {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func imageRequestBodyLoggingEnabled(db *sql.DB) bool {
	return imageLoggingSettingBool(db, "log_request_bodies", true)
}

func imageSuccessRequestLoggingEnabled(db *sql.DB) bool {
	return imageLoggingSettingBool(db, "log_full_requests", false) && !imageLoggingSettingBool(db, "log_errors_only", true)
}

func imageLoggingSettingBool(db *sql.DB, key string, fallback bool) bool {
	raw, err := store.GetSetting(db, key)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return fallback
	}
	return value
}

func (t *imageGenerateTool) logImageProviderFailure(
	ctx context.Context,
	tc *llm.ToolContext,
	model *store.Model,
	channelID string,
	fallback bool,
	diagnostics llm.ProviderRequestDiagnostics,
	providerErr error,
) {
	if tc == nil || tc.DB == nil || strings.TrimSpace(tc.UserID) == "" || providerErr == nil || ctx.Err() != nil {
		return
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	row := store.UsageLog{
		UserID: tc.UserID, WorkspaceID: tc.WorkspaceID,
		ConversationID: tc.ConvID, MessageID: tc.MessageID,
		ModelID: model.ID, Purpose: "image", Currency: model.Currency,
		ChannelID: channelID, Fallback: fallback, Status: "error",
		Error:         truncateImageProviderError(providerErr.Error()),
		RequestMethod: diagnostics.Method, RequestURL: diagnostics.URL,
		RequestHeaders: diagnostics.Headers, RequestBody: diagnostics.Body,
	}
	if err := store.LogUsageAnalytics(logCtx, t.db, row); err != nil && t.logger != nil {
		t.logger.Printf("image: usage error log write failed (msg=%s channel=%s): %v", tc.MessageID, channelID, err)
	}
}

func truncateImageProviderError(message string) string {
	const maxRunes = 2000
	runes := []rune(message)
	if len(runes) <= maxRunes {
		return message
	}
	return string(runes[:maxRunes]) + "…"
}

// moderateImagePrompt screens an image prompt against the admin-managed keyword
// blocklist (§ moderation — settings key "moderation_keywords"). There is no
// hardcoded word list: when the admin hasn't configured any keywords, image
// prompts pass this pre-filter. Matches both the raw lowercased text and a
// normalized form (leetspeak/spacing/punctuation folded) to defeat basic
// evasions. This is a fast PRE-FILTER, not a complete control.
func (t *imageGenerateTool) moderateImagePrompt(prompt string) error {
	raw, err := store.GetSetting(t.db, "moderation_keywords")
	if err != nil || len(raw) == 0 {
		return nil
	}
	var keywords []string
	if json.Unmarshal(raw, &keywords) != nil || len(keywords) == 0 {
		return nil
	}
	low := strings.ToLower(prompt)
	norm := normalizeForModeration(prompt)
	for _, w := range keywords {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(w)) || (norm != "" && strings.Contains(norm, normalizeForModeration(w))) {
			return errors.New("image prompt rejected by content policy")
		}
	}
	return nil
}

// normalizeForModeration lowercases, folds common leetspeak to letters, and
// strips everything except letters/digits (so "c h i l d", "ch1ld", and
// zero-width-injected variants all collapse to the same token stream).
func normalizeForModeration(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t", "@", "a", "$", "s").Replace(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// checkDailyImageLimit atomically reserves the per-user daily image quota.
func (t *imageGenerateTool) checkDailyImageLimit(ctx context.Context, userID string, n int) (*store.QuotaReservation, error) {
	limit := 30
	if raw, err := store.GetSetting(t.db, "daily_image_limit"); err == nil {
		if json.Unmarshal(raw, &limit) != nil || limit < 0 {
			return nil, store.ErrInvalidCreditConfig
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	dayStart := time.Now().Truncate(dailyImageLimitResetWindow).Unix()
	reservation, allowed, err := store.ReserveFixedQuota(
		ctx, t.db, userID, store.QuotaScopeDailyImage, n, limit, dayStart,
		dayStart+int64(dailyImageLimitResetWindow/time.Second),
	)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, llm.ErrDailyImageLimitReached
	}
	return reservation, nil
}

// imageQuotaMessage is the admin-configurable over-limit prompt.
func imageQuotaMessage(db *sql.DB) string {
	if raw, err := store.GetSetting(db, "quota_exceeded_message"); err == nil {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return s
		}
	}
	return "You've reached your plan's image quota for this model."
}

// checkModelImageQuota atomically reserves the image model's per-group quota for
// legacy tool callers that do not expose the credit-aware ImageBilling API.
func (t *imageGenerateTool) checkModelImageQuota(ctx context.Context, userID string, model *store.Model, n int) (*store.QuotaReservation, error) {
	if userID == "" {
		return nil, nil
	}
	u, err := store.FindUserByID(ctx, t.db, userID)
	if err != nil {
		return nil, errors.New(imageQuotaMessage(t.db))
	}
	if u != nil && u.Role == "admin" {
		return nil, nil // admins are exempt from usage quotas
	}
	has, err := store.ModelHasAnyQuota(ctx, t.db, model.ID)
	if err != nil {
		return nil, errors.New(imageQuotaMessage(t.db))
	}
	if !has {
		// This legacy path has no ImageBilling implementation with which to debit
		// credits. Never turn the all-toggles-off state into a free image call.
		return nil, errors.New(imageQuotaMessage(t.db))
	}
	groupID := store.DefaultGroupID
	if u != nil && u.GroupID != "" {
		groupID = u.GroupID
	}
	q, err := store.GetModelQuota(ctx, t.db, model.ID, groupID)
	if err != nil {
		// No free allowance. Without a biller this fallback cannot charge credits,
		// so refuse instead of silently producing a free image.
		return nil, errors.New(imageQuotaMessage(t.db))
	}
	if q.LimitValue <= 0 {
		return nil, nil // granted unlimited
	}
	if n <= 0 {
		n = 1
	}
	requested := float64(n) * model.PricePerImage
	if q.LimitType == "count" {
		requested = float64(n)
	}
	reservation, allowed, err := store.ReserveModelQuota(
		ctx, t.db, userID, model.ID, store.QuotaScopeModelImage, *q, requested, false,
	)
	if err != nil || !allowed {
		return nil, errors.New(imageQuotaMessage(t.db))
	}
	return reservation, nil
}

func mergeImageInputIDs(primary, additional []string) []string {
	out := make([]string, 0, len(primary)+len(additional))
	seen := make(map[string]bool, len(primary)+len(additional))
	for _, ids := range [][]string{primary, additional} {
		for _, rawID := range ids {
			id := strings.TrimSpace(rawID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func validateImageOperation(in imgInput) error {
	switch in.Action {
	case "generate":
		if in.BaseImage != "none" {
			return &llm.ToolUserError{Message: "action=generate requires base_image=none"}
		}
		if in.BaseImageIndex != 0 {
			return &llm.ToolUserError{Message: "action=generate must not set base_image_index"}
		}
		return nil
	case "edit":
		switch in.BaseImage {
		case "previous_generation":
			if in.BaseImageIndex != 0 {
				return &llm.ToolUserError{Message: "base_image_index is only valid with base_image=current_attachment"}
			}
			return nil
		case "current_attachment":
			if in.BaseImageIndex < 0 {
				return &llm.ToolUserError{Message: "base_image_index must be a 1-based current attachment position"}
			}
			return nil
		default:
			return &llm.ToolUserError{Message: "Please specify whether to edit the previous generated image or which current attachment should be used as the base image."}
		}
	default:
		return &llm.ToolUserError{Message: "action must be generate or edit"}
	}
}

func (t *imageGenerateTool) resolveImageOperationInputs(ctx context.Context, tc *llm.ToolContext, in imgInput, limit int) ([]imageBytes, error) {
	if in.Action == "generate" {
		return nil, nil
	}
	if tc == nil || tc.DB == nil {
		return nil, &llm.ToolUserError{Message: "image editing requires an active conversation"}
	}

	currentIDs := mergeImageInputIDs(tc.ImageInputIDs, in.InputImages)
	var base imageBytes
	referenceIDs := currentIDs
	switch in.BaseImage {
	case "previous_generation":
		previous := t.loadNearestBranchImage(ctx, tc)
		if previous == nil {
			return nil, &llm.ToolUserError{Message: "no previous generated image is available on the active conversation branch"}
		}
		base = *previous
	case "current_attachment":
		if len(currentIDs) == 0 {
			return nil, &llm.ToolUserError{Message: "no current-turn image attachment is available as the edit base"}
		}
		index := in.BaseImageIndex
		if index == 0 && len(currentIDs) == 1 {
			index = 1
		}
		if index < 1 || index > len(currentIDs) {
			return nil, &llm.ToolUserError{Message: fmt.Sprintf("base_image_index must select one of the %d current-turn image attachment(s)", len(currentIDs))}
		}
		baseID := currentIDs[index-1]
		baseImages, _ := t.loadInputImages(ctx, tc, []string{baseID}, 1)
		if len(baseImages) != 1 {
			return nil, &llm.ToolUserError{Message: "the selected current-turn base image is unavailable or invalid"}
		}
		base = baseImages[0]
		referenceIDs = append([]string(nil), currentIDs[:index-1]...)
		referenceIDs = append(referenceIDs, currentIDs[index:]...)
	}

	references, tooManyInputs := t.loadInputImages(ctx, tc, referenceIDs, limit)
	inputs := make([]imageBytes, 0, len(references)+1)
	inputs = append(inputs, base)
	inputs = append(inputs, references...)
	if tooManyInputs || (limit > 0 && len(inputs) > limit) {
		return nil, &llm.ToolUserError{Message: fmt.Sprintf("the selected image model accepts at most %d input image(s)", limit)}
	}
	return inputs, nil
}

func faithfulImageEditPrompt(instruction string) string {
	return `Faithfully edit the supplied source image according to the instruction below.
Treat the first supplied image as the authoritative base canvas. Any later supplied images are references for the requested changes, not replacement canvases, unless the instruction explicitly selects a different base. Change only what the instruction explicitly requests. Preserve every other detail as closely as possible, especially the canvas and crop, composition, layout, colors, background, lighting, texture, text content, language, typography, spacing, and alignment. Do not translate, paraphrase, retype, add, remove, or restyle anything unless the instruction explicitly requires it.

Exact user edit instruction:
` + strings.TrimSpace(instruction)
}

// loadInputImages resolves artifact ids to raw image bytes (ownership-checked)
// for image-to-image workflows (§4.12-C).
func (t *imageGenerateTool) loadInputImages(ctx context.Context, tc *llm.ToolContext, ids []string, limit int) ([]imageBytes, bool) {
	if tc == nil || tc.DB == nil {
		return nil, false
	}
	out := []imageBytes{}
	for _, id := range ids {
		// An input image id can be an ARTIFACT (a prior generation) or a user
		// UPLOAD (files table) — the studio passes reference uploads as file ids,
		// so try both. Both are ownership-scoped.
		var data []byte
		var mime string
		if art, err := store.GetArtifact(ctx, t.db, id, tc.UserID); err == nil && art != nil {
			data, mime = readVerifiedImageInput(art.StoragePath, art.SizeBytes, t.artifactDir)
		} else if f, err := store.GetFile(ctx, t.db, id, tc.UserID); err == nil && f != nil && f.ConversationID == tc.ConvID {
			// User uploads are conversation-scoped. An owned file id from a
			// different chat cannot be smuggled into this image-model request.
			data, mime = readVerifiedImageInput(f.StoragePath, f.SizeBytes, t.uploadDir)
		} else {
			continue
		}
		if len(data) == 0 || mime == "" {
			continue
		}
		if limit > 0 && len(out) >= limit {
			return out, true
		}
		out = append(out, imageBytes{data: data, mime: mime})
	}
	return out, false
}

func imageInputImageLimit(channelType, requestID string) int {
	if imageImageInputImageCap > 0 {
		return imageImageInputImageCap
	}
	channelType = strings.ToLower(strings.TrimSpace(channelType))
	modelID := strings.ToLower(strings.TrimSpace(requestID))
	switch {
	case strings.Contains(modelID, "dall-e"):
		return 1
	case channelType == "openai":
		return 16
	case (channelType == "google" || channelType == "gemini") && strings.Contains(modelID, "gemini-3"):
		return 14
	case channelType == "google" || channelType == "gemini":
		return 3
	default:
		return 3
	}
}

// loadNearestBranchImage follows persisted parent links from the assistant
// currently being generated. It also checks the current assistant first so a
// second image_generate call in one tool loop can edit the first call's output.
func (t *imageGenerateTool) loadNearestBranchImage(ctx context.Context, tc *llm.ToolContext) *imageBytes {
	if tc == nil || tc.DB == nil || tc.ConvID == "" || tc.MessageID == "" {
		return nil
	}
	messageID := tc.MessageID
	seen := map[string]bool{}
	for messageID != "" && !seen[messageID] {
		seen[messageID] = true
		if artifact, err := store.FirstImageArtifactForMessage(ctx, tc.DB, messageID, tc.ConvID); err == nil && artifact != nil {
			if data, mimeType := readVerifiedImageInput(artifact.StoragePath, artifact.SizeBytes, t.artifactDir); len(data) > 0 && mimeType != "" {
				return &imageBytes{data: data, mime: mimeType}
			}
		}
		message, err := store.GetMessage(ctx, tc.DB, messageID)
		if err != nil || message == nil || message.ConversationID != tc.ConvID {
			return nil
		}
		messageID = message.ParentID
	}
	return nil
}

func readVerifiedImageInput(path string, storedSize int64, roots ...string) ([]byte, string) {
	if fetchRemoteImageDownloadCap <= 0 || storedSize > fetchRemoteImageDownloadCap {
		return nil, ""
	}
	safePath, err := fileguard.ResolveExisting(path, roots...)
	if err != nil {
		return nil, ""
	}
	f, err := os.Open(safePath)
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, fetchRemoteImageDownloadCap+1))
	if err != nil || int64(len(data)) > fetchRemoteImageDownloadCap {
		return nil, ""
	}
	mimeType := verifiedImageMIMEFromBytes(data)
	if mimeType == "" {
		return nil, ""
	}
	return data, mimeType
}

// geminiGenerateImages calls generateContent with an image-capable model and
// extracts inlineData parts (§4.12-C). Explicit or branch-resolved input images
// ride along as inline_data parts so the
// model edits rather than starts fresh (§4.12-D).
func geminiGenerateImages(ctx context.Context, baseURL, apiKey, requestID string, in imgInput, inputImgs []imageBytes, requestParams map[string]any, requestObservers ...func(*http.Request)) ([]imageBytes, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	parts := []map[string]any{}
	for _, img := range inputImgs {
		// camelCase, not proto snake_case — relay gateways parse into
		// camelCase-only structs and silently drop snake_case keys (see
		// google_provider.go toolsDecl).
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{
				"mimeType": img.mime,
				"data":     base64.StdEncoding.EncodeToString(img.data),
			},
		})
	}
	parts = append(parts, map[string]any{"text": in.Prompt})
	native := map[string]any{
		"contents": []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{
			"responseModalities": []string{"IMAGE"},
			"candidateCount":     llm.ClampImageGenerationCount(in.N),
		},
	}
	cleanParams := sanitizeImageRequestParams(requestParams)
	applyDefaultGeminiImageSize(cleanParams, requestID)
	body := store.DeepMergeJSONObjects(cleanParams, native)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", base, requestID, apiKey)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	notifyImageRequestObservers(req, requestObservers)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini image %d: %s", resp.StatusCode, string(b))
	}
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := []imageBytes{}
	cands, _ := parsed["candidates"].([]any)
	for _, c := range cands {
		cm, _ := c.(map[string]any)
		content, _ := cm["content"].(map[string]any)
		ps, _ := content["parts"].([]any)
		for _, p := range ps {
			pm, _ := p.(map[string]any)
			inl, _ := pm["inlineData"].(map[string]any)
			if inl == nil {
				inl, _ = pm["inline_data"].(map[string]any)
			}
			if inl == nil {
				continue
			}
			b64, _ := inl["data"].(string)
			mime, _ := inl["mimeType"].(string)
			if mime == "" {
				mime, _ = inl["mime_type"].(string)
			}
			data, err := base64.StdEncoding.DecodeString(b64)
			if err == nil && len(data) > 0 {
				if detected := verifiedImageMIMEFromBytes(data); detected != "" {
					mime = detected
				}
				out = append(out, imageBytes{data: data, mime: orDefaultStr(mime, "image/png")})
			}
		}
	}
	return out, nil
}

// openaiGenerateImages calls the Images API (§4.12-C): plain generation via
// /v1/images/generations, or — when input images are supplied — image editing
// via the multipart /v1/images/edits endpoint.
func openaiGenerateImages(ctx context.Context, baseURL, apiKey, requestID string, in imgInput, inputImgs []imageBytes, requestParams map[string]any, requestObservers ...func(*http.Request)) ([]imageBytes, error) {
	base := llm.OpenAIBaseURL(baseURL)

	// gpt-image-1 returns b64_json natively and REJECTS the response_format
	// param; only the DALL·E models accept it. Send it only for dall-e and parse
	// both b64_json and url responses so either model family works.
	isDalle := strings.Contains(strings.ToLower(requestID), "dall")
	cleanParams := sanitizeImageRequestParams(requestParams)
	if _, configured := cleanParams["quality"]; !configured {
		if quality := defaultOpenAIImageQuality(requestID); quality != "" {
			cleanParams["quality"] = quality
		}
	}
	if len(inputImgs) > 0 {
		modelID := strings.ToLower(strings.TrimSpace(requestID))
		switch {
		case isOpenAIModelOrSnapshot(modelID, "gpt-image-2"):
			// GPT Image 2 always processes edit inputs at high fidelity and rejects
			// input_fidelity as a configurable field.
			delete(cleanParams, "input_fidelity")
		case isOpenAIModelOrSnapshot(modelID, "gpt-image-1.5"),
			isOpenAIModelOrSnapshot(modelID, "gpt-image-1-mini"),
			isOpenAIModelOrSnapshot(modelID, "gpt-image-1"):
			// Older GPT Image models default to lower input fidelity. Make faithful
			// editing the safe default while still honoring an explicit admin control.
			if _, configured := cleanParams["input_fidelity"]; !configured {
				cleanParams["input_fidelity"] = "high"
			}
		}
	}
	requestedSize := strings.TrimSpace(in.Size)
	if configuredSize, ok := cleanParams["size"].(string); ok && strings.TrimSpace(configuredSize) != "" {
		requestedSize = strings.TrimSpace(configuredSize)
	} else {
		// A malformed or empty admin fragment must not suppress server sizing.
		delete(cleanParams, "size")
	}
	requestedSize = resolveOpenAIImageSize(requestID, in.UserPrompt, requestedSize, inputImgs)

	native := map[string]any{
		"model":  requestID,
		"prompt": in.Prompt,
		"n":      llm.ClampImageGenerationCount(in.N),
	}
	if requestedSize != "" {
		native["size"] = requestedSize
	}
	if isDalle {
		native["response_format"] = "b64_json"
	}
	body := store.DeepMergeJSONObjects(cleanParams, native)
	var req *http.Request
	var err error
	if len(inputImgs) > 0 {
		// Image edit (图生图): multipart form with the source image + prompt.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		keys := make([]string, 0, len(body))
		for key := range body {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if value, ok := imageMultipartScalar(body[key]); ok {
				if err := mw.WriteField(key, value); err != nil {
					return nil, err
				}
			}
		}
		requestImages := inputImgs
		if isDalle && len(requestImages) > 1 {
			requestImages = requestImages[:1]
		}
		imageField := "image"
		if len(requestImages) > 1 {
			imageField = "image[]"
		}
		for i, inputImg := range requestImages {
			filename := fmt.Sprintf("input_%d%s", i+1, extForMime(inputImg.mime))
			fw, err := mw.CreateFormFile(imageField, filename)
			if err != nil {
				return nil, err
			}
			if _, err := fw.Write(inputImg.data); err != nil {
				return nil, err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, "POST", base+"/images/edits", &buf)
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", mw.FormDataContentType())
	} else {
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return nil, marshalErr
		}
		req, err = http.NewRequestWithContext(ctx, "POST", base+"/images/generations", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
	}
	req.Header.Set("authorization", "Bearer "+apiKey)
	notifyImageRequestObservers(req, requestObservers)
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai image %d: %s", resp.StatusCode, string(b))
	}
	var parsed struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := []imageBytes{}
	for _, d := range parsed.Data {
		if d.B64JSON != "" {
			if data, err := base64.StdEncoding.DecodeString(d.B64JSON); err == nil {
				mimeType := outputFormatMIME(cleanParams["output_format"])
				if detected := verifiedImageMIMEFromBytes(data); detected != "" {
					mimeType = detected
				}
				out = append(out, imageBytes{data: data, mime: mimeType})
			}
			continue
		}
		// Some models / gateways return a hosted URL instead of inline base64 —
		// fetch the bytes so the result is always a stored artifact.
		if d.URL != "" {
			if data, mime := fetchRemoteImage(ctx, d.URL); len(data) > 0 {
				out = append(out, imageBytes{data: data, mime: mime})
			}
		}
	}
	return out, nil
}

func notifyImageRequestObservers(req *http.Request, observers []func(*http.Request)) {
	for _, observer := range observers {
		if observer != nil {
			observer(req)
		}
	}
}

func outputFormatMIME(value any) string {
	format, _ := value.(string)
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

const (
	gptImage2MinPixels       = 655360
	gptImage2MaxPixels       = 8294400
	gptImage2MaxEdge         = 3840
	gptImage2MaxAspect       = 3.0
	gptImage2DefaultLongEdge = 2048
)

var (
	openAIImageDimensionsPattern = regexp.MustCompile(`(?i)([0-9]{2,5})\s*[x×]\s*([0-9]{2,5})(?:\s*(?:px|像素))?`)
	openAIImageAspectPattern     = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*[:：/比]\s*([0-9]+(?:\.[0-9]+)?)`)
	openAIImageKPattern          = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])([0-9]{1,2})\s*k(?:[^0-9a-z]|$)`)
	openAIConfiguredSizePattern  = regexp.MustCompile(`(?i)^\s*([0-9]{2,5})\s*x\s*([0-9]{2,5})\s*$`)
)

type openAIImageSizingDirective struct {
	ratio               float64
	longEdge            int
	ratioSpecified      bool
	resolutionSpecified bool
	preserveSource      bool
}

// resolveOpenAIImageSize turns natural-language aspect-ratio and resolution
// requests into the OpenAI-compatible size field. User text wins over a mapped
// size control. With no resolution request or mapped size, 2K is the server
// default; edits inherit the authoritative first input's ratio, while fresh
// generations default to square.
func resolveOpenAIImageSize(requestID, userPrompt, configuredSize string, inputImgs []imageBytes) string {
	directive := parseOpenAIImageSizingDirective(userPrompt)
	configuredWidth, configuredHeight, hasConfiguredSize := parseOpenAIConfiguredSize(configuredSize)
	sourceWidth, sourceHeight := 0, 0
	if len(inputImgs) > 0 {
		if config, _, err := image.DecodeConfig(bytes.NewReader(inputImgs[0].data)); err == nil {
			sourceWidth, sourceHeight = config.Width, config.Height
		}
	}
	if directive.preserveSource && sourceWidth > 0 && sourceHeight > 0 {
		return closestOpenAIImageSize(requestID, float64(sourceWidth)/float64(sourceHeight), closestOpenAIImageTierLongEdge(max(sourceWidth, sourceHeight)))
	}
	if !directive.ratioSpecified && !directive.resolutionSpecified && hasConfiguredSize {
		return closestOpenAIImageSizeForDimensions(requestID, configuredWidth, configuredHeight)
	}

	ratio := directive.ratio
	if !directive.ratioSpecified {
		switch {
		case hasConfiguredSize:
			ratio = float64(configuredWidth) / float64(configuredHeight)
		case sourceWidth > 0 && sourceHeight > 0:
			ratio = float64(sourceWidth) / float64(sourceHeight)
		default:
			ratio = 1
		}
	}

	longEdge := directive.longEdge
	switch {
	case directive.resolutionSpecified:
		// The user-provided resolution is authoritative.
	case directive.ratioSpecified:
		// A ratio without a resolution uses the requested 2K default, even when a
		// model control happens to contain a lower default size.
		longEdge = gptImage2DefaultLongEdge
	case hasConfiguredSize:
		longEdge = max(configuredWidth, configuredHeight)
	default:
		longEdge = gptImage2DefaultLongEdge
	}
	if longEdge <= 0 {
		longEdge = gptImage2DefaultLongEdge
	}
	return closestOpenAIImageSize(requestID, ratio, longEdge)
}

func parseOpenAIImageSizingDirective(prompt string) openAIImageSizingDirective {
	text := strings.ToLower(strings.TrimSpace(prompt))
	directive := openAIImageSizingDirective{}
	if text == "" {
		return directive
	}
	if match := openAIImageDimensionsPattern.FindStringSubmatch(text); len(match) == 3 {
		width, widthErr := strconv.Atoi(match[1])
		height, heightErr := strconv.Atoi(match[2])
		if widthErr == nil && heightErr == nil && width > 0 && height > 0 {
			if width < 256 && height < 256 {
				directive.ratio = float64(width) / float64(height)
				directive.ratioSpecified = true
			} else {
				directive.ratio = float64(width) / float64(height)
				directive.longEdge = closestOpenAIImageTierLongEdge(max(width, height))
				directive.ratioSpecified = true
				directive.resolutionSpecified = true
			}
		}
	}
	if !directive.ratioSpecified {
		if match := openAIImageAspectPattern.FindStringSubmatch(text); len(match) == 3 {
			width, widthErr := strconv.ParseFloat(match[1], 64)
			height, heightErr := strconv.ParseFloat(match[2], 64)
			hasAspectLabel := containsOpenAIImageTerm(text, "比例", "宽高比", "纵横比", "縱橫比", "aspect", "ratio")
			if widthErr == nil && heightErr == nil && width > 0 && height > 0 &&
				(hasAspectLabel || isCommonOpenAIImageAspect(width, height)) {
				directive.ratio = width / height
				directive.ratioSpecified = true
			}
		}
	}
	if !directive.ratioSpecified {
		switch {
		case containsOpenAIImageTerm(text, "正方形", "方形", "square"):
			directive.ratio = 1
			directive.ratioSpecified = true
		case containsOpenAIImageTerm(text, "手机壁纸", "竖版", "竖屏", "竖图", "竖向", "portrait", "phone wallpaper", "mobile wallpaper"):
			directive.ratio = 9.0 / 16.0
			directive.ratioSpecified = true
		case containsOpenAIImageTerm(text, "电脑壁纸", "桌面壁纸", "横版", "横屏", "横图", "横向", "landscape", "widescreen", "desktop wallpaper"):
			directive.ratio = 16.0 / 9.0
			directive.ratioSpecified = true
		}
	}
	if !directive.resolutionSpecified {
		if match := openAIImageKPattern.FindStringSubmatch(text); len(match) == 2 {
			if requestedK, err := strconv.Atoi(match[1]); err == nil && requestedK > 0 {
				directive.longEdge = closestOpenAIImageTierLongEdge(requestedK * 1024)
			}
			directive.resolutionSpecified = directive.longEdge > 0
		}
	}
	directive.preserveSource = containsOpenAIImageTerm(
		text, "保持原尺寸", "保留原尺寸", "原始尺寸", "保持分辨率", "原分辨率",
		"keep original size", "preserve original size", "same size", "keep the resolution",
	)
	return directive
}

func parseOpenAIConfiguredSize(size string) (int, int, bool) {
	match := openAIConfiguredSizePattern.FindStringSubmatch(strings.TrimSpace(size))
	if len(match) != 3 {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(match[1])
	height, heightErr := strconv.Atoi(match[2])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func closestOpenAIImageTierLongEdge(edge int) int {
	switch {
	case edge <= 1536:
		return 1024
	case edge <= 3072:
		return 2048
	default:
		return 3840
	}
}

func containsOpenAIImageTerm(text string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func isCommonOpenAIImageAspect(width, height float64) bool {
	if width != math.Trunc(width) || height != math.Trunc(height) {
		return false
	}
	pair := fmt.Sprintf("%d:%d", int(width), int(height))
	switch pair {
	case "1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "9:21", "21:9":
		return true
	default:
		return false
	}
}

func closestOpenAIImageSize(requestID string, ratio float64, longEdge int) string {
	modelID := strings.ToLower(strings.TrimSpace(requestID))
	switch {
	case isOpenAIModelOrSnapshot(modelID, "gpt-image-1.5"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1-mini"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1"):
		return closestGPTImage1SizeFromRatio(ratio)
	case modelID == "dall-e-3":
		return closestDALL3Size(ratio)
	case modelID == "dall-e-2":
		return "1024x1024"
	default:
		// gpt-image-2 and OpenAI-compatible image models receive a legal custom
		// pixel size. Compatible gateways can therefore honor the same textual
		// aspect-ratio and resolution contract without exposing provider fields.
		return closestGPTImage2SizeForTarget(ratio, longEdge)
	}
}

func closestOpenAIImageSizeForDimensions(requestID string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	ratio := float64(width) / float64(height)
	modelID := strings.ToLower(strings.TrimSpace(requestID))
	switch {
	case isOpenAIModelOrSnapshot(modelID, "gpt-image-1.5"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1-mini"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1"):
		return closestGPTImage1SizeFromRatio(ratio)
	case modelID == "dall-e-3":
		return closestDALL3Size(ratio)
	case modelID == "dall-e-2":
		return "1024x1024"
	default:
		return closestGPTImage2SizeForDimensions(width, height)
	}
}

// inferredOpenAIEditSize preserves the first edit image's canvas as closely as
// the selected GPT Image generation supports. Unknown models and undecodable
// formats deliberately return empty so the provider's default `auto` behavior
// remains authoritative instead of falling back to a square.
func inferredOpenAIEditSize(requestID string, source imageBytes) string {
	config, _, err := image.DecodeConfig(bytes.NewReader(source.data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return ""
	}

	modelID := strings.ToLower(strings.TrimSpace(requestID))
	switch {
	case isOpenAIModelOrSnapshot(modelID, "gpt-image-2"):
		return closestGPTImage2Size(config.Width, config.Height)
	case isOpenAIModelOrSnapshot(modelID, "gpt-image-1.5"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1-mini"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1"):
		return closestGPTImage1Size(config.Width, config.Height)
	default:
		return ""
	}
}

func isOpenAIModelOrSnapshot(modelID, base string) bool {
	if modelID == base {
		return true
	}
	snapshot, ok := strings.CutPrefix(modelID, base+"-")
	if !ok {
		return false
	}
	_, err := time.Parse("2006-01-02", snapshot)
	return err == nil
}

// closestGPTImage1Size maps arbitrary input canvases to the three sizes that
// GPT Image models before gpt-image-2 accept.
func closestGPTImage1Size(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	return closestGPTImage1SizeFromRatio(float64(width) / float64(height))
}

func closestGPTImage1SizeFromRatio(targetRatio float64) string {
	if targetRatio <= 0 || math.IsNaN(targetRatio) || math.IsInf(targetRatio, 0) {
		return ""
	}
	candidates := []struct {
		size  string
		ratio float64
	}{
		{size: "1024x1024", ratio: 1},
		{size: "1536x1024", ratio: 1.5},
		{size: "1024x1536", ratio: 2.0 / 3.0},
	}
	best := candidates[0]
	bestError := math.Abs(math.Log(best.ratio / targetRatio))
	for _, candidate := range candidates[1:] {
		err := math.Abs(math.Log(candidate.ratio / targetRatio))
		if err < bestError {
			best = candidate
			bestError = err
		}
	}
	return best.size
}

func closestDALL3Size(targetRatio float64) string {
	if targetRatio <= 0 || math.IsNaN(targetRatio) || math.IsInf(targetRatio, 0) {
		return ""
	}
	candidates := []struct {
		size  string
		ratio float64
	}{
		{size: "1024x1024", ratio: 1},
		{size: "1792x1024", ratio: 1.75},
		{size: "1024x1792", ratio: 1.0 / 1.75},
	}
	best := candidates[0]
	bestError := math.Abs(math.Log(best.ratio / targetRatio))
	for _, candidate := range candidates[1:] {
		err := math.Abs(math.Log(candidate.ratio / targetRatio))
		if err < bestError {
			best = candidate
			bestError = err
		}
	}
	return best.size
}

// closestGPTImage2Size preserves the source ratio at the default 2K long edge.
func closestGPTImage2Size(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	return closestGPTImage2SizeForTarget(float64(width)/float64(height), gptImage2DefaultLongEdge)
}

// closestGPTImage2SizeForTarget chooses a legal OpenAI size closest to the
// requested ratio and long-edge resolution. Ratio accuracy is weighted more
// heavily than an exact edge match, while the official pixel, edge, alignment,
// and 3:1 constraints remain hard boundaries.
func closestGPTImage2SizeForTarget(targetRatio float64, targetLongEdge int) string {
	if targetRatio <= 0 || math.IsNaN(targetRatio) || math.IsInf(targetRatio, 0) || targetLongEdge <= 0 {
		return ""
	}
	targetRatio = math.Max(1/gptImage2MaxAspect, math.Min(gptImage2MaxAspect, targetRatio))
	targetLongEdge = max(16, min(gptImage2MaxEdge, targetLongEdge))
	minimumLongEdge := int(math.Ceil(math.Sqrt(float64(gptImage2MinPixels) * math.Max(targetRatio, 1/targetRatio))))
	minimumLongEdge = ((minimumLongEdge + 15) / 16) * 16
	searchMaxEdge := min(gptImage2MaxEdge, max(targetLongEdge, minimumLongEdge))

	bestWidth, bestHeight := 0, 0
	bestScore, bestRatioError, bestEdgeError := math.MaxFloat64, math.MaxFloat64, math.MaxFloat64
	for candidateWidth := 16; candidateWidth <= searchMaxEdge; candidateWidth += 16 {
		for candidateHeight := 16; candidateHeight <= searchMaxEdge; candidateHeight += 16 {
			pixels := candidateWidth * candidateHeight
			if pixels < gptImage2MinPixels || pixels > gptImage2MaxPixels {
				continue
			}
			candidateRatio := float64(candidateWidth) / float64(candidateHeight)
			if candidateRatio > gptImage2MaxAspect || candidateRatio < 1/gptImage2MaxAspect {
				continue
			}
			ratioError := math.Abs(math.Log(candidateRatio / targetRatio))
			candidateLongEdge := max(candidateWidth, candidateHeight)
			edgeError := math.Abs(math.Log(float64(candidateLongEdge) / float64(targetLongEdge)))
			score := 4*ratioError + edgeError
			if score < bestScore-1e-12 ||
				(math.Abs(score-bestScore) <= 1e-12 && ratioError < bestRatioError-1e-12) ||
				(math.Abs(score-bestScore) <= 1e-12 && math.Abs(ratioError-bestRatioError) <= 1e-12 && edgeError < bestEdgeError) {
				bestWidth, bestHeight = candidateWidth, candidateHeight
				bestScore, bestRatioError, bestEdgeError = score, ratioError, edgeError
			}
		}
	}
	if bestWidth == 0 || bestHeight == 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", bestWidth, bestHeight)
}

func closestGPTImage2SizeForDimensions(targetWidth, targetHeight int) string {
	if targetWidth <= 0 || targetHeight <= 0 {
		return ""
	}
	targetWidth = max(16, min(gptImage2MaxEdge, targetWidth))
	targetHeight = max(16, min(gptImage2MaxEdge, targetHeight))
	targetRatio := float64(targetWidth) / float64(targetHeight)
	targetRatio = math.Max(1/gptImage2MaxAspect, math.Min(gptImage2MaxAspect, targetRatio))

	bestWidth, bestHeight := 0, 0
	bestScore, bestRatioError := math.MaxFloat64, math.MaxFloat64
	for candidateWidth := 16; candidateWidth <= gptImage2MaxEdge; candidateWidth += 16 {
		for candidateHeight := 16; candidateHeight <= gptImage2MaxEdge; candidateHeight += 16 {
			pixels := candidateWidth * candidateHeight
			if pixels < gptImage2MinPixels || pixels > gptImage2MaxPixels {
				continue
			}
			candidateRatio := float64(candidateWidth) / float64(candidateHeight)
			if candidateRatio > gptImage2MaxAspect || candidateRatio < 1/gptImage2MaxAspect {
				continue
			}
			widthError := math.Abs(math.Log(float64(candidateWidth) / float64(targetWidth)))
			heightError := math.Abs(math.Log(float64(candidateHeight) / float64(targetHeight)))
			ratioError := math.Abs(math.Log(candidateRatio / targetRatio))
			score := widthError + heightError
			if score < bestScore-1e-12 ||
				(math.Abs(score-bestScore) <= 1e-12 && ratioError < bestRatioError) {
				bestWidth, bestHeight = candidateWidth, candidateHeight
				bestScore, bestRatioError = score, ratioError
			}
		}
	}
	if bestWidth == 0 || bestHeight == 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", bestWidth, bestHeight)
}

// defaultOpenAIImageQuality only applies to documented OpenAI image model IDs.
// Unknown OpenAI-compatible aliases keep provider defaults so a third-party
// gateway is never sent a field it may not implement. An admin-declared quality
// always wins because callers invoke this helper only when the key is absent.
func defaultOpenAIImageQuality(requestID string) string {
	modelID := strings.ToLower(strings.TrimSpace(requestID))
	switch {
	case isOpenAIModelOrSnapshot(modelID, "gpt-image-2"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1.5"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1-mini"),
		isOpenAIModelOrSnapshot(modelID, "gpt-image-1"):
		return "high"
	case modelID == "dall-e-3":
		return "hd"
	default:
		return ""
	}
}

// applyDefaultGeminiImageSize raises known high-resolution Gemini image models
// from their 1K provider default to 2K. The nested lookup deliberately preserves
// an explicit admin imageSize (including 1K or 4K), and unknown aliases are left
// untouched for OpenAI-compatible/proxy safety.
func applyDefaultGeminiImageSize(params map[string]any, requestID string) {
	modelID := strings.ToLower(strings.TrimSpace(requestID))
	supports2K := strings.HasPrefix(modelID, "gemini-3-pro-image") ||
		strings.HasPrefix(modelID, "gemini-3.1-flash-image") ||
		strings.HasPrefix(modelID, "nano-banana-pro")
	if !supports2K {
		return
	}
	generationConfig, ok := params["generationConfig"].(map[string]any)
	if !ok {
		generationConfig = map[string]any{}
		params["generationConfig"] = generationConfig
	}
	imageConfig, ok := generationConfig["imageConfig"].(map[string]any)
	if !ok {
		imageConfig = map[string]any{}
		generationConfig["imageConfig"] = imageConfig
	}
	if _, configured := imageConfig["imageSize"]; !configured {
		imageConfig["imageSize"] = "2K"
	}
}

// sanitizeImageRequestParams clones the admin-declared param-control fragment
// and removes fields that belong to server routing, identity, authentication,
// or user content. Provider-specific generation options remain available, but
// they can never redirect the request or replace the prompt/reference images.
func sanitizeImageRequestParams(params map[string]any) map[string]any {
	clean := store.DeepMergeJSONObjects(nil, params)
	for key := range clean {
		normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
		switch normalized {
		case "model", "prompt", "n", "inputimages", "contents", "responsemodalities", "image", "images", "mask",
			"apikey", "baseurl", "url", "endpoint", "headers", "authorization":
			delete(clean, key)
		}
	}
	return clean
}

func imageMultipartScalar(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case json.Number:
		return v.String(), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32), true
	case int:
		return strconv.Itoa(v), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	default:
		return "", false
	}
}

// fetchRemoteImage downloads an image URL returned by an image API, returning
// its bytes + MIME (defaulting to image/png). Best-effort: returns nil on error.
//
// The URL comes from the upstream RESPONSE body (a gateway/provider we don't
// fully control), not admin config — so it is NOT trusted. We use the
// SSRF-safe client (validates the resolved IP at every redirect hop, restricts
// ports) instead of toolHTTPClient, and require an http(s) scheme, so a
// malicious/compromised gateway can't point us at 169.254.169.254 / localhost /
// internal services.
func fetchRemoteImage(ctx context.Context, rawURL string) ([]byte, string) {
	u, perr := url.Parse(rawURL)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, ""
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, ""
	}
	resp, err := ssrfSafeClient().Do(req)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchRemoteImageDownloadCap+1))
	if err != nil || len(data) == 0 || int64(len(data)) > fetchRemoteImageDownloadCap {
		return nil, ""
	}
	mime := verifiedImageMIMEFromBytes(data)
	if mime == "" {
		return nil, ""
	}
	return data, mime
}

// truncateOutput clips s to max bytes with an explicit marker (pitfall A5).
func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…[output truncated at 32KB]"
}

// isSandboxSessionGone is true for the upstream "session not found" responses
// the sandbox-service returns after the reaper recycled a container (§4.5).
func isSandboxSessionGone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sandbox.ErrSessionGone) {
		return true
	}
	var httpErr *sandbox.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		// FastAPI also returns a generic {"detail":"Not Found"} when an old
		// sidecar image does not implement /files/reset-inputs at all. Treating
		// that as a reaped session would create and persist a replacement, call
		// the same missing route, and leak another container on every attempt.
		msg := strings.ToLower(httpErr.Body)
		return strings.Contains(msg, "session not found") ||
			strings.Contains(msg, "no such session") ||
			strings.Contains(msg, "session_gone") ||
			(strings.Contains(msg, "session") && strings.Contains(msg, "not running"))
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "session not found") || strings.Contains(msg, "no such session") || strings.Contains(msg, "session_gone") {
		return true
	}
	return false
}

func isSandboxSessionBusy(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *sandbox.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests && strings.Contains(strings.ToLower(httpErr.Body), "session is busy")
	}
	// Keep compatibility with alternate Service implementations and older
	// sidecars that only expose a formatted error string.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sandbox 429") && strings.Contains(msg, "session is busy")
}

func isSandboxSessionRecoverable(err error) bool {
	return isSandboxSessionGone(err) || isSandboxSessionBusy(err)
}

func extForMime(mime string) string {
	switch {
	case strings.Contains(mime, "png"):
		return ".png"
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		return ".jpg"
	case strings.Contains(mime, "webp"):
		return ".webp"
	default:
		return ".png"
	}
}

func orDefaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// useSkillTool — design.md §4.17.
type useSkillTool struct {
	db *sql.DB
}

func (t *useSkillTool) Name() string { return "use_skill" }
func (t *useSkillTool) Description() string {
	return "Load the full instructions for one of the skills the user/admin has registered (returned text contains the skill's complete how-to)."
}
func (t *useSkillTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
}

type skillInput struct {
	Name string `json:"name"`
}

func (t *useSkillTool) Execute(ctx context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	var in skillInput
	_ = json.Unmarshal(input, &in)
	if in.Name == "" {
		return "", nil, &llm.ToolUserError{Message: "name required"}
	}
	var currentSkillPolicy *store.ResourceAccessPolicy
	if tc != nil && strings.TrimSpace(tc.UserID) != "" {
		permissions, err := store.UserGroupPermissionsForUser(ctx, t.db, tc.UserID)
		if err != nil {
			return "", nil, err
		}
		currentSkillPolicy = &permissions.Skills
	}
	// Only load a skill bound to the current model (model_skills, §4.17) — the same
	// set advertised in the system-prompt index. Without a model in context, fall
	// back to all enabled skills so non-orchestrated callers still work.
	var skills []store.Skill
	if tc != nil && tc.ModelID != "" {
		ids, err := store.SkillsForModel(ctx, t.db, tc.ModelID)
		if err != nil {
			return "", nil, err
		}
		for _, id := range ids {
			if !tc.AllowsAdminSkill(id) {
				continue
			}
			if currentSkillPolicy != nil && !store.ResourcePolicyAllows(*currentSkillPolicy, id) {
				continue
			}
			if sk, err := store.GetSkill(ctx, t.db, id); err == nil && sk != nil && sk.Enabled {
				skills = append(skills, *sk)
			}
		}
	} else {
		all, err := store.ListSkills(ctx, t.db, true)
		if err != nil {
			return "", nil, err
		}
		skills = all
	}
	for _, s := range skills {
		if strings.EqualFold(s.Name, in.Name) {
			return "Skill: " + s.Name + "\n\n" + s.Instructions, nil, nil
		}
	}
	// Built-in document-generation skill (§4.5.1): served from code, not the
	// skills table, so it can't be deleted in the admin panel. It has no catalog
	// ID; therefore selected/none group policies must fail closed instead of
	// allowing a direct name lookup to bypass the administrator's skill list.
	currentPolicyAllowsBuiltin := currentSkillPolicy == nil || currentSkillPolicy.Mode == store.ResourceAccessAll
	snapshotAllowsBuiltin := tc == nil || tc.AdminSkillIDs == nil
	if strings.EqualFold(in.Name, llm.DocGenSkillName) && currentPolicyAllowsBuiltin && snapshotAllowsBuiltin {
		return "Skill: " + llm.DocGenSkillName + "\n\n" + llm.DocGenRecipes, nil, nil
	}
	return "Skill not found: " + in.Name, nil, nil
}

// saveMemoryTool — design.md §4.16 (synchronous explicit-write path).
type saveMemoryTool struct {
	db *sql.DB
}

func (t *saveMemoryTool) Name() string { return "save_memory" }
func (t *saveMemoryTool) Description() string {
	return "Save a durable fact about the user into long-term memory. Use ONLY when the user explicitly says \"remember…\" or asks you to. Status defaults to ACTIVE."
}
func (t *saveMemoryTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"memory_text":{"type":"string"},"slot":{"type":"string"},"value":{"type":"string"}},"required":["memory_text"]}`)
}

type memInput struct {
	MemoryText string `json:"memory_text"`
	Slot       string `json:"slot"`
	Value      string `json:"value"`
}

func (t *saveMemoryTool) Execute(ctx context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	if t.db == nil || tc == nil || !store.MemoryEnabledForUser(ctx, t.db, tc.UserID) {
		return "", nil, &llm.ToolUserError{Message: "memory is disabled"}
	}
	var in memInput
	_ = json.Unmarshal(input, &in)
	if in.MemoryText == "" {
		return "", nil, &llm.ToolUserError{Message: "memory_text required"}
	}
	_, err := store.CreateMemory(ctx, t.db, store.Memory{
		UserID:     tc.UserID,
		MemoryText: in.MemoryText,
		Slot:       in.Slot,
		Value:      in.Value,
		Status:     "ACTIVE",
		Confidence: saveMemoryConfidence,
	})
	if err != nil {
		return "", nil, err
	}
	return "Memory saved.", nil, nil
}
