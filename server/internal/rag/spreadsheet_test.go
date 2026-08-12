package rag

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMinimalXLSX writes a tiny but structurally-real .xlsx (a zip of the XML
// parts our parser reads) with shared strings, a numeric cell, and two
// date-styled cells (builtin numFmt 14), so the parser is exercised end-to-end.
func writeMinimalXLSX(t *testing.T, path string) {
	t.Helper()
	parts := map[string]string{
		"xl/workbook.xml": `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>`,
		"xl/sharedStrings.xml": `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="5" uniqueCount="5">
<si><t>Name</t></si><si><t>Score</t></si><si><t>Joined</t></si><si><t>Alice</t></si><si><t>Bob</t></si></sst>`,
		"xl/styles.xml": `<?xml version="1.0"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="14"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c></row>
<row r="2"><c r="A2" t="s"><v>3</v></c><c r="B2"><v>95</v></c><c r="C2" s="1"><v>45000</v></c></row>
<row r="3"><c r="A3" t="s"><v>4</v></c><c r="B3"><v>88</v></c><c r="C3" s="1"><v>45100</v></c></row>
</sheetData></worksheet>`,
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create xlsx: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

func TestSpreadsheetPreviewXLSX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.xlsx")
	writeMinimalXLSX(t, path)

	out, err := SpreadsheetPreview(path, "book.xlsx", 30, 40)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// Sheet name + true dimensions.
	if !strings.Contains(out, "book.xlsx › Data") || !strings.Contains(out, "3 rows × 3 cols") {
		t.Fatalf("missing sheet title/shape:\n%s", out)
	}
	// Shared strings + numbers resolved.
	for _, want := range []string{"Name", "Score", "Joined", "Alice", "95", "Bob", "88"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing value %q:\n%s", want, out)
		}
	}
	// Date-styled serials must render as ISO dates, NOT raw serials.
	if !strings.Contains(out, "2023-03-15") {
		t.Fatalf("date serial 45000 not converted to a date:\n%s", out)
	}
	if strings.Contains(out, "45000") {
		t.Fatalf("raw date serial leaked instead of a date:\n%s", out)
	}
}

func TestSpreadsheetPreviewCSVAndTruncation(t *testing.T) {
	// Header + 50 data rows, 3 cols, prefixed with a UTF-8 BOM (built from bytes so
	// the test source stays BOM-free).
	content := []byte{0xEF, 0xBB, 0xBF}
	content = append(content, []byte("name,score,city\n")...)
	for i := 0; i < 50; i++ {
		content = append(content, []byte("row,1,shanghai\n")...)
	}
	path := filepath.Join(t.TempDir(), "data.csv")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	out, err := SpreadsheetPreview(path, "data.csv", 30, 40)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	// True total is 51 rows even though only ~31 are shown.
	if !strings.Contains(out, "51 rows × 3 cols") {
		t.Fatalf("wrong shape line:\n%s", out)
	}
	// BOM stripped — the header row begins directly with "name" after the "===" line.
	if !strings.Contains(out, "===\nname\t") {
		t.Fatalf("BOM not stripped from header:\n%s", out)
	}
	if !strings.Contains(out, "more rows)") {
		t.Fatalf("row truncation notice missing:\n%s", out)
	}
}

func TestSpreadsheetPreviewHonorsConfiguredFileLimit(t *testing.T) {
	content := []byte("name,score\nAlice,95\n")
	path := filepath.Join(t.TempDir(), "limited.csv")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	original := spreadsheetMaxFileBytes
	spreadsheetMaxFileBytes = int64(len(content) - 1)
	t.Cleanup(func() { spreadsheetMaxFileBytes = original })

	_, err := SpreadsheetPreview(path, "limited.csv", 30, 40)
	if err == nil || !strings.Contains(err.Error(), "spreadsheet too large for inline preview") {
		t.Fatalf("preview error = %v, want configured size-limit rejection", err)
	}
}

func TestLoadSpreadsheetMaxFileBytesFromEnv(t *testing.T) {
	t.Setenv(spreadsheetMaxFileBytesEnv, "4194304")
	if got := loadSpreadsheetMaxFileBytes(); got != 4<<20 {
		t.Fatalf("configured spreadsheet preview limit = %d, want %d", got, 4<<20)
	}

	t.Setenv(spreadsheetMaxFileBytesEnv, "not-a-number")
	if got := loadSpreadsheetMaxFileBytes(); got != defaultSpreadsheetMaxFileBytes {
		t.Fatalf("invalid spreadsheet preview limit = %d, want default %d", got, defaultSpreadsheetMaxFileBytes)
	}
}

func TestSpreadsheetPreviewRejectsLegacyXLS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.xls")
	if err := os.WriteFile(path, []byte("\xd0\xcf\x11\xe0not a zip"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := SpreadsheetPreview(path, "old.xls", 30, 40); err == nil {
		t.Fatal("legacy .xls should return an error, not silent garbage")
	}
}

func TestSpreadsheetIndexTextCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.csv")
	content := "name\tscore\nAlice\t95\nBob\t88\n"
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(content, "\t", ",")), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	out, err := SpreadsheetIndexText(path, "records.csv")
	if err != nil {
		t.Fatalf("index text: %v", err)
	}
	for _, want := range []string{"records.csv", "3 rows × 2 cols", "name\tscore", "Alice\t95", "Bob\t88"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in index text:\n%s", want, out)
		}
	}
}

