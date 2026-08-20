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
	inTopK                                 = envcfg.Int("AIVORY_TOOLS_IN_TOP_K", 5)
	webFetchResponseBodyReadCap            = envcfg.Int64("AIVORY_TOOLS_WEB_FETCH_RESPONSE_BODY_READ_CAP", 256*1024)
	webFetchExtractedTextCharCap           = envcfg.Int("AIVORY_TOOLS_WEB_FETCH_EXTRACTED_TEXT_CHAR_CAP", 32000)
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

// webSearchTool implements §4.4 via a pluggable Searcher. When no backend is
// configured it returns a polite placeholder so callers never crash.
type webSearchTool struct {
	cfg      config.Config
	searcher Searcher
}

func (t *webSearchTool) Name() string { return toolnames.AivoryWebSearch }
func (t *webSearchTool) Description() string {
	return "Search the public web for current information. Use when the answer depends on news, prices, recent events, or anything time-sensitive. Returns a list of titled snippets with URLs."
}
func (t *webSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query"}},"required":["query"]}`)
}

type webSearchInput struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

func (t *webSearchTool) Execute(ctx context.Context, input []byte, _ *llm.ToolContext) (string, []llm.Citation, error) {
	var in webSearchInput
	_ = json.Unmarshal(input, &in)
	if in.Query == "" {
		return "", nil, &llm.ToolUserError{Message: "query required"}
	}
	if in.TopK <= 0 {
		in.TopK = inTopK
	}
	if t.searcher == nil {
		// Fallback "result" so the model can still respond gracefully.
		fake := []llm.Citation{
			{ID: "w1", Index: 1, Title: "Aivory local-only mode", URL: "https://example.com/aivory-local-mode", Snippet: "No SEARCH_API_KEY configured. Configure one to enable real aivory_web_search results.", Source: "web"},
		}
		return "Search not yet configured. Reply based on training knowledge or ask the user to configure SEARCH_API_KEY.", fake, nil
	}
	return t.searcher.Search(ctx, in.Query, in.TopK)
}

// webFetchTool implements §4.4 with the SSRF guards.
type webFetchTool struct{}

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch the main text content of a URL. Use after aivory_web_search to read a specific page. SSRF-guarded: internal IPs blocked."
}
func (t *webFetchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
}

type webFetchInput struct {
	URL string `json:"url"`
}

