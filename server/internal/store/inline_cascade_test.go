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

func TestDeleteConversationPreservesForeignOwnedInlineState(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "foreign-inline.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('owner','owner@example.test','h','user')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('member','member@example.test','h','user')`)
	workspace, err := CreateWorkspace(ctx, db, "owner", "Shared")
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := JoinWorkspace(ctx, db, workspace.ID, "member"); err != nil {
		t.Fatalf("join workspace: %v", err)
	}

	create := func(c Conversation) {
		t.Helper()
		if _, err := CreateConversation(ctx, db, c); err != nil {
			t.Fatalf("create %s: %v", c.ID, err)
		}
	}
	create(Conversation{ID: "shared-root", UserID: "owner", Title: "root", WorkspaceID: workspace.ID})
	// This is the shape created when a workspace member opens an inline thread:
	// the source is shared, while the new inline conversation remains personal.
	create(Conversation{ID: "member-inline", UserID: "member", Title: "member", InlineSourceConv: "shared-root"})
	create(Conversation{ID: "owner-grandchild", UserID: "owner", Title: "grand", InlineSourceConv: "member-inline"})

	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_root','owner','shared-root','root.txt','/tmp/root-owned.txt')`)
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_member','member','member-inline','member.txt','/tmp/member-owned.txt')`)
	exec(t, db, `INSERT INTO files(id,user_id,conversation_id,filename,storage_path) VALUES('f_grand','owner','owner-grandchild','grand.txt','/tmp/grand-owned.txt')`)
	exec(t, db, `INSERT INTO documents(id,conversation_id,filename,mime_type,size_bytes,storage_path) VALUES('d_member','member-inline','member.pdf','application/pdf',1,'/tmp/member-doc.pdf')`)

	state, err := DeleteConversationWithState(ctx, db, "shared-root", "owner")
	if err != nil {
		t.Fatalf("DeleteConversationWithState: %v", err)
	}
	wantIDs := []string{"shared-root", "owner-grandchild"}
	if len(state.ConversationIDs) != len(wantIDs) {
		t.Fatalf("deleted ids=%v, want %v", state.ConversationIDs, wantIDs)
	}
	for i := range wantIDs {
		if state.ConversationIDs[i] != wantIDs[i] {
			t.Fatalf("deleted ids=%v, want %v", state.ConversationIDs, wantIDs)
		}
	}
	paths := make(map[string]bool, len(state.StoragePaths))
	for _, path := range state.StoragePaths {
		paths[path] = true
	}
	for _, path := range []string{"/tmp/root-owned.txt", "/tmp/grand-owned.txt"} {
		if !paths[path] {
			t.Fatalf("cleanup paths=%v, missing deleted owner path %q", state.StoragePaths, path)
		}
	}
	for _, path := range []string{"/tmp/member-owned.txt", "/tmp/member-doc.pdf"} {
		if paths[path] {
			t.Fatalf("cleanup paths=%v include preserved member path %q", state.StoragePaths, path)
		}
	}

	assertConvGone(t, db, "shared-root")
	assertConvGone(t, db, "owner-grandchild")
	if !convExists(t, db, "member-inline") {
		t.Fatal("foreign-owned inline conversation must survive")
	}
	assertFileGone(t, db, "f_root")
	assertFileGone(t, db, "f_grand")
	if !fileExists(t, db, "f_member") {
		t.Fatal("foreign-owned inline attachment must survive")
	}
	var conversationID string
	if err := db.QueryRowContext(ctx, `SELECT conversation_id FROM files WHERE id='f_member'`).Scan(&conversationID); err != nil {
		t.Fatalf("read preserved member attachment: %v", err)
	}
	if conversationID != "member-inline" {
		t.Fatalf("preserved member attachment conversation_id=%q, want member-inline", conversationID)
	}
	var documentID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM documents WHERE id='d_member'`).Scan(&documentID); err != nil {
		t.Fatalf("foreign-owned inline document must survive: %v", err)
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