func TestSpreadsheetIndexTextXLSX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.xlsx")
	writeMinimalXLSX(t, path)

	out, err := SpreadsheetIndexText(path, "book.xlsx")
	if err != nil {
		t.Fatalf("index text: %v", err)
	}
	for _, want := range []string{"book.xlsx › Data", "Name\tScore\tJoined", "Alice\t95", "2023-03-15"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in index text:\n%s", want, out)
		}
	}
}

func TestSpreadsheetIndexRejectsCorruptWorksheetButPreviewKeepsReadablePrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.xlsx")
	writeSpreadsheetZip(t, path, map[string]string{
		"xl/workbook.xml": `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>readable-prefix</t></is></c></row>
<row r="2"><c r="A2"><v>broken</row></sheetData></worksheet>`,
	})

	if _, err := SpreadsheetIndexText(path, "corrupt.xlsx"); err == nil || !strings.Contains(err.Error(), "parse worksheet") {
		t.Fatalf("KB index accepted a corrupt worksheet: %v", err)
	}
	preview, err := SpreadsheetPreview(path, "corrupt.xlsx", 30, 40)
	if err != nil || !strings.Contains(preview, "readable-prefix") {
		t.Fatalf("chat preview lost its best-effort behavior: preview=%q err=%v", preview, err)
	}
}

func TestSpreadsheetIndexRejectsMalformedCSVWithoutChangingPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.csv")
	if err := os.WriteFile(path, []byte("name,value\nok,1\n\"unterminated,2\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	if _, err := SpreadsheetIndexText(path, "malformed.csv"); err == nil {
		t.Fatal("KB index accepted a malformed CSV tail")
	}
	preview, err := SpreadsheetPreview(path, "malformed.csv", 30, 40)
	if err != nil || !strings.Contains(preview, "ok\t1") {
		t.Fatalf("chat preview lost its readable prefix: preview=%q err=%v", preview, err)
	}
}

