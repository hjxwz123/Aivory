package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"aivory/server/internal/store"
)

func TestRevokeOtherSessionsPreservesBearerOnlyCurrentFamily(t *testing.T) {
	d := newAuthSecurityDeps(t, "bearer-revoke-others.db")
	user, err := store.CreateUser(t.Context(), d.DB, "bearer@example.test", "Bearer", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	currentAccess := issueBoundTestAccessTokenWithSession(t, d.DB, d.Auth, user, "current-family")
	_ = issueBoundTestAccessTokenWithSession(t, d.DB, d.Auth, user, "other-family")

	h := requireAuth(d, revokeOtherSessionsHandler)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/sessions/revoke-others", nil)
	req.Header.Set("Authorization", "Bearer "+currentAccess)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke others status=%d body=%s", rec.Code, rec.Body.String())
	}

	currentValid, err := store.IsRefreshSessionValid(t.Context(), d.DB, user.ID, "current-family")
	if err != nil {
		t.Fatalf("check current family: %v", err)
	}
	otherValid, err := store.IsRefreshSessionValid(t.Context(), d.DB, user.ID, "other-family")
	if err != nil {
		t.Fatalf("check other family: %v", err)
	}
	if !currentValid || otherValid {
		t.Fatalf("session validity current=%v other=%v, want true/false", currentValid, otherValid)
	}
}
