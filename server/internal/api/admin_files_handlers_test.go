package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminFilesListFilterAndDedupe(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-files.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u1','alex@example.test','Alex','h','admin')`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u2','mia@example.test','Mia','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u2','T')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','C','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m1','ch1','embedding','e','E')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','Handbook','m1',0)`)

	// Conversation upload: files row + documents twin on the same path.
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f1','u2','c1','notes.pdf','application/pdf',100,'/up/f1.pdf','file',100)`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d1','c1','notes.pdf','application/pdf',100,'ready','/up/f1.pdf',100)`)
	// KB document on its own path.
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d2','kb1','spec.md','text/markdown',50,'ready','/up/d2.md',200)`)

	list := func(query string) map[string]any {
		req := httptest.NewRequest("GET", "/api/admin/files"+query, nil)
		rec := httptest.NewRecorder()
		listFilesAdmin(Deps{DB: db}, rec, req)
		if rec.Code != 200 {
			t.Fatalf("list %q status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	// Dedupe: the documents twin d1 must fold into f1 — 2 rows total, not 3.
	out := list("")
	if int(out["total"].(float64)) != 2 {
		t.Fatalf("total=%v want 2 (twin document must be folded)", out["total"])
	}
	rows := out["files"].([]any)
	first := rows[0].(map[string]any)
	if first["id"] != "d2" || first["origin"] != "kb" || first["user_email"] != "alex@example.test" || first["kb_name"] != "Handbook" {
		t.Fatalf("first row = %+v; want kb doc d2 owned by alex", first)
	}

	// Origin filter.
	if got := int(list("?origin=kb")["total"].(float64)); got != 1 {
		t.Fatalf("origin=kb total=%d want 1", got)
	}
	// Owner filter: the KB doc belongs to u1 via the KB join.
	if got := int(list("?user_id=u1")["total"].(float64)); got != 1 {
		t.Fatalf("user_id=u1 total=%d want 1", got)
	}
	// Filename search, case-insensitive.
	if got := int(list("?search=NOTES")["total"].(float64)); got != 1 {
		t.Fatalf("search=NOTES total=%d want 1", got)
	}
	// Type filtering is server-side so Count and pagination use the same result.
	if got := int(list("?type=pdf")["total"].(float64)); got != 1 {
		t.Fatalf("type=pdf total=%d want 1", got)
	}
	// Unknown values are ignored rather than becoming dynamic SQL or an empty list.
	if got := int(list("?type=unknown")["total"].(float64)); got != 2 {
		t.Fatalf("type=unknown total=%d want unfiltered total 2", got)
	}
	// Size sort ascending puts the 50-byte doc first.
	rows = list("?sort=size_bytes&order=asc")["files"].([]any)
	if rows[0].(map[string]any)["id"] != "d2" {
		t.Fatalf("size asc first=%v want d2", rows[0].(map[string]any)["id"])
	}
}

func TestAdminFilesBatchDeleteRemovesRowsAndBytes(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-files-del.db"))
	defer db.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "f1.pdf")
	docPath := filepath.Join(dir, "d2.md")
	writeFile(t, filePath, []byte("pdf-bytes"))
	writeFile(t, docPath, []byte("md-bytes"))

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u2','mia@example.test','Mia','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u2','T')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','C','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m1','ch1','embedding','e','E')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u2','KB','m1',0)`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f1','u2','c1','notes.pdf','application/pdf',100,?,'file',100)`, filePath)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d1','c1','notes.pdf','application/pdf',100,'ready',?,100)`, filePath)
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d2','kb1','spec.md','text/markdown',50,'ready',?,200)`, docPath)
	mustExec(t, db, `INSERT INTO chunks(id,document_id,seq,content) VALUES('ch1','d2',0,'hello')`)

	body := strings.NewReader(`{"items":[{"source":"file","id":"f1"},{"source":"document","id":"d2"},{"source":"file","id":"missing"}]}`)
	req := httptest.NewRequest("POST", "/api/admin/files/delete", body)
	rec := httptest.NewRecorder()
	deleteFilesAdmin(Deps{DB: db}, rec, req)
	if rec.Code != 200 {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Deleted != 2 {
		t.Fatalf("deleted=%d want 2 (missing id skipped, not fatal)", out.Deleted)
	}

	// DB rows gone: the files row, its documents twin, the KB doc, its chunks.
	for _, q := range []string{
		`SELECT COUNT(*) FROM files`,
		`SELECT COUNT(*) FROM documents`,
		`SELECT COUNT(*) FROM chunks`,
	} {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("%s = %d, want 0", q, n)
		}
	}
	// Physical bytes gone.
	for _, p := range []string{filePath, docPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists on disk", p)
		}
	}
}
