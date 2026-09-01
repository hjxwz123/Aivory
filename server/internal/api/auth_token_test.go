package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/config"
	"aivory/server/internal/store"
)

func TestReadAccessTokenPrefersBearerOverCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/me", nil)
	r.Header.Set("Authorization", "Bearer fresh")
	r.Header.Set("Cookie", "auth_token=stale")

	if got := readAccessToken(r); got != "fresh" {
		t.Fatalf("readAccessToken = %q, want bearer token", got)
	}
}

func TestSessionHandlerTreatsMissingRefreshCookieAsUnauthenticated(t *testing.T) {
	d := newAuthSecurityDeps(t, "session-probe.db")
	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", nil)
	rec := httptest.NewRecorder()

	sessionHandler(d, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var got authSessionResp
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Authenticated || got.User != nil || got.AccessToken != "" {
		t.Fatalf("session response = %+v, want unauthenticated", got)
	}
	if got.AuthPolicy == nil || !got.AuthPolicy.PasswordLoginEnabled || got.AuthPolicy.EntryMode != authEntryLoginPage {
		t.Fatalf("session auth policy = %+v, want default public policy", got.AuthPolicy)
	}
}

func TestSessionHandlerRestoresValidRefreshSession(t *testing.T) {
	d := newAuthSecurityDeps(t, "session-probe-valid.db")
	user, err := store.CreateUser(t.Context(), d.DB, "session-probe@example.test", "Session Probe", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	refresh, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		t.Fatalf("issue refresh: %v", err)
	}
	if err := store.SaveRefreshToken(t.Context(), d.DB, jti, user.ID, refreshExp, store.SessionMeta{}); err != nil {
		t.Fatalf("save refresh: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refresh})
	rec := httptest.NewRecorder()

	sessionHandler(d, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var got authSessionResp
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !got.Authenticated || got.User == nil || got.User.ID != user.ID || got.AccessToken == "" || got.RequestSigningKey == "" || got.ExpiresAt == 0 {
		t.Fatalf("session response = %+v, want authenticated user %q with access token", got, user.ID)
	}
	if got.AuthPolicy == nil || got.AuthPolicy.EntryMode != authEntryLoginPage {
		t.Fatalf("session auth policy = %+v, want coalesced public policy", got.AuthPolicy)
	}
}

func TestRequireAuthUsesDatabaseAuthStateAcrossIndependentCaches(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	if err := store.Seed(db, config.Config{}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	u, err := store.CreateUserWithRole(context.Background(), db, "admin@example.test", "Admin", "hash", "admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	cacheA := cache.NewMemory()
	cacheB := cache.NewMemory()
	dA := Deps{
		DB:    db,
		Cache: cacheA,
		Auth:  authsvc.New("test-secret-at-least-32-chars-long!!", time.Hour, 24*time.Hour, cacheA),
	}
	dB := dA
	dB.Cache = cacheB
	dB.Auth = authsvc.New("test-secret-at-least-32-chars-long!!", time.Hour, 24*time.Hour, cacheB)
	stale := *u
	if b, err := json.Marshal(&stale); err == nil {
		cacheB.Set(authUserCacheKey(dB, u.ID), string(b), time.Minute)
	} else {
		t.Fatalf("marshal stale user: %v", err)
	}
	oldToken := issueBoundTestAccessToken(t, db, dA.Auth, u)
	if err := store.BumpTokenVersion(context.Background(), db, u.ID); err != nil {
		t.Fatalf("bump token version: %v", err)
	}

	called := false
	h := requireAuth(dB, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+oldToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("requireAuth status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}

func TestRequireAdminUsesDatabaseRoleAcrossIndependentCaches(t *testing.T) {
	dA := newAuthSecurityDeps(t, "cross-replica-role.db")
	user, err := store.CreateUserWithRole(t.Context(), dA.DB, "cross-role@example.test", "Cross Role", "hash", "admin")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if _, err := store.CreateUserWithRole(t.Context(), dA.DB, "remaining-admin@example.test", "Remaining Admin", "hash", "admin"); err != nil {
		t.Fatalf("create remaining admin: %v", err)
	}

	dB := dA
	dB.Cache = cache.NewMemory()
	dB.Auth = authsvc.New("auth-security-regression-secret", time.Hour, 24*time.Hour, dB.Cache)
	if raw, err := json.Marshal(user); err != nil {
		t.Fatalf("marshal cached admin: %v", err)
	} else {
		dB.Cache.Set(authUserCacheKey(dB, user.ID), string(raw), time.Minute)
	}
	if err := store.SetUserRole(t.Context(), dA.DB, user.ID, "user"); err != nil {
		t.Fatalf("demote admin: %v", err)
	}
	fresh, err := store.FindUserByID(t.Context(), dA.DB, user.ID)
	if err != nil {
		t.Fatalf("reload demoted user: %v", err)
	}
	token := issueBoundTestAccessToken(t, dA.DB, dA.Auth, fresh)

	called := false
	h := requireAdmin(dB, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("requireAdmin status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}

func TestRequireAuthEnforcesMandatoryInitialPassword(t *testing.T) {
	d := newAuthSecurityDeps(t, "mandatory-initial-password.db")
	user, err := store.CreateUser(t.Context(), d.DB, "oauth-user@example.test", "OAuth User", "throwaway-hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := d.DB.Exec(`UPDATE users SET password_set=0 WHERE id=?`, user.ID); err != nil {
		t.Fatalf("mark user passwordless: %v", err)
	}
	user, err = store.FindUserByID(t.Context(), d.DB, user.ID)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	token := issueBoundTestAccessToken(t, d.DB, d.Auth, user)

	request := func(method, path string) (int, bool, string) {
		called := false
		h := requireAuth(d, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		})
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, called, rec.Body.String()
	}

	if status, called, body := request(http.MethodGet, "/api/conversations"); status != http.StatusPreconditionRequired || called {
		t.Fatalf("protected request status=%d called=%v body=%s", status, called, body)
	}
	for _, allowed := range []struct{ method, path string }{
		{http.MethodGet, "/api/me"},
		{http.MethodPost, "/api/me/password/set"},
	} {
		if status, called, body := request(allowed.method, allowed.path); status != http.StatusNoContent || !called {
			t.Fatalf("allowed request %s %s status=%d called=%v body=%s", allowed.method, allowed.path, status, called, body)
		}
	}
}

func TestRevokedSessionFamilyImmediatelyInvalidatesBoundAccessToken(t *testing.T) {
	d := newAuthSecurityDeps(t, "access-session-family.db")
	user, err := store.CreateUser(t.Context(), d.DB, "session-bound@example.test", "Session Bound", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const sessionID = "stable-session-family"
	if err := store.SaveRefreshToken(
		t.Context(), d.DB, "current-refresh-jti", user.ID, time.Now().Add(time.Hour),
		store.SessionMeta{SessionID: sessionID},
	); err != nil {
		t.Fatalf("save refresh token: %v", err)
	}
	access, _, err := d.Auth.IssueAccessForSession(user.ID, user.Role, user.TokenVer, sessionID)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	called := false
	h := requireAuth(d, func(_ Deps, w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(); rec.Code != http.StatusNoContent || !called {
		t.Fatalf("active family status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
	called = false
	if ok, err := store.RevokeUserSession(t.Context(), d.DB, user.ID, sessionID); err != nil || !ok {
		t.Fatalf("revoke family=(%v,%v), want true,nil", ok, err)
	}
	if rec := request(); rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("revoked family status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}

func TestCachedAuthUserRejectsMismatchedIdentity(t *testing.T) {
	d := newAuthSecurityDeps(t, "cache-identity.db")
	wanted, err := store.CreateUser(t.Context(), d.DB, "wanted@example.test", "Wanted", "hash")
	if err != nil {
		t.Fatalf("create wanted user: %v", err)
	}
	other, err := store.CreateUser(t.Context(), d.DB, "other@example.test", "Other", "hash")
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	raw, err := json.Marshal(other)
	if err != nil {
		t.Fatalf("marshal other user: %v", err)
	}
	d.Cache.Set(authUserCacheKey(d, wanted.ID), string(raw), time.Minute)

	got, err := cachedAuthUser(t.Context(), d, wanted.ID)
	if err != nil {
		t.Fatalf("load cached auth user: %v", err)
	}
	if got.ID != wanted.ID {
		t.Fatalf("cached auth user id=%q, want %q", got.ID, wanted.ID)
	}
}
