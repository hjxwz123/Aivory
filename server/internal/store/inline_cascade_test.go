package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// TestDeleteConversationCascadesInlineThreads verifies that deleting a
// conversation also removes every inline sub-conversation transitively anchored
// to it (children, grandchildren), and reports their ids — for both the
// user-scoped and admin delete paths.
func TestDeleteConversationCascadesInlineThreads(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "ic.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)

	// root  ── inline ──▶ child ── inline ──▶ grandchild   (nested sub-threads)
	// plus an unrelated conversation that must survive.
	mk := func(id, src string) {
		t.Helper()
		if _, err := CreateConversation(ctx, db, Conversation{
			ID: id, UserID: "u1", Title: id, InlineSourceConv: src,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("root", "")
	mk("child", "root")
	mk("grand", "child")
	mk("other", "")
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_root','u1','root','root.txt','/tmp/root.txt')`)
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_child','u1','child','child.txt','/tmp/child.txt')`)
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_other','u1','other','other.txt','/tmp/other.txt')`)

	children, err := DeleteConversation(ctx, db, "root", "u1")
	if err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 cascaded children, got %d (%v)", len(children), children)
	}
	for _, id := range []string{"root", "child", "grand"} {
		assertConvGone(t, db, id)
	}
	for _, id := range []string{"f_root", "f_child"} {
		assertFileGone(t, db, id)
	}
	if !fileExists(t, db, "f_other") {
		t.Fatalf("unrelated file 'f_other' was wrongly deleted")
	}
	if !convExists(t, db, "other") {
		t.Fatalf("unrelated conversation 'other' was wrongly deleted")
	}

	// Admin path cascades too.
	mk("aroot", "")
	mk("achild", "aroot")
	mk("agrand", "achild")
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_aroot','u1','aroot','aroot.txt','/tmp/aroot.txt')`)
	achildren, err := DeleteConversationByID(ctx, db, "aroot")
	if err != nil {
		t.Fatalf("DeleteConversationByID: %v", err)
	}
	if len(achildren) != 2 {
		t.Fatalf("admin: expected 2 cascaded children, got %d (%v)", len(achildren), achildren)
	}
	for _, id := range []string{"aroot", "achild", "agrand"} {
		assertConvGone(t, db, id)
	}
	assertFileGone(t, db, "f_aroot")

	// Wrong owner → not found, nothing deleted.
	mk("keep", "")
	if _, err := DeleteConversation(ctx, db, "keep", "intruder"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner: want ErrNotFound, got %v", err)
	}
	if !convExists(t, db, "keep") {
		t.Fatalf("'keep' should survive a wrong-owner delete")
	}
}

func assertFileGone(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if fileExists(t, db, id) {
		t.Fatalf("file %s should be deleted", id)
	}
}

func fileExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var x string
	err := db.QueryRowContext(context.Background(), `SELECT id FROM files WHERE id=?`, id).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query file %s: %v", id, err)
	}
	return true
}

func assertConvGone(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if convExists(t, db, id) {
		t.Fatalf("conversation %s should be deleted", id)
	}
}

func convExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var x string
	err := db.QueryRowContext(context.Background(), `SELECT id FROM conversations WHERE id=?`, id).Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("query conversation %s: %v", id, err)
	}
	return true
}
