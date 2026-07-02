package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeleteRound covers the branch-safe semantics: deleting a round removes the
// user turn + ALL its answers, splices the continuation onto the round's parent,
// and leaves earlier turns, later turns, and sibling branches intact.
func TestDeleteRound(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "dr.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)

	// Linear thread U1→A1→U2→A2→U3→A3 with monotonic created_at.
	rows := []struct{ id, parent, role string }{
		{"U1", "", "user"}, {"A1", "U1", "assistant"},
		{"U2", "A1", "user"}, {"A2", "U2", "assistant"},
		{"U3", "A2", "user"}, {"A3", "U3", "assistant"},
	}
	for i, r := range rows {
		insMsg(t, db, r.id, r.parent, r.role, 1000+int64(i))
	}
	exec(t, db, `UPDATE conversations SET active_leaf_id='A3' WHERE id='c1'`)

	// Delete the MIDDLE round via its answer A2 → removes U2+A2, re-parents U3
	// onto A1 (U2's parent). Active leaf A3 still exists → unchanged.
	leaf, err := DeleteRound(ctx, db, "c1", "u1", "A2")
	if err != nil {
		t.Fatalf("DeleteRound A2: %v", err)
	}
	assertGone(t, db, "U2")
	assertGone(t, db, "A2")
	assertParent(t, db, "U3", "A1")
	assertParent(t, db, "U1", "")
	assertParent(t, db, "A1", "U1")
	if leaf != "A3" {
		t.Fatalf("leaf should stay A3, got %q", leaf)
	}
	if got := join(idsOf(listMsgs(t, db, ""))); got != "U1,A1,U3,A3" {
		t.Fatalf("active path = %s, want U1,A1,U3,A3", got)
	}

	// Branch-safety: U3 gets a 2nd answer A3b (regeneration) + a continuation U4
	// under A3. Deleting the round (click U3) removes U3+A3+A3b; U4 re-parents
	// onto A1; earlier turns survive.
	insMsg(t, db, "A3b", "U3", "assistant", 2000)
	insMsg(t, db, "U4", "A3", "user", 2001)
	exec(t, db, `UPDATE conversations SET active_leaf_id='U4' WHERE id='c1'`)
	if _, err := DeleteRound(ctx, db, "c1", "u1", "U3"); err != nil {
		t.Fatalf("DeleteRound U3: %v", err)
	}
	assertGone(t, db, "U3")
	assertGone(t, db, "A3")
	assertGone(t, db, "A3b")
	assertParent(t, db, "U4", "A1")
	assertParent(t, db, "A1", "U1")

	// Wrong owner → not found, no mutation.
	if _, err := DeleteRound(ctx, db, "c1", "intruder", "U1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner: want ErrNotFound, got %v", err)
	}

	// Delete the ROOT round (U1) → U1+A1 go; U4 (continuation) becomes a root.
	if _, err := DeleteRound(ctx, db, "c1", "u1", "U1"); err != nil {
		t.Fatalf("DeleteRound U1: %v", err)
	}
	assertGone(t, db, "U1")
	assertGone(t, db, "A1")
	assertParent(t, db, "U4", "") // re-anchored to root
}

// TestDeleteBranchKeepsSiblings locks in the §4.15 data-loss fix: deleting ONE
// regenerated answer (an assistant with sibling answers under the same question)
// removes only that branch's subtree — the question and the other branches
// survive — while deleting the LAST remaining answer still falls back to the
// whole-round delete.
func TestDeleteBranchKeepsSiblings(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "db.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','T')`)

	// One question Q with FOUR regenerated answers (4 branches). Branch A2 has a
	// downstream continuation Q2→A2x.
	insMsg(t, db, "Q", "", "user", 1000)
	insMsg(t, db, "A1", "Q", "assistant", 1001)
	insMsg(t, db, "A2", "Q", "assistant", 1002)
	insMsg(t, db, "A3", "Q", "assistant", 1003)
	insMsg(t, db, "A4", "Q", "assistant", 1004)
	insMsg(t, db, "Q2", "A2", "user", 1005)
	insMsg(t, db, "A2x", "Q2", "assistant", 1006)
	exec(t, db, `UPDATE conversations SET active_leaf_id='A2x' WHERE id='c1'`)

	// Delete branch A2 (an answer WITH siblings) → only A2 + its downstream go.
	leaf, err := DeleteRound(ctx, db, "c1", "u1", "A2")
	if err != nil {
		t.Fatalf("DeleteRound A2: %v", err)
	}
	assertGone(t, db, "A2")
	assertGone(t, db, "Q2")
	assertGone(t, db, "A2x")
	// Question + the other three branches are untouched.
	assertParent(t, db, "Q", "")
	assertParent(t, db, "A1", "Q")
	assertParent(t, db, "A3", "Q")
	assertParent(t, db, "A4", "Q")
	// The active leaf lived inside the deleted branch → re-pointed to a surviving
	// message under the question.
	if leaf == "" {
		t.Fatalf("active leaf should re-point to a surviving branch, got empty")
	}
	var x string
	if err := db.QueryRowContext(ctx, `SELECT id FROM messages WHERE id=?`, leaf).Scan(&x); err != nil {
		t.Fatalf("re-pointed leaf %q does not exist: %v", leaf, err)
	}

	// Peel branches down to one: deleting A1 then A3 (each still has siblings)
	// removes only that branch; A4 remains the sole answer under Q.
	if _, err := DeleteRound(ctx, db, "c1", "u1", "A1"); err != nil {
		t.Fatalf("DeleteRound A1: %v", err)
	}
	if _, err := DeleteRound(ctx, db, "c1", "u1", "A3"); err != nil {
		t.Fatalf("DeleteRound A3: %v", err)
	}
	assertGone(t, db, "A1")
	assertGone(t, db, "A3")
	assertParent(t, db, "A4", "Q") // sole survivor, still attached to the question

	// Deleting the LAST answer falls back to the whole-round delete (question too).
	if _, err := DeleteRound(ctx, db, "c1", "u1", "A4"); err != nil {
		t.Fatalf("DeleteRound A4: %v", err)
	}
	assertGone(t, db, "A4")
	assertGone(t, db, "Q")
}

