package rag

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func openGrepTestDB(t *testing.T) *sql.DB {
	t.Helper()
	store.InvalidateConfig()
	t.Cleanup(store.InvalidateConfig)
	db, err := store.Open(filepath.Join(t.TempDir(), "grep-test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestGrepIngestSkipsChunkingAndEmbeddingWithoutEmbeddingModel(t *testing.T) {
	ctx := context.Background()
	db := openGrepTestDB(t)
	if err := store.SetSetting(db, store.DocumentRetrievalModeSettingKey, store.DocumentRetrievalModeGrep); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	docPath := filepath.Join(dir, "spec.md")
	content := "# Overview\n\nError code ERR_ALPHA_42 occurs during handshake.\n\n## Details\n\nRetry after 5 seconds.\n"
	if err := os.WriteFile(docPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Grep KB','',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d1','kb1','spec.md','text/markdown',?,?,?)`, len(content), "pending", docPath); err != nil {
		t.Fatal(err)
	}

	svc := New(db, nil, nil, dir)
	if err := svc.runPipeline(ctx, "d1", nil); err != nil {
		t.Fatalf("runPipeline: %v", err)
	}

	doc, err := store.GetDocument(ctx, db, "d1")
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc.Status != "ready" || doc.ChunkCount != 0 {
		t.Fatalf("doc status=%q chunks=%d, want ready with 0 chunks", doc.Status, doc.ChunkCount)
	}

	var chunkRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE document_id='d1'`).Scan(&chunkRows); err != nil || chunkRows != 0 {
		t.Fatalf("chunks count=%d err=%v, want 0", chunkRows, err)
	}

	dt, err := store.GetDocumentText(ctx, db, "d1")
	if err != nil {
		t.Fatalf("GetDocumentText: %v", err)
	}
	if !strings.Contains(dt.Content, "ERR_ALPHA_42") || dt.LineCount < 5 {
		t.Fatalf("unexpected document_text: %+v", dt)
	}
	var headings []store.DocumentHeading
	if err := json.Unmarshal([]byte(dt.OutlineJSON), &headings); err != nil || len(headings) != 2 {
		t.Fatalf("headings=%v err=%v, want 2 headings", headings, err)
	}
}

func TestGrepScopeThresholdsSearchReadAndFallback(t *testing.T) {
	ctx := context.Background()
	db := openGrepTestDB(t)
	if err := store.SetSetting(db, store.DocumentRetrievalModeSettingKey, store.DocumentRetrievalModeGrep); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Grep chat')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Manual','',0)`,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d-small','c1','note.txt','text/plain',24,'ready')`,
		`INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d-code-big','c1','big.go','text/plain',4000,'ready')`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status) VALUES('d-kb','kb1','guide.md','text/markdown',500,'ready')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	if err := store.UpsertDocumentText(ctx, db, store.DocumentText{
		DocumentID: "d-small", ConversationID: "c1",
		Content: "Small pinned note.", LineCount: 1, CharCount: 18, TokenEstimate: 10,
	}); err != nil {
		t.Fatal(err)
	}

	// Build a 220-line Go file (> default 200 code lines) so it stays unpinned.
	var bigCode strings.Builder
	for i := 1; i <= 220; i++ {
		if i == 150 {
			bigCode.WriteString("func HandleNeedle99() error { return nil }\n")
		} else {
			bigCode.WriteString("var filler = 1\n")
		}
	}
	if err := store.UpsertDocumentText(ctx, db, store.DocumentText{
		DocumentID: "d-code-big", ConversationID: "c1",
		Content: bigCode.String(), LineCount: 220, CharCount: bigCode.Len(), TokenEstimate: 900,
	}); err != nil {
		t.Fatal(err)
	}

	kbText := "# Architecture\nLine 2\nErrorCode: E_TIMEOUT_777\nLine 4\nLine 5\n"
	if err := store.UpsertDocumentText(ctx, db, store.DocumentText{
		DocumentID: "d-kb", KBID: "kb1",
		Content: kbText, LineCount: 5, CharCount: len(kbText), TokenEstimate: 25,
	}); err != nil {
		t.Fatal(err)
	}

	svc := New(db, nil, nil)
	scope, err := svc.LoadGrepScope(ctx, "c1", []string{"kb1"}, []string{"d-small", "d-code-big"}, []string{"d-small"})
	if err != nil {
		t.Fatalf("LoadGrepScope: %v", err)
	}
	if len(scope.PinnedSnippets) != 1 || scope.PinnedSnippets[0].Title != "note.txt" {
		t.Fatalf("pinned=%+v, want only d-small", scope.PinnedSnippets)
	}
	if !scope.HasUnpinned || len(scope.Documents) != 3 {
		t.Fatalf("documents=%d hasUnpinned=%v, want 3 and true", len(scope.Documents), scope.HasUnpinned)
	}

	// Literal search across scope
	res, snips, err := SearchGrep(scope, GrepSearchRequest{Pattern: "E_TIMEOUT_777"})
	if err != nil || res.TotalMatches != 1 || len(snips) != 1 {
		t.Fatalf("SearchGrep literal res=%+v snips=%d err=%v", res, len(snips), err)
	}
	if snips[0].URL != "kbdoc://d-kb" || !strings.Contains(res.Formatted, "L3:") {
		t.Fatalf("unexpected snippet/formatted: %+v %q", snips[0], res.Formatted)
	}

	// Regex search targeting d-code-big
	res, snips, err = SearchGrep(scope, GrepSearchRequest{
		Pattern:     `HandleNeedle\d+`,
		IsRegex:     true,
		DocumentIDs: []string{"d-code-big"},
	})
	if err != nil || res.TotalMatches != 1 || len(snips) != 1 || snips[0].URL != "doc://d-code-big" {
		t.Fatalf("SearchGrep regex res=%+v snips=%+v err=%v", res, snips, err)
	}

	// Out-of-scope document ID must be rejected
	if _, _, err := SearchGrep(scope, GrepSearchRequest{Pattern: "anything", DocumentIDs: []string{"other-doc"}}); err == nil {
		t.Fatal("expected error for unauthorized document_id")
	}

	// Line-range read
	readRes, readSnip, err := ReadGrepLines(scope, "d-code-big", 149, 3)
	if err != nil || readSnip == nil || readRes.StartLine != 149 || readRes.EndLine != 151 || !readRes.HasMore {
		t.Fatalf("ReadGrepLines res=%+v snip=%+v err=%v", readRes, readSnip, err)
	}
	if !strings.Contains(readRes.Formatted, "L150: func HandleNeedle99()") {
		t.Fatalf("unexpected ReadGrepLines output: %q", readRes.Formatted)
	}

	// Server fallback grep when tools are off
	fb := FallbackGrep(scope, "What causes E_TIMEOUT_777?")
	if len(fb) == 0 || !strings.Contains(fb[0].Snippet, "E_TIMEOUT_777") {
		t.Fatalf("FallbackGrep=%+v, want E_TIMEOUT_777 hit", fb)
	}

	// Threshold 0 disables small-doc pinning
	if err := store.SetSetting(db, "grep_full_text_threshold", 0); err != nil {
		t.Fatal(err)
	}
	scopeZero, err := svc.LoadGrepScope(ctx, "c1", nil, []string{"d-small"}, []string{"d-small"})
	if err != nil || len(scopeZero.PinnedSnippets) != 0 || !scopeZero.HasUnpinned {
		t.Fatalf("zero threshold pinned=%d hasUnpinned=%v err=%v", len(scopeZero.PinnedSnippets), scopeZero.HasUnpinned, err)
	}
}

