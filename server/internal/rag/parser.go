package rag

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aurelia/server/internal/netsafe"
	"aurelia/server/internal/sandbox"
	"aurelia/server/internal/storage"
	"aurelia/server/internal/store"

	"github.com/ledongthuc/pdf"
)

// MinerUResult is the structured output we get back from MinerU. Markdown is
// the body of `full.md` from the zip with image refs rewritten to the
// `mineru://filename` markers the existing chunker recognises (§4.11-C image
// embed). Images is the per-image metadata pulled from the same zip — caption,
// page number, filename — so the citation UI can link back to the source
// image when an image_caption chunk is retrieved.
type MinerUResult struct {
	Markdown string
	Images   []MinerUImage
}

// MinerUImage is one image extracted from a non-text document. Caption may be
// blank — the orchestrator can route blank captions through a VLM later if
// needed. Filename is the basename inside the zip (e.g. `foo.png`), used as
// the `image_ref` on the resulting image_caption chunk and matched against
// the inline `mineru://filename` markers in the markdown.
type MinerUImage struct {
	PageNo   int
	Caption  string
	Filename string
	MimeType string
}

// parseDocument extracts text from a document (§4.11-C). The decision rule:
//
//   - mime says text/* / json / xml / csv / markdown → local read (MinerU
//     does not accept these formats anyway).
//   - PDF/DOC/PPT/XLS/image/HTML → MinerU cloud API: upload bytes to the
//     admin-configured bucket (S3 or Aliyun OSS), hand the presigned URL to
//     MinerU's submit endpoint, poll, download the zip, unpack `full.md`.
//     Pipeline model with OCR on per the project requirement.
//   - When MinerU isn't configured (no token) or the bucket isn't configured,
//     binary files fall back to a placeholder so the ingest pipeline still
//     completes without a hard failure.
//
// Live-config: the MinerU URL/token + storage block are passed in by the
// caller (rag.runPipeline) which re-reads them from admin settings on every
// ingest, so admin changes apply on the very next document without a server
// restart.
func parseDocument(
	ctx context.Context,
	docPath, mime, filename string,
	mineruURL, mineruToken string,
	sb *storage.Client,
) (string, error) {
	if docPath == "" {
		return "", nil
	}
	if isProbablyText(mime, docPath, filename) {
		b, err := os.ReadFile(docPath)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	// Binary formats. Project rule: PDF / DOC(X) / PPT(X) are parsed LOCALLY
	// when they're text-native; only image-bearing or scanned documents are sent
	// to MinerU (paid OCR). Everything else (images, legacy .doc/.ppt, html)
	// goes to MinerU when configured, otherwise a one-line placeholder so ingest
	// still completes. CSV/XLS(X) never reach here — runPipeline short-circuits
	// spreadsheets to the code sandbox instead of parsing/embedding.
	ext := docExt(filename, docPath)
	mineruReady := mineruURL != "" && mineruToken != "" && sb.Enabled()
	tryMineru := func() (string, bool) {
		if !mineruReady {
			return "", false
		}
		md, err := runMinerUMarkdown(ctx, docPath, filename, mime, mineruURL, mineruToken, sb)
		if err != nil || strings.TrimSpace(md) == "" {
			return "", false
		}
		return md, true
	}

	switch ext {
	case "docx", "pptx":
		text, hasImages, ok := extractOfficeXML(docPath, ext)
		if ok && !hasImages {
			return text, nil // pure-text office doc → local
		}
		if md, done := tryMineru(); done {
			return md, nil // image-bearing → MinerU OCR/figure captions
		}
		if ok && strings.TrimSpace(text) != "" {
			return text, nil // degraded: MinerU unavailable, keep local text
		}
	case "pdf":
		text, hasImages, ok := extractPDFText(docPath)
		scanned := !ok || strings.TrimSpace(text) == ""
		if ok && !scanned && !hasImages {
			return text, nil // text-native, no figures → local
		}
		if md, done := tryMineru(); done {
			return md, nil // scanned or image-bearing → MinerU
		}
		if ok && !scanned {
			return text, nil // degraded: MinerU unavailable, keep local text
		}
	default:
		// Images, legacy .doc/.ppt, .html, etc. — OCR/conversion territory.
		if md, done := tryMineru(); done {
			return md, nil
		}
	}

	info, _ := os.Stat(docPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	return filepath.Base(docPath) + " — binary document, " + formatBytes(size) + " (configure MinerU + object storage in admin settings to extract content, or analyse spreadsheets in the code sandbox).", nil
}

// runMinerUMarkdown runs the cloud OCR pipeline and assembles the markdown body
// plus any per-image markers the zip layout didn't already embed.
func runMinerUMarkdown(ctx context.Context, docPath, filename, mime, baseURL, token string, sb *storage.Client) (string, error) {
	res, err := minerUExtractViaCloud(ctx, docPath, filename, mime, baseURL, token, sb)
	if err != nil {
		return "", err
	}
	b := strings.Builder{}
	b.WriteString(res.Markdown)
	for _, im := range res.Images {
		if strings.Contains(b.String(), "mineru://"+im.Filename) {
			continue
		}
		caption := strings.TrimSpace(im.Caption)
		if caption == "" {
			caption = im.Filename
		}
		fmt.Fprintf(&b, "\n\n<!-- mineru-image page=%d -->\n![%s](mineru://%s)\n", im.PageNo, caption, im.Filename)
	}
	return b.String(), nil
}

// docExt returns the lower-case extension (no dot) from the original filename,
// falling back to the on-disk path.
func docExt(filename, docPath string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(docPath), "."))
	}
	return ext
}

