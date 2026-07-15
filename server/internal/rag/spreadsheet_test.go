package rag

import (
	"archive/zip"
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

func TestColIndex(t *testing.T) {
	cases := map[string]int{"A1": 0, "B3": 1, "Z9": 25, "AA1": 26, "AB12": 27, "": -1, "12": -1}
	for ref, want := range cases {
		if got := colIndex(ref); got != want {
			t.Fatalf("colIndex(%q) = %d, want %d", ref, got, want)
		}
	}
}
