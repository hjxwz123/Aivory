package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

func TestConcurrentUnbindPreservesLastOAuthLoginMethod(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	exec(t, db, `UPDATE users SET password_set=0 WHERE id='u1'`)
	if err := BindOAuthIdentity(ctx, db, "oa_google", "sub-a", "u1", "a@gmail.com"); err != nil {
		t.Fatal(err)
	}
	if err := BindOAuthIdentity(ctx, db, "oa_google", "sub-b", "u1", "b@gmail.com"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, subject := range []string{"sub-a", "sub-b"} {
		wg.Add(1)
		go func(subject string) {
			defer wg.Done()
			<-start
			_, err := UnbindOAuthIdentity(ctx, db, "oa_google", subject, "u1")
			results <- err
		}(subject)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrOAuthLastLoginMethod):
			refused++
		default:
			t.Fatalf("concurrent unbind error: %v", err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("concurrent results: succeeded=%d refused=%d", succeeded, refused)
	}
	if n, err := CountOAuthIdentitiesForUser(ctx, db, "u1"); err != nil || n != 1 {
		t.Fatalf("remaining identities = %d, err=%v; want 1", n, err)
	}
}

func TestUnbindOAuthIdentityKeepsAutoRedirectProvider(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	exec(t, db, `INSERT INTO oauth_providers(id,kind,name,client_id,enabled) VALUES('oa_github','github','GitHub','cid',1)`)
	if err := BindOAuthIdentity(ctx, db, "oa_google", "google-sub", "u1", "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if err := BindOAuthIdentity(ctx, db, "oa_github", "github-sub", "u1", "a@b.c"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(db, "auth_entry_mode", "auto_redirect"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(db, "auth_default_provider_id", "oa_google"); err != nil {
		t.Fatal(err)
	}

	if ok, err := UnbindOAuthIdentity(ctx, db, "oa_google", "google-sub", "u1"); ok || !errors.Is(err, ErrOAuthLastLoginMethod) {
		t.Fatalf("default-provider unbind = ok %v err %v, want lockout rejection", ok, err)
	}
	if ok, err := UnbindOAuthIdentity(ctx, db, "oa_github", "github-sub", "u1"); err != nil || !ok {
		t.Fatalf("non-default provider unbind = ok %v err %v, want success", ok, err)
	}
}

func TestCreateOAuthUserRollsBackWhenIdentityAlreadyExists(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	if err := BindOAuthIdentity(ctx, db, "oa_google", "existing-subject", "u1", "a@gmail.com"); err != nil {
		t.Fatal(err)
	}
	created, err := CreateOAuthUser(ctx, db, "oa_google", "existing-subject", "orphan@example.test", "Orphan", "active")
	if created != nil || !errors.Is(err, ErrOAuthIdentityConflict) {
		t.Fatalf("CreateOAuthUser = user %+v err %v, want identity conflict", created, err)
	}
	if u, err := FindUserByEmail(ctx, db, "orphan@example.test"); err == nil || u != nil {
		t.Fatalf("identity conflict left orphan user %+v err=%v", u, err)
	}
	owner, err := FindOAuthIdentityUser(ctx, db, "oa_google", "existing-subject")
	if err != nil || owner != "u1" {
		t.Fatalf("identity owner = %q err=%v, want u1", owner, err)
	}
}

func TestBindOAuthIdentityForSessionAcceptsCurrentSession(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	const sessionID = "link-session-current"
	if err := SaveRefreshToken(ctx, db, sessionID, "u1", time.Now().Add(time.Hour), SessionMeta{SessionID: sessionID}); err != nil {
		t.Fatal(err)
	}
	if err := BindOAuthIdentityForSession(
		ctx, db, "oa_google", "session-subject", "u1", "linked@gmail.com", 0, sessionID,
	); err != nil {
		t.Fatalf("bind from current session: %v", err)
	}
	if owner, err := FindOAuthIdentityUser(ctx, db, "oa_google", "session-subject"); err != nil || owner != "u1" {
		t.Fatalf("identity owner=%q err=%v, want u1", owner, err)
	}
}

func TestBindOAuthIdentityForSessionRejectsPasswordResetState(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	const sessionID = "link-session-reset"
	if err := SaveRefreshToken(ctx, db, sessionID, "u1", time.Now().Add(time.Hour), SessionMeta{SessionID: sessionID}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateUserPassword(ctx, db, "u1", "new-hash"); err != nil {
		t.Fatal(err)
	}
	err := BindOAuthIdentityForSession(
		ctx, db, "oa_google", "reset-stale-subject", "u1", "attacker@gmail.com", 0, sessionID,
	)
	if !errors.Is(err, ErrOAuthLinkSessionExpired) {
		t.Fatalf("bind after password reset error=%v, want ErrOAuthLinkSessionExpired", err)
	}
	if owner, err := FindOAuthIdentityUser(ctx, db, "oa_google", "reset-stale-subject"); !errors.Is(err, ErrNotFound) || owner != "" {
		t.Fatalf("password-reset-stale state bound owner=%q err=%v", owner, err)
	}
}

func TestBindOAuthIdentityForSessionRejectsRevokedFamily(t *testing.T) {
	ctx := context.Background()
	db := setupOAuthDB(t)
	const sessionID = "link-session-revoked"
	if err := SaveRefreshToken(ctx, db, sessionID, "u1", time.Now().Add(time.Hour), SessionMeta{SessionID: sessionID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := RevokeUserSession(ctx, db, "u1", sessionID); err != nil || !ok {
		t.Fatalf("revoke initiating session=(%v,%v), want true,nil", ok, err)
	}
	err := BindOAuthIdentityForSession(
		ctx, db, "oa_google", "revoked-stale-subject", "u1", "attacker@gmail.com", 0, sessionID,
	)
	if !errors.Is(err, ErrOAuthLinkSessionExpired) {
		t.Fatalf("bind after session revoke error=%v, want ErrOAuthLinkSessionExpired", err)
	}
	if owner, err := FindOAuthIdentityUser(ctx, db, "oa_google", "revoked-stale-subject"); !errors.Is(err, ErrNotFound) || owner != "" {
		t.Fatalf("revoked-session state bound owner=%q err=%v", owner, err)
	}
}

func TestBindOAuthIdentityForSessionLinearizesWithRevocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(context.Context, *sql.DB, string) error
	}{
		{
			name: "password reset",
			mutate: func(ctx context.Context, db *sql.DB, _ string) error {
				return UpdateUserPassword(ctx, db, "u1", "new-hash")
			},
		},
		{
			name: "single session revoke",
			mutate: func(ctx context.Context, db *sql.DB, sessionID string) error {
				ok, err := RevokeUserSession(ctx, db, "u1", sessionID)
				if err == nil && !ok {
					return errors.New("session was not revoked")
				}
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := setupOAuthDB(t)
			sessionID := "concurrent-" + tc.name
			if err := SaveRefreshToken(ctx, db, sessionID, "u1", time.Now().Add(time.Hour), SessionMeta{SessionID: sessionID}); err != nil {
				t.Fatal(err)
			}

			start := make(chan struct{})
			bindResult := make(chan error, 1)
			mutationResult := make(chan error, 1)
			go func() {
				<-start
				bindResult <- BindOAuthIdentityForSession(
					ctx, db, "oa_google", "concurrent-subject", "u1", "attacker@gmail.com", 0, sessionID,
				)
			}()
			go func() {
				<-start
				mutationResult <- tc.mutate(ctx, db, sessionID)
			}()
			close(start)

			bindErr := <-bindResult
			if bindErr != nil && !errors.Is(bindErr, ErrOAuthLinkSessionExpired) {
				t.Fatalf("concurrent bind error: %v", bindErr)
			}
			if err := <-mutationResult; err != nil {
				t.Fatalf("concurrent revocation error: %v", err)
			}

			// Whichever transaction acquired the user lock first is allowed to win.
			// Once the reset/revoke has committed, the old authorization state must
			// be a permanent loser for every later identity subject.
			err := BindOAuthIdentityForSession(
				ctx, db, "oa_google", "post-revocation-subject", "u1", "attacker@gmail.com", 0, sessionID,
			)
			if !errors.Is(err, ErrOAuthLinkSessionExpired) {
				t.Fatalf("post-revocation bind error=%v, want ErrOAuthLinkSessionExpired", err)
			}
		})
	}
}