// extractOfficeXML pulls plain text out of a DOCX/PPTX (both are ZIP+XML) using
// the standard library only, and reports whether the archive embeds any images
// (a media/ entry) — image-bearing docs are routed to MinerU for figure OCR.
func extractOfficeXML(docPath, ext string) (text string, hasImages bool, ok bool) {
	zr, err := zip.OpenReader(docPath)
	if err != nil {
		return "", false, false
	}
	defer zr.Close()

	var bodyParts []string
	for _, f := range zr.File {
		name := f.Name
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "word/media/") || strings.HasPrefix(lower, "ppt/media/") {
			// Skip vector-only chrome (theme images are rare); any real media
			// entry flags the doc as image-bearing.
			hasImages = true
			continue
		}
		want := false
		switch ext {
		case "docx":
			want = lower == "word/document.xml" ||
				strings.HasPrefix(lower, "word/header") ||
				strings.HasPrefix(lower, "word/footer")
		case "pptx":
			want = strings.HasPrefix(lower, "ppt/slides/slide") && strings.HasSuffix(lower, ".xml")
		}
		if !want {
			continue
		}
		raw, rerr := readZipEntry(f, 16*1024*1024)
		if rerr != nil {
			continue
		}
		if s := stripOfficeXML(string(raw)); strings.TrimSpace(s) != "" {
			bodyParts = append(bodyParts, s)
		}
	}
	return strings.TrimSpace(strings.Join(bodyParts, "\n\n")), hasImages, true
}

func readZipEntry(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, max))
}

// officeBreakRe matches the block/cell boundaries we turn into whitespace so
// words don't run together when the tags are stripped.
var (
	officeParaBreakRe = regexp.MustCompile(`(?i)</w:p>|</a:p>|<w:br\s*/?>|<a:br\s*/?>|</w:tr>|</a:tr>`)
	officeCellBreakRe = regexp.MustCompile(`(?i)</w:tc>|</a:tc>`)
	xmlTagRe          = regexp.MustCompile(`<[^>]+>`)
	blankLinesRe      = regexp.MustCompile(`\n{3,}`)
)

