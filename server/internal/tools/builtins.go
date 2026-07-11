package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"aivory/server/internal/config"
	"aivory/server/internal/envcfg"
	"aivory/server/internal/llm"
	"aivory/server/internal/rag"
	"aivory/server/internal/sandbox"
	"aivory/server/internal/store"
)

// Env-overridable defaults (see docs/config-reference.md). Each falls back to
// the original hardcoded value when its AIVORY_* variable is unset.
var (
	inTopK                                 = envcfg.Int("AIVORY_TOOLS_IN_TOP_K", 5)
	webFetchResponseBodyReadCap            = envcfg.Int64("AIVORY_TOOLS_WEB_FETCH_RESPONSE_BODY_READ_CAP", 256*1024)
	webFetchExtractedTextCharCap           = envcfg.Int("AIVORY_TOOLS_WEB_FETCH_EXTRACTED_TEXT_CHAR_CAP", 32000)
	pythonExecuteUploadStagingFileSize     = envcfg.Int64("AIVORY_TOOLS_PYTHON_EXECUTE_UPLOAD_STAGING_FILE_SIZE", 20*1024*1024)
	pythonExecuteImageArtifactStagingSize  = envcfg.Int64("AIVORY_TOOLS_PYTHON_EXECUTE_IMAGE_ARTIFACT_STAGING_SIZE", 20*1024*1024)
	pythonExecuteStdoutStderrTruncationCap = envcfg.Int("AIVORY_TOOLS_PYTHON_EXECUTE_STDOUT_STDERR_TRUNCATION_CAP", 32*1024)
	inN                                    = envcfg.Int("AIVORY_TOOLS_IN_N", 4)
	inSize                                 = envcfg.Str("AIVORY_TOOLS_IN_SIZE", "1024x1024")
	dailyImageLimitResetWindow             = envcfg.Dur("AIVORY_TOOLS_DAILY_IMAGE_LIMIT_RESET_WINDOW", 24*time.Hour)
	imageQuotaDefaultPeriod                = int64(604800)
	imageImageInputImageCap                = envcfg.Int("AIVORY_TOOLS_IMAGE_IMAGE_INPUT_IMAGE_CAP", 3)
	fetchRemoteImageDownloadCap            = envcfg.Int64("AIVORY_TOOLS_FETCHREMOTEIMAGE_DOWNLOAD_CAP", 32<<20)
	inTopK2                                = envcfg.Int("AIVORY_TOOLS_IN_TOP_K_2", 5)
	saveMemoryConfidence                   = envcfg.F64("AIVORY_TOOLS_CONFIDENCE", 0.95)
)

// webSearchTool implements §4.4 via a pluggable Searcher. When no backend is
// configured it returns a polite placeholder so callers never crash.
type webSearchTool struct {
	cfg      config.Config
	searcher Searcher
}

func (t *webSearchTool) Name() string { return "web_search" }
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
		return "", nil, errors.New("query required")
	}
	if in.TopK <= 0 {
		in.TopK = inTopK
	}
	if t.searcher == nil {
		// Fallback "result" so the model can still respond gracefully.
		fake := []llm.Citation{
			{ID: "w1", Index: 1, Title: "Aivory local-only mode", URL: "https://example.com/aivory-local-mode", Snippet: "No SEARCH_API_KEY configured. Configure one to enable real web_search results.", Source: "web"},
		}
		return "Search not yet configured. Reply based on training knowledge or ask the user to configure SEARCH_API_KEY.", fake, nil
	}
	return t.searcher.Search(ctx, in.Query, in.TopK)
}

// webFetchTool implements §4.4 with the SSRF guards.
type webFetchTool struct{}

func (t *webFetchTool) Name() string { return "web_fetch" }
func (t *webFetchTool) Description() string {
	return "Fetch the main text content of a URL. Use after web_search to read a specific page. SSRF-guarded: internal IPs blocked."
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
		return "", nil, errors.New("invalid URL")
	}
	// Reject non-web ports up-front (defence in depth — the dialer re-checks
	// the resolved IP + port on every hop, defeating redirects/rebinding).
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return "", nil, errors.New("blocked non-web port")
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

// fetchImageTool downloads a public image into the conversation's sandbox at
// /workspace/uploads/ so it can be embedded into a generated deck/doc. The
// sandbox itself has no outbound network (§ security), so this Go-side fetch is
// the bridge for "find images on the web → build a PPT". SSRF-guarded + size
// capped, same as web_fetch.
type fetchImageTool struct {
	sandbox sandbox.Service
	logger  *log.Logger
}

