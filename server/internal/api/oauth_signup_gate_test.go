package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

func newOAuthGateTestDeps(t *testing.T) Deps {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	// The settings cache (store.GetSetting) is a process-global 15s-TTL cache
	// keyed by setting name only, not by *sql.DB — harmless in production (one
	// DB per process) but it would otherwise leak a prior test's cached
	// signup_open value across these tests' separate temp DBs.
	store.InvalidateConfig()
	return Deps{DB: db, Cache: cache.NewMemory()}
}

// TestResolveOAuthUserBlocksNewSignupWhenClosed covers the §login/register
// hardening gap: a first-time visitor with no existing account or linked
// identity must not be able to route around a closed register form just by
// clicking "Continue with Google" — resolveOAuthUser must refuse to
// provision a brand-new account once the admin sets signup_open=false.
func TestResolveOAuthUserBlocksNewSignupWhenClosed(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	if err := store.SetSetting(d.DB, "signup_open", false); err != nil {
		t.Fatalf("set signup_open: %v", err)
	}
	p := &store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"}
	info := oauth.UserInfo{Subject: "sub-new-1", Email: "brandnew@example.test", EmailVerified: true, Name: "Brand New"}

	u, err := resolveOAuthUser(context.Background(), d, p, info)
	if u != nil {
		t.Fatalf("expected no user to be provisioned, got %+v", u)
	}
	if !errors.Is(err, errSignupClosed) {
		t.Fatalf("err = %v, want errSignupClosed", err)
	}

	// And no account/identity was actually created despite the attempt.
	if got, err := store.FindUserByEmail(context.Background(), d.DB, "brandnew@example.test"); err == nil && got != nil {
		t.Fatalf("a user row was created despite the closed-signup gate: %+v", got)
	}
}

// TestResolveOAuthUserAllowsExistingUserLoginWhenClosed: closed registration
// must only block NEW accounts — an OAuth identity already linked to an
// existing user must still be able to sign IN (that's a login, not a signup).
func TestResolveOAuthUserAllowsExistingUserLoginWhenClosed(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	p := &store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"}

	// First contact while signups are open: provisions + links the identity.
	info := oauth.UserInfo{Subject: "sub-existing-1", Email: "existing@example.test", EmailVerified: true, Name: "Existing User"}
	first, err := resolveOAuthUser(context.Background(), d, p, info)
	if err != nil || first == nil {
		t.Fatalf("initial provisioning failed: %v", err)
	}

	// Admin closes registration afterward.
	if err := store.SetSetting(d.DB, "signup_open", false); err != nil {
		t.Fatalf("set signup_open: %v", err)
	}

	// Same identity signing back in must still succeed (linked-identity path).
	second, err := resolveOAuthUser(context.Background(), d, p, info)
	if err != nil {
		t.Fatalf("existing linked identity was blocked by the signup-closed gate: %v", err)
	}
	if second == nil || second.ID != first.ID {
		t.Fatalf("expected the same existing user, got %+v", second)
	}

	// A second, already-registered (verified) email that hasn't linked THIS
	// provider yet must also still be able to auto-link and log in — that's
	// resolving an existing account, not minting a new one.
	if _, err := store.CreateUser(context.Background(), d.DB, "verified-elsewhere@example.test", "Someone", "hash"); err != nil {
		t.Fatalf("seed second user: %v", err)
	}
	info2 := oauth.UserInfo{Subject: "sub-existing-2", Email: "verified-elsewhere@example.test", EmailVerified: true, Name: "Someone"}
	third, err := resolveOAuthUser(context.Background(), d, p, info2)
	if err != nil {
		t.Fatalf("existing account auto-link was blocked by the signup-closed gate: %v", err)
	}
	if third == nil || third.Email != "verified-elsewhere@example.test" {
		t.Fatalf("expected the existing account to be resolved, got %+v", third)
	}
}

// TestResolveOAuthUserAllowsNewSignupWhenOpen: the default (open) behaviour
// must be completely unaffected by the new gate.
func TestResolveOAuthUserAllowsNewSignupWhenOpen(t *testing.T) {
	d := newOAuthGateTestDeps(t)
	p := &store.OAuthProvider{ID: "google", Kind: "google", Name: "Google"}
	info := oauth.UserInfo{Subject: "sub-open-1", Email: "open@example.test", EmailVerified: true, Name: "Open Signup"}

	u, err := resolveOAuthUser(context.Background(), d, p, info)
	if err != nil {
		t.Fatalf("resolveOAuthUser with open signups: %v", err)
	}
	if u == nil || u.Email != "open@example.test" {
		t.Fatalf("expected a provisioned user, got %+v", u)
	}
}
