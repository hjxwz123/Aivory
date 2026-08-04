package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"aivory/server/internal/oauth"
	"aivory/server/internal/store"
)

func stubOAuthCallbackIdentity(t *testing.T, info oauth.UserInfo) {
	t.Helper()
	previousExchange := exchangeOAuthAuthorizationCode
	previousFetch := fetchOAuthCallbackUserInfo
	exchangeOAuthAuthorizationCode = func(context.Context, oauth.Config, string, string, string) (oauth.Tokens, error) {
		return oauth.Tokens{AccessToken: "test-access-token"}, nil
	}
	fetchOAuthCallbackUserInfo = func(context.Context, oauth.Config, oauth.Tokens, string) (oauth.UserInfo, error) {
		return info, nil
	}
	t.Cleanup(func() {
		exchangeOAuthAuthorizationCode = previousExchange
		fetchOAuthCallbackUserInfo = previousFetch
	})
}

func createCallbackOAuth2Provider(t *testing.T, d Deps, id string) *store.OAuthProvider {
	t.Helper()
	input := store.OAuthProvider{
		ID: id, Kind: "oauth2", Name: "Generic OAuth", ClientID: "client-id", Enabled: true,
		AuthURL: "https://identity.example.test/authorize", TokenURL: "https://identity.example.test/token",
		UserInfoURL: "https://identity.example.test/me",
	}
	input.SubjectNamespace = oauth.Resolve(toOAuthConfig(&input)).SubjectNamespace()
	provider, err := store.CreateOAuthProvider(t.Context(), d.DB, input)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func mustOAuthSubjectForTest(t testing.TB, provider *store.OAuthProvider, raw string) string {
	t.Helper()
	subject, err := oauthSubjectForProvider(provider, raw)
	if err != nil {
		t.Fatalf("namespace OAuth subject: %v", err)
	}
	return subject
}

func namespacedOAuthProviderForTest(provider *store.OAuthProvider) *store.OAuthProvider {
	provider.SubjectNamespace = oauth.Resolve(toOAuthConfig(provider)).SubjectNamespace()
	return provider
}

func TestOAuthCallbackMigratesLegacyRawIdentityForEveryProviderKind(t *testing.T) {
	for _, kind := range []string{"google", "github", "apple", "oidc"} {
		t.Run(kind, func(t *testing.T) {
			d := newAuthSecurityDeps(t, "legacy-"+kind+"-identity.db")
			d.Config.OAuthCallbackBaseURL = "https://app.example.test"
			user, err := store.CreateUser(t.Context(), d.DB, kind+"@example.test", "Legacy User", "hash")
			if err != nil {
				t.Fatal(err)
			}
			input := store.OAuthProvider{
				ID: "oa_legacy_" + kind, Kind: kind, Name: "Legacy " + kind,
				ClientID: "client-id", Enabled: true,
			}
			if kind == "oidc" {
				input.IssuerURL = "https://issuer.example.test"
				input.JWKSURL = "https://issuer.example.test/keys"
				input.AuthURL = "https://issuer.example.test/authorize"
				input.TokenURL = "https://issuer.example.test/token"
			}
			provider, err := store.CreateOAuthProvider(t.Context(), d.DB, input)
			if err != nil {
				t.Fatal(err)
			}
			if provider.SubjectNamespace != "" {
				t.Fatalf("legacy fixture unexpectedly has namespace %q", provider.SubjectNamespace)
			}
			const rawSubject = "legacy-raw-subject"
			if err := store.BindOAuthIdentity(t.Context(), d.DB, provider.ID, rawSubject, user.ID, user.Email); err != nil {
				t.Fatal(err)
			}
			stubOAuthCallbackIdentity(t, oauth.UserInfo{Subject: rawSubject, Email: user.Email, EmailVerified: true})
			stateID := "legacy-" + kind + "-callback"
			binding := stateID + "-browser-binding"
			cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{
				ProviderID: provider.ID, BrowserBinding: binding,
			})
			rec := runOAuthCallbackForTest(d, provider.ID, stateID, stateID, binding)
			if rec.Code != http.StatusFound || strings.Contains(rec.Header().Get("Location"), "oauth_error=") {
				t.Fatalf("legacy callback status=%d location=%q", rec.Code, rec.Header().Get("Location"))
			}

			current, err := store.GetOAuthProvider(t.Context(), d.DB, provider.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantNamespace := oauth.Resolve(toOAuthConfig(current)).SubjectNamespace()
			if current.SubjectNamespace != wantNamespace {
				t.Fatalf("migrated namespace=%q want=%q", current.SubjectNamespace, wantNamespace)
			}
			subject := mustOAuthSubjectForTest(t, current, rawSubject)
			if owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, subject); err != nil || owner != user.ID {
				t.Fatalf("migrated identity owner=%q err=%v, want %q", owner, err, user.ID)
			}
			if owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, rawSubject); owner != "" || !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("legacy raw identity remained owner=%q err=%v", owner, err)
			}
		})
	}
}

