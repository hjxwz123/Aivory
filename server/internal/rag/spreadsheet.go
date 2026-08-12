package rag

import (
	"archive/zip"
	"bufio"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"aivory/server/internal/envcfg"
)

// Spreadsheet → text preview. This is deliberately stdlib-only (archive/zip +
// encoding/xml + encoding/csv): the "disable tools" path parses uploaded
// spreadsheets IN-PROCESS and injects the text into the prompt, so it must never
// depend on the code sandbox (python_execute) or a third-party xlsx library that
// would drag in a Go-version / crypto bump on a pinned build image. It extracts a
// BOUNDED preview (per sheet: dimensions, then the first rows/cols) — enough for a
// model to see and reason about the data, not a full re-materialisation.
const (
	spreadsheetMaxFileBytesEnv     = "AIVORY_RAG_SPREADSHEET_PREVIEW_MAX_FILE_BYTES"
	defaultSpreadsheetMaxFileBytes = int64(30 << 20) // 30 MiB

	spreadsheetIndexMaxFileBytesEnv = "AIVORY_RAG_SPREADSHEET_INDEX_MAX_FILE_BYTES"
	spreadsheetIndexMaxRowsEnv      = "AIVORY_RAG_SPREADSHEET_INDEX_MAX_ROWS"
	spreadsheetIndexMaxColsEnv      = "AIVORY_RAG_SPREADSHEET_INDEX_MAX_COLS"
	spreadsheetIndexMaxSheetsEnv    = "AIVORY_RAG_SPREADSHEET_INDEX_MAX_SHEETS"
	spreadsheetIndexMaxTextBytesEnv = "AIVORY_RAG_SPREADSHEET_INDEX_MAX_TEXT_BYTES"

	defaultSpreadsheetIndexMaxFileBytes = int64(30 << 20) // 30 MiB
	defaultSpreadsheetIndexMaxRows      = 2_000
	defaultSpreadsheetIndexMaxCols      = 128
	defaultSpreadsheetIndexMaxSheets    = 32
	defaultSpreadsheetIndexMaxTextBytes = 4 << 20 // 4 MiB

	// spreadsheetEntryReadCap bounds decompressed bytes read per zip entry — a
	// zip-bomb guard so a tiny xlsx can't expand to gigabytes in memory.
	spreadsheetEntryReadCap = 64 << 20 // 64 MiB
	// spreadsheetMaxScanRows bounds how many rows we iterate to compute the true
	// row count, so a million-row sheet can't pin a CPU.
	spreadsheetMaxScanRows = 200_000
)

// Files larger than this are skipped for inline preview. Operators can raise or
// lower the ceiling independently of the upload and python-sandbox staging caps.
var spreadsheetMaxFileBytes = loadSpreadsheetMaxFileBytes()

// Index limits are deliberately independent of SpreadsheetPreview. Knowledge-base
// ingestion needs substantially more rows than a chat preview, but still has to
// bound memory, XML work, embedding input, and the number of generated chunks.
var (
	spreadsheetIndexMaxFileBytes = loadSpreadsheetIndexMaxFileBytes()
	spreadsheetIndexMaxRows      = envcfg.Int(spreadsheetIndexMaxRowsEnv, defaultSpreadsheetIndexMaxRows)
	spreadsheetIndexMaxCols      = envcfg.Int(spreadsheetIndexMaxColsEnv, defaultSpreadsheetIndexMaxCols)
	spreadsheetIndexMaxSheets    = envcfg.Int(spreadsheetIndexMaxSheetsEnv, defaultSpreadsheetIndexMaxSheets)
	spreadsheetIndexMaxTextBytes = envcfg.Int(spreadsheetIndexMaxTextBytesEnv, defaultSpreadsheetIndexMaxTextBytes)
)

// ErrLegacyXLSUnsupported reports that the legacy BIFF format must be converted to XLSX.
var ErrLegacyXLSUnsupported = errors.New("legacy .xls (BIFF) is not supported")

