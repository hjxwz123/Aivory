package api

import (
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

func TestListUserLoginHistoryAdmin(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-login-history.db"))
	defer db.Close()
	for _, row := range []struct {
		id, email, role string
	}{
		{id: "admin", email: "admin@example.test", role: "admin"},
		{id: "u1", email: "u1@example.test", role: "user"},
		{id: "u2", email: "u2@example.test", role: "user"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status) VALUES(?,?,?,'h',?,'active')`, row.id, row.email, row.id, row.role)
	}
	first, err := store.RecordLoginHistory(t.Context(), db, "u1", store.LoginMethodPassword, store.SessionMeta{IP: "203.0.113.1"})
	if err != nil {
		t.Fatalf("record first history: %v", err)
	}
	second, err := store.RecordLoginHistory(t.Context(), db, "u1", store.LoginMethodOAuth, store.SessionMeta{IP: "203.0.113.2"})
	if err != nil {
		t.Fatalf("record second history: %v", err)
	}
	if _, err := store.RecordLoginHistory(t.Context(), db, "u2", store.LoginMethodPassword, store.SessionMeta{IP: "198.51.100.1"}); err != nil {
		t.Fatalf("record other-user history: %v", err)
	}
	mustExec(t, db, `UPDATE login_histories SET login_at=100 WHERE id=?`, first.ID)
	mustExec(t, db, `UPDATE login_histories SET login_at=200 WHERE id=?`, second.ID)

	c := cache.NewMemory()
	d := Deps{DB: db, Cache: c, Auth: authsvc.New("admin-login-history-test-secret", time.Hour, 24*time.Hour, c)}
	issue := func(userID string) string {
		t.Helper()
		user, err := store.FindUserByID(t.Context(), db, userID)
		if err != nil {
			t.Fatalf("find %s: %v", userID, err)
		}
		token := issueBoundTestAccessToken(t, db, d.Auth, user)
		c.Set("seen:"+user.ID, "1", time.Minute)
		return token
	}
	adminToken, userToken := issue("admin"), issue("u1")
	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/users/:id/login-history", requireAdmin(d, listUserLoginHistoryAdmin))
	get := func(path, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mx.ServeHTTP(rec, req)
		return rec
	}

	if rec := get("/api/admin/users/u1/login-history", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := get("/api/admin/users/u1/login-history", userToken); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := get("/api/admin/users/missing/login-history", adminToken); rec.Code != http.StatusNotFound {
		t.Fatalf("missing-user status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := get("/api/admin/users/u1/login-history?limit=1&offset=1", adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("success status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items  []store.LoginHistory `json:"items"`
		Total  int                  `json:"total"`
		Limit  int                  `json:"limit"`
		Offset int                  `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Total != 2 || body.Limit != 1 || body.Offset != 1 || len(body.Items) != 1 || body.Items[0].ID != first.ID {
		t.Fatalf("response = %+v, want second of two u1 rows", body)
	}
}
