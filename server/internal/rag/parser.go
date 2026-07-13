package rag

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/netsafe"
	"aivory/server/internal/sandbox"
	"aivory/server/internal/storage"
	"aivory/server/internal/store"

	"github.com/ledongthuc/pdf"
)

// MinerUResult is the structured output we get back from MinerU. Markdown is
// the body of `full.md` from the zip; image refs may be normalised temporarily
// to `mineru://filename` markers so the ingest cleanup can remove them before
// chunking, embedding, or storing text.
type MinerUResult struct {
	Markdown string
	Images   []MinerUImage
}

// MinerUImage is one image extracted from a non-text document. Caption may be
// blank — the orchestrator can route blank captions through a VLM later if
// needed. Filename is the basename inside the zip (e.g. `foo.png`).
type MinerUImage struct {
	PageNo   int
	Caption  string
	Filename string
	MimeType string
}

const (
	pdfInspectionChildModeEnv = "AIVORY_INTERNAL_PDF_INSPECTION_CHILD"
	pdfInspectionChildPathEnv = "AIVORY_INTERNAL_PDF_INSPECTION_PATH"
)

var pdfInspectionTimeout = envcfg.Dur("AIVORY_RAG_PDF_INSPECTION_TIMEOUT", 8*time.Second)

// The PDF reader does not accept a context and can spend minutes interpreting a
// pathological content stream. Run probes in a copy of this executable so the
// parent can enforce a real deadline by killing the process. The same init hook
// also works when the parent is a Go test binary.
func init() {
	if os.Getenv(pdfInspectionChildModeEnv) != "1" {
		return
	}
	inspection, ok := inspectPDFTextLayer(os.Getenv(pdfInspectionChildPathEnv))
	_ = json.NewEncoder(os.Stdout).Encode(pdfInspectionProbeResultFrom(inspection, ok))
	os.Exit(0)
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
	mineruConfigIssues []string,
	logger *log.Logger,
) (content string, extracted bool, err error) {
	if docPath == "" {
		return "", true, nil
	}
	if isProbablyText(mime, docPath, filename) {
		b, err := os.ReadFile(docPath)
		if err != nil {
			return "", false, err
		}
		return string(b), true, nil
	}

	// Binary formats. Project rule (§4.11-C latency-first): PDF / DOC(X) / PPT(X)
	// with a usable text layer are parsed LOCALLY — instantly — even when they
	// embed figures; ONLY scanned / effectively text-less documents are sent to
	// MinerU (paid cloud OCR, minutes of queue+poll during which the composer
	// stays gated on "indexing"). Everything else (images, legacy .doc/.ppt,
	// html) goes to MinerU when configured, otherwise a one-line placeholder so
	// ingest still completes. CSV/XLS(X) never reach here — runPipeline
	// short-circuits spreadsheets to the code sandbox instead of parsing/embedding.
	ext := docExt(filename, docPath)
	// MinerU OCR needs API creds and an object store URL it can fetch. Source
	// files are uploaded directly by the Go backend to S3/OSS; the sandbox
	// sidecar is intentionally not in this path, so large scanned PDFs don't
	// fail on the old Go→sidecar→OSS hop.
	storageReady := false
	if sb != nil {
		storageReady = storage.DirectUploadSupported(sb.Storage)
	}
	mineruReady := len(mineruConfigIssues) == 0 && mineruURL != "" && mineruToken != "" && storageReady
	mineruIssueSummary := strings.Join(mineruConfigIssues, "; ")
	if mineruIssueSummary == "" && !mineruReady {
		mineruIssueSummary = "object storage upload client is disabled"
	}
	var mineruErr error // last MinerU failure reason, for the diagnostic placeholder + logs
	logf := func(format string, args ...any) {
		if logger != nil {
			logger.Printf(format, args...)
		}
	}
	tryMineru := func() (string, bool) {
		if !mineruReady {
			return "", false
		}
		md, err := runMinerUMarkdown(ctx, docPath, filename, mime, mineruURL, mineruToken, sb)
		if err != nil {
			mineruErr = err
			logf("rag: MinerU parse failed for %q: %v", filename, err)
			return "", false
		}
		if strings.TrimSpace(md) == "" {
			mineruErr = fmt.Errorf("MinerU returned empty content")
			logf("rag: MinerU returned empty content for %q", filename)
			return "", false
		}
		return md, true
	}

	switch ext {
	case "docx", "pptx":
		text, _, ok := extractOfficeXML(docPath, ext)
		// Office files are born-digital: when XML text extraction works, use it
		// immediately. Embedded images no longer force the whole doc through cloud
		// OCR — that pipeline (upload → queue → per-page OCR → poll) takes minutes
		// while the composer stays gated on "indexing" (§4.11-C latency-first).
		// Only an effectively text-less file (e.g. a pptx of pure screenshots)
		// still goes to MinerU for OCR.
		if ok && strings.TrimSpace(text) != "" {
			return text, true, nil
		}
		if md, done := tryMineru(); done {
			return md, true, nil // text-less office doc → MinerU OCR
		}
	case "pdf":
		inspectionStart := time.Now()
		inspection, ok := inspectPDFTextLayerBounded(ctx, docPath)
		inspectionTook := time.Since(inspectionStart).Round(time.Millisecond)
		scanned := !ok || inspection.scanned
		thin := ok && inspection.thin
		logf("rag: PDF inspection file=%q method=%s pages=%d sampled=%d sample_chars=%d images=%v scanned=%v thin=%v took=%s",
			filename, inspection.method, inspection.pages, inspection.sampledPages, inspection.sampleChars, inspection.hasImages, scanned, thin, inspectionTook)

		// Only born-digital PDFs pay for full text extraction. Scans used to call
		// Reader.GetPlainText across every page merely to discover there was no
		// text, which made a large scan spend minutes locally before MinerU even
		// received it.
		if ok && !scanned && !thin {
			text, _, extractedOK := extractFullPDFText(docPath)
			if extractedOK && strings.TrimSpace(text) != "" {
				return text, true, nil
			}
			scanned = true
			logf("rag: %q passed the fast PDF text-layer check but full extraction failed — routing to MinerU OCR", filename)
		}
		if scanned {
			logf("rag: %q is a scanned/text-less PDF — routing to MinerU OCR after %s inspection (%d/%d sampled pages) in %s (configured=%v missing=%q)", filename, inspection.method, inspection.sampledPages, inspection.pages, inspectionTook, mineruReady, mineruIssueSummary)
		} else if thin {
			logf("rag: %q has a thin sampled text layer (%d chars over %d sampled pages, images present) — routing to MinerU OCR (configured=%v missing=%q)", filename, inspection.sampleChars, inspection.sampledPages, mineruReady, mineruIssueSummary)
		}
		if md, done := tryMineru(); done {
			return md, true, nil // scanned / thin-text → MinerU
		}
		if ok && thin {
			// MinerU unavailable or failed: preserve the old degraded behavior by
			// extracting the complete thin text layer instead of returning only the
			// sampled pages.
			if text, _, extractedOK := extractFullPDFText(docPath); extractedOK && strings.TrimSpace(text) != "" {
				return text, true, nil
			}
		}
	default:
		// Images, legacy .doc/.ppt, .html, etc. — OCR/conversion territory.
		if md, done := tryMineru(); done {
			return md, true, nil
		}
	}

	info, _ := os.Stat(docPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	// Couldn't extract text (e.g. a scan with no text layer). Spell out WHY so it
	// shows in the doc content + admin page instead of a silent "no text".
	var reason string
	switch {
	case !mineruReady:
		reason = "It looks scanned/image-only, which needs MinerU OCR — but MinerU isn't fully configured. Missing: " + mineruIssueSummary + ". Configure mineru_api_url + mineru_api_token and S3/OSS object storage (local storage cannot be used by MinerU), then re-upload."
	case mineruErr != nil:
		reason = "MinerU OCR was attempted but failed: " + mineruErr.Error() + ". Check the MinerU API token/quota and that object storage is reachable, then re-upload."
	default:
		reason = "No extractable text was found."
	}
	logf("rag: no text extracted from %q (%s): %s", filename, ext, reason)
	return filepath.Base(docPath) + " — could not extract text (" + formatBytes(size) + "). " + reason, false, nil
}

// runMinerUMarkdown runs the cloud OCR pipeline and returns the markdown body
// with MinerU image markdown stripped. Raw image markers are useful only for
// display; storing or embedding them pollutes retrieval with opaque filenames.
func runMinerUMarkdown(ctx context.Context, docPath, filename, mime, baseURL, token string, sb *storage.Client) (string, error) {
	res, err := minerUExtractViaCloud(ctx, docPath, filename, mime, baseURL, token, sb)
	if err != nil {
		return "", err
	}
	return stripMinerUMarkdownImages(res.Markdown), nil
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

// officeXMLZipEntryReadCap bounds a single DOCX/PPTX zip-entry read.
var officeXMLZipEntryReadCap = envcfg.Int64("AIVORY_RAG_OFFICE_XML_ZIP_ENTRY_READ_CAP", 16*1024*1024)

// extractOfficeXML pulls plain text out of a DOCX/PPTX (both are ZIP+XML) using
// the standard library only, and reports whether the archive embeds any images
// (a media/ entry). Since §4.11-C latency-first the image flag no longer routes
// text-bearing docs to OCR — parseDocument keeps local text whenever it's
// non-empty — but callers may still use it as a signal.
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
		raw, rerr := readZipEntry(f, officeXMLZipEntryReadCap)
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

var (
	pdfInspectionSampleLimit = envcfg.Int("AIVORY_RAG_PDF_INSPECTION_SAMPLE_LIMIT", 3)
	pdfThinCharsPerPage      = envcfg.Int("AIVORY_RAG_PDF_THIN_CHARS_PER_PAGE", 200)
)

type pdfTextInspection struct {
	pages        int
	sampledPages int
	sampleChars  int
	hasImages    bool
	scanned      bool
	thin         bool
	method       string
}

type pdfInspectionProbeResult struct {
	Pages        int    `json:"pages"`
	SampledPages int    `json:"sampled_pages"`
	SampleChars  int    `json:"sample_chars"`
	HasImages    bool   `json:"has_images"`
	Scanned      bool   `json:"scanned"`
	Thin         bool   `json:"thin"`
	Method       string `json:"method"`
	OK           bool   `json:"ok"`
}

func pdfInspectionProbeResultFrom(inspection pdfTextInspection, ok bool) pdfInspectionProbeResult {
	return pdfInspectionProbeResult{
		Pages: inspection.pages, SampledPages: inspection.sampledPages,
		SampleChars: inspection.sampleChars, HasImages: inspection.hasImages,
		Scanned: inspection.scanned, Thin: inspection.thin, Method: inspection.method, OK: ok,
	}
}

func (r pdfInspectionProbeResult) inspection() pdfTextInspection {
	return pdfTextInspection{
		pages: r.Pages, sampledPages: r.SampledPages, sampleChars: r.SampleChars,
		hasImages: r.HasImages, scanned: r.Scanned, thin: r.Thin, method: r.Method,
	}
}

// cmdWaitDelay is the grace period before the PDF-inspection child process is
// force-killed after its context deadline fires.
var cmdWaitDelay = envcfg.Dur("AIVORY_RAG_CMD_WAIT_DELAY", 500*time.Millisecond)

var pdfInspectionCommand = func(ctx context.Context, docPath string) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = append(os.Environ(),
		pdfInspectionChildModeEnv+"=1",
		pdfInspectionChildPathEnv+"="+docPath,
	)
	cmd.WaitDelay = cmdWaitDelay
	return cmd, nil
}

func inspectPDFTextLayerBounded(ctx context.Context, docPath string) (pdfTextInspection, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, pdfInspectionTimeout)
	defer cancel()
	cmd, err := pdfInspectionCommand(probeCtx, docPath)
	if err != nil {
		return pdfTextInspection{scanned: true, method: "probe-error"}, false
	}
	out, err := cmd.Output()
	if probeCtx.Err() != nil {
		return pdfTextInspection{scanned: true, method: "timeout"}, true
	}
	if err != nil {
		return pdfTextInspection{scanned: true, method: "probe-error"}, false
	}
	var result pdfInspectionProbeResult
	if err := json.Unmarshal(out, &result); err != nil {
		return pdfTextInspection{scanned: true, method: "probe-error"}, false
	}
	return result.inspection(), result.OK
}

