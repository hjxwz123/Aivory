package api

import (
	"context"
	"database/sql"
	"testing"

	authsvc "aivory/server/internal/auth"
	"aivory/server/internal/store"
)

// issueBoundTestAccessToken mirrors every production access-token issuance:
// the token is tied to a persisted refresh-session family so requireAuth's
// session revocation check remains meaningful in HTTP tests.
func issueBoundTestAccessToken(t testing.TB, db *sql.DB, svc *authsvc.Service, user *store.User) string {
	t.Helper()
	_, refreshExp, jti, err := svc.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		t.Fatalf("issue test refresh token: %v", err)
	}
	if err := store.SaveRefreshToken(context.Background(), db, jti, user.ID, refreshExp, store.SessionMeta{SessionID: jti}); err != nil {
		t.Fatalf("save test refresh token: %v", err)
	}
	token, _, err := svc.IssueAccessForSession(user.ID, user.Role, user.TokenVer, jti)
	if err != nil {
		t.Fatalf("issue bound test access token: %v", err)
	}
	return token
}

func issueBoundTestAccessTokenWithSession(t testing.TB, db *sql.DB, svc *authsvc.Service, user *store.User, sessionID string) string {
	t.Helper()
	_, refreshExp, jti, err := svc.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		t.Fatalf("issue test refresh token: %v", err)
	}
	if sessionID == "" {
		sessionID = jti
	}
	if err := store.SaveRefreshToken(context.Background(), db, jti, user.ID, refreshExp, store.SessionMeta{SessionID: sessionID}); err != nil {
		t.Fatalf("save test refresh token: %v", err)
	}
	token, _, err := svc.IssueAccessForSession(user.ID, user.Role, user.TokenVer, sessionID)
	if err != nil {
		t.Fatalf("issue bound test access token: %v", err)
	}
	return token
}