const spreadsheetIndexTruncationNotice = "... (knowledge-base index text limit reached; remaining rows or worksheets omitted)\n"

func loadSpreadsheetMaxFileBytes() int64 {
	return envcfg.Int64(spreadsheetMaxFileBytesEnv, defaultSpreadsheetMaxFileBytes)
}

func loadSpreadsheetIndexMaxFileBytes() int64 {
	return envcfg.Int64(spreadsheetIndexMaxFileBytesEnv, defaultSpreadsheetIndexMaxFileBytes)
}

// SpreadsheetPreview reads a csv/tsv/xlsx/xlsm file from disk and returns a
// bounded, model-readable text preview (per sheet: "name (R rows × C cols)"
// followed by the first maxRows rows, maxCols columns). Legacy binary .xls
// (BIFF) is not supported — callers should surface that as "re-save as .xlsx".
func SpreadsheetPreview(path, filename string, maxRows, maxCols int) (string, error) {
	if maxRows <= 0 {
		maxRows = 30
	}
	if maxCols <= 0 {
		maxCols = 40
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Size() > spreadsheetMaxFileBytes {
		return "", fmt.Errorf("spreadsheet too large for inline preview (%d bytes)", fi.Size())
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), ".")) {
	case "csv", "tsv":
		return csvPreview(path, filename, maxRows, maxCols)
	case "xlsx", "xlsm":
		return xlsxPreview(path, filename, maxRows, maxCols)
	case "xls":
		return "", fmt.Errorf("%w for inline preview; re-save as .xlsx", ErrLegacyXLSUnsupported)
	default:
		// Unknown extension — try the zip (xlsx) container first, then plain text.
		if s, xerr := xlsxPreview(path, filename, maxRows, maxCols); xerr == nil {
			return s, nil
		}
		return csvPreview(path, filename, maxRows, maxCols)
	}
}

// SpreadsheetIndexText extracts bounded, model-readable text for knowledge-base
// ingestion. Unlike SpreadsheetPreview it keeps enough rows for useful search,
// but it still caps rows, columns, worksheets, decompressed entries, source file
// size, and final text size. Truncation is explicit so an answer model never
// mistakes a partial workbook for the complete source.
func SpreadsheetIndexText(path, filename string) (string, error) {
	maxRows := positiveOrDefault(spreadsheetIndexMaxRows, defaultSpreadsheetIndexMaxRows)
	maxCols := positiveOrDefault(spreadsheetIndexMaxCols, defaultSpreadsheetIndexMaxCols)
	maxSheets := positiveOrDefault(spreadsheetIndexMaxSheets, defaultSpreadsheetIndexMaxSheets)
	maxTextBytes := positiveOrDefault(spreadsheetIndexMaxTextBytes, defaultSpreadsheetIndexMaxTextBytes)
	maxFileBytes := spreadsheetIndexMaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = defaultSpreadsheetIndexMaxFileBytes
	}

	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Size() > maxFileBytes {
		return "", fmt.Errorf("spreadsheet too large for knowledge-base indexing (%d bytes)", fi.Size())
	}

	displayName := strings.TrimSpace(filename)
	if displayName == "" {
		displayName = filepath.Base(path)
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(displayName), "."))
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	switch ext {
	case "csv", "tsv":
		text, err := csvText(path, displayName, maxRows, maxCols, true, 0)
		if err != nil {
			return "", err
		}
		return truncateSpreadsheetIndexText(text, maxTextBytes), nil
	case "xlsx", "xlsm":
		return xlsxIndexText(path, displayName, maxRows, maxCols, maxSheets, maxTextBytes)
	case "xls":
		return "", fmt.Errorf("%w for knowledge-base indexing; re-save as .xlsx", ErrLegacyXLSUnsupported)
	default:
		if ext == "" {
			ext = "unknown"
		}
		return "", fmt.Errorf("unsupported spreadsheet format %q for knowledge-base indexing", ext)
	}
}

func positiveOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// truncateSpreadsheetIndexText preserves complete rows wherever possible and
// reserves room for an explicit omission notice. The returned string never
// exceeds maxBytes when maxBytes is positive.
func truncateSpreadsheetIndexText(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	if maxBytes <= len(spreadsheetIndexTruncationNotice) {
		return spreadsheetIndexTruncationNotice[:maxBytes]
	}
	contentLimit := maxBytes - len(spreadsheetIndexTruncationNotice)
	prefix := text[:contentLimit]
	if newline := strings.LastIndexByte(prefix, '\n'); newline >= 0 {
		prefix = prefix[:newline+1]
	} else {
		prefix = ""
	}
	return prefix + spreadsheetIndexTruncationNotice
}

// ---- CSV / TSV ----

func csvPreview(path, filename string, maxRows, maxCols int) (string, error) {
	return csvText(path, filename, maxRows, maxCols, false, 80)
}

// csvText is shared by chat preview and KB indexing. Preview mode preserves
// the historical malformed-tail tolerance and short cells; strict index mode
// rejects partial parses and relies on the overall text cap instead.
func csvText(path, filename string, maxRows, maxCols int, strict bool, cellRuneLimit int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	br := bufio.NewReader(io.LimitReader(f, spreadsheetEntryReadCap))
	// Strip a leading UTF-8 BOM so the first header cell isn't polluted with it.
	if bom, _ := br.Peek(3); len(bom) == 3 && bom[0] == 0xEF && bom[1] == 0xBB && bom[2] == 0xBF {
		_, _ = br.Discard(3)
	}
	r := csv.NewReader(br)
	r.FieldsPerRecord = -1 // ragged rows are fine for a preview
	r.LazyQuotes = !strict
	if strings.HasSuffix(strings.ToLower(filename), ".tsv") {
		r.Comma = '\t'
	}

	var rows [][]string
	total, maxColSeen := 0, 0
	for {
		rec, rerr := r.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if strict {
				return "", fmt.Errorf("parse spreadsheet row %d: %w", total+1, rerr)
			}
			// Chat preview remains best-effort: keep the readable prefix.
			break
		}
		total++
		if len(rec) > maxColSeen {
			maxColSeen = len(rec)
		}
		if len(rows) < maxRows+1 {
			if len(rec) > maxCols {
				rec = rec[:maxCols]
			}
			rows = append(rows, rec)
		}
		if total >= spreadsheetMaxScanRows {
			break
		}
	}
	if total == 0 {
		return "", fmt.Errorf("empty or unreadable csv")
	}
	return formatSheet(filename, "", rows, total, maxColSeen, maxRows, maxCols, cellRuneLimit), nil
}

// ---- XLSX / XLSM (Office Open XML — a zip of XML parts) ----

type xlsxCell struct {
	R  string `xml:"r,attr"` // cell ref, e.g. "B3"
	T  string `xml:"t,attr"` // type: s|inlineStr|str|b|n(default)
	S  string `xml:"s,attr"` // style index → cellXfs → date?
	V  string `xml:"v"`
	Is struct {
		T string `xml:"t"`
		R []struct {
			T string `xml:"t"`
		} `xml:"r"`
	} `xml:"is"`
}

type xlsxRow struct {
	C []xlsxCell `xml:"c"`
}

type xlsxSheetRef struct {
	name   string
	target string // zip path, e.g. "xl/worksheets/sheet1.xml"
}

func xlsxPreview(path, filename string, maxRows, maxCols int) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	shared, _ := parseSharedStrings(files["xl/sharedStrings.xml"])
	dateStyles, _ := parseDateStyles(files["xl/styles.xml"])
	sheets, _ := parseWorkbookSheets(files, false)
	if len(sheets) == 0 {
		return "", fmt.Errorf("no worksheets found in xlsx")
	}

	var b strings.Builder
	for _, sh := range sheets {
		wf := files[sh.target]
		if wf == nil {
			continue
		}
		rows, total, maxColSeen, _ := parseWorksheet(wf, shared, dateStyles, maxRows, maxCols)
		b.WriteString(formatSheet(filename, sh.name, rows, total, maxColSeen, maxRows, maxCols, 80))
		b.WriteString("\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("xlsx produced no readable rows")
	}
	return out, nil
}