type pdfRawSignals struct {
	imageCount      int
	fontCount       int
	hasObjectStream bool
}

func inspectPDFRawSignals(raw []byte) pdfRawSignals {
	count := func(markers ...string) int {
		total := 0
		for _, marker := range markers {
			total += bytes.Count(raw, []byte(marker))
		}
		return total
	}
	return pdfRawSignals{
		imageCount: count("/Subtype/Image", "/Subtype /Image"),
		fontCount: count(
			"/BaseFont", "/Type/Font", "/Type /Font", "/FontDescriptor", "/DescendantFonts",
		),
		hasObjectStream: count("/Type/ObjStm", "/Type /ObjStm") > 0,
	}
}

// strongScan image-to-page ratio: default imageCount*5 >= pages*4 (≈ one image
// per 80% of pages). Both multipliers are independently overridable.
var (
	pdfStrongScanImageFactor = envcfg.Int("AIVORY_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_IMAGE", 5)
	pdfStrongScanPageFactor  = envcfg.Int("AIVORY_RAG_PDF_RAW_SIGNALS_STRONG_SCAN_PAGE", 4)
)

func (s pdfRawSignals) strongScan(pages int) bool {
	if pages <= 0 || s.imageCount == 0 || s.fontCount != 0 || s.hasObjectStream {
		return false
	}
	// Require roughly one image definition for at least 80% of pages. Counts are
	// deliberately a one-way confidence signal: shared or compressed images fall
	// through to structural inspection rather than being misclassified.
	return s.imageCount*pdfStrongScanImageFactor >= pages*pdfStrongScanPageFactor
}