func TestGrepReconstructsLegacyChunksAndSupportsSwitchingBackToRAG(t *testing.T) {
	ctx := context.Background()
	db := openGrepTestDB(t)

	for _, stmt := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@example.test','h','user')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Mixed','',0)`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,chunk_count) VALUES('d-legacy','kb1','legacy.txt','text/plain',100,'ready',2)`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content) VALUES('c1','d-legacy','kb1',0,'text','First paragraph')`,
		`INSERT INTO chunks(id,document_id,kb_id,seq,chunk_type,content) VALUES('c2','d-legacy','kb1',1,'text','First paragraph` + "\n\n" + `Second paragraph')`,
		`INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,chunk_count) VALUES('d-grep','kb1','grep.txt','text/plain',100,'ready',0)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	if err := store.UpsertDocumentText(ctx, db, store.DocumentText{
		DocumentID: "d-grep", KBID: "kb1",
		Content: "Grep only keyword TOKEN_ZEBRA", LineCount: 1, CharCount: 29, TokenEstimate: 8,
	}); err != nil {
		t.Fatal(err)
	}

	svc := New(db, nil, nil)
	scope, err := svc.LoadGrepScope(ctx, "", []string{"kb1"}, nil, nil)
	if err != nil {
		t.Fatalf("LoadGrepScope: %v", err)
	}
	if doc := scope.ByID["d-legacy"]; doc == nil || strings.Count(doc.Content, "First paragraph") != 1 || !strings.Contains(doc.Content, "Second paragraph") {
		t.Fatalf("legacy chunk reconstruction failed: %+v", doc)
	}

	// Switch back to RAG (vector disabled -> relational fallback) and verify d-grep is still searchable.
	chunks, err := svc.listChunksInScopeWithFallback(ctx, []string{"kb1"}, "")
	if err != nil {
		t.Fatalf("listChunksInScopeWithFallback: %v", err)
	}
	foundGrepChunk := false
	for _, c := range chunks {
		if c.DocumentID == "d-grep" && strings.Contains(c.Content, "TOKEN_ZEBRA") {
			foundGrepChunk = true
			break
		}
	}
	if !foundGrepChunk {
		t.Fatalf("expected synthesized chunk for d-grep when switching back to RAG, got %+v", chunks)
	}
}