// stripOfficeXML converts a WordprocessingML / DrawingML fragment to plain text:
// paragraph and row closes become newlines, cell closes become tabs, remaining
// tags are dropped, and XML entities are unescaped.
func stripOfficeXML(raw string) string {
	s := officeParaBreakRe.ReplaceAllString(raw, "\n")
	s = officeCellBreakRe.ReplaceAllString(s, "\t")
	s = xmlTagRe.ReplaceAllString(s, "")
	s = strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&apos;", "'", "&#39;", "'",
	).Replace(s)
	s = blankLinesRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// extractPDFText extracts the text layer with a pure-Go reader and reports
// whether the PDF embeds image XObjects. ok=false (or empty text) means the PDF
// has no extractable text — i.e. a scan — and should go to MinerU. The reader
// panics on some malformed PDFs, so the whole call is recover-guarded.
func extractPDFText(docPath string) (text string, hasImages bool, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			text, hasImages, ok = "", false, false
		}
	}()
	f, reader, err := pdf.Open(docPath)
	if err != nil {
		return "", false, false
	}
	defer f.Close()
	var buf bytes.Buffer
	if tr, terr := reader.GetPlainText(); terr == nil {
		_, _ = io.Copy(&buf, tr)
	}
	return strings.TrimSpace(buf.String()), pdfHasImages(docPath), true
}

// pdfHasImages is a cheap heuristic: image XObjects declare `/Subtype /Image`
// in their (usually uncompressed) object dictionary, so a raw-bytes scan finds
// them without a full structural parse. Misses are safe — a scanned PDF has no
// text layer and is caught by the empty-text check instead.
func pdfHasImages(docPath string) bool {
	b, err := os.ReadFile(docPath)
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte("/Subtype/Image")) || bytes.Contains(b, []byte("/Subtype /Image"))
}

// minerUExtractViaCloud runs the four-step pipeline against the MinerU cloud
// API (https://mineru.net):
//
//  1. Upload the document bytes to the admin-configured bucket (S3 or OSS)
//     via the sidecar's /storage/put → returns a 1-hour presigned GET URL.
//  2. POST /api/v4/extract/task {url, model_version: pipeline, is_ocr: true}.
//  3. Poll GET /api/v4/extract/task/{task_id} until state == "done" or
//     "failed". Pipeline model returns within minutes for typical PDFs;
//     we cap at 20 minutes (200-page ceiling, 6s/page worst case).
//  4. Download the full_zip_url, unpack `full.md` + image files in-memory,
//     rewrite image references to `mineru://filename` markers.
//
// The uploaded object is best-effort deleted at the end so the bucket doesn't
// accumulate sources. Failure to delete is logged but doesn't fail the parse.
func minerUExtractViaCloud(
	ctx context.Context,
	docPath, filename, mime string,
	baseURL, token string,
	sb *storage.Client,
) (*MinerUResult, error) {
	body, err := os.ReadFile(docPath)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("mineru: empty document")
	}

	// Upload — key is mineru/<short-id>/<safe-name> so the bucket layout is
	// inspectable and the prefix is owned by the parser. The sidecar adds the
	// admin-configured storage_prefix in front.
	safe := filepath.Base(filename)
	if safe == "" {
		safe = filepath.Base(docPath)
	}
	key := "mineru/" + store.GenID("u") + "/" + safe
	if mime == "" {
		mime = "application/octet-stream"
	}
	put, err := sb.Put(ctx, key, body, mime, 0)
	if err != nil {
		return nil, fmt.Errorf("mineru: upload: %w", err)
	}
	// Always clean up — even on a poll failure the source object should die.
	defer func() {
		// Use a fresh context so client-cancel doesn't skip cleanup.
		dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = sb.Delete(dctx, put.Key)
	}()

	// Submit the task. MinerU requires the file URL to be reachable from
	// their side — presigned S3/OSS URLs do that without making the bucket
	// public.
	taskID, err := minerUSubmitTask(ctx, baseURL, token, put.URL)
	if err != nil {
		return nil, fmt.Errorf("mineru: submit: %w", err)
	}

	// Poll until done or failed. 5s interval keeps a tight feedback loop; cap
	// at 20 minutes — anything longer than that is an operational failure
	// rather than slow processing.
	zipURL, perr := minerUPollTask(ctx, baseURL, token, taskID, 5*time.Second, 20*time.Minute)
	if perr != nil {
		return nil, fmt.Errorf("mineru: poll: %w", perr)
	}

	// Download and unpack.
	res, err := minerUDownloadAndUnpack(ctx, zipURL)
	if err != nil {
		return nil, fmt.Errorf("mineru: unpack: %w", err)
	}
	return res, nil
}