// inspectPDFTextLayer samples the first, middle and last page instead of
// extracting the entire document. Empty sampled pages identify normal scans in
// bounded work; evenly spaced samples avoid treating a scanned cover as proof
// that an otherwise born-digital document needs OCR.
func inspectPDFTextLayer(docPath string) (inspection pdfTextInspection, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			inspection, ok = pdfTextInspection{scanned: true, method: "probe-panic"}, false
		}
	}()
	f, reader, err := pdf.Open(docPath)
	if err != nil {
		return pdfTextInspection{scanned: true, method: "open-error"}, false
	}
	defer f.Close()
	inspection.pages = reader.NumPage()
	raw, _ := os.ReadFile(docPath)
	rawSignals := inspectPDFRawSignals(raw)
	if rawSignals.strongScan(inspection.pages) {
		inspection.hasImages = true
		inspection.scanned = true
		inspection.method = "raw"
		return inspection, true
	}
	pageNumbers := pdfInspectionPages(inspection.pages, pdfInspectionSampleLimit)
	imageResourcePages := 0
	fontResourcePages := 0
	for _, pageNumber := range pageNumbers {
		resources := reader.Page(pageNumber).Resources()
		if pdfResourcesHaveImages(resources, 2) {
			imageResourcePages++
		}
		if pdfResourcesHaveFonts(resources, 2) {
			fontResourcePages++
		}
	}
	inspection.sampledPages = len(pageNumbers)
	if inspection.sampledPages > 0 && imageResourcePages == inspection.sampledPages && fontResourcePages == 0 {
		inspection.hasImages = true
		inspection.scanned = true
		inspection.method = "resources"
		return inspection, true
	}

	for _, pageNumber := range pageNumbers {
		text, err := reader.Page(pageNumber).GetPlainText(nil)
		if err != nil {
			inspection.scanned = true
			inspection.method = "text-error"
			return inspection, false
		}
		inspection.sampleChars += len(strings.TrimSpace(text))
	}
	inspection.scanned = inspection.sampleChars == 0
	inspection.method = "text-sample"
	if !inspection.scanned {
		inspection.hasImages = rawSignals.imageCount > 0 || imageResourcePages > 0
		inspection.thin = inspection.hasImages && inspection.sampleChars < pdfThinCharsPerPage*inspection.sampledPages
	}
	return inspection, inspection.pages > 0 && inspection.sampledPages > 0
}