func (t *webFetchTool) Execute(ctx context.Context, input []byte, _ *llm.ToolContext) (string, []llm.Citation, error) {
	var in webFetchInput
	_ = json.Unmarshal(input, &in)
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", nil, &llm.ToolUserError{Message: "invalid URL"}
	}
	// Reject non-web ports up-front (defence in depth — the dialer re-checks
	// the resolved IP + port on every hop, defeating redirects/rebinding).
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return "", nil, &llm.ToolUserError{Message: "blocked non-web port"}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", in.URL, nil)
	req.Header.Set("user-agent", "AivoryBot/1.0")
	resp, err := ssrfSafeClient().Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	// Truncate after 256 KB — keeps tokens bounded.
	limited := io.LimitReader(resp.Body, webFetchResponseBodyReadCap)
	body, _ := io.ReadAll(limited)
	text := stripHTML(string(body))
	// Roughly cap at ~8K tokens (≈32K chars) per §4.4.
	if len(text) > webFetchExtractedTextCharCap {
		text = text[:webFetchExtractedTextCharCap] + "\n…[truncated]"
	}
	return text, nil, nil
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
	return "Run Python in a persistent sandbox for math, data analysis, image editing, plotting, spreadsheet/CSV processing, and generating downloadable files (PDF/PPTX/DOCX/XLSX/PNG). The session and its /workspace persist across calls AND across turns in this conversation, so call it several times in a row — inspect the inputs first, then edit or compute, and read again differently if the first attempt doesn't fit. Supported data uploads, verified user-uploaded images, and prior image-generation outputs from this conversation are staged in /workspace/uploads/; public images fetched with fetch_image are stored in /workspace/downloads/. Run `import os; os.listdir('/workspace/uploads')` and inspect /workspace/downloads when needed, then use the real paths (for example Pillow for images, pandas.read_csv / pandas.read_excel for tables). Write outputs, including edited images, plots, and documents, to /workspace/outputs to return them as downloadable artifacts. Stdout/stderr is returned."
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
		sid, err := t.sandbox.NewSession(ctx, tc.ConvID)
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
			return "", nil, fmt.Errorf("sandbox session: %w", err)
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

	// Reset the persistent input namespaces, then stage the conversation's
	// current data files and verified user-uploaded images into
	// /workspace/uploads. Reset prevents stale inputs from surviving deletion or
	// permission changes between calls.
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
				// Only data-oriented inputs and explicit image rows are eligible.
				// Image rows must also pass byte-signature verification below; legacy
				// text/data rows containing disguised image bytes remain excluded.
				spreadsheetName := false
				switch strings.ToLower(filepath.Ext(strings.TrimSpace(f.Filename))) {
				case ".csv", ".tsv", ".xlsx", ".xls", ".xlsm":
					spreadsheetName = true
				}
				imageMetadata := conversationImageMetadata(f.Kind, f.MimeType)
				dataInput := f.Kind == "sheet" || f.Kind == "text" || f.Kind == "code" || spreadsheetName
				if !dataInput && !imageMetadata {
					continue
				}
				data, err := readSandboxUpload(f.StoragePath, f.SizeBytes, pythonExecuteUploadStagingFileSize, t.uploadDir)
				if err != nil {
					continue
				}
				verifiedImage := verifiedImageMIMEFromBytes(data) != ""
				if imageMetadata && !verifiedImage {
					continue
				}
				if !imageMetadata && verifiedImage {
					continue
				}
				_ = t.sandbox.PutFile(ctx, sid, "/workspace/uploads/"+uniqueName(f.Filename), data)
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
		// still stored on the conversation. ResetInputs is the first sidecar call,
		// so recover here as well as in the Exec path below.
		if !isSandboxSessionGone(err) {
			return "", nil, err
		}
		rebuilt := ""
		if hasConv {
			relock := lockConvSandbox(tc.ConvID)
			cur, _ := store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id")
			if cur != "" && cur != sessionID {
				rebuilt = cur
			} else {
				sid2, sErr := t.sandbox.NewSession(ctx, tc.ConvID)
				if sErr != nil {
					relock()
					return "", nil, fmt.Errorf("sandbox session (rebuild): %w", sErr)
				}
				if perr := store.SetConvProviderStateKeyForUser(ctx, tc.DB, tc.ConvID, tc.MessageID, tc.UserID, "sandbox_id", sid2); perr != nil {
					_ = t.sandbox.Release(ctx, sid2)
					relock()
					return "", nil, fmt.Errorf("persist sandbox session (rebuild): %w", perr)
				}
				rebuilt = sid2
			}
			relock()
		} else {
			sid2, sErr := t.sandbox.NewSession(ctx, "")
			if sErr != nil {
				return "", nil, fmt.Errorf("sandbox session (rebuild): %w", sErr)
			}
			rebuilt = sid2
		}
		sessionID = rebuilt
		if err := stageFiles(sessionID); err != nil {
			return "", nil, err
		}
	}

	res, err := t.sandbox.Exec(ctx, sessionID, in.Code)
	if err != nil {
		// §4.5 reaper recovery: if the upstream reaped the session container
		// while we were idle, Exec returns 404. Provision a fresh session,
		// re-stage uploads + skills, and retry once before bubbling the error.
		if isSandboxSessionGone(err) {
			rebuilt := ""
			if hasConv {
				// Re-provision under the per-conversation lock so two python_execute
				// calls that both hit a reaped session don't each NewSession() and
				// leak one container. Re-read sandbox_id under the lock first: a peer
				// may have already rebuilt it — adopt that id instead of creating a
				// second one.
				relock := lockConvSandbox(tc.ConvID)
				cur, _ := store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id")
				if cur != "" && cur != sessionID {
					rebuilt = cur
				} else {
					sid2, sErr := t.sandbox.NewSession(ctx, tc.ConvID)
					if sErr != nil {
						relock()
						return "", nil, fmt.Errorf("sandbox session (rebuild): %w", sErr)
					}
					if perr := store.SetConvProviderStateKeyForUser(ctx, tc.DB, tc.ConvID, tc.MessageID, tc.UserID, "sandbox_id", sid2); perr != nil {
						_ = t.sandbox.Release(ctx, sid2)
						relock()
						return "", nil, fmt.Errorf("persist sandbox session (rebuild): %w", perr)
					}
					rebuilt = sid2
				}
				relock()
			} else {
				sid2, sErr := t.sandbox.NewSession(ctx, tc.ConvID)
				if sErr != nil {
					return "", nil, fmt.Errorf("sandbox session (rebuild): %w", sErr)
				}
				rebuilt = sid2
			}
			sessionID = rebuilt
			// §4.5 workspace restore: if a prior run archived /workspace, the
			// sandbox-service auto-restores on session creation. We re-stage
			// uploads (always cheap) so the new container has user data.
			if stageErr := stageFiles(sessionID); stageErr != nil {
				return "", nil, stageErr
			}
			res, err = t.sandbox.Exec(ctx, sessionID, in.Code)
		}
	}
	if err != nil {
		return "", nil, err
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
	}
	if out.Len() == 0 {
		out.WriteString("(no output)")
	}
	return out.String(), nil, nil
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