// minerUSubmitTask creates one extract task. We hard-code:
//   - model_version: "pipeline" (project requirement; PDF/DOC/PPT/IMG work
//     here, HTML would need "MinerU-HTML" but the existing classifier puts
//     html into the local-text bucket).
//   - is_ocr: true (project requirement — guarantees scanned PDFs work).
//
// MinerU 限速 hint: 1000 high-prio pages/day. We don't try to throttle from
// the client — the queue's natural concurrency cap (4 workers) keeps the
// flow well below MinerU's per-token rate limits.
func minerUSubmitTask(ctx context.Context, baseURL, token, fileURL string) (string, error) {
	payload := map[string]any{
		"url":            fileURL,
		"model_version":  "pipeline",
		"is_ocr":         true,
		"enable_formula": true,
		"enable_table":   true,
	}
	body, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v4/extract/task"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "*/*")
	req.Header.Set("authorization", "Bearer "+token)
	resp, err := mineruClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, truncateAtN(string(b), 256))
	}
	var parsed struct {
		Code    int             `json:"code"`
		Msg     string          `json:"msg"`
		TraceID string          `json:"trace_id"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.Code != 0 {
		return "", fmt.Errorf("mineru code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	var d struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(parsed.Data, &d); err != nil {
		return "", err
	}
	if d.TaskID == "" {
		return "", fmt.Errorf("mineru: empty task_id")
	}
	return d.TaskID, nil
}

// minerUPollTask polls /api/v4/extract/task/{task_id} until state ∈
// {done, failed} or the deadline expires. Returns the full_zip_url on done.
func minerUPollTask(ctx context.Context, baseURL, token, taskID string, interval, max time.Duration) (string, error) {
	deadline := time.Now().Add(max)
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v4/extract/task/" + taskID
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("accept", "*/*")
		req.Header.Set("authorization", "Bearer "+token)
		resp, err := mineruClient.Do(req)
		if err != nil {
			return "", err
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("status %d: %s", resp.StatusCode, truncateAtN(string(bodyBytes), 256))
		}
		var parsed struct {
			Code int `json:"code"`
			Msg  string
			Data struct {
				TaskID      string `json:"task_id"`
				State       string `json:"state"`
				FullZipURL  string `json:"full_zip_url"`
				ErrMsg      string `json:"err_msg"`
				ExtractProg struct {
					ExtractedPages int    `json:"extracted_pages"`
					TotalPages     int    `json:"total_pages"`
					StartTime      string `json:"start_time"`
				} `json:"extract_progress"`
			} `json:"data"`
		}
		if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
			return "", fmt.Errorf("decode: %w", err)
		}
		if parsed.Code != 0 {
			return "", fmt.Errorf("mineru code=%d msg=%s", parsed.Code, parsed.Msg)
		}
		switch parsed.Data.State {
		case "done":
			if parsed.Data.FullZipURL == "" {
				return "", fmt.Errorf("mineru: done without full_zip_url")
			}
			return parsed.Data.FullZipURL, nil
		case "failed":
			return "", fmt.Errorf("mineru parse failed: %s", parsed.Data.ErrMsg)
		case "pending", "running", "converting", "waiting-file", "":
			// keep polling
		default:
			// Unknown state — keep polling but be defensive about ttl.
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("mineru: poll timed out after %s (last state=%s)", max, parsed.Data.State)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
	}
}