func pdfResourcesHaveImages(resources pdf.Value, depth int) bool {
	if depth < 0 {
		return false
	}
	xobjects := resources.Key("XObject")
	for _, name := range xobjects.Keys() {
		object := xobjects.Key(name)
		switch object.Key("Subtype").Name() {
		case "Image":
			return true
		case "Form":
			if pdfResourcesHaveImages(object.Key("Resources"), depth-1) {
				return true
			}
		}
	}
	return false
}

func pdfResourcesHaveFonts(resources pdf.Value, depth int) bool {
	if depth < 0 {
		return false
	}
	if len(resources.Key("Font").Keys()) > 0 {
		return true
	}
	xobjects := resources.Key("XObject")
	for _, name := range xobjects.Keys() {
		object := xobjects.Key(name)
		if object.Key("Subtype").Name() == "Form" && pdfResourcesHaveFonts(object.Key("Resources"), depth-1) {
			return true
		}
	}
	return false
}

func pdfInspectionPages(total, limit int) []int {
	if total <= 0 || limit <= 0 {
		return nil
	}
	if total <= limit {
		pages := make([]int, total)
		for i := range pages {
			pages[i] = i + 1
		}
		return pages
	}
	if limit == 1 {
		return []int{1}
	}
	pages := make([]int, 0, limit)
	seen := make(map[int]bool, limit)
	for i := 0; i < limit; i++ {
		page := 1 + i*(total-1)/(limit-1)
		if !seen[page] {
			seen[page] = true
			pages = append(pages, page)
		}
	}
	return pages
}

