package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestSetUserEmailAdmin(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-user-email.db"))
	defer db.Close()

	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,token_ver,group_id,group_expires_at,previous_group_id,credits_permanent)
		VALUES('admin','admin@example.test','Admin','hash','admin','active',4,'ug_free',0,'',0)`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status,token_ver,group_id,group_expires_at,previous_group_id,credits_permanent)
		VALUES('u1','before@example.test','Target Name','target-hash','user','active',17,'ug_pro',1900000000,'ug_free',42.5)`)
	mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status)
		VALUES('u2','taken@example.test','Other Name','other-hash','user','active')`)

	c := cache.NewMemory()
	d := Deps{
		DB:    db,
		Cache: c,
		Auth:  authsvc.New("admin-user-email-test-secret-32-bytes", time.Hour, 24*time.Hour, c),
	}
	admin, err := store.FindUserByID(t.Context(), db, "admin")
	if err != nil {
		t.Fatalf("find admin: %v", err)
	}
	token := issueBoundTestAccessToken(t, db, d.Auth, admin)
	c.Set("seen:"+admin.ID, "1", time.Minute)

	mx := newMux()
	mx.handle(http.MethodPost, "/api/admin/users/:id/email", requireAdmin(d, setUserEmailAdmin))
	request := func(id string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/admin/users/"+id+"/email", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		return rec
	}

	t.Run("normalizes email, preserves other fields, and invalidates cache", func(t *testing.T) {
		c.Set(authUserCacheKey(d, "u1"), "stale", time.Minute)
		rec := request("u1", map[string]string{"email": "  NEW@Example.Test  "})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if _, ok := c.Get(authUserCacheKey(d, "u1")); ok {
			t.Fatal("target auth cache entry was not invalidated")
		}

		var got struct {
			Email            string
			Name             string
			PasswordHash     string
			Role             string
			Status           string
			TokenVer         int
			GroupID          string
			GroupExpiresAt   int64
			PreviousGroupID  string
			CreditsPermanent float64
		}
		if err := db.QueryRowContext(context.Background(), `SELECT email,name,password_hash,role,status,token_ver,group_id,group_expires_at,previous_group_id,credits_permanent FROM users WHERE id='u1'`).Scan(
			&got.Email, &got.Name, &got.PasswordHash, &got.Role, &got.Status, &got.TokenVer,
			&got.GroupID, &got.GroupExpiresAt, &got.PreviousGroupID, &got.CreditsPermanent,
		); err != nil {
			t.Fatalf("read updated user: %v", err)
		}
		if got.Email != "new@example.test" {
			t.Errorf("email = %q, want normalized value", got.Email)
		}
		if got.Name != "Target Name" || got.PasswordHash != "target-hash" || got.Role != "user" || got.Status != "active" ||
			got.TokenVer != 17 || got.GroupID != "ug_pro" || got.GroupExpiresAt != 1900000000 ||
			got.PreviousGroupID != "ug_free" || got.CreditsPermanent != 42.5 {
			t.Errorf("email update changed unrelated fields: %+v", got)
		}
	})

	for _, tc := range []struct {
		name       string
		id         string
		email      string
		wantStatus int
		wantError  string
	}{
		{name: "invalid", id: "u1", email: "not-an-email", wantStatus: http.StatusBadRequest, wantError: errInvalidEmail.Error()},
		{name: "empty", id: "u1", email: "", wantStatus: http.StatusBadRequest, wantError: errInvalidEmail.Error()},
		{name: "duplicate", id: "u1", email: "TAKEN@example.test", wantStatus: http.StatusConflict, wantError: errEmailAlreadyRegistered.Error()},
		{name: "missing user", id: "missing", email: "new@example.test", wantStatus: http.StatusNotFound, wantError: errNotFound.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := request(tc.id, map[string]string{"email": tc.email})
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if body["error"] != tc.wantError {
				t.Errorf("error = %q, want %q", body["error"], tc.wantError)
			}
		})
	}
}
