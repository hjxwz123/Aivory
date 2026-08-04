package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

func TestOAuthLinkStartBindsStateToTokenVersionAndSessionFamily(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth-link-start-state.db")
	user, err := store.CreateUser(t.Context(), d.DB, "link-start@example.test", "Link Start", "hash")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := store.CreateOAuthProvider(t.Context(), d.DB, store.OAuthProvider{
		ID: "oa_link_start", Kind: "github", Name: "GitHub Link", ClientID: "client-id", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	access := issueBoundTestAccessToken(t, d.DB, d.Auth, user)
	claims, err := d.Auth.ParseAccess(access)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "https://app.example.test/api/me/identities/"+provider.ID+"/link", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	NewRouter(d).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("link start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	authorizeURL, err := url.Parse(response.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	stateID := authorizeURL.Query().Get("state")
	raw, ok := d.Cache.Get("oauth:state:" + stateID)
	if stateID == "" || !ok {
		t.Fatalf("link state id=%q cached=%v", stateID, ok)
	}
	var state oauthFlowState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	if state.LinkUserID != user.ID || state.LinkTokenVer != strconv.Itoa(user.TokenVer) || state.LinkSessionID != claims.SessionID {
		t.Fatalf("link state=%+v claims=%+v", state, claims)
	}
}

func TestOAuthLinkStateAcceptsUnchangedAuthorizingSession(t *testing.T) {
	d, user, state := newOAuthLinkSecurityState(t, "oauth-link-current.db")
	info := oauth.UserInfo{Subject: "current-link-subject", Email: "linked@example.test"}
	provider := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "oa_security", Kind: "google"})
	if err := bindOAuthIdentityFromState(context.Background(), d, state, provider, info); err != nil {
		t.Fatalf("bind current link state: %v", err)
	}
	subject := mustOAuthSubjectForTest(t, provider, info.Subject)
	if owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, "oa_security", subject); err != nil || owner != user.ID {
		t.Fatalf("identity owner=%q err=%v, want %q", owner, err, user.ID)
	}
}

func TestOAuthLinkStateFailsClosedAfterAuthorizationContextRevocation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(testing.TB, Deps, *store.User, oauthFlowState)
	}{
		{
			name: "password reset",
			mutate: func(t testing.TB, d Deps, user *store.User, _ oauthFlowState) {
				t.Helper()
				if err := store.UpdateUserPassword(context.Background(), d.DB, user.ID, "new-hash"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "initiating session revoked",
			mutate: func(t testing.TB, d Deps, user *store.User, state oauthFlowState) {
				t.Helper()
				ok, err := store.RevokeUserSession(context.Background(), d.DB, user.ID, state.LinkSessionID)
				if err != nil || !ok {
					t.Fatalf("revoke session=(%v,%v), want true,nil", ok, err)
				}
			},
		},
		{
			name: "account banned",
			mutate: func(t testing.TB, d Deps, user *store.User, _ oauthFlowState) {
				t.Helper()
				ok, err := store.SetUserStatusGuarded(context.Background(), d.DB, user.ID, "banned")
				if err != nil || !ok {
					t.Fatalf("ban account=(%v,%v), want true,nil", ok, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, user, state := newOAuthLinkSecurityState(t, "oauth-link-revoked.db")
			tc.mutate(t, d, user, state)
			info := oauth.UserInfo{Subject: "attacker-link-subject", Email: "attacker@example.test"}
			provider := namespacedOAuthProviderForTest(&store.OAuthProvider{ID: "oa_security", Kind: "google"})
			err := bindOAuthIdentityFromState(context.Background(), d, state, provider, info)
			if !errors.Is(err, store.ErrOAuthLinkSessionExpired) {
				t.Fatalf("stale link state error=%v, want ErrOAuthLinkSessionExpired", err)
			}
			if owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, "oa_security", info.Subject); !errors.Is(err, store.ErrNotFound) || owner != "" {
				t.Fatalf("stale link state bound owner=%q err=%v", owner, err)
			}
		})
	}
}

func TestOAuthLinkStateRejectsLegacyMissingRevocationContext(t *testing.T) {
	d, user, _ := newOAuthLinkSecurityState(t, "oauth-link-legacy.db")
	legacy := oauthFlowState{ProviderID: "oa_security", LinkUserID: user.ID}
	err := bindOAuthIdentityFromState(context.Background(), d, legacy, &store.OAuthProvider{ID: "oa_security", Kind: "google"}, oauth.UserInfo{
		Subject: "legacy-link-subject", Email: "attacker@example.test",
	})
	if !errors.Is(err, store.ErrOAuthLinkSessionExpired) {
		t.Fatalf("legacy link state error=%v, want ErrOAuthLinkSessionExpired", err)
	}
}

func newOAuthLinkSecurityState(t *testing.T, dbName string) (Deps, *store.User, oauthFlowState) {
	t.Helper()
	d := newAuthSecurityDeps(t, dbName)
	user, err := store.CreateUser(context.Background(), d.DB, "oauth-link@example.test", "OAuth Link", "hash")
	if err != nil {
		t.Fatal(err)
	}
	_, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRefreshToken(
		context.Background(), d.DB, jti, user.ID, refreshExp,
		store.SessionMeta{SessionID: jti, CreatedAt: time.Now().Unix()},
	); err != nil {
		t.Fatal(err)
	}
	return d, user, oauthFlowState{
		ProviderID: "oa_security", LinkUserID: user.ID,
		LinkTokenVer: strconv.Itoa(user.TokenVer), LinkSessionID: jti,
	}
}