// extractFullPDFText is reserved for PDFs whose sampled pages demonstrate a
// usable text layer. The reader panics on some malformed PDFs, so the full call
// remains recover-guarded.
func extractFullPDFText(docPath string) (text string, pages int, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			text, pages, ok = "", 0, false
		}
	}()
	f, reader, err := pdf.Open(docPath)
	if err != nil {
		return "", 0, false
	}
	defer f.Close()
	tr, err := reader.GetPlainText()
	if err != nil {
		return "", reader.NumPage(), false
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, tr); err != nil {
		return "", reader.NumPage(), false
	}
	return strings.TrimSpace(buf.String()), reader.NumPage(), true
}

// mineruSourceObjectCleanupTimeout bounds the best-effort delete of the source
// object we uploaded for MinerU, run on a fresh context after the parse.
var mineruSourceObjectCleanupTimeout = envcfg.Dur("AIVORY_RAG_MINERU_SOURCE_OBJECT_CLEANUP_TIMEOUT", 30*time.Second)

// mineruPollDeadline caps the total MinerU extract poll loop.
var mineruPollDeadline = envcfg.Dur("AIVORY_RAG_MINERU_POLL_DEADLINE", 20*time.Minute)

// minerUExtractViaCloud runs the four-step pipeline against the MinerU cloud
// API (https://mineru.net):
//
//  1. Upload the document to the admin-configured bucket (S3 or OSS) from the
//     Go backend → returns a 1-hour presigned GET URL. The sandbox sidecar is
//     not used for this MinerU source-upload path.
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
	if sb == nil {
		return nil, fmt.Errorf("mineru: storage client is nil")
	}
	info, err := os.Stat(docPath)
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("mineru: empty document")
	}

	// Upload — key is mineru/<short-id>/<safe-name> so the bucket layout is
	// inspectable and the prefix is owned by the parser. The storage client adds
	// the admin-configured storage_prefix in front.
	safe := filepath.Base(filename)
	if safe == "" {
		safe = filepath.Base(docPath)
	}
	key := "mineru/" + store.GenID("u") + "/" + safe
	if mime == "" {
		mime = "application/octet-stream"
	}
	// Presigned URL must stay valid for the WHOLE OCR window: MinerU queues the
	// task and fetches the file some time after submit, and we poll up to 20 min.
	// A short default could expire mid-processing → MinerU fetch fails → silent
	// empty result. Ask for a TTL that comfortably covers it.
	if !storage.DirectUploadSupported(sb.Storage) {
		return nil, fmt.Errorf("mineru: direct S3/OSS upload is not configured")
	}
	put, err := sb.PutFileDirect(ctx, key, docPath, mime, mineruSourceTTLSeconds)
	if err != nil {
		return nil, fmt.Errorf("mineru: upload: %w", err)
	}
	// Always clean up — even on a poll failure the source object should die.
	defer func() {
		// Use a fresh context so client-cancel doesn't skip cleanup.
		dctx, cancel := context.WithTimeout(context.Background(), mineruSourceObjectCleanupTimeout)
		defer cancel()
		_ = sb.DeleteDirect(dctx, put.Key)
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
	zipURL, perr := minerUPollTask(ctx, baseURL, token, taskID, 5*time.Second, mineruPollDeadline)
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

// mineruSubmitErrorBodyTruncation caps the MinerU submit error body kept in logs.
var mineruSubmitErrorBodyTruncation = 256

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
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, truncateAtN(string(b), mineruSubmitErrorBodyTruncation))
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

