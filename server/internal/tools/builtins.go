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

	"aurelia/server/internal/config"
	"aurelia/server/internal/llm"
	"aurelia/server/internal/rag"
	"aurelia/server/internal/sandbox"
	"aurelia/server/internal/store"
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
		in.TopK = 5
	}
	if t.searcher == nil {
		// Fallback "result" so the model can still respond gracefully.
		fake := []llm.Citation{
			{ID: "w1", Index: 1, Title: "Aurelia local-only mode", URL: "https://example.com/aurelia-local-mode", Snippet: "No SEARCH_API_KEY configured. Configure one to enable real web_search results.", Source: "web"},
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
	req.Header.Set("user-agent", "AureliaBot/1.0")
	resp, err := ssrfSafeClient().Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	// Truncate after 256 KB — keeps tokens bounded.
	limited := io.LimitReader(resp.Body, 256*1024)
	body, _ := io.ReadAll(limited)
	text := stripHTML(string(body))
	// Roughly cap at ~8K tokens (≈32K chars) per §4.4.
	if len(text) > 32000 {
		text = text[:32000] + "\n…[truncated]"
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
	req.Header.Set("user-agent", "AureliaBot/1.0")
	resp, err := ssrfSafeClient().Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", nil, fmt.Errorf("image fetch failed: HTTP %d", resp.StatusCode)
	}
	const maxImg = 15 * 1024 * 1024
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxImg+1))
	if len(data) == 0 {
		return "", nil, errors.New("empty image response")
	}
	if len(data) > maxImg {
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
		sid, err := t.sandbox.NewSession(ctx)
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
		sid, err := t.sandbox.NewSession(ctx)
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
				if f.Kind != "sheet" && f.Kind != "text" && f.Kind != "code" && f.Kind != "pdf" && f.Kind != "image" {
					continue
				}
				if f.SizeBytes > 20*1024*1024 {
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
				if a.SizeBytes > 20*1024*1024 {
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
		// from /workspace/skills/<name>/.
		if tc.DB != nil && tc.UserID != "" {
			if skillIDs, err := store.SkillsForUser(ctx, tc.DB, tc.UserID); err == nil {
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
					sid2, sErr := t.sandbox.NewSession(ctx)
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
				sid2, sErr := t.sandbox.NewSession(ctx)
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
		out.WriteString("stdout:\n" + truncateOutput(res.Stdout, 32*1024) + "\n")
	}
	if res.Stderr != "" {
		out.WriteString("stderr:\n" + truncateOutput(res.Stderr, 32*1024) + "\n")
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
	if in.N > 4 {
		in.N = 4
	}
	if in.Size == "" {
		in.Size = "1024x1024"
	}

	// §4.12-E 内容安全: 生成前 prompt 过审（关键词来自管理员配置，非硬编码）。
	if err := t.moderateImagePrompt(in.Prompt); err != nil {
		return "", nil, err
	}

	// §8.2 每用户每日图像张数限额（按 usage_logs 当日累计）。
	if tc != nil && tc.DB != nil {
		if err := t.checkDailyImageLimit(ctx, tc.UserID, in.N); err != nil {
			return "", nil, err
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

	var images []imageBytes
	switch {
	case isGemini:
		images, err = geminiGenerateImages(ctx, channel.BaseURL, channel.APIKey, model.RequestID, in, inputImgs)
	case channel.Type == "openai":
		images, err = openaiGenerateImages(ctx, channel.BaseURL, channel.APIKey, model.RequestID, in, inputImgs)
	default:
		return "", nil, fmt.Errorf("image generation not supported for channel type %q", channel.Type)
	}
	if err != nil {
		return "", nil, err
	}
	if len(images) == 0 {
		return "The image model returned no images.", nil, nil
	}

	lastArtifactID := ""
	for i, img := range images {
		ext := extForMime(img.mime)
		name := fmt.Sprintf("image_%d%s", i+1, ext)
		if art := saveArtifact(ctx, tc, t.artifactDir, name, img.mime, img.data); art != nil {
			lastArtifactID = art.ID
		}
	}
	// §4.12-D Gemini 多轮编辑: remember the latest generation on the
	// conversation so the next image_generate call edits it by default.
	if isGemini && lastArtifactID != "" && tc != nil && tc.DB != nil && tc.ConvID != "" {
		_ = store.SetConvProviderStateKey(ctx, tc.DB, tc.ConvID, "image_session", lastArtifactID)
	}

	// Record cost (§8.3) — one usage row, images_count = N.
	if tc != nil && tc.DB != nil {
		_ = store.LogUsage(ctx, t.db, store.UsageLog{
			UserID:         tc.UserID,
			ConversationID: tc.ConvID,
			MessageID:      tc.MessageID,
			ModelID:        model.ID,
			Purpose:        "image",
			ImagesCount:    len(images),
			Cost:           float64(len(images)) * model.PricePerImage,
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
	dayStart := time.Now().Truncate(24 * time.Hour).Unix()
	var used int
	_ = t.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(images_count),0) FROM usage_logs WHERE user_id=? AND purpose='image' AND created_at>=?`,
		userID, dayStart).Scan(&used)
	if used+n > limit {
		return fmt.Errorf("daily image limit reached (%d/%d)", used, limit)
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
		art, err := store.GetArtifact(ctx, t.db, id, tc.UserID)
		if err != nil || art == nil || !strings.HasPrefix(art.MimeType, "image/") {
			continue
		}
		data, err := os.ReadFile(art.StoragePath)
		if err != nil {
			continue
		}
		out = append(out, imageBytes{data: data, mime: art.MimeType})
		if len(out) >= 3 {
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
		parts = append(parts, map[string]any{
			"inline_data": map[string]any{
				"mime_type": img.mime,
				"data":      base64.StdEncoding.EncodeToString(img.data),
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

	var req *http.Request
	if len(inputImgs) > 0 {
		// Image edit (图生图): multipart form with the source image + prompt.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("model", requestID)
		_ = mw.WriteField("prompt", in.Prompt)
		_ = mw.WriteField("n", fmt.Sprintf("%d", in.N))
		_ = mw.WriteField("size", in.Size)
		_ = mw.WriteField("response_format", "b64_json")
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
			"model":           requestID,
			"prompt":          in.Prompt,
			"n":               in.N,
			"size":            in.Size,
			"response_format": "b64_json",
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
		}
	}
	return out, nil
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
		in.TopK = 5
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

func (t *useSkillTool) Execute(ctx context.Context, input []byte, _ *llm.ToolContext) (string, []llm.Citation, error) {
	var in skillInput
	_ = json.Unmarshal(input, &in)
	if in.Name == "" {
		return "", nil, errors.New("name required")
	}
	skills, err := store.ListSkills(ctx, t.db, true)
	if err != nil {
		return "", nil, err
	}
	for _, s := range skills {
		if strings.EqualFold(s.Name, in.Name) {
			return "Skill: " + s.Name + "\n\n" + s.Instructions, nil, nil
		}
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
		Confidence: 0.95,
	})
	if err != nil {
		return "", nil, err
	}
	return "Memory saved.", nil, nil
}