func (t *fetchImageTool) Name() string { return "fetch_image" }
func (t *fetchImageTool) Description() string {
	return "Download an image from a public http(s) URL into /workspace/uploads/ so python_execute can embed it (e.g. python-pptx add_picture, python-docx, reportlab). Use this for web images — the sandbox has no internet of its own. Returns the local sandbox path to reference."
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
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", nil, errors.New("invalid URL")
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return "", nil, errors.New("blocked non-web port")
	}
	if t.sandbox == nil || !t.sandbox.Enabled() {
		return "", nil, errors.New("fetch_image needs the sandbox configured (Admin → settings)")
	}
	if tc == nil || tc.DB == nil || tc.ConvID == "" {
		return "", nil, errors.New("fetch_image requires a conversation context")
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("user-agent", "AivoryBot/1.0")
	resp, err := ssrfSafeClient().Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("image fetch failed: HTTP %d", resp.StatusCode)
	}
	maxImg := envcfg.Int64("AIVORY_TOOLS_MAX_IMG", 15*1024*1024)
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxImg+1))
	if len(data) == 0 {
		return "", nil, errors.New("empty image response")
	}
	if int64(len(data)) > maxImg {
		return "", nil, errors.New("image too large (>15MB)")
	}
	ct := resp.Header.Get("content-type")
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(data)
		if !strings.HasPrefix(ct, "image/") {
			return "", nil, errors.New("URL did not return an image")
		}
	}
	name := sanitizeImageName(in.Filename, u, ct)

	// Reuse (or provision) the conversation's persistent sandbox session so the
	// image lands in the SAME /workspace python_execute will read. Serialise the
	// get→create→persist on the SAME per-conversation lock python_execute uses, so
	// a fetch_image and a python_execute running concurrently in one turn don't
	// each provision a session and clobber sandbox_id (leaking a container).
	unlock := lockConvSandbox(tc.ConvID)
	sessionID, _ := store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id")
	if sessionID == "" {
		sid, err := t.sandbox.NewSession(ctx, tc.ConvID)
		if err != nil {
			unlock()
			if t.logger != nil {
				t.logger.Printf("fetch_image: sandbox NewSession failed: %v", err)
			}
			return "", nil, fmt.Errorf("sandbox session: %w", err)
		}
		sessionID = sid
		if perr := store.SetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id", sessionID); perr != nil {
			_ = t.sandbox.Release(ctx, sessionID)
			unlock()
			return "", nil, fmt.Errorf("persist sandbox session: %w", perr)
		}
	}
	unlock()
	path := "/workspace/uploads/" + name
	if err := t.sandbox.PutFile(ctx, sessionID, path, data); err != nil {
		return "", nil, fmt.Errorf("stage image: %w", err)
	}
	return fmt.Sprintf("Saved image (%d bytes, %s) to %s — embed it via python_execute, e.g. python-pptx add_picture(%q) or <img src='%s'>.", len(data), ct, path, path, path), nil, nil
}

// sanitizeImageName derives a safe /workspace filename from the requested name
// or the URL, guaranteeing a sane image extension matching the content type.
func sanitizeImageName(want string, u *url.URL, contentType string) string {
	base := strings.TrimSpace(want)
	if base == "" {
		base = filepath.Base(u.Path)
	}
	base = filepath.Base(base)
	base = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ' ' || r == '?' || r == '#' || r == '&':
			return '-'
		default:
			return r
		}
	}, base)
	if base == "" || base == "." || base == "-" {
		base = "image"
	}
	lower := strings.ToLower(base)
	known := []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"}
	hasExt := false
	for _, e := range known {
		if strings.HasSuffix(lower, e) {
			hasExt = true
			break
		}
	}
	if !hasExt {
		base += imageExtForType(contentType)
	}
	return base
}

func imageExtForType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	case strings.Contains(ct, "gif"):
		return ".gif"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "svg"):
		return ".svg"
	default:
		return ".png"
	}
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
	artifactDir string
	logger      *log.Logger
}