// mineruPollErrorBodyTruncation caps the MinerU poll error body kept in logs.
var mineruPollErrorBodyTruncation = 256

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
			return "", fmt.Errorf("status %d: %s", resp.StatusCode, truncateAtN(string(bodyBytes), mineruPollErrorBodyTruncation))
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
// We rewrite `![alt](images/foo.png)` → `![alt](mineru://foo.png)` as a
// canonical intermediate form; runMinerUMarkdown strips those markers before
// the text reaches chunking, embedding, or database storage.
// mineruZipClient downloads the MinerU result zip. Because the zip URL comes
// from the API *response* (not admin config), it goes through an SSRF-safe
// client that blocks private/internal IPs at dial time (§C6).
var mineruZipClient = netsafe.PrivateBlockClient(envcfg.Dur("AIVORY_RAG_MINERUZIPCLIENT_TIMEOUT", 5*time.Minute))

// fullMdReadCapInsideZip bounds the in-zip full.md read.
var fullMdReadCapInsideZip = envcfg.Int64("AIVORY_RAG_FULL_MD_READ_CAP_INSIDE_ZIP", 32*1024*1024)

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
	maxZip := envcfg.Int64("AIVORY_RAG_MAX_ZIP", 500*1024*1024)
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
			b, err := io.ReadAll(io.LimitReader(rc, fullMdReadCapInsideZip))
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
	// order. We attach the list for future image-aware indexing, but the current
	// text ingest path strips opaque image markdown before storage/embedding.
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

