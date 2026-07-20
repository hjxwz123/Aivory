package llm

import (
	"archive/zip"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

type spreadsheetCaptureProvider struct {
	request UnifiedChatRequest
}

func (p *spreadsheetCaptureProvider) ID() string { return "openai" }

func (p *spreadsheetCaptureProvider) Stream(
	_ context.Context,
	req UnifiedChatRequest,
	_ ToolRunner,
	_ func(SseEvent),
) (*UnifiedResult, error) {
	p.request = req
	return &UnifiedResult{
		Blocks:     []UnifiedBlock{{Kind: "text", Text: "ok"}},
		StopReason: "stop",
		Usage:      Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

type spreadsheetTestTools struct{}

func (spreadsheetTestTools) List(string) []ToolDef {
	return []ToolDef{{Name: "python_execute"}, {Name: "web_search"}}
}

func (spreadsheetTestTools) Run(context.Context, string, []byte, *ToolContext) (string, []Citation, error) {
	return "", nil, nil
}

func writeFastModeTestXLSX(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create xlsx: %v", err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatalf("create worksheet: %v", err)
	}
	const worksheet = `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>FAST_MODE_XLSX_CELL</t></is></c></row>
</sheetData></worksheet>`
	if _, err := w.Write([]byte(worksheet)); err != nil {
		t.Fatalf("write worksheet: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close xlsx zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close xlsx: %v", err)
	}
}

func TestSandboxFilesHaveSheet(t *testing.T) {
	if sandboxFilesHaveSheet([]ProjectFileSummary{{Name: "a.txt", Kind: "text"}, {Name: "b.png", Kind: "image"}}) {
		t.Fatal("no sheet present, should be false")
	}
	if !sandboxFilesHaveSheet([]ProjectFileSummary{{Name: "a.txt", Kind: "text"}, {Name: "data.xlsx", Kind: "sheet"}}) {
		t.Fatal("a sheet is present, should be true")
	}
	if sandboxFilesHaveSheet(nil) {
		t.Fatal("nil should be false")
	}
}

func TestListSandboxFilesNeverAdvertisesImages(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "sandbox-files.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,name) VALUES('u1','u1@example.com','hash','User')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Test')`,
		`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,storage_path,kind) VALUES('data','u1','c1','data.csv','text/csv','/tmp/data.csv','sheet')`,
		`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,storage_path,kind) VALUES('renamed-image','u1','c1','renamed.csv','image/png','/tmp/renamed.csv','image')`,
		`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,storage_path,kind) VALUES('photo','u1','c1','photo.png','image/png','/tmp/photo.png','image')`,
		`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,storage_path,kind) VALUES('notes','u1','c1','notes.txt','text/plain','/tmp/notes.txt','text')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	legacyPath := filepath.Join(t.TempDir(), "legacy.csv")
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	if err := os.WriteFile(legacyPath, png, 0o600); err != nil {
		t.Fatalf("write legacy image: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind) VALUES('legacy-image','u1','c1','legacy.csv','text/csv',? ,?,'sheet')`,
		len(png), legacyPath,
	); err != nil {
		t.Fatalf("seed legacy image bytes: %v", err)
	}

	files := listSandboxFiles(ctx, db, "c1", "u1")
	want := map[string]string{"data.csv": "sheet", "notes.txt": "text"}
	if len(files) != len(want) {
		t.Fatalf("sandbox prompt files = %+v, want only non-images", files)
	}
	for _, file := range files {
		if want[file.Name] != file.Kind {
			t.Errorf("unexpected sandbox prompt file: %+v", file)
		}
	}
}

func TestShouldInjectSpreadsheetPreviewUsesActualPythonAvailability(t *testing.T) {
	sheetFiles := []ProjectFileSummary{{Name: "data.xlsx", Kind: "sheet"}}
	textFiles := []ProjectFileSummary{{Name: "notes.txt", Kind: "text"}}
	cases := []struct {
		name                   string
		files                  []ProjectFileSummary
		pythonExecuteAvailable bool
		want                   bool
	}{
		{name: "fast mode without python", files: sheetFiles, pythonExecuteAvailable: false, want: true},
		{name: "disable tools", files: sheetFiles, pythonExecuteAvailable: false, want: true},
		{name: "advanced mode with python", files: sheetFiles, pythonExecuteAvailable: true, want: false},
		{name: "no spreadsheet", files: textFiles, pythonExecuteAvailable: false, want: false},
		{name: "no files", files: nil, pythonExecuteAvailable: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInjectSpreadsheetPreview(tc.files, tc.pythonExecuteAvailable); got != tc.want {
				t.Fatalf("shouldInjectSpreadsheetPreview() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFastModeInjectsXLSXPreviewIntoProviderHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "fast-xlsx.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','admin')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	channel, err := store.CreateChannel(ctx, db, "Test", "openai", "chat", "https://example.invalid", "key")
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	model, err := store.CreateModel(ctx, db, store.Model{
		ChannelID: channel.ID,
		Kind:      "chat",
		RequestID: "fast-test",
		Label:     "Fast test",
		Enabled:   true,
		Stream:    true,
		ToolMode:  "native",
	})
	if err != nil {
		t.Fatalf("model: %v", err)
	}
	if err := store.SetFastModel(ctx, db, model.ID); err != nil {
		t.Fatalf("set fast model: %v", err)
	}
	conv, err := store.CreateConversation(ctx, db, store.Conversation{ID: "c1", UserID: "u1", Title: "Spreadsheet test"})
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	xlsxPath := filepath.Join(t.TempDir(), "data.xlsx")
	writeFastModeTestXLSX(t, xlsxPath)
	file, err := store.CreateFile(ctx, db, store.File{
		ID:             "f1",
		UserID:         "u1",
		ConversationID: conv.ID,
		Filename:       "data.xlsx",
		MimeType:       "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Kind:           "sheet",
		StoragePath:    xlsxPath,
	})
	if err != nil {
		t.Fatalf("file: %v", err)
	}

	logger := log.New(io.Discard, "", 0)
	provider := &spreadsheetCaptureProvider{}
	registry := NewRegistry(logger)
	registry.Register(provider)
	orchestrator := NewOrchestrator(db, registry, spreadsheetTestTools{}, nil, nil, nil, nil, nil, logger)
	_, err = orchestrator.Run(ctx, RunRequest{
		UserID:         "u1",
		ConversationID: conv.ID,
		UserText:       "Analyze this spreadsheet",
		Fast:           true,
		Attachments: []Attachment{{
			ID: file.ID, Filename: file.Filename, MimeType: file.MimeType, Kind: file.Kind,
		}},
	}, func(SseEvent) {})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var historyText strings.Builder
	for _, message := range provider.request.History {
		for _, block := range message.Blocks {
			if block.Kind == "text" {
				historyText.WriteString(block.Text)
				historyText.WriteByte('\n')
			}
		}
	}
	got := historyText.String()
	for _, want := range []string{"<uploaded-data-preview>", "data.xlsx", "FAST_MODE_XLSX_CELL"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fast provider history missing %q:\n%s", want, got)
		}
	}
	webSearchAvailable := false
	for _, tool := range provider.request.Tools {
		if tool.Name == "python_execute" {
			t.Fatal("fast provider request must not expose python_execute")
		}
		if tool.Name == "web_search" {
			webSearchAvailable = true
		}
	}
	if !webSearchAvailable {
		t.Fatal("fast provider request should retain non-Python tools")
	}
}

// A turn without python_execute (including fast and no-tools modes) parses a
// staged spreadsheet IN-PROCESS and injects a bounded
// <uploaded-data-preview> block. Non-sheet files are ignored.
func TestPreviewSpreadsheetFilesInjectsParsedCSV(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "preview.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`); err != nil {
		t.Fatalf("conv: %v", err)
	}

	csvPath := filepath.Join(t.TempDir(), "sales.csv")
	if err := os.WriteFile(csvPath, []byte("region,units\nEast,10\nWest,20\n"), 0o644); err != nil {
		t.Fatalf("csv: %v", err)
	}
	txtPath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(txtPath, []byte("ignore me"), 0o644); err != nil {
		t.Fatalf("txt: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f1','u1','c1','sales.csv','text/csv',30,'sheet',?)`, csvPath); err != nil {
		t.Fatalf("file sheet: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f2','u1','c1','notes.txt','text/plain',9,'text',?)`, txtPath); err != nil {
		t.Fatalf("file text: %v", err)
	}

	o := &Orchestrator{db: db}
	out := o.previewSpreadsheetFiles(context.Background(), "u1", "c1")
	if !strings.Contains(out, "<uploaded-data-preview>") || !strings.Contains(out, "</uploaded-data-preview>") {
		t.Fatalf("missing wrapper block:\n%s", out)
	}
	for _, want := range []string{"sales.csv", "3 rows × 2 cols", "region", "East", "20"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in preview:\n%s", want, out)
		}
	}
	// The plain-text file must NOT be pulled in here (it's RAG-injected elsewhere).
	if strings.Contains(out, "ignore me") || strings.Contains(out, "notes.txt") {
		t.Fatalf("non-sheet file leaked into the sheet preview:\n%s", out)
	}
}

// No spreadsheet files → nothing injected.
func TestPreviewSpreadsheetFilesEmptyWhenNoSheets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`); err != nil {
		t.Fatalf("conv: %v", err)
	}
	o := &Orchestrator{db: db}
	if out := o.previewSpreadsheetFiles(context.Background(), "u1", "c1"); out != "" {
		t.Fatalf("expected empty injection, got %q", out)
	}
}

// The preview is capped so a huge sheet can't blow the context budget.
func TestPreviewSpreadsheetFilesCapsHugeSheet(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cap.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`); err != nil {
		t.Fatalf("conv: %v", err)
	}
	// Wide sheet: 40 columns × 40 rows of 30-char cells. Per-cell truncation (80
	// runes) keeps each cell intact, so the formatted preview (~40×31×31 chars)
	// comfortably exceeds the 8000-rune injection cap and must be truncated.
	row := strings.TrimSuffix(strings.Repeat(strings.Repeat("v", 30)+",", 40), ",") + "\n"
	var b strings.Builder
	for i := 0; i < 41; i++ {
		b.WriteString(row)
	}
	csvPath := filepath.Join(t.TempDir(), "big.csv")
	if err := os.WriteFile(csvPath, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("csv: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f1','u1','c1','big.csv','text/csv',20000,'sheet',?)`, csvPath); err != nil {
		t.Fatalf("file: %v", err)
	}
	o := &Orchestrator{db: db}
	out := o.previewSpreadsheetFiles(context.Background(), "u1", "c1")
	if !strings.Contains(out, "…(truncated)") {
		t.Fatalf("oversized preview should be truncated")
	}
	if len([]rune(out)) > spreadsheetPreviewInjectionCap+80 {
		t.Fatalf("preview not capped: %d runes", len([]rune(out)))
	}
}
