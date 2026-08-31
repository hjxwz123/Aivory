package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/llm"
	"aivory/server/internal/queue"
	"aivory/server/internal/rag"
	"aivory/server/internal/store"
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

func TestListConversationFilesFollowsActiveBranch(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "branch-files.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','u@example.test','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	for _, file := range []store.File{
		{ID: "parent-file", UserID: "u1", ConversationID: "c1", Filename: "parent.txt", StoragePath: "/tmp/parent.txt", BranchMessageID: "root"},
		{ID: "branch-1-file", UserID: "u1", ConversationID: "c1", Filename: "one.txt", StoragePath: "/tmp/one.txt", BranchMessageID: "branch-1"},
		{ID: "branch-2-file", UserID: "u1", ConversationID: "c1", Filename: "two.txt", StoragePath: "/tmp/two.txt", BranchMessageID: "branch-2"},
	} {
		if _, err := store.CreateFile(ctx, db, file); err != nil {
			t.Fatalf("create file %s: %v", file.ID, err)
		}
	}
	for _, message := range []store.Message{
		{ID: "root", ConversationID: "c1", Role: "user", AuthorID: "u1", Blocks: json.RawMessage(`[]`)},
		{ID: "branch-1", ConversationID: "c1", ParentID: "root", Role: "user", AuthorID: "u1", Blocks: json.RawMessage(`[]`)},
		{ID: "branch-2", ConversationID: "c1", ParentID: "root", Role: "user", AuthorID: "u1", Blocks: json.RawMessage(`[]`)},
	} {
		if _, err := store.CreateMessage(ctx, db, message); err != nil {
			t.Fatalf("create message %s: %v", message.ID, err)
		}
	}

	list := func(want ...string) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/conversations/c1/files", nil)
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
		got := make(map[string]bool, len(rows))
		for _, row := range rows {
			got[row.ID] = true
		}
		if len(got) != len(want) {
			t.Fatalf("files=%v; want %v", got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("files=%v; missing %s", got, id)
			}
		}
	}

	list("parent-file", "branch-2-file")
	if _, err := normalizeConversationBranchAttachments(ctx, db, "c1", "u1", "branch-2", []llm.Attachment{{ID: "branch-1-file"}}); !errors.Is(err, errAttachmentUnavailable) {
		t.Fatalf("sibling attachment normalization err=%v; want unavailable", err)
	}
	if _, err := store.CreateFile(ctx, db, store.File{
		ID: "branch-1-draft", UserID: "u1", ConversationID: "c1", Filename: "draft.txt",
		StoragePath: "/tmp/draft.txt", BranchMessageID: "branch-1", Draft: true,
	}); err != nil {
		t.Fatalf("create sibling draft: %v", err)
	}
	if err := ensureAttachedDocumentsReadyForUserBranch(ctx, db, "c1", "u1", "branch-2", nil); err != nil {
		t.Fatalf("sibling draft blocked branch 2: %v", err)
	}
	if err := ensureAttachedDocumentsReadyForUserBranch(ctx, db, "c1", "u1", "branch-1", nil); err == nil || !strings.Contains(err.Error(), "unsent attachments") {
		t.Fatalf("current-branch draft readiness err=%v; want unsent attachments", err)
	}
	mustExec(t, db, `UPDATE conversations SET active_leaf_id='branch-1' WHERE id='c1'`)
	list("parent-file", "branch-1-file", "branch-1-draft")
}