func xlsxIndexText(path, filename string, maxRows, maxCols, maxSheets, maxTextBytes int) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	shared, err := parseSharedStrings(files["xl/sharedStrings.xml"])
	if err != nil {
		return "", fmt.Errorf("parse xlsx shared strings: %w", err)
	}
	dateStyles, err := parseDateStyles(files["xl/styles.xml"])
	if err != nil {
		return "", fmt.Errorf("parse xlsx styles: %w", err)
	}
	sheets, err := parseWorkbookSheets(files, true)
	if err != nil {
		return "", err
	}
	if len(sheets) == 0 {
		return "", fmt.Errorf("no worksheets found in xlsx")
	}

	sheetCount := len(sheets)
	if sheetCount > maxSheets {
		sheetCount = maxSheets
	}
	var b strings.Builder
	readableSheets := 0
	outputTruncated := false
	for _, sh := range sheets[:sheetCount] {
		wf := files[sh.target]
		if wf == nil {
			continue
		}
		rows, total, maxColSeen, parseErr := parseWorksheet(wf, shared, dateStyles, maxRows, maxCols)
		if parseErr != nil {
			return "", fmt.Errorf("parse worksheet %q: %w", sh.name, parseErr)
		}
		part := formatSheet(filename, sh.name, rows, total, maxColSeen, maxRows, maxCols, 0)
		if !appendSpreadsheetIndexPart(&b, part, maxTextBytes) {
			outputTruncated = true
			break
		}
		readableSheets++
	}
	if readableSheets == 0 && !outputTruncated {
		return "", fmt.Errorf("xlsx produced no readable rows")
	}
	if len(sheets) > sheetCount && !outputTruncated {
		appendSpreadsheetIndexNotice(&b, maxTextBytes)
	}
	out := strings.TrimRight(b.String(), "\n")
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("xlsx produced no readable rows")
	}
	return out, nil
}

func appendSpreadsheetIndexPart(b *strings.Builder, part string, maxBytes int) bool {
	separator := ""
	if b.Len() > 0 {
		separator = "\n"
	}
	if maxBytes <= 0 || b.Len()+len(separator)+len(part) <= maxBytes {
		b.WriteString(separator)
		b.WriteString(part)
		return true
	}

	contentLimit := maxBytes - len(spreadsheetIndexTruncationNotice)
	if contentLimit <= 0 {
		b.Reset()
		b.WriteString(spreadsheetIndexTruncationNotice[:maxBytes])
		return false
	}
	existing := b.String()
	if len(existing) > contentLimit {
		existing = spreadsheetLinePrefix(existing, contentLimit)
	}
	remaining := contentLimit - len(existing)
	pending := separator + part
	if remaining > 0 {
		existing += spreadsheetLinePrefix(pending, remaining)
	}
	b.Reset()
	b.Grow(len(existing) + len(spreadsheetIndexTruncationNotice))
	b.WriteString(existing)
	b.WriteString(spreadsheetIndexTruncationNotice)
	return false
}

func appendSpreadsheetIndexNotice(b *strings.Builder, maxBytes int) {
	if strings.HasSuffix(b.String(), spreadsheetIndexTruncationNotice) {
		return
	}
	if maxBytes <= len(spreadsheetIndexTruncationNotice) {
		b.Reset()
		b.WriteString(spreadsheetIndexTruncationNotice[:maxBytes])
		return
	}
	contentLimit := maxBytes - len(spreadsheetIndexTruncationNotice)
	prefix := b.String()
	if len(prefix) > contentLimit {
		prefix = spreadsheetLinePrefix(prefix, contentLimit)
	}
	b.Reset()
	b.Grow(len(prefix) + len(spreadsheetIndexTruncationNotice))
	b.WriteString(prefix)
	b.WriteString(spreadsheetIndexTruncationNotice)
}