// conversationImageMetadata deliberately ignores the filename extension. A
// legacy text/data row named "photo.png" is not enough authority to cross the
// image boundary; uploads are classified server-side and carry kind/MIME.
func conversationImageMetadata(kind, mimeType string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "image") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])), "image/")
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
	return "Generate a new image or faithfully edit a user-provided image. Current-turn attachments and the nearest generated image on this conversation branch are supplied automatically; do not invent file ids. For edits, describe only the requested change and preserve all other source details. Returns the image as a downloadable artifact."
}
func (t *imageGenerateTool) InputSchema() json.RawMessage {
	// Reference images are resolved server-side from current-turn attachments and
	// the active conversation branch. Keep the legacy input_images decoder in
	// Execute for trusted callers, but do not expose internal artifact ids to chat
	// models: a model cannot reliably know which ids are valid and an invalid id
	// must never turn an edit into an unrelated fresh generation.
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"The requested image or exact edit instruction. Current-turn attachments and the nearest generated image on this conversation branch are supplied automatically. For edits, do not translate, paraphrase, or restyle text that should remain unchanged."},"n":{"type":"integer","default":1},"size":{"type":"string","description":"Optional output size. Omit for edits to preserve the source aspect ratio automatically. GPT Image 1.x supports 1024x1024, 1536x1024, and 1024x1536; GPT Image 2 also supports valid WIDTHxHEIGHT values."}},"required":["prompt"]}`)
}

type imgInput struct {
	Prompt      string   `json:"prompt"`
	N           int      `json:"n"`
	Size        string   `json:"size"`
	InputImages []string `json:"input_images"`
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

	// §4.12-C/D 图生图: load explicit input images first. With no explicit
	// reference, walk the current message's parent chain and reuse the closest
	// generated image. This is provider-neutral and branch-aware: OpenAI and Gemini
	// both continue the active branch, while regenerate (an assistant sibling under
	// the original user message) starts from that user's original inputs.
	inputImageIDs := mergeImageInputIDs(nil, in.InputImages)
	if tc != nil {
		// Current-turn uploads are the primary source image and must stay first. For
		// GPT Image 1.x the first image receives the richest texture preservation.
		inputImageIDs = mergeImageInputIDs(tc.ImageInputIDs, inputImageIDs)
	}
	inputLimit := imageInputImageLimit(channel.Type, model.RequestID)
	inputImgs, tooManyInputs := t.loadInputImages(ctx, tc, inputImageIDs, inputLimit)
	if tooManyInputs {
		return "", nil, &llm.ToolUserError{Message: fmt.Sprintf("the selected image model accepts at most %d reference image(s)", inputLimit)}
	}
	// Explicit ids are a trusted compatibility path, but they can be stale (for
	// example when a model copied an old artifact URL). If none resolved to a
	// verified image, continue with the active branch's nearest generated image
	// instead of silently switching an edit into a fresh generation.
	if len(inputImgs) == 0 {
		if previous := t.loadNearestBranchImage(ctx, tc); previous != nil {
			inputImgs = []imageBytes{*previous}
		}
	}
	// The exact user instruction is authoritative whenever this request becomes an
	// edit, including automatic branch continuation with no new attachment. A chat
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

func faithfulImageEditPrompt(instruction string) string {
	return `Faithfully edit the supplied source image according to the instruction below.
