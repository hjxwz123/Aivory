package rag

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeZip builds a zip file on disk from name→content entries and returns its path.
func writeZip(t *testing.T, name string, entries map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for n, c := range entries {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatalf("zip create %s: %v", n, err)
		}
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatalf("zip write %s: %v", n, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return p
}

func TestStripOfficeXML(t *testing.T) {
	in := `<w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t> world</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>Line &amp; two &lt;ok&gt;</w:t></w:r></w:p>`
	got := stripOfficeXML(in)
	if !strings.Contains(got, "Hello world") {
		t.Errorf("expected joined run text, got %q", got)
	}
	if !strings.Contains(got, "Line & two <ok>") {
		t.Errorf("entities not unescaped: %q", got)
	}
	// Two paragraphs → a newline between them.
	if !strings.Contains(got, "\n") {
		t.Errorf("expected paragraph break, got %q", got)
	}
}

func TestExtractOfficeXML_DOCXTextOnly(t *testing.T) {
	p := writeZip(t, "doc.docx", map[string]string{
		"word/document.xml":   `<w:document><w:body><w:p><w:r><w:t>Quarterly report body.</w:t></w:r></w:p></w:body></w:document>`,
		"[Content_Types].xml": `<x/>`,
	})
	text, hasImages, ok := extractOfficeXML(p, "docx")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if hasImages {
		t.Error("text-only docx should not report images")
	}
	if !strings.Contains(text, "Quarterly report body.") {
		t.Errorf("missing body text: %q", text)
	}
}

func TestExtractOfficeXML_DOCXWithImage(t *testing.T) {
	p := writeZip(t, "doc.docx", map[string]string{
		"word/document.xml":     `<w:document><w:body><w:p><w:r><w:t>Has a figure.</w:t></w:r></w:p></w:body></w:document>`,
		"word/media/image1.png": "\x89PNG\r\n\x1a\n fake",
	})
	text, hasImages, ok := extractOfficeXML(p, "docx")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !hasImages {
		t.Error("docx with word/media/ should report images → routes to MinerU")
	}
	if !strings.Contains(text, "Has a figure.") {
		t.Errorf("missing body text: %q", text)
	}
}

func TestExtractOfficeXML_PPTX(t *testing.T) {
	p := writeZip(t, "deck.pptx", map[string]string{
		"ppt/slides/slide1.xml": `<p:sld><a:p><a:r><a:t>Slide one title</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide2.xml": `<p:sld><a:p><a:r><a:t>Second slide</a:t></a:r></a:p></p:sld>`,
	})
	text, hasImages, ok := extractOfficeXML(p, "pptx")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if hasImages {
		t.Error("no media → should not report images")
	}
	if !strings.Contains(text, "Slide one title") || !strings.Contains(text, "Second slide") {
		t.Errorf("missing slide text: %q", text)
	}
}

func TestIsProbablyText(t *testing.T) {
	const ooxmlWord = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	const ooxmlPpt = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	const ooxmlXls = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	cases := []struct {
		name     string
		mime     string
		path     string
		filename string
		want     bool
	}{
		// OOXML mimes literally contain "xml" ("openxmlformats") — they are ZIP
		// archives and MUST NOT be read as text (regression: garbled zip bytes).
		{"docx by mime", ooxmlWord, "/tmp/up_abc", "report.docx", false},
		{"pptx by mime", ooxmlPpt, "/tmp/up_abc", "deck.pptx", false},
		{"xlsx by mime", ooxmlXls, "/tmp/up_abc", "book.xlsx", false},
		// Even with an empty/octet mime, the extension must win.
		{"docx by ext only", "application/octet-stream", "/tmp/up_abc.docx", "", false},
		{"pptx by ext only", "", "/tmp/up_abc.pptx", "report.pptx", false},
		{"pdf", "application/pdf", "/tmp/up_abc", "scan.pdf", false},
		// Genuine text formats still take the local-read path.
		{"plain text", "text/plain", "/tmp/up_abc", "notes.txt", true},
		{"markdown ext", "", "/tmp/up_abc", "README.md", true},
		{"json mime", "application/json", "/tmp/up_abc", "data", true},
		{"real xml", "application/xml", "/tmp/up_abc", "feed.xml", true},
	}
	for _, c := range cases {
		if got := isProbablyText(c.mime, c.path, c.filename); got != c.want {
			t.Errorf("%s: isProbablyText(%q,%q,%q)=%v want %v", c.name, c.mime, c.path, c.filename, got, c.want)
		}
	}
}

func TestIsSpreadsheetData(t *testing.T) {
	cases := []struct {
		name string
		mime string
		want bool
	}{
		{"data.csv", "text/csv", true},
		{"book.xlsx", "", true},
		{"old.xls", "", true},
		{"sheet", "application/vnd.ms-excel", true},
		{"report.pdf", "application/pdf", false},
		{"notes.txt", "text/plain", false},
		{"deck.pptx", "", false},
	}
	for _, c := range cases {
		if got := isSpreadsheetData(c.name, c.mime); got != c.want {
			t.Errorf("isSpreadsheetData(%q,%q)=%v want %v", c.name, c.mime, got, c.want)
		}
	}
}

func TestDocExt(t *testing.T) {
	if docExt("Report.PDF", "") != "pdf" {
		t.Error("docExt should lower-case and strip the dot")
	}
	if docExt("", "/tmp/x_d_abc.docx") != "docx" {
		t.Error("docExt should fall back to the on-disk path")
	}
}

func TestPDFInspectionPagesUsesEvenSamples(t *testing.T) {
	got := pdfInspectionPages(10, 3)
	want := []int{1, 5, 10}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pdfInspectionPages(10, 3) = %v, want %v", got, want)
	}
	if got := pdfInspectionPages(20, 1); len(got) != 1 || got[0] != 1 {
		t.Fatalf("single sample = %v, want [1]", got)
	}
}

func TestInspectPDFTextLayerClassifiesScanWithoutFullExtraction(t *testing.T) {
	pages := make([]string, 12)
	p := writeSimplePDF(t, pages, true)
	inspection, ok := inspectPDFTextLayer(p)
	if !ok {
		t.Fatal("scan inspection failed")
	}
	if !inspection.scanned || inspection.thin {
		t.Fatalf("scan inspection = %+v, want scanned only", inspection)
	}
	if inspection.method != "resources" {
		t.Fatalf("scan method = %q, want resources", inspection.method)
	}
	if inspection.sampledPages != pdfInspectionSampleLimit {
		t.Fatalf("sampled pages = %d, want %d", inspection.sampledPages, pdfInspectionSampleLimit)
	}
}

func TestPDFRawSignalsArePositiveOnlyWithoutFontsOrObjectStreams(t *testing.T) {
	images := []byte("/Subtype /Image /Subtype/Image")
	if signals := inspectPDFRawSignals(images); !signals.strongScan(2) {
		t.Fatalf("image-only signals = %+v, want strong scan", signals)
	}
	if signals := inspectPDFRawSignals(append(images, []byte(" /BaseFont /Helvetica")...)); signals.strongScan(2) {
		t.Fatalf("font-bearing signals = %+v, must fall through", signals)
	}
	if signals := inspectPDFRawSignals(append(images, []byte(" /Type /ObjStm")...)); signals.strongScan(2) {
		t.Fatalf("object-stream signals = %+v, must fall through", signals)
	}
}

func TestBoundedPDFInspectionRunsInChildProcess(t *testing.T) {
	p := writeSimplePDF(t, []string{"selectable digital text"}, false)
	inspection, ok := inspectPDFTextLayerBounded(context.Background(), p)
	if !ok || inspection.scanned || inspection.method != "text-sample" {
		t.Fatalf("bounded inspection = %+v ok=%v", inspection, ok)
	}
	if pdfInspectionTimeout > 9*time.Second {
		t.Fatalf("inspection timeout = %s, must leave room under the 10s SLO", pdfInspectionTimeout)
	}
}

func TestBoundedPDFInspectionKillsHungProbe(t *testing.T) {
	oldCommand := pdfInspectionCommand
	oldTimeout := pdfInspectionTimeout
	defer func() {
		pdfInspectionCommand = oldCommand
		pdfInspectionTimeout = oldTimeout
	}()
	pdfInspectionTimeout = 30 * time.Millisecond
	pdfInspectionCommand = func(ctx context.Context, _ string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestPDFInspectionSleepHelper")
		cmd.Env = append(os.Environ(), "AIVORY_PDF_INSPECTION_TEST_SLEEP=1")
		return cmd, nil
	}

	started := time.Now()
	inspection, ok := inspectPDFTextLayerBounded(context.Background(), "unused.pdf")
	if !ok || !inspection.scanned || inspection.method != "timeout" {
		t.Fatalf("timeout inspection = %+v ok=%v", inspection, ok)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("hung probe returned after %s, want under 1s in test", elapsed)
	}
}

func TestPDFInspectionSleepHelper(t *testing.T) {
	if os.Getenv("AIVORY_PDF_INSPECTION_TEST_SLEEP") != "1" {
		return
	}
	time.Sleep(10 * time.Second)
}

func TestInspectPDFTextLayerClassifiesDigitalAndThinPDFs(t *testing.T) {
	digital := writeSimplePDF(t, []string{
		"A complete first page with enough ordinary digital text.",
		"A complete middle page with selectable digital text.",
		"A complete final page with selectable digital text.",
	}, false)
	inspection, ok := inspectPDFTextLayer(digital)
	if !ok || inspection.scanned || inspection.thin {
		t.Fatalf("digital inspection = %+v ok=%v", inspection, ok)
	}
	full, pages, ok := extractFullPDFText(digital)
	if !ok || pages != 3 || !strings.Contains(full, "complete middle page") {
		t.Fatalf("full extraction pages=%d ok=%v text=%q", pages, ok, full)
	}

	thin := writeSimplePDF(t, []string{"x", "y", "z"}, true)
	inspection, ok = inspectPDFTextLayer(thin)
	if !ok || inspection.scanned || !inspection.thin || !inspection.hasImages {
		t.Fatalf("thin inspection = %+v ok=%v", inspection, ok)
	}
}

// writeSimplePDF creates a small, structurally valid PDF without adding another
// PDF writer dependency to the server. Each string is the selectable text on one
// page; includeImage adds a shared 1x1 image XObject to exercise thin-text scans.
func writeSimplePDF(t *testing.T, pageTexts []string, includeImage bool) string {
	t.Helper()
	if len(pageTexts) == 0 {
		pageTexts = []string{""}
	}
	imageID := 4 + 2*len(pageTexts)
	lastID := imageID - 1
	if includeImage {
		lastID = imageID
	}
	objects := make([]string, lastID+1)
	objects[1] = `<< /Type /Catalog /Pages 2 0 R >>`
	objects[3] = `<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>`
	var kids strings.Builder
	for i, text := range pageTexts {
		pageID := 4 + 2*i
		contentID := pageID + 1
		fmt.Fprintf(&kids, "%d 0 R ", pageID)
		resourceEntries := ""
		if text != "" {
			resourceEntries = "/Font << /F1 3 0 R >> "
		}
		imageDraw := ""
		if includeImage {
			resourceEntries += fmt.Sprintf(`/XObject << /Im0 %d 0 R >> `, imageID)
			imageDraw = "q 1 0 0 1 0 0 cm /Im0 Do Q\n"
		}
		resources := "<< " + resourceEntries + ">>"
		objects[pageID] = fmt.Sprintf(`<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources %s /Contents %d 0 R >>`, resources, contentID)
		escaped := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(text)
		stream := imageDraw
		if text != "" {
			stream += fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\n", escaped)
		}
		objects[contentID] = fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream)
	}
	objects[2] = fmt.Sprintf(`<< /Type /Pages /Count %d /Kids [%s] >>`, len(pageTexts), kids.String())
	if includeImage {
		objects[imageID] = "<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\nx\nendstream"
	}

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, lastID+1)
	for id := 1; id <= lastID; id++ {
		offsets[id] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", id, objects[id])
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", lastID+1)
	for id := 1; id <= lastID; id++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[id])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", lastID+1, xref)
	p := filepath.Join(t.TempDir(), "fixture.pdf")
	if err := os.WriteFile(p, out.Bytes(), 0o644); err != nil {
		t.Fatalf("write PDF: %v", err)
	}
	return p
}
