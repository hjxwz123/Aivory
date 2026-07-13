package rag

import (
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

// §4.11-B3 line gate: code/config/txt/unknown-format conversation docs are
// injected in full (pinned, no vectors) at/below the admin line cap and
// chunked+embedded above it. Prose keeps the token threshold.

func TestIsLineGatedText(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		// code / config → line-gated
		{"main.go", true},
		{"app.py", true},
		{"config.yaml", true},
		{"data.xml", true},
		{"cpu.v", true},
		{"settings.ini", true},
		// txt moved from prose to the line gate (admin decision)
		{"notes.txt", true},
		// unknown / niche formats → line-gated
		{"diagram.drawio", true},
		{"weird.foobar", true},
		// prose → token-gated
		{"README.md", false},
		{"guide.markdown", false},
		{"server.log", false},
		{"page.html", false},
		// prose-markup / subtitle formats stay token-gated (rtf is in the
		// default upload allowlist; all read as few very long lines)
		{"report.rtf", false},
		{"paper.tex", false},
		{"notes.org", false},
		{"movie.srt", false},
		// binary-parsed docs → token-gated (content arrives as extracted prose)
		{"report.pdf", false},
		{"slides.pptx", false},
		{"doc.docx", false},
	}
	for _, c := range cases {
		if got := isLineGatedText(c.name); got != c.want {
			t.Errorf("isLineGatedText(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCountLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"one", 1},
		{"one\n", 1},
		{"one\ntwo", 2},
		{"one\ntwo\nthree\n", 3},
		{"\n\n", 0}, // only blank trailing newlines
	}
	for _, c := range cases {
		if got := countLines(c.in); got != c.want {
			t.Errorf("countLines(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// lineGateHarness seeds a user+conversation-scoped document with the given
// filename/content, runs the ingest pipeline and returns the child chunks.
func lineGateHarness(t *testing.T, db *sql.DB, filename, content string) []store.Chunk {
	t.Helper()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
	doc, err := store.CreateDocument(ctx, db, store.Document{
		ConversationID: "c1",
		Filename:       filename,
		MimeType:       "text/plain",
		SizeBytes:      int64(len(content)),
		StoragePath:    p,
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	svc := New(db, nil, log.New(io.Discard, "", 0))
	if err := svc.runPipeline(ctx, doc.ID, nil); err != nil {
		t.Fatalf("runPipeline(%s): %v", filename, err)
	}
	all, err := store.ListChunksInScope(ctx, db, nil, "c1")
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	out := []store.Chunk{}
	for _, c := range all {
		if c.DocumentID == doc.ID && c.ChunkType != "parent" {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no chunks ingested for %s", filename)
	}
	return out
}

func assertPinned(t *testing.T, filename string, chunks []store.Chunk, wantPinned bool) {
	t.Helper()
	for _, c := range chunks {
		pinned := strings.TrimSpace(c.EmbeddingModel) == ""
		if pinned != wantPinned {
			t.Fatalf("%s: chunk %s pinned=%v, want %v (EmbeddingModel=%q)",
				filename, c.ID, pinned, wantPinned, c.EmbeddingModel)
		}
	}
}

func TestLineGateFullInjectVsEmbed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "line-gate.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,name,role) VALUES('u1','a@b.c','h','A','user')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}

	code := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n" // 5 lines

	// Default cap (2000): a small code file stays pinned (full inject, no vectors).
	assertPinned(t, "small.go", lineGateHarness(t, db, "small.go", code), true)

	// Unknown format (.drawio) follows the same line gate.
	assertPinned(t, "d.drawio", lineGateHarness(t, db, "d.drawio", "<mxfile>\n<diagram>x</diagram>\n</mxfile>\n"), true)

	// Line-count gaming guard: a single-line minified dump is "1 line" but its
	// token-equivalent (cap × 20 tokens/line, ~40k under defaults) must also
	// fit — 200k chars (~50k est. tokens) on one line embeds instead of pinning.
	assertPinned(t, "bundle.min.js", lineGateHarness(t, db, "bundle.min.js", strings.Repeat("x", 200_000)), false)

	// Cap lowered below the file's line count → the SAME kind of file now embeds,
	// even though its token count is far below the prose threshold (proves the
	// line gate, not the token gate, decides for code).
	if err := store.SetSetting(db, "rag_code_full_text_max_lines", 2); err != nil {
		t.Fatalf("set line cap: %v", err)
	}
	assertPinned(t, "big.go", lineGateHarness(t, db, "big.go", code), false)

	// txt is line-gated too now: 3 short lines (~10 tokens ≪ 8000) but > cap → embeds.
	assertPinned(t, "notes.txt", lineGateHarness(t, db, "notes.txt", "a\nb\nc\n"), false)

	// Prose is untouched by the line cap: a small .md still pins under the token
	// threshold even with the tiny line cap in force.
	assertPinned(t, "readme.md", lineGateHarness(t, db, "readme.md", "hello\nworld\nagain\n"), true)
}