func TestGenericOAuth2LoginCallbackNamespacesSubjectPerTrustConfiguration(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth2-callback-namespace.db")
	d.Config.OAuthCallbackBaseURL = "https://app.example.test"
	if _, err := store.CreateUserWithRole(t.Context(), d.DB, "admin@example.test", "Admin", "hash", "admin"); err != nil {
		t.Fatal(err)
	}
	provider := createCallbackOAuth2Provider(t, d, "oa_callback_namespace")
	const rawSubject = "same-provider-subject"
	stubOAuthCallbackIdentity(t, oauth.UserInfo{Subject: rawSubject, Name: "OAuth User"})

	runLogin := func(stateID string) *store.OAuthProvider {
		binding := stateID + "-browser-binding"
		cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{
			ProviderID: provider.ID, BrowserBinding: binding, SignupIP: "192.0.2.40",
		})
		rec := runOAuthCallbackForTest(d, provider.ID, stateID, stateID, binding)
		if rec.Code != http.StatusFound || strings.Contains(rec.Header().Get("Location"), "oauth_error=") {
			t.Fatalf("OAuth2 login callback status=%d location=%q", rec.Code, rec.Header().Get("Location"))
		}
		current, err := store.GetOAuthProvider(t.Context(), d.DB, provider.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}

	firstConfig := runLogin("oauth2-login-first")
	firstSubject := mustOAuthSubjectForTest(t, firstConfig, rawSubject)
	firstOwner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, firstSubject)
	if err != nil {
		t.Fatal(err)
	}
	changedUserInfo := "https://identity.example.test/v2/me"
	changed := *firstConfig
	changed.UserInfoURL = changedUserInfo
	if _, err := store.UpdateOAuthProviderCAS(
		t.Context(), d.DB, provider.ID, store.OAuthProviderPatch{UserInfoURL: &changedUserInfo},
		*firstConfig,
		oauth.Resolve(toOAuthConfig(firstConfig)).SubjectNamespace(),
		oauth.Resolve(toOAuthConfig(&changed)).SubjectNamespace(),
	); err != nil {
		t.Fatal(err)
	}

	secondConfig := runLogin("oauth2-login-second")
	secondSubject := mustOAuthSubjectForTest(t, secondConfig, rawSubject)
	secondOwner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, secondSubject)
	if err != nil {
		t.Fatal(err)
	}
	if firstSubject == secondSubject || firstOwner == secondOwner {
		t.Fatalf("trust change reused identity: first=(%q,%q) second=(%q,%q)", firstSubject, firstOwner, secondSubject, secondOwner)
	}
	for _, check := range []struct {
		provider *store.OAuthProvider
		subject  string
	}{{firstConfig, firstSubject}, {secondConfig, secondSubject}} {
		doubleNamespaced := mustOAuthSubjectForTest(t, check.provider, check.subject)
		if owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, doubleNamespaced); owner != "" ||
			!errors.Is(err, store.ErrNotFound) {
			t.Fatalf("login callback applied subject namespace twice: owner=%q err=%v", owner, err)
		}
	}
	if owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, rawSubject); owner != "" || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("raw OAuth2 subject was persisted: owner=%q err=%v", owner, err)
	}
}

func TestGenericOAuth2LinkCallbackNamespacesSubjectExactlyOnce(t *testing.T) {
	d := newAuthSecurityDeps(t, "oauth2-link-callback-namespace.db")
	d.Config.OAuthCallbackBaseURL = "https://app.example.test"
	user, err := store.CreateUser(t.Context(), d.DB, "link-owner@example.test", "Link Owner", "hash")
	if err != nil {
		t.Fatal(err)
	}
	provider := createCallbackOAuth2Provider(t, d, "oa_link_namespace")
	_, refreshExp, jti, err := d.Auth.IssueRefresh(user.ID, user.TokenVer)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRefreshToken(t.Context(), d.DB, jti, user.ID, refreshExp, store.SessionMeta{
		SessionID: jti, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	const rawSubject = "link-provider-subject"
	stubOAuthCallbackIdentity(t, oauth.UserInfo{Subject: rawSubject, Email: user.Email, EmailVerified: true})
	const stateID = "oauth2-link-callback"
	cacheOAuthFlowStateForTest(t, d, stateID, oauthFlowState{
		ProviderID: provider.ID, LinkUserID: user.ID,
		LinkTokenVer: strconv.Itoa(user.TokenVer), LinkSessionID: jti,
	})
	rec := runOAuthCallbackForTest(d, provider.ID, stateID, "", "")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "linked=") {
		t.Fatalf("OAuth2 link callback status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	current, err := store.GetOAuthProvider(t.Context(), d.DB, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := mustOAuthSubjectForTest(t, current, rawSubject)
	owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, expected)
	if err != nil || owner != user.ID {
		t.Fatalf("namespaced linked identity owner=%q err=%v", owner, err)
	}
	doubleNamespaced := mustOAuthSubjectForTest(t, current, expected)
	if owner, err := store.FindOAuthIdentityUser(t.Context(), d.DB, provider.ID, doubleNamespaced); owner != "" || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link callback applied subject namespace twice: owner=%q err=%v", owner, err)
	}
}
