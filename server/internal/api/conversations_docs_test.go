package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aurelia/server/internal/queue"
	"aurelia/server/internal/rag"
	"aurelia/server/internal/store"
)

type recordingQueue struct {
	names []string
}

func (q *recordingQueue) Enqueue(name string, _ queue.Job) {
	q.names = append(q.names, name)
}

func (q *recordingQueue) Close() {}

func TestRetryConversationDocumentRequeuesFailedDoc(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "retry-doc.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	doc, err := store.CreateDocument(ctx, db, store.Document{
		ConversationID: "c1",
		Filename:       "scan.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      10,
		Status:         "failed",
		Error:          "could not extract text",
		StoragePath:    filepath.Join(t.TempDir(), "scan.pdf"),
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	q := &recordingQueue{}
	req := httptest.NewRequest("POST", "/api/conversations/c1/documents/"+doc.ID+"/retry", nil)
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1", "docId": doc.ID}))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()

	retryConversationDocumentHandler(Deps{
		DB:  db,
		RAG: rag.New(db, q, log.New(io.Discard, "", 0)),
	}, rec, req)
	if rec.Code != 200 {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.GetDocument(ctx, db, doc.ID)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if got.Status != "pending" || got.Error != "" || got.ChunkCount != 0 {
		t.Fatalf("document after retry = status=%q err=%q chunks=%d, want pending clean", got.Status, got.Error, got.ChunkCount)
	}
	if len(q.names) != 1 || q.names[0] != "rag.ingest" {
		t.Fatalf("queued jobs = %#v, want one rag.ingest", q.names)
	}
}

func TestListConversationDraftFilesIncludesDocumentStatus(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "draft-files.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	if _, err := store.CreateFile(ctx, db, store.File{
		ID: "f_draft", UserID: "u1", ConversationID: "c1", Filename: "scan.pdf",
		MimeType: "application/pdf", Kind: "pdf", SizeBytes: 42, StoragePath: "/tmp/scan.pdf", Draft: true,
	}); err != nil {
		t.Fatalf("create draft file: %v", err)
	}
	if _, err := store.CreateFile(ctx, db, store.File{
		ID: "f_committed", UserID: "u1", ConversationID: "c1", Filename: "old.txt",
		MimeType: "text/plain", Kind: "text", SizeBytes: 3, StoragePath: "/tmp/old.txt",
	}); err != nil {
		t.Fatalf("create committed file: %v", err)
	}
	if _, err := store.CreateDocument(ctx, db, store.Document{
		ID: "d_scan", ConversationID: "c1", Filename: "scan.pdf", MimeType: "application/pdf",
		SizeBytes: 42, Status: "embedding", StoragePath: "/tmp/scan.pdf",
	}); err != nil {
		t.Fatalf("create document: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/conversations/c1/files?draft=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "c1"}))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()
	listConversationFilesHandler(Deps{DB: db}, rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rows []convFile
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "f_draft" {
		t.Fatalf("rows = %+v, want only f_draft", rows)
	}
	if !rows[0].Draft || rows[0].DocumentID != "d_scan" || rows[0].DocumentStatus != "embedding" {
		t.Fatalf("draft status row = %+v", rows[0])
	}
}