func (t *pythonExecuteTool) Name() string { return "python_execute" }
func (t *pythonExecuteTool) Description() string {
	return "Run Python in a persistent sandbox for math, data analysis, plotting, spreadsheet/CSV processing, and generating downloadable files (PDF/PPTX/DOCX/XLSX/PNG). The session and its /workspace persist across calls AND across turns in this conversation, so call it several times in a row — inspect the data first (shape/columns/head), then compute, and read again differently if the first attempt doesn't fit. ALL user-uploaded files AND any images you generated earlier are staged in /workspace/uploads/ — ALWAYS run `import os; os.listdir('/workspace/uploads')` first to see the exact filenames (there may be several; names are de-duplicated), then use them by their real paths (read spreadsheets with pandas.read_csv / pandas.read_excel; embed images with python-pptx add_picture / python-docx; pandas, numpy, openpyxl are installed). Write outputs to /workspace/outputs to return them as downloadable artifacts. Stdout/stderr is returned."
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
		return "", nil, errors.New("code required")
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
			if perr := store.SetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id", sessionID); perr != nil {
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

	// Stage the conversation's uploaded data files into /workspace/uploads so
	// the code can read them (§4.5). Best-effort and idempotent — the sandbox
	// overwrites same-path files.
	stageFiles := func(sid string) {
		if tc == nil || tc.DB == nil || tc.ConvID == "" {
			return
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
				// Stage data files AND images — images so they can be embedded into
				// generated decks/docs (python-pptx/-docx read them from /workspace
				// /uploads). Other binary kinds (audio/video/archives) stay out.
				if f.Kind != "sheet" && f.Kind != "text" && f.Kind != "code" && f.Kind != "image" {
					continue
				}
				if f.SizeBytes > pythonExecuteUploadStagingFileSize {
					continue
				}
				data, err := os.ReadFile(f.StoragePath)
				if err != nil {
					continue
				}
				_ = t.sandbox.PutFile(ctx, sid, "/workspace/uploads/"+uniqueName(f.Filename), data)
			}
		}
		// Stage generated images (image_generate artifacts) so a follow-up turn
		// can build a deck/doc with them — they live as artifacts, not uploads.
		if arts, err := store.ListImageArtifactsByConversation(ctx, tc.DB, tc.ConvID, tc.UserID); err == nil {
			for _, a := range arts {
				if a.SizeBytes > pythonExecuteImageArtifactStagingSize {
					continue
				}
				data, err := os.ReadFile(a.StoragePath)
				if err != nil {
					continue
				}
				_ = t.sandbox.PutFile(ctx, sid, "/workspace/uploads/"+uniqueName(a.Filename), data)
			}
		}
		// Stage skill assets too (§4.17) so use_skill can reference scripts/data
		// from /workspace/skills/<name>/. Scope to the skills bound to THIS model
		// (model_skills) — the same set use_skill can load and the index advertises.
		if tc.DB != nil && tc.ModelID != "" {
			if skillIDs, err := store.SkillsForModel(ctx, tc.DB, tc.ModelID); err == nil {
				for _, sid2 := range skillIDs {
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
						data, err := os.ReadFile(a.StoragePath)
						if err != nil {
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
	}
	stageFiles(sessionID)

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
					if perr := store.SetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "sandbox_id", sid2); perr != nil {
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
			stageFiles(sessionID)
			res, err = t.sandbox.Exec(ctx, sessionID, in.Code)
		}
	}
	if err != nil {
		return "", nil, err
	}

	// Persist produced files as artifacts + surface them to the orchestrator.
	for _, f := range res.Files {
		saveArtifact(ctx, tc, t.artifactDir, f.Name, f.MimeType, f.Data)
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

// saveArtifact writes a tool-produced file to ArtifactDir, records it, and
// notifies the orchestrator so it streams an artifact event + persists a block.
// Shared by python_execute (sandbox outputs) and image_generate.
func saveArtifact(ctx context.Context, tc *llm.ToolContext, artifactDir, name, mime string, data []byte) *store.Artifact {
	if tc == nil || tc.DB == nil || tc.MessageID == "" {
		return nil
	}
	dir := artifactDir
	if dir == "" {
		dir = "./data/artifacts"
	}
	_ = os.MkdirAll(dir, 0o755)
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
		return nil
	}
	art, err := store.CreateArtifact(ctx, tc.DB, store.Artifact{
		MessageID:   tc.MessageID,
		Filename:    safe,
		StoragePath: path,
		MimeType:    mime,
		SizeBytes:   int64(len(data)),
	})
	if err != nil || art == nil {
		return nil
	}
	if tc.OnArtifact != nil {
		tc.OnArtifact(llm.ArtifactRef{
			ID: art.ID, Filename: safe, URL: "/api/artifacts/" + art.ID,
			MimeType: mime, Size: int64(len(data)),
		})
	}
	return art
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
	artifactDir string
}

func (t *imageGenerateTool) Name() string { return "image_generate" }
func (t *imageGenerateTool) Description() string {
	return "Generate or edit an image from a textual prompt. Use when the user explicitly asks for an illustration, poster, diagram, or photo. Returns the image as a downloadable artifact."
}
func (t *imageGenerateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"What to draw"},"n":{"type":"integer","default":1},"size":{"type":"string","enum":["256x256","512x512","1024x1024","1792x1024","1024x1792"],"default":"1024x1024"},"input_images":{"type":"array","items":{"type":"string"},"description":"Artifact ids of images to edit (image-to-image)"}},"required":["prompt"]}`)
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
	if strings.TrimSpace(in.Prompt) == "" {
		return "", nil, errors.New("prompt required")
	}
	if in.N <= 0 {
		in.N = 1
	}
	if in.N > inN {
		in.N = inN
	}
	if in.Size == "" {
		in.Size = inSize
	}

	// §4.12-E 内容安全: 生成前 prompt 过审（关键词来自管理员配置，非硬编码）。
	if err := t.moderateImagePrompt(in.Prompt); err != nil {
		return "", nil, &llm.ToolRefusalError{Message: err.Error()}
	}

	// §8.2 每用户每日图像张数限额（按 usage_logs 当日累计）。
	if tc != nil && tc.DB != nil {
		if err := t.checkDailyImageLimit(ctx, tc.UserID, in.N); err != nil {
			return "", nil, &llm.ToolRefusalError{Message: err.Error()}
		}
	}

	// Resolve the image model: the user's pre-selected one (§4.12-B) first,
	// else the first enabled kind=image model.
	model, err := t.resolveImageModel(ctx, tc)
	if err != nil {
		return "", nil, err
	}
	channel, err := store.GetChannel(ctx, t.db, model.ChannelID)
	if err != nil {
		return "", nil, err
	}
	if channel.APIKey == "" {
		return "No API key on the image channel — ask an admin to configure it.", nil, nil
	}

	// §4.20 per-model image quota — shared across drawing mode and chat tool-call
	// (both log purpose='image' against this model id), enforced here so neither
	// path bypasses the other's usage.
	// §4.20 image quota. Drawing mode (tc.SkipImageQuota) already metered + charged
	// upstream → skip here. Otherwise (chat tool-call) run the SAME free→credits→
	// block decision via ImageBilling so it matches drawing mode; payImageCredits
	// is honored after generation. (Falls back to the legacy hard cap only if no
	// biller is wired, e.g. a non-orchestrator caller.)
	payImageCredits := false
	if tc != nil && tc.DB != nil && !tc.SkipImageQuota {
		if tc.ImageBilling != nil {
			allow, pay, refuseMsg := tc.ImageBilling.CheckImageCredits(ctx, tc.UserID, model, in.N)
			if !allow {
				return "", nil, &llm.ToolRefusalError{Message: refuseMsg}
			}
			payImageCredits = pay
		} else if err := t.checkModelImageQuota(ctx, tc.UserID, model, in.N); err != nil {
			return "", nil, &llm.ToolRefusalError{Message: err.Error()}
		}
	}

	// §4.12-C/D 图生图: load explicit input images (artifact ids); for Gemini,
	// fall back to the conversation's image_session so multi-turn edits stay
	// anchored on the previous generation.
	inputImgs := t.loadInputImages(ctx, tc, in.InputImages)
	isGemini := channel.Type == "google" || channel.Type == "gemini"
	if isGemini && len(inputImgs) == 0 && tc != nil && tc.DB != nil && tc.ConvID != "" {
		if sessRef, _ := store.GetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "image_session"); sessRef != "" {
			inputImgs = t.loadInputImages(ctx, tc, []string{sessRef})
		}
	}

	// §4.20 per-model image timeout: cap this single generation/edit request when
	// the admin set one (0 = no per-model cap; bounded only by the turn context).
	genCtx := ctx
	if model.ImageTimeoutSec > 0 {
		var cancel context.CancelFunc
		genCtx, cancel = context.WithTimeout(ctx, time.Duration(model.ImageTimeoutSec)*time.Second)
		defer cancel()
	}

	var images []imageBytes
	switch {
	case isGemini:
		images, err = geminiGenerateImages(genCtx, channel.BaseURL, channel.APIKey, model.RequestID, in, inputImgs)
	case channel.Type == "openai":
		images, err = openaiGenerateImages(genCtx, channel.BaseURL, channel.APIKey, model.RequestID, in, inputImgs)
	default:
		return "", nil, fmt.Errorf("image generation not supported for channel type %q", channel.Type)
	}
	if err != nil {
		return "", nil, err
	}
	if len(images) == 0 {
		// A per-model timeout (genCtx) can fire DURING a url-fetch of the result;
		// fetchRemoteImage swallows that error, so surface the deadline here rather
		// than reporting a misleading "no images" success.
		if genCtx.Err() != nil {
			return "", nil, genCtx.Err()
		}
		return "The image model returned no images.", nil, nil
	}

	// The bytes are in hand → persist the artifacts + meter on a DETACHED context
	// so a stop / timeout landing in this narrow window can't drop a delivered
	// image or skip its usage row (which feeds the daily limit + per-model quota).
	persistCtx := context.WithoutCancel(ctx)
	lastArtifactID := ""
	for i, img := range images {
		ext := extForMime(img.mime)
		name := fmt.Sprintf("image_%d%s", i+1, ext)
		if art := saveArtifact(persistCtx, tc, t.artifactDir, name, img.mime, img.data); art != nil {
			lastArtifactID = art.ID
		}
	}
	// §4.12-D Gemini 多轮编辑: remember the latest generation on the
	// conversation so the next image_generate call edits it by default.
	if isGemini && lastArtifactID != "" && tc != nil && tc.DB != nil && tc.ConvID != "" {
		_ = store.SetConvProviderStateKey(persistCtx, tc.DB, tc.ConvID, "image_session", lastArtifactID)
	}

	// §4.20: if the image model's free allotment is exhausted, charge the image
	// cost in credits (same flow as drawing mode) via ImageBilling, and record the
	// timed portion on the usage row so the credit window survives restarts.
	imageCost := float64(len(images)) * model.PricePerImage
	var imageTimedCredits float64
	if payImageCredits && imageCost > 0 && tc != nil && tc.ImageBilling != nil {
		timed, total := tc.ImageBilling.ChargeImageCredits(persistCtx, tc.UserID, imageCost)
		imageTimedCredits = timed
		tc.AddImageCredits(total)
	}

	// Record cost (§8.3) — one usage row, images_count = N.
	if tc != nil && tc.DB != nil {
		_ = store.LogUsage(persistCtx, t.db, store.UsageLog{
			UserID:         tc.UserID,
			WorkspaceID:    tc.WorkspaceID,
			ConversationID: tc.ConvID,
			MessageID:      tc.MessageID,
			ModelID:        model.ID,
			Purpose:        "image",
			ImagesCount:    len(images),
			Cost:           imageCost,
			Credits:        imageTimedCredits,
			Currency:       model.Currency,
		})
	}
	return fmt.Sprintf("Generated %d image(s) for: %s. They are attached as downloadable artifacts.", len(images), in.Prompt), nil, nil
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
		return nil, errors.New("no image model configured — an admin must add one (kind=image)")
	}
	m := models[0]
	return &m, nil
}

type imageBytes struct {
	data []byte
	mime string
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

// checkDailyImageLimit enforces the per-user daily image quota (§8.2) from the
// usage_logs ledger — robust across restarts and multi-process deployments.
func (t *imageGenerateTool) checkDailyImageLimit(ctx context.Context, userID string, n int) error {
	limit := 30
	if raw, err := store.GetSetting(t.db, "daily_image_limit"); err == nil {
		_ = json.Unmarshal(raw, &limit)
	}
	if limit <= 0 {
		return nil
	}
	dayStart := time.Now().Truncate(dailyImageLimitResetWindow).Unix()
	var used int
	_ = t.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(images_count),0) FROM usage_logs WHERE user_id=? AND purpose='image' AND created_at>=?`,
		userID, dayStart).Scan(&used)
	if used+n > limit {
		return fmt.Errorf("daily image limit reached (%d/%d)", used, limit)
	}
	return nil
}