func spreadsheetLinePrefix(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	prefix := text[:maxBytes]
	if newline := strings.LastIndexByte(prefix, '\n'); newline >= 0 {
		return prefix[:newline+1]
	}
	return ""
}

func parseSharedStrings(f *zip.File) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var doc struct {
		SI []struct {
			T string `xml:"t"`
			R []struct {
				T string `xml:"t"`
			} `xml:"r"`
		} `xml:"si"`
	}
	if err := xml.NewDecoder(io.LimitReader(rc, spreadsheetEntryReadCap)).Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]string, len(doc.SI))
	for i, si := range doc.SI {
		if len(si.R) > 0 {
			var s strings.Builder
			for _, r := range si.R {
				s.WriteString(r.T)
			}
			out[i] = s.String()
		} else {
			out[i] = si.T
		}
	}
	return out, nil
}

// parseDateStyles returns cellXf index → whether that style is a date/time
// number-format, so numeric cells carrying a date serial render as a real date.
func parseDateStyles(f *zip.File) (map[int]bool, error) {
	res := map[int]bool{}
	if f == nil {
		return res, nil
	}
	rc, err := f.Open()
	if err != nil {
		return res, err
	}
	defer rc.Close()
	var doc struct {
		NumFmts struct {
			NumFmt []struct {
				ID   int    `xml:"numFmtId,attr"`
				Code string `xml:"formatCode,attr"`
			} `xml:"numFmt"`
		} `xml:"numFmts"`
		CellXfs struct {
			Xf []struct {
				NumFmtID int `xml:"numFmtId,attr"`
			} `xml:"xf"`
		} `xml:"cellXfs"`
	}
	if err := xml.NewDecoder(io.LimitReader(rc, spreadsheetEntryReadCap)).Decode(&doc); err != nil {
		return res, err
	}
	custom := map[int]bool{}
	for _, nf := range doc.NumFmts.NumFmt {
		custom[nf.ID] = looksLikeDateFormat(nf.Code)
	}
	for i, xf := range doc.CellXfs.Xf {
		res[i] = isDateNumFmt(xf.NumFmtID, custom)
	}
	return res, nil
}

// isDateNumFmt covers the OOXML builtin date/time format ids plus any custom
// (id ≥ 164) format whose code looks like a date.
func isDateNumFmt(id int, custom map[int]bool) bool {
	switch id {
	case 14, 15, 16, 17, 18, 19, 20, 21, 22, 45, 46, 47:
		return true
	}
	if id >= 164 {
		return custom[id]
	}
	return false
}

func looksLikeDateFormat(code string) bool {
	c := strings.ToLower(code)
	// y (year) or d (day) is an unambiguous date signal; h/s indicate time.
	// 'm' alone is skipped (month vs minute ambiguity) to avoid false positives
	// on formats like "0.00".
	return strings.ContainsAny(c, "yd") || strings.ContainsAny(c, "hs")
}

