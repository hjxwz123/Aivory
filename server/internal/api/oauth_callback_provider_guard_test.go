package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

func TestOAuthCallbackRejectsProviderMutationDuringUserInfo(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(testing.TB, Deps, *store.OAuthProvider)
	}{
		{
			name: "disabled",
			mutate: func(t testing.TB, d Deps, provider *store.OAuthProvider) {
				t.Helper()
				disabled := false
				if _, err := store.UpdateOAuthProvider(
					context.Background(), d.DB, provider.ID, store.OAuthProviderPatch{Enabled: &disabled},
				); err != nil {
					t.Fatalf("disable provider: %v", err)
				}
			},
		},
		{
			name: "deleted",
			mutate: func(t testing.TB, d Deps, provider *store.OAuthProvider) {
				t.Helper()
				if err := store.DeleteOAuthProvider(context.Background(), d.DB, provider.ID); err != nil {
					t.Fatalf("delete provider: %v", err)
				}
			},
		},
		{
			name: "trust rotated",
			mutate: func(t testing.TB, d Deps, provider *store.OAuthProvider) {
				t.Helper()
				current, err := store.GetOAuthProvider(context.Background(), d.DB, provider.ID)
				if err != nil {
					t.Fatalf("load provider: %v", err)
				}
				userinfoURL := "https://identity.example.test/v2/me"
				next := *current
				next.UserInfoURL = userinfoURL
				if _, err := store.UpdateOAuthProviderCAS(
					context.Background(), d.DB, provider.ID,
					store.OAuthProviderPatch{UserInfoURL: &userinfoURL}, *current,
					oauth.Resolve(toOAuthConfig(current)).SubjectNamespace(),
					oauth.Resolve(toOAuthConfig(&next)).SubjectNamespace(),
				); err != nil {
					t.Fatalf("rotate provider trust: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "oauth-callback-provider-guard.db")
			d.Config.OAuthCallbackBaseURL = "https://app.example.test"
			if _, err := store.CreateUserWithRole(
				t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin",
			); err != nil {
				t.Fatal(err)
			}
			provider := createCallbackOAuth2Provider(t, d, "oa_callback_guard")

			previousExchange := exchangeOAuthAuthorizationCode
			previousFetch := fetchOAuthCallbackUserInfo
			enteredUserInfo := make(chan struct{})
			releaseUserInfo := make(chan struct{})
			exchangeOAuthAuthorizationCode = func(
				context.Context, oauth.Config, string, string, string,
			) (oauth.Tokens, error) {
				return oauth.Tokens{AccessToken: "callback-access-token"}, nil
			}
			fetchOAuthCallbackUserInfo = func(
				ctx context.Context, _ oauth.Config, _ oauth.Tokens, _ string,
			) (oauth.UserInfo, error) {
				close(enteredUserInfo)
				select {
				case <-releaseUserInfo:
					return oauth.UserInfo{
						Subject: "new-subject", Email: "new-user@example.test", Name: "New User",
					}, nil
				case <-ctx.Done():
					return oauth.UserInfo{}, ctx.Err()
				}
			}
			t.Cleanup(func() {
				exchangeOAuthAuthorizationCode = previousExchange
				fetchOAuthCallbackUserInfo = previousFetch
			})

			stateID := "provider-guard-" + strings.ReplaceAll(tc.name, " ", "-")
			binding := stateID + "-browser-binding"
			cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{
				ProviderID: provider.ID, BrowserBinding: binding, SignupIP: "192.0.2.44",
			})
			result := make(chan callbackTestResult, 1)
			go func() {
				rec := runOAuthCallbackForTest(d, provider.ID, stateID, stateID, binding)
				result <- callbackTestResult{status: rec.Code, location: rec.Header().Get("Location")}
			}()

			<-enteredUserInfo
			tc.mutate(t, d, provider)
			close(releaseUserInfo)
			rec := <-result
			if rec.status != http.StatusFound || !strings.Contains(rec.location, "oauth_error=provider_unavailable") {
				t.Fatalf("callback status=%d location=%q", rec.status, rec.location)
			}

			var users, identities, sessions int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE email='new-user@example.test'`).Scan(&users); err != nil {
				t.Fatal(err)
			}
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM oauth_identities WHERE provider_id=?`, provider.ID).Scan(&identities); err != nil {
				t.Fatal(err)
			}
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens`).Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if users != 0 || identities != 0 || sessions != 0 {
				t.Fatalf("stale callback persisted users=%d identities=%d sessions=%d", users, identities, sessions)
			}
		})
	}
}

type callbackTestResult struct {
	status   int
	location string
}

func TestOAuthCallbackTailRejectsStaleUserAuthenticationState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(testing.TB, Deps, *store.User)
	}{
		{
			name: "2FA enabled after user read",
			mutate: func(t testing.TB, d Deps, user *store.User) {
				t.Helper()
				if err := store.SetUserTotp(context.Background(), d.DB, user.ID, "NEW-TOTP-SECRET", true); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "token version changed after user read",
			mutate: func(t testing.TB, d Deps, user *store.User) {
				t.Helper()
				if _, err := d.DB.ExecContext(
					context.Background(), `UPDATE users SET token_ver=token_ver+1 WHERE id=?`, user.ID,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "status changed after user read",
			mutate: func(t testing.TB, d Deps, user *store.User) {
				t.Helper()
				updated, err := store.SetUserStatusGuarded(context.Background(), d.DB, user.ID, "banned")
				if err != nil || !updated {
					t.Fatalf("ban stale callback user=(%v,%v), want true,nil", updated, err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "oauth-callback-user-state.db")
			provider := createCallbackOAuth2Provider(t, d, "oa_callback_user_state")
			user, err := store.CreateUser(t.Context(), d.DB, "callback-user@example.test", "Callback User", "hash")
			if err != nil {
				t.Fatal(err)
			}
			staleUser, err := store.FindUserByID(t.Context(), d.DB, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			guard := store.NewOAuthProviderCallbackGuard(*provider)
			tc.mutate(t, d, user)

			req := httptest.NewRequest(http.MethodGet, "https://app.example.test/api/auth/oauth/callback", nil)
			rec := httptest.NewRecorder()
			completeOAuthLoginWithGuard(d, rec, req, staleUser, "https://app.example.test", &guard)
			if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oauth_error=session_error") {
				t.Fatalf("stale callback status=%d location=%q", rec.Code, rec.Header().Get("Location"))
			}
			if responseCookie(rec, "auth_token") != nil || responseCookie(rec, "refresh_token") != nil {
				t.Fatal("stale callback wrote authentication cookies")
			}
			var sessions int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id=?`, user.ID).Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if sessions != 0 {
				t.Fatalf("stale callback persisted %d refresh sessions", sessions)
			}
		})
	}
}