var (
	// A MinerU-only image block can be either just the markdown image marker, or
	// the marker preceded by the optional metadata comment older parser versions
	// appended. Remove the whole line/block so it cannot become a standalone
	// child chunk.
	mineruImageOnlyBlockRe = regexp.MustCompile(`(?m)^[ \t]*(?:<!--\s*mineru-image\b[^>]*-->\s*\r?\n[ \t]*)?!\[[^\]\n]*\]\(\s*mineru://[^)\n]*\)[ \t]*(?:\r?\n)?`)
	mineruMarkdownImageRe  = regexp.MustCompile(`!\[[^\]\n]*\]\(\s*mineru://[^)\n]*\)`)
	mineruImageCommentRe   = regexp.MustCompile(`(?m)^[ \t]*<!--\s*mineru-image\b[^>]*-->[ \t]*(?:\r?\n)?`)
	excessiveBlankLinesRe  = regexp.MustCompile(`\n{3,}`)
)

func stripMinerUMarkdownImages(s string) string {
	if !strings.Contains(s, "mineru://") && !strings.Contains(s, "mineru-image") {
		return s
	}
	s = mineruImageOnlyBlockRe.ReplaceAllString(s, "\n")
	s = mineruMarkdownImageRe.ReplaceAllString(s, " ")
	s = mineruImageCommentRe.ReplaceAllString(s, "")
	s = excessiveBlankLinesRe.ReplaceAllString(s, "\n\n")
	return s
}

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
var mineruClient = &http.Client{Timeout: envcfg.Dur("AIVORY_RAG_MINERUCLIENT_TIMEOUT", 5*time.Minute)}

// mineruSourceTTLSeconds is the presigned-URL lifetime for the document we hand
// MinerU. It must outlast the full OCR window (poll cap 20 min + MinerU queue
// time); 1 hour gives generous head-room without leaving objects around long
// (they're also explicitly deleted right after the parse).
var mineruSourceTTLSeconds = envcfg.Int("AIVORY_RAG_MINERU_SOURCE_TTLSECONDS", 60*60)

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

// tokenGatedProseExts lists the formats whose extracted content reads as
// natural-language prose — legitimate dense-vector targets that keep the
// token-based full-text threshold (§4.11-B). Everything OUTSIDE this set that
// reaches the text path — source code, structured config, plain .txt, and any
// unknown / niche extension (.drawio, .foobar, …) — is line-gated instead
// (§4.11-B3): injected whole when at/below the admin-configured line cap,
// chunked + embedded above it. Code/config are poor dense-vector targets
// (chunking breaks function/structure boundaries), but past a certain size
// full injection blows the prompt, so the line cap restores a ceiling.
var tokenGatedProseExts = map[string]bool{
	// prose text read locally. rtf is in the default upload allowlist and its
	// markup reads as few very long lines, so leaving it to the line gate would
	// pin whole documents that used to embed; the lightweight prose-markup
	// cousins (tex/adoc/org) and subtitle formats (srt/vtt) are the same shape.
	"md": true, "markdown": true, "rst": true, "log": true, "html": true, "htm": true,
	"rtf": true, "tex": true, "adoc": true, "asciidoc": true, "org": true, "srt": true, "vtt": true,
	// binary-parsed document formats (content arrives as extracted/OCR'd prose)
	"pdf": true, "docx": true, "doc": true, "pptx": true, "ppt": true,
	// spreadsheets never reach the gate (sandbox path) — listed defensively
	"xlsx": true, "xls": true, "xlsm": true, "csv": true, "tsv": true,
	// images (KB-only; content is MinerU OCR prose) — listed defensively
	"png": true, "jpg": true, "jpeg": true, "jpe": true, "jfif": true, "gif": true,
	"webp": true, "bmp": true, "tif": true, "tiff": true, "heic": true, "heif": true,
	"avif": true, "ico": true,
}

