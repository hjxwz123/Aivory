package rag

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"word/document.xml": `<w:document><w:body><w:p><w:r><w:t>Quarterly report body.</w:t></w:r></w:p></w:body></w:document>`,
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
		"word/document.xml": `<w:document><w:body><w:p><w:r><w:t>Has a figure.</w:t></w:r></w:p></w:body></w:document>`,
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