// imageQuotaWindow computes the fixed-window start for a period (mirrors the
// orchestrator's quotaWindow; blank/0 period defaults to 7 days).
func imageQuotaWindow(periodSeconds int) int64 {
	p := int64(periodSeconds)
	if p <= 0 {
		p = imageQuotaDefaultPeriod
	}
	now := time.Now().Unix()
	return (now / p) * p
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

// checkModelImageQuota enforces the image model's per-group quota (§ user groups)
// from the shared usage_logs image ledger, so drawing-mode and chat tool-call
// generations on the SAME model draw from one pool (§4.20). Admins are exempt.
// This is a hard cap (no credit fallback), consistent with the daily image limit.
func (t *imageGenerateTool) checkModelImageQuota(ctx context.Context, userID string, model *store.Model, n int) error {
	if userID == "" {
		return nil
	}
	u, _ := store.FindUserByID(ctx, t.db, userID)
	if u != nil && u.Role == "admin" {
		return nil // admins are exempt from usage quotas
	}
	has, err := store.ModelHasAnyQuota(ctx, t.db, model.ID)
	if err != nil || !has {
		return nil // no quota rows → unlimited (fail-open on error)
	}
	groupID := store.DefaultGroupID
	if u != nil && u.GroupID != "" {
		groupID = u.GroupID
	}
	q, err := store.GetModelQuota(ctx, t.db, model.ID, groupID)
	if err != nil {
		// Restricted model with no row for this group → not available to them.
		return errors.New(imageQuotaMessage(t.db))
	}
	if q.LimitValue <= 0 {
		return nil // granted unlimited
	}
	if n <= 0 {
		n = 1
	}
	start := imageQuotaWindow(q.PeriodSeconds)
	cost, images, err := store.ImageUsageInWindow(ctx, t.db, userID, model.ID, start)
	if err != nil {
		// Hard cap → fail CLOSED on a usage-ledger read error rather than silently
		// disabling the quota (a fail-open here would let the cap be bypassed).
		return errors.New(imageQuotaMessage(t.db))
	}
	over := false
	if q.LimitType == "count" {
		over = images+n > int(q.LimitValue+0.5)
	} else {
		// Pre-project this request's cost (n × per-image) like the count branch,
		// so the cap enforces before the overshoot, not after.
		over = cost+float64(n)*model.PricePerImage > q.LimitValue
	}
	if over {
		return errors.New(imageQuotaMessage(t.db))
	}
	return nil
}

// loadInputImages resolves artifact ids to raw image bytes (ownership-checked)
// for image-to-image workflows (§4.12-C).
func (t *imageGenerateTool) loadInputImages(ctx context.Context, tc *llm.ToolContext, ids []string) []imageBytes {
	if tc == nil || tc.DB == nil {
		return nil
	}
	out := []imageBytes{}
	for _, id := range ids {
		// An input image id can be an ARTIFACT (a prior generation) or a user
		// UPLOAD (files table) — the studio passes reference uploads as file ids,
		// so try both. Both are ownership-scoped.
		var data []byte
		var mime string
		if art, err := store.GetArtifact(ctx, t.db, id, tc.UserID); err == nil && art != nil && strings.HasPrefix(art.MimeType, "image/") {
			b, rerr := os.ReadFile(art.StoragePath)
			if rerr != nil {
				continue
			}
			data, mime = b, art.MimeType
		} else if f, err := store.GetFile(ctx, t.db, id, tc.UserID); err == nil && f != nil && strings.HasPrefix(f.MimeType, "image/") {
			b, rerr := os.ReadFile(f.StoragePath)
			if rerr != nil {
				continue
			}
			data, mime = b, f.MimeType
		} else {
			continue
		}
		out = append(out, imageBytes{data: data, mime: mime})
		if len(out) >= imageImageInputImageCap {
			break
		}
	}
	return out
}

// geminiGenerateImages calls generateContent with an image-capable model and
// extracts inlineData parts (§4.12-C). Input images (explicit image-to-image
// or the conversation's image_session) ride along as inline_data parts so the
// model edits rather than starts fresh (§4.12-D).
func geminiGenerateImages(ctx context.Context, baseURL, apiKey, requestID string, in imgInput, inputImgs []imageBytes) ([]imageBytes, error) {
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
	body := map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{"responseModalities": []string{"IMAGE"}},
	}
	raw, _ := json.Marshal(body)
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", base, requestID, apiKey)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(raw)))
	req.Header.Set("content-type", "application/json")
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
				out = append(out, imageBytes{data: data, mime: orDefaultStr(mime, "image/png")})
			}
		}
	}
	return out, nil
}

