package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestConversationGenerationLeaseSerializesOneBranch(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "generation-lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users(id,email,password_hash) VALUES('u1','lease@example.test','h')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO conversations(id,user_id,title) VALUES('c1','u1','Lease')`); err != nil {
		t.Fatal(err)
	}

	lease, parentID, acquired, err := TryAcquireConversationGenerationLease(
		ctx, db, "c1", "", "u1", "owner-1", time.Hour,
	)
	if err != nil || !acquired || lease == nil || parentID != "" {
		t.Fatalf("first acquire lease=%+v parent=%q acquired=%v err=%v", lease, parentID, acquired, err)
	}
	if second, _, acquired, err := TryAcquireConversationGenerationLease(
		ctx, db, "c1", "", "u1", "owner-2", time.Hour,
	); err != nil || acquired || second != nil {
		t.Fatalf("second acquire lease=%+v acquired=%v err=%v, want conflict", second, acquired, err)
	}
	memberLease, _, acquired, err := TryAcquireConversationGenerationLease(
		ctx, db, "c1", "", "u2", "member-owner", time.Hour,
	)
	if err != nil || !acquired || memberLease == nil {
		t.Fatalf("other member acquire lease=%+v acquired=%v err=%v", memberLease, acquired, err)
	}
	if err := ReleaseConversationGenerationLease(ctx, db, memberLease); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseConversationGenerationLease(ctx, db, lease); err != nil {
		t.Fatal(err)
	}
	third, _, acquired, err := TryAcquireConversationGenerationLease(
		ctx, db, "c1", "", "u1", "owner-3", time.Hour,
	)
	if err != nil || !acquired || third == nil {
		t.Fatalf("acquire after release lease=%+v acquired=%v err=%v", third, acquired, err)
	}
}

func TestConversationGenerationLeaseChecksOnlySelectedPathAndSettlesStaleRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "generation-path-lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO users(id,email,password_hash) VALUES('u1','path-lease@example.test','h')`,
		`INSERT INTO conversations(id,user_id,title,active_leaf_id) VALUES('c1','u1','Paths',NULL)`,
		`INSERT INTO messages(id,conversation_id,role,status,created_at) VALUES('u-root','c1','user','complete',100)`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,status,created_at) VALUES('a-live','c1','u-root','assistant','streaming',200)`,
		`INSERT INTO messages(id,conversation_id,parent_id,role,status,created_at) VALUES('a-sibling','c1','u-root','assistant','complete',201)`,
		`UPDATE conversations SET active_leaf_id='a-live' WHERE id='c1'`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}

	// Keep the seeded row fresh for this assertion despite its deterministic
	// timestamp, then verify that the active path is blocked.
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at=? WHERE id='a-live'`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if lease, parentID, acquired, err := TryAcquireConversationGenerationLease(
		ctx, db, "c1", "", "u1", "active-owner", time.Hour,
	); err != nil || acquired || lease != nil || parentID != "a-live" {
		t.Fatalf("active streaming path lease=%+v parent=%q acquired=%v err=%v", lease, parentID, acquired, err)
	}

	// Selecting the completed sibling is a different branch and remains usable.
	siblingLease, parentID, acquired, err := TryAcquireConversationGenerationLease(
		ctx, db, "c1", "a-sibling", "u1", "sibling-owner", time.Hour,
	)
	if err != nil || !acquired || siblingLease == nil || parentID != "a-sibling" {
		t.Fatalf("sibling lease=%+v parent=%q acquired=%v err=%v", siblingLease, parentID, acquired, err)
	}
	if err := ReleaseConversationGenerationLease(ctx, db, siblingLease); err != nil {
		t.Fatal(err)
	}

	// A crash-leftover older than the protected lease lifetime is terminalized
	// and no longer leaves the branch permanently blocked.
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at=? WHERE id='a-live'`, time.Now().Add(-2*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	staleLease, parentID, acquired, err := TryAcquireConversationGenerationLease(
		ctx, db, "c1", "", "u1", "stale-owner", time.Hour,
	)
	if err != nil || !acquired || staleLease == nil || parentID != "a-live" {
		t.Fatalf("stale path lease=%+v parent=%q acquired=%v err=%v", staleLease, parentID, acquired, err)
	}
	var status, stopReason string
	if err := db.QueryRowContext(ctx,
		`SELECT status,stop_reason FROM messages WHERE id='a-live'`,
	).Scan(&status, &stopReason); err != nil {
		t.Fatal(err)
	}
	if status != "error" || stopReason != "generation_interrupted" {
		t.Fatalf("stale assistant status=%q stop_reason=%q", status, stopReason)
	}
}
