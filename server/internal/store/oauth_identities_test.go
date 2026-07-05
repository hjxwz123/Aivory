package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// setupOAuthDB opens a migrated DB with two users and one enabled provider.
func setupOAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u1','a@b.c','h','user')`)
	exec(t, db, `INSERT INTO users(id,email,password_hash,role) VALUES('u2','c@d.e','h','user')`)
	exec(t, db, `INSERT INTO oauth_providers(id,kind,name,client_id,enabled) VALUES('oa_google','google','Google','cid',1)`)
	return db
}

// TestBindOAuthIdentityConflicts locks in the two conflict rules the account
// page enforces (§ identity linking):
//  1. a Google account already used to LOG IN to one account can't be bound by another;
//  2. a Google account already BOUND to one account can't be bound by another.
//
// Both reduce to a (provider, subject) primary-key collision with a different
// user — BindOAuthIdentity must return ErrOAuthIdentityConflict, never reassign.
func TestBindOAuthIdentityConflicts(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)

	// Case 1: Google account "A" logged u1 in (login path records the identity).
	if err := LinkOAuthIdentity(ctx, db, "oa_google", "google-A", "u1", "a@gmail.com"); err != nil {
		t.Fatalf("seed login identity: %v", err)
	}
	// u2 tries to BIND the same Google account A → conflict.
	if err := BindOAuthIdentity(ctx, db, "oa_google", "google-A", "u2", "a@gmail.com"); !errors.Is(err, ErrOAuthIdentityConflict) {
		t.Fatalf("case 1 (login-owned): got %v, want ErrOAuthIdentityConflict", err)
	}
	// The original owner is untouched (no reassignment).
	if owner, _ := FindOAuthIdentityUser(ctx, db, "oa_google", "google-A"); owner != "u1" {
		t.Fatalf("identity A was reassigned to %q, want u1", owner)
	}

	// Case 2: Google account "B" is BOUND to u1.
	if err := BindOAuthIdentity(ctx, db, "oa_google", "google-B", "u1", "b@gmail.com"); err != nil {
		t.Fatalf("bind B to u1: %v", err)
	}
	// u2 tries to bind the same account B → conflict.
	if err := BindOAuthIdentity(ctx, db, "oa_google", "google-B", "u2", "b@gmail.com"); !errors.Is(err, ErrOAuthIdentityConflict) {
		t.Fatalf("case 2 (bind-owned): got %v, want ErrOAuthIdentityConflict", err)
	}
}

// TestBindOAuthIdentityIdempotent: re-binding the caller's OWN identity succeeds
// (no error, no duplicate) and refreshes the stored email.
func TestBindOAuthIdentityIdempotent(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)

	if err := BindOAuthIdentity(ctx, db, "oa_google", "sub-1", "u1", "old@gmail.com"); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if err := BindOAuthIdentity(ctx, db, "oa_google", "sub-1", "u1", "new@gmail.com"); err != nil {
		t.Fatalf("re-bind by same user should succeed: %v", err)
	}
	n, err := CountOAuthIdentitiesForUser(ctx, db, "u1")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-bind duplicated the identity: count=%d, want 1", n)
	}
	rows, err := ListOAuthIdentitiesForUser(ctx, db, "u1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: rows=%d err=%v", len(rows), err)
	}
	if rows[0].Email != "new@gmail.com" {
		t.Fatalf("email not refreshed on re-bind: %q", rows[0].Email)
	}
}

// TestListOAuthIdentitiesJoinsProvider: the list returns provider display fields
// and the enabled flag, and stays scoped to the requesting user.
func TestListOAuthIdentitiesJoinsProvider(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	if err := BindOAuthIdentity(ctx, db, "oa_google", "sub-u1", "u1", "u1@gmail.com"); err != nil {
		t.Fatal(err)
	}
	if err := BindOAuthIdentity(ctx, db, "oa_google", "sub-u2", "u2", "u2@gmail.com"); err != nil {
		t.Fatal(err)
	}
	rows, err := ListOAuthIdentitiesForUser(ctx, db, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("u1 list scoped wrong: got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.ProviderName != "Google" || got.ProviderKind != "google" || !got.ProviderEnabled {
		t.Fatalf("provider join wrong: %+v", got)
	}
	if got.Subject != "sub-u1" || got.Email != "u1@gmail.com" {
		t.Fatalf("identity fields wrong: %+v", got)
	}

	// Disabling the provider keeps the binding visible but flags it not-enabled.
	exec(t, db, `UPDATE oauth_providers SET enabled=0 WHERE id='oa_google'`)
	rows, _ = ListOAuthIdentitiesForUser(ctx, db, "u1")
	if len(rows) != 1 || rows[0].ProviderEnabled {
		t.Fatalf("disabled provider: rows=%d enabled=%v, want 1/false", len(rows), rows[0].ProviderEnabled)
	}
}

// TestUnbindOAuthIdentity: removal is scoped to the owner and reports whether a
// row was actually deleted.
func TestUnbindOAuthIdentity(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	if err := BindOAuthIdentity(ctx, db, "oa_google", "sub-1", "u1", "x@gmail.com"); err != nil {
		t.Fatal(err)
	}

	// u2 can't remove u1's binding (scoped by user_id) → no row deleted.
	if ok, err := UnbindOAuthIdentity(ctx, db, "oa_google", "sub-1", "u2"); err != nil || ok {
		t.Fatalf("cross-user unbind removed a row: ok=%v err=%v", ok, err)
	}
	// The owner removes it → deleted.
	if ok, err := UnbindOAuthIdentity(ctx, db, "oa_google", "sub-1", "u1"); err != nil || !ok {
		t.Fatalf("owner unbind failed: ok=%v err=%v", ok, err)
	}
	if n, _ := CountOAuthIdentitiesForUser(ctx, db, "u1"); n != 0 {
		t.Fatalf("count after unbind = %d, want 0", n)
	}
	// Unbinding again → nothing to delete.
	if ok, _ := UnbindOAuthIdentity(ctx, db, "oa_google", "sub-1", "u1"); ok {
		t.Fatal("second unbind reported a deletion")
	}
}