// minerUDownloadAndUnpack pulls the zip and extracts `full.md` plus every
// image file referenced from it. The zip layout per MinerU's docs:
//   - `full.md` — the markdown result. Image refs are relative paths like
//     `images/foo.png` pointing at the same zip's `images/` directory.
//   - `images/` — image files (png/jpg/etc.).
//   - `*_content_list.json`, `*_layout.json`, `*_model.json` — metadata, ignored.
//
// We rewrite `![alt](images/foo.png)` → `![alt](mineru://foo.png)` so the
// existing chunker's `mineruImageMarker` regex picks them up as image_caption
// chunks.
// mineruZipClient downloads the MinerU result zip. Because the zip URL comes
// from the API *response* (not admin config), it goes through an SSRF-safe
// client that blocks private/internal IPs at dial time (§C6).
var mineruZipClient = netsafe.PrivateBlockClient(5 * time.Minute)

func minerUDownloadAndUnpack(ctx context.Context, zipURL string) (*MinerUResult, error) {
	// §C6: only fetch http(s) zip URLs; reject file://, gopher://, etc.
	if u, perr := url.Parse(zipURL); perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("mineru: refusing non-http(s) zip url")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", zipURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := mineruZipClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("zip download status %d", resp.StatusCode)
	}
	// Cap the download at 500 MiB — zips for normal documents are <100 MiB;
	// anything bigger is a runaway and we'd rather error than OOM the server.
	const maxZip = 500 * 1024 * 1024
	zipBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxZip+1))
	if err != nil {
		return nil, err
	}
	if int64(len(zipBytes)) > maxZip {
		return nil, fmt.Errorf("zip too large (>%d bytes)", maxZip)
	}

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, err
	}

	// Walk once: pick the `*.md` body and collect image entries.
	var markdown string
	type imgEntry struct {
		basename string
		mimeType string
	}
	var images []imgEntry
	for _, f := range zr.File {
		// Real zip-slip defence: per-segment check. `path.Clean` would resolve
		// a leading `..` against the rooted prefix and hide the traversal, so
		// we split and reject any `..`, empty segment, or absolute path. Today
		// we never write zip entries to disk (only read full.md + harvest
		// basenames), but the next change might — keep the guard honest.
		if !zipNameIsSafe(f.Name) {
			continue
		}
		base := path.Base(f.Name)
		if base == "" || strings.HasSuffix(f.Name, "/") {
			continue
		}
		lower := strings.ToLower(f.Name)
		switch {
		case lower == "full.md" || strings.HasSuffix(lower, "/full.md"):
			rc, err := f.Open()
			if err != nil {
				continue
			}
			b, err := io.ReadAll(io.LimitReader(rc, 32*1024*1024))
			rc.Close()
			if err == nil {
				markdown = string(b)
			}
		case strings.HasPrefix(lower, "images/") || strings.Contains(lower, "/images/"):
			images = append(images, imgEntry{basename: base, mimeType: guessImageMime(base)})
		}
	}
	if strings.TrimSpace(markdown) == "" {
		return nil, fmt.Errorf("mineru: no full.md in zip")
	}

	// Rewrite ![alt](images/...filename) → ![alt](mineru://filename).
	// We anchor the regex on either `images/` or `./images/` since the zip
	// layout has occasionally appeared with both — and we drop everything
	// before the basename so the chunker's marker regex matches.
	markdown = mineruImagesPathRe.ReplaceAllStringFunc(markdown, func(m string) string {
		sub := mineruImagesPathRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		alt := sub[1]
		fname := path.Base(sub[2])
		return "![" + alt + "](mineru://" + fname + ")"
	})

	out := &MinerUResult{Markdown: markdown}
	// We don't reorder images — the markdown already has them in document
	// order. We attach the list so the ingest pipeline can persist
	// image_caption rows with the right `image_ref` even when the markdown
	// rewriter missed a path (defensive — appended at the bottom by
	// parseDocument).
	seen := map[string]bool{}
	for _, im := range images {
		if seen[im.basename] {
			continue
		}
		seen[im.basename] = true
		out.Images = append(out.Images, MinerUImage{
			Filename: im.basename,
			MimeType: im.mimeType,
		})
	}
	return out, nil
}