Treat the source image as authoritative. Change only what the instruction explicitly requests. Preserve every other detail as closely as possible, especially the canvas and crop, composition, layout, colors, background, lighting, texture, text content, language, typography, spacing, and alignment. Do not translate, paraphrase, retype, add, remove, or restyle anything unless the instruction explicitly requires it.

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
		// A malformed or empty admin fragment must not suppress provider auto-sizing.
		delete(cleanParams, "size")
	}
	if requestedSize == "" && len(inputImgs) > 0 {
		requestedSize = inferredOpenAIEditSize(requestID, inputImgs[0])
	}

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
	gptImage2MinPixels        = 655360
	gptImage2MaxPixels        = 8294400
	gptImage2MaxEdge          = 3840
	gptImage2MaxAspect        = 3.0
	gptImage2DefaultMaxPixels = 2048 * 2048
)

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
	targetRatio := float64(width) / float64(height)
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

// closestGPTImage2Size preserves the source canvas up to a conservative 2K
// (~4 MP) default. Explicit admin/user sizes can still request the provider's
// full legal range, but automatic edits must not shrink every large reference
// to the old ~1 MP budget before the original bytes are saved unchanged.
func closestGPTImage2Size(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	targetRatio := float64(width) / float64(height)
	targetRatio = math.Max(1/gptImage2MaxAspect, math.Min(gptImage2MaxAspect, targetRatio))
	// Start at the automatic cap and only multiply when the product is known to
	// fit below it. Malformed image headers can carry enormous dimensions; this
	// avoids an int overflow before the value is clamped.
	targetPixels := gptImage2DefaultMaxPixels
	if height > 0 && width <= gptImage2DefaultMaxPixels/height {
		targetPixels = width * height
	}
	if targetPixels < gptImage2MinPixels {
		targetPixels = gptImage2MinPixels
	}
	if targetPixels > gptImage2DefaultMaxPixels {
		targetPixels = gptImage2DefaultMaxPixels
	}
	searchMinPixels := max(gptImage2MinPixels, 3*targetPixels/4)
	searchMaxPixels := min(gptImage2DefaultMaxPixels, 5*targetPixels/4)

	bestWidth, bestHeight := 0, 0
	bestRatioError, bestAreaError := math.MaxFloat64, math.MaxFloat64
	for candidateWidth := 16; candidateWidth <= gptImage2MaxEdge; candidateWidth += 16 {
		for candidateHeight := 16; candidateHeight <= gptImage2MaxEdge; candidateHeight += 16 {
			pixels := candidateWidth * candidateHeight
			if pixels < searchMinPixels || pixels > searchMaxPixels || pixels < gptImage2MinPixels || pixels > gptImage2MaxPixels {
				continue
			}
			candidateRatio := float64(candidateWidth) / float64(candidateHeight)
			if candidateRatio > gptImage2MaxAspect || candidateRatio < 1/gptImage2MaxAspect {
				continue
			}
			ratioError := math.Abs(math.Log(candidateRatio / targetRatio))
			areaError := math.Abs(math.Log(float64(pixels) / float64(targetPixels)))
			if ratioError < bestRatioError-1e-12 || (math.Abs(ratioError-bestRatioError) <= 1e-12 && areaError < bestAreaError) {
				bestWidth, bestHeight = candidateWidth, candidateHeight
				bestRatioError, bestAreaError = ratioError, areaError
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
// We detect by string match because the HTTPSandbox wraps every non-2xx in a
// generic "sandbox <code>: <body>" — fragile but the surface is tiny + ours.
func isSandboxSessionGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "sandbox 404") {
		return true
	}
	if strings.Contains(msg, "session not found") || strings.Contains(msg, "no such session") || strings.Contains(msg, "session_gone") {
		return true
	}
	return false
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
