package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aivory/server/internal/store"
)

func TestUserStorageUsageExcludesImagesAndTwins(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "usage.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u1','a@x.test','A','h','user')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','C','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m1','ch1','embedding','e','E')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u1','KB','m1',0)`)

	// Image: never counts.
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f-img','u1','c1','photo.png','image/png',5000000,'/up/img.png','image',100)`)
	// Non-image attachment: counts once even with a documents twin.
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f-pdf','u1','c1','doc.pdf','application/pdf',1000,'/up/doc.pdf','pdf',100)`)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d-twin','c1','doc.pdf','application/pdf',1000,'ready','/up/doc.pdf',100)`)
	// KB document on its own path: counts.
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d-kb','kb1','spec.md','text/markdown',500,'ready','/up/spec.md',200)`)
	// Another user's file: never counts.
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u2','b@x.test','B','h','user')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f-other','u2','x.pdf','application/pdf',7777,'/up/x.pdf','pdf',100)`)

	used, err := store.UserStorageUsage(ctx, db, "u1")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if used != 1500 {
		t.Fatalf("used=%d want 1500 (pdf 1000 + kb doc 500; image and twin excluded)", used)
	}
}

func TestStorageQuotaBytesFromGroup(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "quota.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO user_groups(id,name,max_storage_mb,created_at,updated_at) VALUES('g1','Capped',10,1,1)`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,group_id) VALUES('u1','a@x.test','A','h','user','g1')`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u2','b@x.test','B','h','user')`)

	q, err := store.StorageQuotaBytes(ctx, db, "u1")
	if err != nil || q != 10<<20 {
		t.Fatalf("quota=%d err=%v want %d", q, err, 10<<20)
	}
	// User whose group has no cap (or group missing) → 0 = unlimited.
	q, err = store.StorageQuotaBytes(ctx, db, "u2")
	if err != nil || q != 0 {
		t.Fatalf("uncapped quota=%d err=%v want 0", q, err)
	}
}

func TestCheckStorageQuotaBlocksWhenFull(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "quota-check.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO user_groups(id,name,max_storage_mb,created_at,updated_at) VALUES('g1','Tiny',1,1,1)`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,group_id) VALUES('u1','a@x.test','A','h','user','g1')`)
	// 900 KB already used.
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f1','u1','big.pdf','application/pdf',921600,'/up/big.pdf','pdf',100)`)

	d := Deps{DB: db}
	req := httptest.NewRequest("POST", "/api/files", nil)

	// 200 KB more would exceed the 1 MB cap.
	if err := checkStorageQuota(req, d, "u1", 204800); err == nil {
		t.Fatal("expected quota error")
	}
	// 100 KB still fits.
	if err := checkStorageQuota(req, d, "u1", 102400); err != nil {
		t.Fatalf("within quota rejected: %v", err)
	}
}

func TestDeleteMyFilesOwnershipGate(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "own.db"))
	defer db.Close()

	dir := t.TempDir()
	minePath := filepath.Join(dir, "mine.pdf")
	theirsPath := filepath.Join(dir, "theirs.pdf")
	writeFile(t, minePath, []byte("m"))
	writeFile(t, theirsPath, []byte("t"))

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u1','a@x.test','A','h','user')`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u2','b@x.test','B','h','user')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f-mine','u1','mine.pdf','application/pdf',1,?,'pdf',100)`, minePath)
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f-theirs','u2','theirs.pdf','application/pdf',1,?,'pdf',100)`, theirsPath)

	body := strings.NewReader(`{"items":[{"source":"file","id":"f-mine"},{"source":"file","id":"f-theirs"}]}`)
	req := httptest.NewRequest("POST", "/api/me/files/delete", body)
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
	rec := httptest.NewRecorder()
	deleteMyFilesHandler(Deps{DB: db}, rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Deleted != 1 {
		t.Fatalf("deleted=%d err=%v want exactly 1 (own file only)", out.Deleted, err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE id='f-theirs'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("other user's file must survive (n=%d err=%v)", n, err)
	}
	if _, err := os.Stat(theirsPath); err != nil {
		t.Fatal("other user's bytes must survive")
	}
	if _, err := os.Stat(minePath); !os.IsNotExist(err) {
		t.Fatal("own file bytes not removed")
	}
}

func TestListMyFilesTypeFilterAndUnknownFallback(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "my-file-types.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u1','a@x.test','A','h','user')`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u2','b@x.test','B','h','user')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,created_at) VALUES
		('pdf-1','u1','one.pdf','application/pdf',1,'/up/one.pdf','pdf',1),
		('pdf-2','u1','two.PDF','application/octet-stream',1,'/up/two.pdf','pdf',2),
		('doc-1','u1','notes.docx','application/vnd.openxmlformats-officedocument.wordprocessingml.document',1,'/up/notes.docx','doc',3),
		('other-user','u2','hidden.pdf','application/pdf',1,'/up/hidden.pdf','pdf',4)`)

	list := func(query string) struct {
		Files []store.AdminFile `json:"files"`
		Total int               `json:"total"`
	} {
		req := httptest.NewRequest("GET", "/api/me/files"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "u1", Role: "user", Status: "active"}))
		rec := httptest.NewRecorder()
		listMyFilesHandler(Deps{DB: db}, rec, req)
		if rec.Code != 200 {
			t.Fatalf("list %q status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		var out struct {
			Files []store.AdminFile `json:"files"`
			Total int               `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", query, err)
		}
		return out
	}

	pdf := list("?type=pdf&limit=1")
	if pdf.Total != 2 || len(pdf.Files) != 1 {
		t.Fatalf("paginated pdf total=%d rows=%d, want total 2 and one row", pdf.Total, len(pdf.Files))
	}
	unknown := list("?type=made-up")
	if unknown.Total != 3 || len(unknown.Files) != 3 {
		t.Fatalf("unknown type total=%d rows=%d, want all 3 owned files", unknown.Total, len(unknown.Files))
	}
}
