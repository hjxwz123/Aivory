package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAccessAndRefreshTokensHaveStrictlySeparatedPurposes(t *testing.T) {
	svc := New("purpose-test-secret-at-least-32-bytes", time.Hour, 24*time.Hour, nil)

	access, _, err := svc.IssueAccessForSession("u_test", "admin", 7, "session-test")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	refresh, _, _, err := svc.IssueRefresh("u_test", 7)
	if err != nil {
		t.Fatalf("issue refresh token: %v", err)
	}

	accessClaims, err := svc.ParseAccess(access)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if accessClaims.TokenUse != accessTokenUse || accessClaims.TV != 7 {
		t.Fatalf("access claims = %+v", accessClaims)
	}
	refreshClaims, err := svc.ParseRefresh(refresh)
	if err != nil {
		t.Fatalf("parse refresh token: %v", err)
	}
	if refreshClaims.TokenUse != refreshTokenUse || refreshClaims.TV != 7 {
		t.Fatalf("refresh claims = %+v", refreshClaims)
	}

	if _, err := svc.ParseAccess(refresh); err == nil {
		t.Fatal("refresh token was accepted by the access-token parser")
	}
	if _, err := svc.ParseRefresh(access); err == nil {
		t.Fatal("access token was accepted by the refresh-token parser")
	}
}

func TestIssueAccessRequiresSessionBinding(t *testing.T) {
	svc := New("session-binding-test-secret-at-least-32-bytes", time.Hour, 24*time.Hour, nil)
	for _, sessionID := range []string{"", " \t\n "} {
		access, _, err := svc.IssueAccessForSession("u_test", "user", 1, sessionID)
		if err == nil {
			t.Fatalf("IssueAccessForSession(%q) succeeded with token %q", sessionID, access)
		}
		if access != "" {
			t.Fatalf("IssueAccessForSession(%q) returned a token on failure", sessionID)
		}
	}
}

func TestParsersRejectLegacyTokensWithoutPurpose(t *testing.T) {
	svc := New("legacy-test-secret-at-least-32-bytes", time.Hour, 24*time.Hour, nil)
	now := time.Now()
	legacyClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "u_legacy",
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        "legacy-jti",
		},
		UID:  "u_legacy",
		Role: "user",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, legacyClaims)
	// Even a token signed with the correct access key is rejected when its
	// protected type header and token_use claim are absent.
	legacy, err := token.SignedString(svc.accessKey)
	if err != nil {
		t.Fatalf("sign legacy token: %v", err)
	}
	if _, err := svc.ParseAccess(legacy); err == nil {
		t.Fatal("legacy token without an explicit purpose was accepted")
	}
}
