package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/cache"
	"aivory/server/internal/store"
)

func TestListUserMemoriesAdmin(t *testing.T) {
	db := openMigrated(t, filepath.Join(t.TempDir(), "admin-user-memories.db"))
	defer db.Close()

	for _, row := range []struct {
		id, email, role string
	}{
		{id: "admin", email: "admin@example.test", role: "admin"},
		{id: "u1", email: "user@example.test", role: "user"},
		{id: "u2", email: "other@example.test", role: "user"},
		{id: "u-empty", email: "empty@example.test", role: "user"},
	} {
		mustExec(t, db, `INSERT INTO users(id,email,name,password_hash,role,status)
			VALUES(?,?,?,'password-hash',?,'active')`, row.id, row.email, row.id, row.role)
	}

	active := store.Memory{
		ID:               "mem-active",
		UserID:           "u1",
		MemoryText:       "Prefers concise Chinese answers",
		MemoryType:       "preference",
		Slot:             "response_language",
		Value:            "zh-CN",
		Status:           "ACTIVE",
		Confidence:       0.91,
		SourceMessageIDs: []string{"msg-1", "msg-2"},
		Supersedes:       []string{"mem-old"},
		SupersededBy:     []string{},
		AffectedDomains:  []string{"conversation"},
		Reason:           "Explicit user preference",
		ValidFrom:        1700000000,
		ValidUntil:       1800000000,
	}
	if _, err := store.CreateMemory(t.Context(), db, active); err != nil {
		t.Fatalf("create active memory: %v", err)
	}
	if _, err := store.CreateMemory(t.Context(), db, store.Memory{
		ID:         "mem-stale",
		UserID:     "u1",
		MemoryText: "Old preference",
		Status:     "STALE",
		Confidence: 0.75,
	}); err != nil {
		t.Fatalf("create stale memory: %v", err)
	}
	if _, err := store.CreateMemory(t.Context(), db, store.Memory{
		ID:         "mem-other-user",
		UserID:     "u2",
		MemoryText: "Must not be returned",
		Status:     "ACTIVE",
		Confidence: 0.8,
	}); err != nil {
		t.Fatalf("create other user's memory: %v", err)
	}
	mustExec(t, db, `UPDATE memories SET updated_at=100 WHERE id='mem-active'`)
	mustExec(t, db, `UPDATE memories SET updated_at=200 WHERE id='mem-stale'`)
	mustExec(t, db, `UPDATE memories SET updated_at=300 WHERE id='mem-other-user'`)

	c := cache.NewMemory()
	d := Deps{
		DB:    db,
		Cache: c,
		Auth:  authsvc.New("admin-user-memories-test-secret", time.Hour, 24*time.Hour, c),
	}
	issueToken := func(userID string) string {
		t.Helper()
		user, err := store.FindUserByID(t.Context(), db, userID)
		if err != nil {
			t.Fatalf("find %s: %v", userID, err)
		}
		token := issueBoundTestAccessToken(t, db, d.Auth, user)
		c.Set("seen:"+user.ID, "1", time.Minute)
		return token
	}
	adminToken := issueToken("admin")
	userToken := issueToken("u1")

	mx := newMux()
	mx.handle(http.MethodGet, "/api/admin/users/:id/memories", requireAdmin(d, listUserMemoriesAdmin))
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

	t.Run("requires authentication", func(t *testing.T) {
		rec := get("/api/admin/users/u1/memories", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
	})

	t.Run("requires admin role", func(t *testing.T) {
		rec := get("/api/admin/users/u1/memories", userToken)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
		}
	})

	t.Run("returns not found for missing user", func(t *testing.T) {
		rec := get("/api/admin/users/missing/memories", adminToken)
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

	t.Run("returns only the target user's memories", func(t *testing.T) {
		rec := get("/api/admin/users/u1/memories", adminToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var memories []store.Memory
		if err := json.Unmarshal(rec.Body.Bytes(), &memories); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(memories) != 2 {
			t.Fatalf("memory count = %d, want 2; body=%s", len(memories), rec.Body.String())
		}
		if memories[0].ID != "mem-stale" || memories[1].ID != "mem-active" {
			t.Fatalf("memory order = [%s, %s], want [mem-stale, mem-active]", memories[0].ID, memories[1].ID)
		}
		for _, memory := range memories {
			if memory.UserID != "u1" {
				t.Fatalf("returned memory %q for user %q, want u1", memory.ID, memory.UserID)
			}
		}
	})

	t.Run("returns an empty array for a user without memories", func(t *testing.T) {
		rec := get("/api/admin/users/u-empty/memories", adminToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
			t.Fatalf("body = %s, want []", got)
		}
	})

	t.Run("supports the existing status filter and returns full data", func(t *testing.T) {
		rec := get("/api/admin/users/u1/memories?status=ACTIVE", adminToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var memories []store.Memory
		if err := json.Unmarshal(rec.Body.Bytes(), &memories); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(memories) != 1 {
			t.Fatalf("memory count = %d, want 1; body=%s", len(memories), rec.Body.String())
		}
		got := memories[0]
		if got.ID != active.ID || got.UserID != active.UserID || got.MemoryText != active.MemoryText ||
			got.MemoryType != active.MemoryType || got.Slot != active.Slot || got.Value != active.Value ||
			got.Status != active.Status || got.Confidence != active.Confidence || got.Reason != active.Reason ||
			got.ValidFrom != active.ValidFrom || got.ValidUntil != active.ValidUntil {
			t.Fatalf("memory scalar fields mismatch: got %#v, want %#v", got, active)
		}
		if !reflect.DeepEqual(got.SourceMessageIDs, active.SourceMessageIDs) ||
			!reflect.DeepEqual(got.Supersedes, active.Supersedes) ||
			!reflect.DeepEqual(got.SupersededBy, active.SupersededBy) ||
			!reflect.DeepEqual(got.AffectedDomains, active.AffectedDomains) {
			t.Fatalf("memory list fields mismatch: got %#v, want %#v", got, active)
		}
	})
}
