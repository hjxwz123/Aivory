package rag

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aurelia/server/internal/store"
)

func TestRunPipelineFailsConversationDocWhenTextExtractionFails(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "parse-failed.db"))
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
	p := filepath.Join(t.TempDir(), "scan.pdf")
	if err := os.WriteFile(p, []byte("%PDF scanned placeholder without a text layer"), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	doc, err := store.CreateDocument(ctx, db, store.Document{
		ConversationID: "c1",
		Filename:       "scan.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      42,
		StoragePath:    p,
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	svc := New(db, nil, log.New(io.Discard, "", 0))
	if err := svc.runPipeline(ctx, doc.ID, nil); err != nil {
		t.Fatalf("runPipeline: %v", err)
	}

	got, err := store.GetDocument(ctx, db, doc.ID)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "could not extract text") && !strings.Contains(got.Error, "MinerU") {
		t.Fatalf("failure reason should explain extraction failure, got %q", got.Error)
	}
	var chunks int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE document_id=?`, doc.ID).Scan(&chunks); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunks != 0 {
		t.Fatalf("chunks = %d, want 0; failed extraction must not store placeholder chunks", chunks)
	}
}
