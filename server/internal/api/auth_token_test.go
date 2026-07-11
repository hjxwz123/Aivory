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

func TestRequireAuthRefreshesStaleTokenVersionCache(t *testing.T) {
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

	c := cache.NewMemory()
	d := Deps{
		DB:    db,
		Cache: c,
		Auth:  authsvc.New("test-secret-at-least-32-chars-long!!", time.Hour, 24*time.Hour, c),
	}
	stale := *u
	if b, err := json.Marshal(&stale); err == nil {
		c.Set(authUserCacheKey(d, u.ID), string(b), time.Minute)
	} else {
		t.Fatalf("marshal stale user: %v", err)
	}
	if err := store.BumpTokenVersion(context.Background(), db, u.ID); err != nil {
		t.Fatalf("bump token version: %v", err)
	}
	fresh, err := store.FindUserByID(context.Background(), db, u.ID)
	if err != nil {
		t.Fatalf("read fresh user: %v", err)
	}
	token, _, err := d.Auth.IssueAccess(fresh.ID, fresh.Role, fresh.TokenVer)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	called := false
	h := requireAuth(d, func(_ Deps, w http.ResponseWriter, r *http.Request) {
		called = true
		if got := authUser(r).TokenVer; got != fresh.TokenVer {
			t.Fatalf("auth user token_ver = %d, want %d", got, fresh.TokenVer)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("requireAuth status=%d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}
