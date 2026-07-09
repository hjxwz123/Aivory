package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteUserRemovesOwnedFilesDocumentsAndStorage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "users-delete.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	uploadPath := filepath.Join(root, "upload.txt")
	kbPath := filepath.Join(root, "kb.txt")
	if err := os.WriteFile(uploadPath, []byte("upload"), 0o600); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := os.WriteFile(kbPath, []byte("kb"), 0o600); err != nil {
		t.Fatalf("write kb: %v", err)
	}

	for _, q := range []string{
		`INSERT INTO users(id,email,password_hash,role) VALUES('u1','u1@x.test','h','user')`,
		`INSERT INTO users(id,email,password_hash,role) VALUES('u2','u2@x.test','h','user')`,
		`INSERT INTO channels(id,name,type) VALUES('ch1','c','openai')`,
		`INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('emb1','ch1','embedding','embed','Embed')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','own')`,
		`INSERT INTO conversations(id,user_id,title) VALUES('c2','u2','shared-owner')`,
		`INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','kb','emb1',3)`,
	} {
		exec(t, db, q)
	}
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f1','u1','c2','upload.txt',?)`, uploadPath)
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_file','c2','upload.txt','text/plain',6,'ready',?)`, uploadPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,conversation_id,seq,content,embedding_model) VALUES('chunk_file','d_file','c2',0,'hello','emb:test')`)
	exec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path) VALUES('d_kb','kb1','kb.txt','text/plain',2,'ready',?)`, kbPath)
	exec(t, db, `INSERT INTO chunks(id,document_id,kb_id,seq,content,embedding_model) VALUES('chunk_kb','d_kb','kb1',0,'kb','emb:test')`)

	plan, err := BuildUserCleanupPlan(ctx, db, "u1")
	if err != nil {
		t.Fatalf("BuildUserCleanupPlan: %v", err)
	}
	if !has(plan.ConversationIDs, "c1") || !has(plan.KBIDs, "kb1") || !has(plan.DocumentIDs, "d_file") {
		t.Fatalf("cleanup plan missed side-state ids: %+v", plan)
	}
	if !has(plan.StoragePaths, uploadPath) || !has(plan.StoragePaths, kbPath) {
		t.Fatalf("cleanup plan missed storage paths: %+v", plan.StoragePaths)
	}

	if err := DeleteUser(ctx, db, "u1"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	assertMissing(t, db, `SELECT id FROM files WHERE id='f1'`)
	assertMissing(t, db, `SELECT id FROM documents WHERE id='d_file'`)
	assertMissing(t, db, `SELECT id FROM documents WHERE id='d_kb'`)
	assertMissing(t, db, `SELECT id FROM chunks WHERE id='chunk_file'`)
	if !convExists(t, db, "c2") {
		t.Fatalf("other user's conversation should survive")
	}
	if _, err := os.Stat(uploadPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upload storage should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(kbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("kb storage should be removed, stat err=%v", err)
	}
}

func has(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func assertMissing(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	var id string
	err := db.QueryRowContext(context.Background(), q).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected missing row for %q, got id=%q err=%v", q, id, err)
	}
}