// mineruImagesPathRe matches `![alt](images/foo.png)` and `![alt](./images/foo.png)`.
// Capture (1) = alt text, (2) = full path so we can take the basename.
var mineruImagesPathRe = regexp.MustCompile(`!\[([^\]]*)\]\(((?:\./)?images/[^)\s]+)\)`)

// zipNameIsSafe returns true when the zip entry's name has no traversal
// segments, no empty/absolute prefix, and no Windows-y backslashes. We don't
// use `path.Clean` because Clean *resolves* leading `..` against the rooted
// prefix and hides the traversal entirely (so its output never contains `..`
// no matter how malicious the input).
func zipNameIsSafe(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." || seg == "" {
			// empty segment = `a//b` or trailing slash on a dir entry; let the
			// caller handle the directory case separately (we test HasSuffix
			// after). Trailing-slash-only names land here too — reject and the
			// `HasSuffix "/"` branch in the walker is now dead but harmless.
			if seg == "" && strings.HasSuffix(name, "/") && strings.Count(name, "/") == 1 {
				continue
			}
			return false
		}
	}
	return true
}

// mineruClient is a long-timeout HTTP client used for the submit + poll +
// download legs of the cloud API. 60s connect + a per-call deadline via the
// request context keeps individual round-trips honest while the larger
// poll-loop ceiling is enforced in minerUPollTask.
var mineruClient = &http.Client{Timeout: 5 * time.Minute}

func guessImageMime(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	}
	return "image/png"
}

func truncateAtN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// isSpreadsheetData reports whether a document is a spreadsheet / tabular data
// file that should be handed to the code sandbox (python_execute reads it from
// /workspace/uploads with pandas/openpyxl) rather than parsed and embedded.
func isSpreadsheetData(filename, mime string) bool {
	switch docExt(filename, filename) {
	case "csv", "tsv", "xlsx", "xls", "xlsm":
		return true
	}
	m := strings.ToLower(mime)
	return strings.Contains(m, "spreadsheet") || strings.Contains(m, "ms-excel")
}

// isCodeOrConfigText reports whether a file is source code or structured-config
// text (json/yaml/toml/ini/xml/…). These are poor dense-vector targets: chunking
// breaks function/structure boundaries and semantic similarity over code/config
// retrieves badly. In a conversation they're injected whole (when small) and are
// always reachable via the code sandbox (/workspace/uploads), so the embedding
// pass is skipped for them. PROSE is deliberately excluded — md/txt/log/rst/html
// are legitimate RAG targets and still embed.
func isCodeOrConfigText(filename string) bool {
	switch docExt(filename, filename) {
	case
		// source code
		"go", "py", "pyw", "js", "jsx", "ts", "tsx", "mjs", "cjs", "vue", "svelte",
		"c", "h", "cpp", "cxx", "cc", "hpp", "hh", "cs", "java", "kt", "kts", "swift", "rs",
		"rb", "php", "scala", "r", "jl", "lua", "pl", "pm", "dart",
		"ex", "exs", "erl", "hrl", "clj", "hs", "ml", "mli", "fs", "f90", "asm", "s",
		"sh", "bash", "zsh", "ps1", "bat", "sql",
		"v", "sv", "svh", "vh", "vhd", "vhdl",
		"proto", "graphql", "gql", "tcl", "groovy", "gradle",
		"css", "scss", "sass", "less",
		// structured config / data text
		"json", "yaml", "yml", "toml", "ini", "cfg", "conf", "env", "properties", "xml":
		return true
	}
	return false
}