// isLineGatedText reports whether a conversation-scoped document uses the
// line-count full-text gate instead of the token threshold. Covers source
// code, structured config (json/yaml/toml/ini/xml/…), plain .txt (admin
// decision: exact text beats lossy chunk retrieval for pasted blobs), and
// every unknown / undeclared extension — those parse as plain text anyway,
// so they follow the same exact-context rule.
func isLineGatedText(filename string) bool {
	return !tokenGatedProseExts[docExt(filename, filename)]
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
// settings table. Returns nil + human-readable issues when no provider is set
// or required fields are missing — caller treats that as "no upload bucket;
// MinerU disabled". The sandbox's "local" provider is intentionally rejected:
// it can archive workspaces, but it cannot give MinerU cloud a presigned fetch
// URL.
func storageBlockFromSettings(db *sql.DB) (*sandbox.StorageConfig, []string) {
	if db == nil {
		return nil, []string{"settings database is unavailable"}
	}
	provider := readSettingString(db, "storage_provider", "")
	if provider != "s3" && provider != "aliyun_oss" {
		if provider == "" {
			return nil, []string{"storage_provider is empty (choose s3 or aliyun_oss for MinerU)"}
		}
		if provider == "local" {
			return nil, []string{`storage_provider is "local"; MinerU needs s3 or aliyun_oss because local storage has no presigned fetch URL`}
		}
		return nil, []string{"storage_provider " + strconv.Quote(provider) + " is unsupported for MinerU"}
	}
	cfg := &sandbox.StorageConfig{
		Provider: provider,
		Prefix:   readSettingString(db, "storage_prefix", "workspaces/"),
	}
	issues := []string{}
	switch provider {
	case "s3":
		cfg.S3Bucket = readSettingString(db, "storage_s3_bucket", "")
		cfg.S3Region = readSettingString(db, "storage_s3_region", "")
		cfg.S3Endpoint = readSettingString(db, "storage_s3_endpoint", "")
		cfg.S3AccessKey = readSettingString(db, "storage_s3_access_key", "")
		cfg.S3SecretKey = readSettingString(db, "storage_s3_secret_key", "")
		if cfg.S3Bucket == "" {
			issues = append(issues, "storage_s3_bucket is empty")
		}
	case "aliyun_oss":
		cfg.OSSBucket = readSettingString(db, "storage_aliyun_bucket", "")
		cfg.OSSEndpoint = readSettingString(db, "storage_aliyun_endpoint", "")
		cfg.OSSAccessKeyID = readSettingString(db, "storage_aliyun_access_key_id", "")
		cfg.OSSAccessKeySecret = readSettingString(db, "storage_aliyun_access_key_secret", "")
		if cfg.OSSBucket == "" {
			issues = append(issues, "storage_aliyun_bucket is empty")
		}
		if cfg.OSSEndpoint == "" {
			issues = append(issues, "storage_aliyun_endpoint is empty")
		}
		if cfg.OSSAccessKeyID == "" {
			issues = append(issues, "storage_aliyun_access_key_id is empty")
		}
		if cfg.OSSAccessKeySecret == "" {
			issues = append(issues, "storage_aliyun_access_key_secret is empty")
		}
	}
	if len(issues) > 0 || !cfg.Effective() {
		if len(issues) == 0 {
			issues = append(issues, "object storage config is incomplete")
		}
		return nil, issues
	}
	return cfg, nil
}

func minerUConfigIssues(mineruURL, mineruToken string, storageCfg *sandbox.StorageConfig, storageIssues []string) []string {
	issues := []string{}
	if strings.TrimSpace(mineruURL) == "" {
		issues = append(issues, "mineru_api_url is empty")
	}
	if strings.TrimSpace(mineruToken) == "" {
		issues = append(issues, "mineru_api_token is empty")
	}
	if len(storageIssues) > 0 {
		issues = append(issues, storageIssues...)
	} else if storageCfg == nil || !storageCfg.Effective() || !storage.DirectUploadSupported(storageCfg) {
		issues = append(issues, "S3/OSS object storage is not configured")
	}
	return issues
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