func TestSpreadsheetIndexPreservesLongCellsBeyondPreviewLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.csv")
	longCell := strings.Repeat("abstract", 20)
	if err := os.WriteFile(path, []byte("title,body\nPaper,"+longCell+"\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	indexed, err := SpreadsheetIndexText(path, "long.csv")
	if err != nil {
		t.Fatalf("index text: %v", err)
	}
	if !strings.Contains(indexed, longCell) {
		t.Fatalf("KB index truncated a long cell: %q", indexed)
	}
	preview, err := SpreadsheetPreview(path, "long.csv", 30, 40)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if strings.Contains(preview, longCell) || !strings.Contains(preview, "…") {
		t.Fatalf("chat preview cell limit changed: %q", preview)
	}
}

func TestSpreadsheetIndexTextReportsRowTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.csv")
	if err := os.WriteFile(path, []byte("name,value\nA,1\nB,2\nC,3\nD,4\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	original := spreadsheetIndexMaxRows
	spreadsheetIndexMaxRows = 1
	t.Cleanup(func() { spreadsheetIndexMaxRows = original })

	out, err := SpreadsheetIndexText(path, "many.csv")
	if err != nil {
		t.Fatalf("index text: %v", err)
	}
	if !strings.Contains(out, "3 more rows") {
		t.Fatalf("row truncation notice missing:\n%s", out)
	}
}

func TestSpreadsheetIndexTextRejectsLegacyXLSWithSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.xls")
	if err := os.WriteFile(path, []byte("\xd0\xcf\x11\xe0not a zip"), 0o644); err != nil {
		t.Fatalf("write xls: %v", err)
	}

	_, err := SpreadsheetIndexText(path, "old.xls")
	if !errors.Is(err, ErrLegacyXLSUnsupported) {
		t.Fatalf("index error = %v, want ErrLegacyXLSUnsupported", err)
	}
	if !strings.Contains(err.Error(), "re-save as .xlsx") {
		t.Fatalf("legacy xls error lacks remediation: %v", err)
	}
	if _, err := SpreadsheetPreview(path, "old.xls", 30, 40); !errors.Is(err, ErrLegacyXLSUnsupported) {
		t.Fatalf("preview error = %v, want same sentinel", err)
	}
}

func TestSpreadsheetIndexFileLimitDoesNotChangePreviewLimit(t *testing.T) {
	content := []byte("name,score\nAlice,95\n")
	path := filepath.Join(t.TempDir(), "limited.csv")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	originalIndex := spreadsheetIndexMaxFileBytes
	originalPreview := spreadsheetMaxFileBytes
	spreadsheetIndexMaxFileBytes = int64(len(content) - 1)
	spreadsheetMaxFileBytes = int64(len(content) + 1)
	t.Cleanup(func() {
		spreadsheetIndexMaxFileBytes = originalIndex
		spreadsheetMaxFileBytes = originalPreview
	})

	if _, err := SpreadsheetIndexText(path, "limited.csv"); err == nil || !strings.Contains(err.Error(), "too large for knowledge-base indexing") {
		t.Fatalf("index error = %v, want independent size-limit rejection", err)
	}
	if _, err := SpreadsheetPreview(path, "limited.csv", 30, 40); err != nil {
		t.Fatalf("preview inherited index limit: %v", err)
	}
}

func TestTruncateSpreadsheetIndexTextUsesCompleteLineBoundary(t *testing.T) {
	text := "header\nrow-one\n" + strings.Repeat("row-two-is-long\n", 20)
	maxBytes := len(spreadsheetIndexTruncationNotice) + len("header\nrow-one\n")
	out := truncateSpreadsheetIndexText(text, maxBytes)
	if len(out) > maxBytes {
		t.Fatalf("truncated output has %d bytes, max %d", len(out), maxBytes)
	}
	if !strings.HasPrefix(out, "header\nrow-one\n") || strings.Contains(out, "row-two") {
		t.Fatalf("output did not stop at a complete row:\n%s", out)
	}
	if !strings.HasSuffix(out, spreadsheetIndexTruncationNotice) {
		t.Fatalf("output lacks explicit truncation notice:\n%s", out)
	}
}

func TestSpreadsheetIndexTextReportsOmittedWorksheets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two-sheets.xlsx")
	writeTwoSheetXLSX(t, path)
	original := spreadsheetIndexMaxSheets
	spreadsheetIndexMaxSheets = 1
	t.Cleanup(func() { spreadsheetIndexMaxSheets = original })

	out, err := SpreadsheetIndexText(path, "two-sheets.xlsx")
	if err != nil {
		t.Fatalf("index text: %v", err)
	}
	if !strings.Contains(out, "two-sheets.xlsx › First") || strings.Contains(out, "second-only-value") {
		t.Fatalf("worksheet limit was not applied:\n%s", out)
	}
	if !strings.Contains(out, "remaining rows or worksheets omitted") {
		t.Fatalf("worksheet omission notice missing:\n%s", out)
	}
}

func writeTwoSheetXLSX(t *testing.T, path string) {
	t.Helper()
	parts := map[string]string{
		"xl/workbook.xml": `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="First" sheetId="1" r:id="rId1"/><sheet name="Second" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/>
</Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>first-only-value</t></is></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>second-only-value</t></is></c></row></sheetData></worksheet>`,
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create xlsx: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

func writeSpreadsheetZip(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create spreadsheet zip: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

func TestColIndex(t *testing.T) {
	cases := map[string]int{"A1": 0, "B3": 1, "Z9": 25, "AA1": 26, "AB12": 27, "": -1, "12": -1}
	for ref, want := range cases {
		if got := colIndex(ref); got != want {
			t.Fatalf("colIndex(%q) = %d, want %d", ref, got, want)
		}
	}
}
