package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestGetUserAdmin(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-user-detail.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status)
		VALUES('admin','admin@example.test','Admin','admin-password-hash','admin','active')`)
	mustExec(t, db, `INSERT INTO users(
		id,email,name,password_hash,role,status,token_ver,settings,group_id,
		totp_secret,totp_enabled,password_set,password_changed_at,credits_permanent,
		sort_order,created_at
	) VALUES(
		'u1','user@example.test','Test User','target-password-hash','user','active',17,
		'{"theme":"dark"}','ug_free','target-totp-secret',1,1,1700000000,42.5,3,1690000000
	)`)

	c := cache.NewMemory()
	d := Deps{
		DB:    db,
		Cache: c,
		Auth:  authsvc.New("admin-user-detail-test-secret-32-bytes", time.Hour, 24*time.Hour, c),
	}
	admin, err := store.FindUserByID(t.Context(), db, "admin")
	if err != nil {
		t.Fatalf("find admin: %v", err)
	}
	token, _, err := d.Auth.IssueAccess(admin.ID, admin.Role, admin.TokenVer)
	if err != nil {
		t.Fatalf("issue admin access token: %v", err)
	}
	c.Set("seen:"+admin.ID, "1", time.Minute)

	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/users/:id", requireAdmin(d, getUserAdmin))
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		return rec
	}

	t.Run("success without sensitive fields", func(t *testing.T) {
		rec := get("/api/admin/users/u1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		for key, want := range map[string]any{
			"id":           "u1",
			"email":        "user@example.test",
			"name":         "Test User",
			"role":         "user",
			"status":       "active",
			"group_id":     "ug_free",
			"totp_enabled": true,
		} {
			if got := body[key]; got != want {
				t.Errorf("%s = %#v, want %#v", key, got, want)
			}
		}
		for _, key := range []string{"password", "password_hash", "token_ver", "totp_secret"} {
			if _, ok := body[key]; ok {
				t.Errorf("response contains sensitive field %q", key)
			}
		}
		responseText := rec.Body.String()
		for _, secret := range []string{"target-password-hash", "target-totp-secret"} {
			if strings.Contains(responseText, secret) {
				t.Errorf("response leaked sensitive value %q", secret)
			}
		}
	})

	t.Run("not found", func(t *testing.T) {
		rec := get("/api/admin/users/missing")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body["error"] != "not found" {
			t.Fatalf("error = %q, want %q", body["error"], "not found")
		}
	})
}
