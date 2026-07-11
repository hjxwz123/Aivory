package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/llm"
)

func TestEnsureAttachedDocumentsReadyRequiresReadyStatus(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "attached-docs.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c2','u1','Other')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d_pending','c1','p.pdf','application/pdf',10,'embedding')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d_ready','c1','r.pdf','application/pdf',10,'ready')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status) VALUES('d_other','c2','o.pdf','application/pdf',10,'ready')`)

	if err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{DocumentID: "d_ready"}}); err != nil {
		t.Fatalf("ready document rejected: %v", err)
	}
	err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{DocumentID: "d_pending"}})
	if err == nil || !strings.Contains(err.Error(), "still indexing") {
		t.Fatalf("pending document error = %v, want still indexing", err)
	}
	err = ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{DocumentID: "d_other"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("foreign document error = %v, want not found", err)
	}
}

func TestEnsureAttachedDocumentsReadyFallsBackToFileID(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "attached-files.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f_ready','u1','c1','r.pdf','application/pdf',10,'pdf','/tmp/r.pdf')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path) VALUES('f_pending','u1','c1','p.pdf','application/pdf',10,'pdf','/tmp/p.pdf')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_ready','c1','r.pdf','application/pdf',10,'ready','/tmp/r.pdf')`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_pending','c1','p.pdf','application/pdf',10,'embedding','/tmp/p.pdf')`)

	if err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_ready", Kind: "pdf"}}); err != nil {
		t.Fatalf("ready file-backed document rejected: %v", err)
	}
	err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_pending", Kind: "pdf"}})
	if err == nil || !strings.Contains(err.Error(), "still indexing") {
		t.Fatalf("pending file-backed document error = %v, want still indexing", err)
	}
	err = ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_missing", Kind: "pdf"}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing file-backed document error = %v, want not found", err)
	}
}

func TestEnsureAttachedDocumentsReadyRejectsOmittedServerDraft(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "draft-preflight.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,kind,storage_path,draft) VALUES('f_draft','u1','c1','ready.pdf','application/pdf',10,'pdf','/tmp/ready.pdf',1)`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_ready','c1','ready.pdf','application/pdf',10,'ready','/tmp/ready.pdf')`)

	err := ensureAttachedDocumentsReady(ctx, db, "c1", nil)
	if err == nil || !strings.Contains(err.Error(), "unsent attachments") {
		t.Fatalf("omitted draft error = %v, want unsent attachments", err)
	}
	if err := ensureAttachedDocumentsReady(ctx, db, "c1", []llm.Attachment{{ID: "f_draft", Kind: "pdf"}}); err != nil {
		t.Fatalf("included ready draft rejected: %v", err)
	}
}