func parseWorkbookSheets(files map[string]*zip.File, strict bool) ([]xlsxSheetRef, error) {
	wb := files["xl/workbook.xml"]
	if wb == nil {
		if strict {
			return nil, errors.New("xlsx workbook.xml is missing")
		}
		return fallbackSheets(files), nil
	}
	var wbDoc struct {
		Sheets struct {
			Sheet []struct {
				Name string `xml:"name,attr"`
				// r:id — encoding/xml matches namespaced attrs by full namespace URL.
				RID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			} `xml:"sheet"`
		} `xml:"sheets"`
	}
	rc, err := wb.Open()
	if err != nil {
		if strict {
			return nil, fmt.Errorf("open xlsx workbook: %w", err)
		}
		return fallbackSheets(files), nil
	}
	decodeErr := xml.NewDecoder(io.LimitReader(rc, spreadsheetEntryReadCap)).Decode(&wbDoc)
	rc.Close()
	if decodeErr != nil {
		if strict {
			return nil, fmt.Errorf("parse xlsx workbook: %w", decodeErr)
		}
		return fallbackSheets(files), nil
	}

	relMap := map[string]string{}
	if rels := files["xl/_rels/workbook.xml.rels"]; rels != nil {
		var relDoc struct {
			Rel []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		rc, openErr := rels.Open()
		if openErr != nil {
			if strict {
				return nil, fmt.Errorf("open xlsx workbook relationships: %w", openErr)
			}
			return fallbackSheets(files), nil
		}
		decodeErr := xml.NewDecoder(io.LimitReader(rc, spreadsheetEntryReadCap)).Decode(&relDoc)
		rc.Close()
		if decodeErr != nil {
			if strict {
				return nil, fmt.Errorf("parse xlsx workbook relationships: %w", decodeErr)
			}
			return fallbackSheets(files), nil
		}
		for _, r := range relDoc.Rel {
			relMap[r.ID] = r.Target
		}
	} else if strict {
		return nil, errors.New("xlsx workbook relationships are missing")
	}

	var out []xlsxSheetRef
	for _, s := range wbDoc.Sheets.Sheet {
		target := normalizeSheetTarget(relMap[s.RID])
		if files[target] == nil {
			if strict {
				return nil, fmt.Errorf("worksheet %q target %q is missing", s.Name, target)
			}
			continue
		}
		out = append(out, xlsxSheetRef{name: s.Name, target: target})
	}
	if len(out) == 0 {
		if strict {
			return nil, errors.New("xlsx contains no readable worksheet relationships")
		}
		return fallbackSheets(files), nil
	}
	return out, nil
}

// normalizeSheetTarget resolves a workbook-relative rels target (e.g.
// "worksheets/sheet1.xml" or "/xl/worksheets/sheet1.xml") to a zip path.
func normalizeSheetTarget(t string) string {
	if t == "" {
		return ""
	}
	t = strings.TrimPrefix(t, "/")
	if strings.HasPrefix(t, "xl/") {
		return t
	}
	return "xl/" + t
}

// fallbackSheets is used when workbook.xml / its rels can't be parsed: list the
// worksheet parts directly, in filename order.
func fallbackSheets(files map[string]*zip.File) []xlsxSheetRef {
	var names []string
	for n := range files {
		if strings.HasPrefix(n, "xl/worksheets/") && strings.HasSuffix(n, ".xml") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]xlsxSheetRef, len(names))
	for i, n := range names {
		out[i] = xlsxSheetRef{name: strings.TrimSuffix(filepath.Base(n), ".xml"), target: n}
	}
	return out
}

func parseWorksheet(f *zip.File, shared []string, dateStyles map[int]bool, maxRows, maxCols int) ([][]string, int, int, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, 0, 0, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(io.LimitReader(rc, spreadsheetEntryReadCap))
	var rows [][]string
	total, maxColSeen := 0, 0
	for {
		tok, terr := dec.Token()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return rows, total, maxColSeen, terr
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "row" {
			continue
		}
		total++
		if total > spreadsheetMaxScanRows {
			break
		}
		// Once we've kept enough rows, keep counting but skip decoding the body.
		if len(rows) >= maxRows+1 {
			if err := dec.Skip(); err != nil {
				return rows, total, maxColSeen, err
			}
			continue
		}
		var row xlsxRow
		if err := dec.DecodeElement(&row, &se); err != nil {
			return rows, total, maxColSeen, err
		}
		cells := map[int]string{}
		rowWidth := 0
		for _, c := range row.C {
			ci := colIndex(c.R)
			if ci < 0 {
				continue
			}
			if ci+1 > rowWidth {
				rowWidth = ci + 1
			}
			if ci >= maxCols {
				continue
			}
			cells[ci] = cellValue(c, shared, dateStyles)
		}
		if rowWidth > maxColSeen {
			maxColSeen = rowWidth
		}
		width := rowWidth
		if width > maxCols {
			width = maxCols
		}
		rec := make([]string, width)
		for i := 0; i < width; i++ {
			rec[i] = cells[i]
		}
		rows = append(rows, rec)
	}
	return rows, total, maxColSeen, nil
}