// TestDeleteRoundWorkspaceMember locks in the §workspaces fix: a non-creator
// member may delete their OWN round in a shared conversation. Before the fix,
// DeleteRound hard-required caller==conversation.user_id, so a member who
// passed deleteMessageHandler's per-round author check still got ErrNotFound
// here — a regression the handler's own check couldn't catch on its own.
func TestDeleteRoundWorkspaceMember(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "drw.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('owner','o@b.c','h','user')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('member','m@b.c','h','user')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('outsider','x@b.c','h','user')`)
	exec(t, db, `INSERT INTO workspaces(id,name,owner_id,invite_token) VALUES('ws1','WS','owner','tok1')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','owner','owner')`)
	exec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws1','member','member')`)
	exec(t, db, `INSERT INTO conversations(id,user_id,title,workspace_id) VALUES('c1','owner','T','ws1')`)

	// member sends U1, gets an assistant reply A1.
	insMsg(t, db, "U1", "", "user", 1000)
	insMsg(t, db, "A1", "U1", "assistant", 1001)
	exec(t, db, `UPDATE conversations SET active_leaf_id='A1' WHERE id='c1'`)

	// A workspace member (not the conversation creator) deletes their own round.
	if _, err := DeleteRound(ctx, db, "c1", "member", "U1"); err != nil {
		t.Fatalf("member deleting own round: want ok, got %v", err)
	}
	assertGone(t, db, "U1")
	assertGone(t, db, "A1")

	// A user with no workspace membership at all is still rejected.
	insMsg(t, db, "U2", "", "user", 2000)
	exec(t, db, `UPDATE conversations SET active_leaf_id='U2' WHERE id='c1'`)
	if _, err := DeleteRound(ctx, db, "c1", "outsider", "U2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-member: want ErrNotFound, got %v", err)
	}
	assertParent(t, db, "U2", "") // untouched
}

// --- helpers ---------------------------------------------------------------

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func insMsg(t *testing.T, db *sql.DB, id, parent, role string, ts int64) {
	t.Helper()
	var p any
	if parent != "" {
		p = parent
	}
	exec(t, db, `INSERT INTO messages(id,conversation_id,parent_id,role,created_at) VALUES(?,?,?,?,?)`,
		id, "c1", p, role, ts)
}

func assertGone(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	var x string
	err := db.QueryRowContext(context.Background(), `SELECT id FROM messages WHERE id=?`, id).Scan(&x)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("message %s should be deleted (err=%v)", id, err)
	}
}

func assertParent(t *testing.T, db *sql.DB, id, wantParent string) {
	t.Helper()
	var got string
	err := db.QueryRowContext(context.Background(), `SELECT COALESCE(parent_id,'') FROM messages WHERE id=?`, id).Scan(&got)
	if err != nil {
		t.Fatalf("read parent of %s: %v", id, err)
	}
	if got != wantParent {
		t.Fatalf("parent of %s = %q, want %q", id, got, wantParent)
	}
}

func listMsgs(t *testing.T, db *sql.DB, leaf string) []Message {
	t.Helper()
	m, err := ListMessages(context.Background(), db, "c1", leaf)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	return m
}

func idsOf(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func join(ss []string) string { return strings.Join(ss, ",") }
