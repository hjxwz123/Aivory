package api

import (
	"context"
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
