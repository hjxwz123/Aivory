package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestAsyncUserDeletionEndToEnd(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "user-del.db"))
	defer db.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "attach.pdf")
	kbDocPath := filepath.Join(dir, "spec.md")
	writeFile(t, filePath, []byte("pdf"))
	writeFile(t, kbDocPath, []byte("md"))

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('adm','root@example.test','Root','h','admin')`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u9','mia@example.test','Mia','h','user')`)
	mustExec(t, db, `INSERT INTO refresh_tokens(jti,user_id,expires_at) VALUES('rt1','u9',9999999999)`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u9','T')`)
	mustExec(t, db, `INSERT INTO messages(id,conversation_id,role,blocks) VALUES('m1','c1','user','[]')`)
	mustExec(t, db, `INSERT INTO messages(id,conversation_id,role,blocks) VALUES('m2','c1','assistant','[]')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f1','u9','c1','attach.pdf','application/pdf',3,?,'file',100)`, filePath)
	mustExec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d1','c1','attach.pdf','application/pdf',3,'ready',?,100)`, filePath)
	mustExec(t, db, `INSERT INTO channels(id,name,type) VALUES('ch1','C','openai')`)
	mustExec(t, db, `INSERT INTO models(id,channel_id,kind,request_id,label) VALUES('m1','ch1','embedding','e','E')`)
	mustExec(t, db, `INSERT INTO knowledge_bases(id,user_id,name,embedding_model_id,embedding_dim) VALUES('kb1','u9','KB','m1',0)`)
	mustExec(t, db, `INSERT INTO documents(id,kb_id,filename,mime_type,size_bytes,status,storage_path,created_at)
	  VALUES('d2','kb1','spec.md','text/markdown',2,'ready',?,200)`, kbDocPath)
	mustExec(t, db, `INSERT INTO chunks(id,document_id,seq,content) VALUES('ch1','d2',0,'hello')`)
	mustExec(t, db, `INSERT INTO usage_logs(user_id,model_id,purpose,input_tokens,output_tokens,cost,created_at)
	  VALUES('u9','m1','chat',10,5,0.01,100)`)

	d := Deps{DB: db, Cache: cache.NewMemory()}

	// Phase 1: the DELETE request — instant lockout, 202, no heavy work yet.
	req := httptest.NewRequest("DELETE", "/api/admin/users/u9", nil)
	req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "u9"}))
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "adm", Role: "admin", Status: "active"}))
	rec := httptest.NewRecorder()
	deleteUserAdmin(d, rec, req)
	if rec.Code != 202 {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}

	u, err := store.FindUserByID(ctx, db, "u9")
	if err != nil {
		t.Fatalf("user vanished before background job: %v", err)
	}
	if u.Status != "deleting" {
		t.Fatalf("status=%q want deleting", u.Status)
	}
	var revoked int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM refresh_tokens WHERE user_id='u9' AND revoked=1`).Scan(&revoked); err != nil || revoked != 1 {
		t.Fatalf("refresh token not revoked (revoked=%d err=%v)", revoked, err)
	}

	// Phase 2: wait for the background job the handler spawned.
	waitForDeletion(t, "u9")

	for _, q := range []string{
		`SELECT COUNT(*) FROM users WHERE id='u9'`,
		`SELECT COUNT(*) FROM conversations`,
		`SELECT COUNT(*) FROM messages`,
		`SELECT COUNT(*) FROM files`,
		`SELECT COUNT(*) FROM documents`,
		`SELECT COUNT(*) FROM chunks`,
		`SELECT COUNT(*) FROM usage_logs WHERE user_id='u9'`,
		`SELECT COUNT(*) FROM knowledge_bases`,
		`SELECT COUNT(*) FROM refresh_tokens WHERE user_id='u9'`,
	} {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("%s = %d, want 0", q, n)
		}
	}
	for _, p := range []string{filePath, kbDocPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still on disk", p)
		}
	}
	var pending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_storage_cleanup`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("cleanup ledger not drained (pending=%d err=%v)", pending, err)
	}
}