// openaiGenerateImages calls the Images API (§4.12-C): plain generation via
// /v1/images/generations, or — when input images are supplied — image editing
// via the multipart /v1/images/edits endpoint.
func openaiGenerateImages(ctx context.Context, baseURL, apiKey, requestID string, in imgInput, inputImgs []imageBytes) ([]imageBytes, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = "https://api.openai.com"
	}

	// gpt-image-1 returns b64_json natively and REJECTS the response_format
	// param; only the DALL·E models accept it. Send it only for dall-e and parse
	// both b64_json and url responses so either model family works.
	isDalle := strings.Contains(strings.ToLower(requestID), "dall")
	var req *http.Request
	if len(inputImgs) > 0 {
		// Image edit (图生图): multipart form with the source image + prompt.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("model", requestID)
		_ = mw.WriteField("prompt", in.Prompt)
		_ = mw.WriteField("n", fmt.Sprintf("%d", in.N))
		_ = mw.WriteField("size", in.Size)
		if isDalle {
			_ = mw.WriteField("response_format", "b64_json")
		}
		fw, err := mw.CreateFormFile("image", "input"+extForMime(inputImgs[0].mime))
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(inputImgs[0].data); err != nil {
			return nil, err
		}
		_ = mw.Close()
		req, _ = http.NewRequestWithContext(ctx, "POST", base+"/v1/images/edits", &buf)
		req.Header.Set("content-type", mw.FormDataContentType())
	} else {
		body := map[string]any{
			"model":  requestID,
			"prompt": in.Prompt,
			"n":      in.N,
			"size":   in.Size,
		}
		if isDalle {
			body["response_format"] = "b64_json"
		}
		raw, _ := json.Marshal(body)
		req, _ = http.NewRequestWithContext(ctx, "POST", base+"/v1/images/generations", strings.NewReader(string(raw)))
		req.Header.Set("content-type", "application/json")
	}
	req.Header.Set("authorization", "Bearer "+apiKey)
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
				out = append(out, imageBytes{data: data, mime: "image/png"})
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchRemoteImageDownloadCap)) // 32MB cap
	if err != nil || len(data) == 0 {
		return nil, ""
	}
	mime := resp.Header.Get("content-type")
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/png"
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