// colIndex parses the column letters of a cell ref ("AB12" → column AB) into a
// 0-based index; returns -1 when the ref has no leading letters.
func colIndex(ref string) int {
	col, seen := 0, false
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'A' && c <= 'Z':
			col = col*26 + int(c-'A'+1)
			seen = true
		case c >= 'a' && c <= 'z':
			col = col*26 + int(c-'a'+1)
			seen = true
		default:
			if seen {
				return col - 1
			}
			return -1
		}
	}
	if seen {
		return col - 1
	}
	return -1
}

func cellValue(c xlsxCell, shared []string, dateStyles map[int]bool) string {
	switch c.T {
	case "s": // shared-string index
		if idx, err := strconv.Atoi(strings.TrimSpace(c.V)); err == nil && idx >= 0 && idx < len(shared) {
			return shared[idx]
		}
		return ""
	case "inlineStr":
		if len(c.Is.R) > 0 {
			var s strings.Builder
			for _, r := range c.Is.R {
				s.WriteString(r.T)
			}
			return s.String()
		}
		return c.Is.T
	case "str": // formula string result
		return c.V
	case "b": // boolean
		if strings.TrimSpace(c.V) == "1" {
			return "TRUE"
		}
		return "FALSE"
	default: // number (or empty)
		if c.V == "" {
			return ""
		}
		if c.S != "" && len(dateStyles) > 0 {
			if si, err := strconv.Atoi(c.S); err == nil && dateStyles[si] {
				return excelSerialToDate(c.V)
			}
		}
		return c.V
	}
}

// excelSerialToDate converts an Excel date serial (days since 1899-12-30, which
// bakes in Excel's fictitious 1900 leap day) to an ISO date/time string.
func excelSerialToDate(v string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return v
	}
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	days := int(f)
	frac := f - float64(days)
	t := base.AddDate(0, 0, days).Add(time.Duration(frac * 24 * float64(time.Hour)))
	if frac == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatSheet renders one sheet's kept rows as tab-separated lines, prefixed with
// its true dimensions and suffixed with "… (N more rows/columns)" when truncated.
func formatSheet(filename, sheet string, rows [][]string, total, maxColSeen, maxRows, maxCols, cellRuneLimit int) string {
	var b strings.Builder
	title := filename
	if sheet != "" {
		title = filename + " › " + sheet
	}
	fmt.Fprintf(&b, "=== %s (%d rows × %d cols) ===\n", title, total, maxColSeen)
	if len(rows) == 0 {
		b.WriteString("(no rows)\n")
		return b.String()
	}
	shown := 0
	for _, r := range rows {
		cells := make([]string, len(r))
		for j, v := range r {
			v = strings.ReplaceAll(v, "\n", " ")
			v = strings.ReplaceAll(v, "\t", " ")
			if rr := []rune(v); cellRuneLimit > 0 && len(rr) > cellRuneLimit {
				v = string(rr[:cellRuneLimit]) + "…"
			}
			cells[j] = v
		}
		b.WriteString(strings.Join(cells, "\t"))
		b.WriteByte('\n')
		shown++
		if shown >= maxRows+1 {
			break
		}
	}
	if total > shown {
		fmt.Fprintf(&b, "… (%d more rows)\n", total-shown)
	}
	if maxColSeen > maxCols {
		fmt.Fprintf(&b, "… (%d more columns)\n", maxColSeen-maxCols)
	}
	return b.String()
}