func TestMarkUserDeletingLastAdminGuard(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "last-admin.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('a1','a1@x.test','A1','h','admin')`)

	if _, err := store.MarkUserDeleting(ctx, db, "a1"); err == nil {
		t.Fatal("deleting the only active admin must be refused")
	} else if err != store.ErrLastAdmin {
		t.Fatalf("err=%v want ErrLastAdmin", err)
	}

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('a2','a2@x.test','A2','h','admin')`)
	if changed, err := store.MarkUserDeleting(ctx, db, "a1"); err != nil || !changed {
		t.Fatalf("delete with second admin present: changed=%v err=%v", changed, err)
	}
	// Idempotent second call: no error, no change.
	if changed, err := store.MarkUserDeleting(ctx, db, "a1"); err != nil || changed {
		t.Fatalf("repeat mark: changed=%v err=%v (want false, nil)", changed, err)
	}
}

func TestBanAndUnbanRefuseDeletingAccount(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "ban-deleting.db"))
	defer db.Close()
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('adm','root@x.test','Root','h','admin')`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES('u5','v@x.test','V','h','user','deleting')`)

	d := Deps{DB: db, Cache: cache.NewMemory()}
	for _, tc := range []struct {
		name    string
		handler func(Deps, http.ResponseWriter, *http.Request)
		method  string
	}{
		{"ban", banUserAdmin, "POST"},
		{"unban", unbanUserAdmin, "POST"},
	} {
		req := httptest.NewRequest(tc.method, "/api/admin/users/u5/"+tc.name, nil)
		req = req.WithContext(context.WithValue(req.Context(), pathCtxKey{}, map[string]string{"id": "u5"}))
		req = req.WithContext(context.WithValue(req.Context(), userCtxKey{}, &store.User{ID: "adm", Role: "admin", Status: "active"}))
		rec := httptest.NewRecorder()
		tc.handler(d, rec, req)
		if rec.Code != 409 {
			t.Fatalf("%s on deleting account: status=%d want 409", tc.name, rec.Code)
		}
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id='u5'`).Scan(&status); err != nil || status != "deleting" {
		t.Fatalf("status=%q err=%v — must remain deleting", status, err)
	}
}

func TestStartupSweepFinishesOrphanedLedgerPaths(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "sweep.db"))
	defer db.Close()

	dir := t.TempDir()
	orphanPath := filepath.Join(dir, "orphan.bin")
	ownedPath := filepath.Join(dir, "owned.bin")
	writeFile(t, orphanPath, []byte("x"))
	writeFile(t, ownedPath, []byte("y"))

	// ownedPath is still referenced by a live files row (its owner's job is
	// notionally still running); orphanPath belongs to a fully-deleted user.
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role) VALUES('u1','a@x.test','A','h','user')`)
	mustExec(t, db, `INSERT INTO files(id,user_id,filename,mime_type,size_bytes,storage_path,kind,created_at)
	  VALUES('f1','u1','owned.bin','application/octet-stream',1,?,'file',100)`, ownedPath)
	mustExec(t, db, `INSERT INTO pending_storage_cleanup(path,user_id,created_at) VALUES(?,?,100)`, orphanPath, "gone")
	mustExec(t, db, `INSERT INTO pending_storage_cleanup(path,user_id,created_at) VALUES(?,?,100)`, ownedPath, "u1")

	sweepPendingStorageCleanup(Deps{DB: db, Cache: cache.NewMemory()})

	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatal("orphaned path not removed by sweep")
	}
	if _, err := os.Stat(ownedPath); err != nil {
		t.Fatal("referenced path must be left alone")
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_storage_cleanup WHERE path=?`, orphanPath).Scan(&n); err != nil || n != 0 {
		t.Fatalf("orphan ledger row not forgotten (n=%d err=%v)", n, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_storage_cleanup WHERE path=?`, ownedPath).Scan(&n); err != nil || n != 1 {
		t.Fatalf("referenced ledger row must be kept (n=%d err=%v)", n, err)
	}
}

func TestResumeUserDeletionsPicksUpStrandedAccounts(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t, filepath.Join(t.TempDir(), "user-del-resume.db"))
	defer db.Close()

	// An account left mid-deletion by a crashed process.
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES('u7','ghost@example.test','Ghost','h','user','deleting')`)
	mustExec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c7','u7','T')`)
	mustExec(t, db, `INSERT INTO messages(id,conversation_id,role,blocks) VALUES('m7','c7','user','[]')`)

	d := Deps{DB: db, Cache: cache.NewMemory()}
	resumeUserDeletions(d)
	waitForDeletion(t, "u7")

	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id='u7'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stranded user not purged (n=%d err=%v)", n, err)
	}
}

func TestUserDeletionSingleFlight(t *testing.T) {
	m := &userDeletionManager{jobs: map[string]*userDeletionJob{}}
	if !m.start("ux", "a@b") {
		t.Fatal("first start refused")
	}
	if m.start("ux", "a@b") {
		t.Fatal("second start while running must be refused")
	}
	m.finish("ux", "failed", "boom")
	if !m.start("ux", "a@b") {
		t.Fatal("restart after failure must be allowed")
	}
}

// waitForDeletion polls the manager until the user's job reaches a terminal
// state; fails the test on timeout or on a failed job.
func waitForDeletion(t *testing.T, userID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, job := range userDeletions.list() {
			if job.UserID != userID {
				continue
			}
			switch job.Status {
			case "completed":
				return
			case "failed":
				t.Fatalf("deletion job failed: %s", job.Error)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("deletion job for %s did not finish in time", userID)
}
