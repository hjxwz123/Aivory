package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

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

// A no-tools turn with a staged spreadsheet parses it IN-PROCESS (no sandbox, no
// python_execute) and injects a bounded <uploaded-data-preview> block. Non-sheet
// files are ignored.
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
