package api

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aivory/server/internal/rag"
	"aivory/server/internal/store"
)

func TestRetryKBDocumentRequeuesFailedDoc(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "retry-kb-doc.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','C','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m1','ch1','embedding','e','E')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','KB','m1',3)`)
	doc, err := store.CreateDocument(ctx, db, store.Document{
		KBID: "kb1", Filename: "table.csv", MimeType: "text/csv", SizeBytes: 10,
		Status: "failed", Error: "embedding unavailable", ChunkCount: 4,
		StoragePath: filepath.Join(t.TempDir(), "table.csv"),
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	q := &recordingQueue{}
	req := httptest.NewRequest(http.MethodPost, "/api/kbs/kb1/documents/"+doc.ID+"/retry", nil)
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "kb1", "docId": doc.ID}))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()

	retryKBDocHandler(Deps{DB: db, RAG: rag.New(db, q, log.New(io.Discard, "", 0))}, rec, req)
	if rec.Code != http.StatusOK {
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

func TestRetryKBDocumentRejectsNonFailedDoc(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "retry-ready-kb-doc.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','C','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m1','ch1','embedding','e','E')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','KB','m1',3)`)
	doc, err := store.CreateDocument(ctx, db, store.Document{
		KBID: "kb1", Filename: "ready.txt", MimeType: "text/plain", Status: "ready",
		StoragePath: filepath.Join(t.TempDir(), "ready.txt"),
	})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}

	q := &recordingQueue{}
	req := httptest.NewRequest(http.MethodPost, "/api/kbs/kb1/documents/"+doc.ID+"/retry", nil)
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "kb1", "docId": doc.ID}))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()

	retryKBDocHandler(Deps{DB: db, RAG: rag.New(db, q, log.New(io.Discard, "", 0))}, rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if len(q.names) != 0 {
		t.Fatalf("queued jobs = %#v, want none", q.names)
	}
}