// isProbablyText decides whether to read a file directly as text. Policy:
// ANYTHING unrecognized is treated as plain text (so an uploaded .v / .ini / any
// source file is read instead of dropped) — we only bail out for formats that
// have a real binary parser (office, pdf) or are clearly non-text (images,
// audio/video, archives, executables), which would yield garbage as text.
// `p` may be a temp path without the original extension, so `filename` is
// consulted too.
func isProbablyText(mime, p, filename string) bool {
	mime = strings.ToLower(mime)
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(p))
	}
	// Formats with dedicated extraction (office/pdf) or that are inherently
	// binary → not plain text.
	switch ext {
	case ".docx", ".pptx", ".xlsx", ".xlsm", ".doc", ".ppt", ".xls", ".pdf",
		".zip", ".gz", ".tgz", ".tar", ".rar", ".7z", ".bz2",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".tif", ".tiff", ".heic",
		".mp3", ".wav", ".flac", ".ogg", ".m4a", ".mp4", ".mov", ".avi", ".mkv", ".webm",
		".exe", ".dll", ".so", ".dylib", ".bin", ".o", ".a", ".class", ".wasm":
		return false
	}
	if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "audio/") ||
		strings.HasPrefix(mime, "video/") || mime == "application/pdf" ||
		strings.Contains(mime, "officedocument") || strings.Contains(mime, "msword") ||
		strings.Contains(mime, "ms-excel") || strings.Contains(mime, "ms-powerpoint") ||
		strings.Contains(mime, "zip") || strings.Contains(mime, "octet-stream") && ext == "" {
		return false
	}
	// Everything else → read as plain text.
	return true
}

func formatBytes(n int64) string {
	if n < 1024 {
		return formatInt(n) + " B"
	}
	if n < 1024*1024 {
		return formatFloat(float64(n)/1024) + " KB"
	}
	return formatFloat(float64(n)/1024/1024) + " MB"
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	if neg {
		out = append([]byte{'-'}, out...)
	}
	return string(out)
}

func formatFloat(f float64) string {
	if f >= 100 {
		return formatInt(int64(f + 0.5))
	}
	intPart := int64(f)
	frac := int64((f-float64(intPart))*10 + 0.5)
	return formatInt(intPart) + "." + formatInt(frac)
}

// storageBlockFromSettings reads the admin-configured S3 / OSS block from the
// settings table. Returns nil when no provider is set or required fields are
// missing — caller treats that as "no upload bucket; MinerU disabled".
func storageBlockFromSettings(db *sql.DB) *sandbox.StorageConfig {
	if db == nil {
		return nil
	}
	provider := readSettingString(db, "storage_provider", "")
	if provider != "s3" && provider != "aliyun_oss" {
		return nil
	}
	cfg := &sandbox.StorageConfig{
		Provider: provider,
		Prefix:   readSettingString(db, "storage_prefix", "workspaces/"),
	}
	switch provider {
	case "s3":
		cfg.S3Bucket = readSettingString(db, "storage_s3_bucket", "")
		cfg.S3Region = readSettingString(db, "storage_s3_region", "")
		cfg.S3Endpoint = readSettingString(db, "storage_s3_endpoint", "")
		cfg.S3AccessKey = readSettingString(db, "storage_s3_access_key", "")
		cfg.S3SecretKey = readSettingString(db, "storage_s3_secret_key", "")
	case "aliyun_oss":
		cfg.OSSBucket = readSettingString(db, "storage_aliyun_bucket", "")
		cfg.OSSEndpoint = readSettingString(db, "storage_aliyun_endpoint", "")
		cfg.OSSAccessKeyID = readSettingString(db, "storage_aliyun_access_key_id", "")
		cfg.OSSAccessKeySecret = readSettingString(db, "storage_aliyun_access_key_secret", "")
	}
	if !cfg.Effective() {
		return nil
	}
	return cfg
}

// readSettingString reads one setting key as a JSON string. When the row is
// missing (admin never touched it), returns `def`. When the row exists,
// returns whatever the admin saved — including an empty string. That's the
// explicit "clear / disable" gesture from the UI; falling back to env in
// that case would silently keep the feature alive against the admin's intent.
func readSettingString(db *sql.DB, key, def string) string {
	raw, err := store.GetSetting(db, key)
	if err != nil {
		return def
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return def
	}
	return strings.TrimSpace(v)
}