// searchKnowledgeBaseTool — design.md §4.11 (optional tool-mode path). Calls
// the same rag.Retrieve the orchestrator uses for the always-on inject path.
type searchKnowledgeBaseTool struct {
	rag *rag.Service
}

func (t *searchKnowledgeBaseTool) Name() string { return "search_knowledge_base" }
func (t *searchKnowledgeBaseTool) Description() string {
	return "Search the user's attached knowledge bases or conversation files. Use when the question is about uploaded documents. Returns numbered snippets."
}
func (t *searchKnowledgeBaseTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"top_k":{"type":"integer","default":5}},"required":["query"]}`)
}

type kbInput struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

func (t *searchKnowledgeBaseTool) Execute(ctx context.Context, input []byte, tc *llm.ToolContext) (string, []llm.Citation, error) {
	var in kbInput
	_ = json.Unmarshal(input, &in)
	if in.TopK <= 0 {
		in.TopK = inTopK2
	}
	snippets, err := tc.RAG.Retrieve(ctx, tc.UserID, tc.ConvID, tc.KBIDs, in.Query, in.TopK)
	if err != nil {
		return "", nil, err
	}
	out := strings.Builder{}
	cites := []llm.Citation{}
	for _, c := range snippets {
		out.WriteString(fmt.Sprintf("[%d] %s\n%s\n\n", c.Index, c.Title, c.Snippet))
		cites = append(cites, llm.Citation{ID: c.ID, Index: c.Index, Title: c.Title, URL: c.URL, Snippet: c.Snippet, Source: c.Source})
	}
	if out.Len() == 0 {
		return "No matching content found in the user's documents.", nil, nil
	}
	return out.String(), cites, nil
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
		return "", nil, errors.New("name required")
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
	// skills table, so it can't be deleted in the admin panel. Checked AFTER
	// the DB skills so an admin skill with the same name shadows it, matching
	// the system-prompt index in composeSystemPrompt.
	if strings.EqualFold(in.Name, llm.DocGenSkillName) {
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
	var in memInput
	_ = json.Unmarshal(input, &in)
	if in.MemoryText == "" {
		return "", nil, errors.New("memory_text required")
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
