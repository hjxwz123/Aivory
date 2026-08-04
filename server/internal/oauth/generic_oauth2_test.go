package oauth

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type oauth2RoundTripFunc func(*http.Request) (*http.Response, error)

func (fn oauth2RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func useGenericOAuth2TestClient(t *testing.T, fn oauth2RoundTripFunc) {
	t.Helper()
	previous := genericOAuth2HTTPClient
	genericOAuth2HTTPClient = &http.Client{Transport: fn}
	t.Cleanup(func() { genericOAuth2HTTPClient = previous })
}

func oauth2JSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGenericOAuth2AuthorizationCodeExchangeUsesConfiguredHTTPSURL(t *testing.T) {
	useGenericOAuth2TestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != "https://identity.example.test/oauth/token" {
			t.Fatalf("token request = %s %s", req.Method, req.URL)
		}
		if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("token content type = %q", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"grant_type": "authorization_code", "code": "auth-code", "redirect_uri": "https://app.example.test/callback",
			"client_id": "client-id", "client_secret": "client-secret", "code_verifier": "pkce-verifier",
		} {
			if got := form.Get(key); got != want {
				t.Fatalf("token form %s = %q, want %q", key, got, want)
			}
		}
		return oauth2JSONResponse(`{"access_token":"access-token","token_type":"Bearer"}`), nil
	})

	cfg := Config{
		Kind: "oauth2", ClientID: "client-id", ClientSecret: "client-secret",
		TokenURL: "https://identity.example.test/oauth/token",
	}
	tokens, err := cfg.Exchange(context.Background(), "https://app.example.test/callback", "auth-code", "pkce-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tokens.AccessToken != "access-token" || tokens.IDToken != "" {
		t.Fatalf("tokens = %+v", tokens)
	}
}

func TestOAuthClientSecretBasicFormEncodesCredentials(t *testing.T) {
	requests := 0
	useGenericOAuth2TestClient(t, func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_client"}`)),
			}, nil
		}
		encoded := strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		want := url.QueryEscape("client:id +") + ":" + url.QueryEscape("s: e%")
		if string(raw) != want {
			t.Fatalf("client_secret_basic credentials=%q, want %q", raw, want)
		}
		return oauth2JSONResponse(`{"access_token":"access-token"}`), nil
	})
	cfg := Config{
		Kind: "oauth2", ClientID: "client:id +", ClientSecret: "s: e%",
		TokenURL: "https://identity.example.test/token",
	}
	if _, err := cfg.Exchange(context.Background(), "https://app.example.test/callback", "code", "verifier"); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("token endpoint requests=%d, want post then basic fallback", requests)
	}
}

func TestGenericOAuth2FetchUserInfoUsesBearerAndStandardFieldMapping(t *testing.T) {
	useGenericOAuth2TestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != "https://identity.example.test/api/user?format=json" {
			t.Fatalf("userinfo request = %s %s", req.Method, req.URL)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("userinfo authorization = %q", got)
		}
		if got := req.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("userinfo accept = %q", got)
		}
		return oauth2JSONResponse(`{
			"sub":"provider-user-1",
			"id":"must-not-win",
			"email":"user@example.test",
			"email_verified":"true",
			"name":"Example User",
			"picture":"https://cdn.example.test/avatar.png"
		}`), nil
	})

	cfg := Config{Kind: "oauth2", UserInfoURL: "https://identity.example.test/api/user?format=json"}
	info, err := cfg.FetchUserInfo(context.Background(), Tokens{
		AccessToken: "access-token",
		IDToken:     "attacker-controlled.payload.must-not-be-decoded",
	}, "must-not-be-used")
	if err != nil {
		t.Fatalf("FetchUserInfo: %v", err)
	}
	if info.Subject != "provider-user-1" || info.Email != "user@example.test" || !info.EmailVerified ||
		info.Name != "Example User" || info.AvatarURL != "https://cdn.example.test/avatar.png" {
		t.Fatalf("userinfo mapping = %+v", info)
	}
}

func TestGenericOAuth2UserInfoFallsBackFromSubToID(t *testing.T) {
	useGenericOAuth2TestClient(t, func(_ *http.Request) (*http.Response, error) {
		return oauth2JSONResponse(`{"id":12345,"email":"unverified@example.test"}`), nil
	})
	cfg := Config{Kind: "oauth2", UserInfoURL: "https://identity.example.test/me"}
	info, err := cfg.FetchUserInfo(context.Background(), Tokens{AccessToken: "access-token"}, "")
	if err != nil {
		t.Fatalf("FetchUserInfo: %v", err)
	}
	if info.Subject != "12345" || info.Email != "unverified@example.test" || info.EmailVerified {
		t.Fatalf("userinfo fallback = %+v", info)
	}
}

func TestGenericOAuth2PreservesAdjacentLargeNumericSubjects(t *testing.T) {
	for _, field := range []string{"sub", "id"} {
		t.Run(field, func(t *testing.T) {
			values := []string{"9007199254740992", "9007199254740993"}
			request := 0
			useGenericOAuth2TestClient(t, func(_ *http.Request) (*http.Response, error) {
				body := fmt.Sprintf(`{"%s":%s}`, field, values[request])
				request++
				return oauth2JSONResponse(body), nil
			})
			cfg := Config{Kind: "oauth2", UserInfoURL: "https://identity.example.test/me"}
			got := make([]string, 0, len(values))
			for range values {
				info, err := cfg.FetchUserInfo(context.Background(), Tokens{AccessToken: "access-token"}, "")
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, info.Subject)
			}
			if got[0] != values[0] || got[1] != values[1] || got[0] == got[1] {
				t.Fatalf("large numeric %s subjects = %#v, want %#v", field, got, values)
			}
		})
	}
}

func TestGenericOAuth2UserInfoRejectsMultipleJSONValues(t *testing.T) {
	useGenericOAuth2TestClient(t, func(_ *http.Request) (*http.Response, error) {
		return oauth2JSONResponse(`{"sub":"first"} {"sub":"second"}`), nil
	})
	cfg := Config{Kind: "oauth2", UserInfoURL: "https://identity.example.test/me"}
	if _, err := cfg.FetchUserInfo(context.Background(), Tokens{AccessToken: "access-token"}, ""); err == nil ||
		!strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("multiple userinfo JSON values error = %v", err)
	}
}

func TestGenericOAuth2RequiresAccessTokenAndStableSubject(t *testing.T) {
	cfg := Config{Kind: "oauth2", UserInfoURL: "https://identity.example.test/me"}
	if _, err := cfg.FetchUserInfo(context.Background(), Tokens{IDToken: "not-an-identity-token"}, ""); err == nil ||
		!strings.Contains(err.Error(), "no access_token") {
		t.Fatalf("missing access_token error = %v", err)
	}

	useGenericOAuth2TestClient(t, func(_ *http.Request) (*http.Response, error) {
		return oauth2JSONResponse(`{"email":"user@example.test","email_verified":true}`), nil
	})
	if _, err := cfg.FetchUserInfo(context.Background(), Tokens{AccessToken: "access-token"}, ""); err == nil ||
		!strings.Contains(err.Error(), "missing subject") {
		t.Fatalf("missing subject error = %v", err)
	}
}

func TestGenericOAuth2UsesPKCEWithoutOIDCNonce(t *testing.T) {
	if !UsesPKCE("oauth2") || UsesIDToken("oauth2") {
		t.Fatalf("oauth2 protocol flags: PKCE=%v IDToken=%v", UsesPKCE("oauth2"), UsesIDToken("oauth2"))
	}
	u, err := url.Parse((Config{
		Kind: "oauth2", AuthURL: "https://identity.example.test/oauth/authorize", ClientID: "client-id", Scopes: "profile email",
	}).AuthCodeURL("https://app.example.test/callback", "state", "challenge", "must-not-leak"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("code_challenge") != "challenge" || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("PKCE query = %s", u.RawQuery)
	}
	if nonce := u.Query().Get("nonce"); nonce != "" {
		t.Fatalf("generic oauth2 authorization URL contains OIDC nonce %q", nonce)
	}
	withoutScopes, err := url.Parse((Config{
		Kind: "oauth2", AuthURL: "https://identity.example.test/oauth/authorize", ClientID: "client-id",
	}).AuthCodeURL("https://app.example.test/callback", "state", "challenge", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, present := withoutScopes.Query()["scope"]; present {
		t.Fatalf("empty generic OAuth2 scope was sent: %s", withoutScopes.RawQuery)
	}
}

func TestGenericOAuth2SubjectNamespaceChangesWithTrustConfiguration(t *testing.T) {
	base := Config{
		Kind: "oauth2", ClientID: "client-id", AuthURL: "https://identity.example.test/authorize",
		TokenURL: "https://identity.example.test/token", UserInfoURL: "https://identity.example.test/me",
	}
	subject := base.NamespaceSubject("provider-subject")
	if subject == "provider-subject" || !strings.HasSuffix(subject, ":provider-subject") {
		t.Fatalf("namespaced subject = %q", subject)
	}
	changed := base
	changed.UserInfoURL = "https://identity.example.test/v2/me"
	if got := changed.NamespaceSubject("provider-subject"); got == subject {
		t.Fatalf("trust configuration change reused subject namespace %q", got)
	}
	oidc := Config{
		Kind: "oidc", ClientID: "client-id", IssuerURL: "https://issuer.example.test",
		JWKSURL: "https://issuer.example.test/keys", AuthURL: "https://issuer.example.test/authorize",
		TokenURL: "https://issuer.example.test/token",
	}
	oidcSubject := oidc.NamespaceSubject("provider-subject")
	if oidcSubject == "provider-subject" || !strings.HasSuffix(oidcSubject, ":provider-subject") {
		t.Fatalf("OIDC subject was not namespaced: %q", oidcSubject)
	}
	changedOIDC := oidc
	changedOIDC.IssuerURL = "https://issuer-v2.example.test"
	changedOIDC.JWKSURL = "https://issuer-v2.example.test/keys"
	if got := changedOIDC.NamespaceSubject("provider-subject"); got == oidcSubject {
		t.Fatalf("OIDC trust configuration change reused subject namespace %q", got)
	}
	if got := (Config{Kind: "google", ClientID: "client-id"}).NamespaceSubject("provider-subject"); got == oidcSubject {
		t.Fatalf("built-in/custom kind switch reused subject namespace %q", got)
	}
}

func TestGenericOAuth2HTTPClientRejectsEndpointRedirects(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://other.example.test/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := http.NewRequest(http.MethodGet, "https://identity.example.test/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := genericOAuth2HTTPClient.CheckRedirect(request, []*http.Request{previous}); err == nil ||
		!strings.Contains(err.Error(), "redirects are not allowed") {
		t.Fatalf("generic OAuth2 redirect error = %v", err)
	}
}

func TestGenericOIDCRejectsPrivateTokenAndJWKSEndpoints(t *testing.T) {
	cfg := Config{
		Kind: "oidc", ClientID: "client-id", ClientSecret: "secret",
		TokenURL: "https://127.0.0.1/token", JWKSURL: "https://169.254.169.254/keys",
	}
	if _, err := cfg.Exchange(context.Background(), "https://app.example.test/callback", "code", "verifier"); err == nil ||
		!strings.Contains(err.Error(), "private or reserved") {
		t.Fatalf("private OIDC token endpoint error = %v", err)
	}
	if _, err := cfg.loadVerificationJWKs(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "private or reserved") {
		t.Fatalf("private OIDC JWKS endpoint error = %v", err)
	}
}

func TestValidateHTTPSProviderEndpointRejectsSSRFPrimitives(t *testing.T) {
	valid := []string{
		"https://identity.example.test/oauth/token",
		"https://identity.example.test:8443/api/user?format=json",
	}
	for _, raw := range valid {
		if err := ValidateHTTPSProviderEndpoint(raw); err != nil {
			t.Errorf("ValidateHTTPSProviderEndpoint(%q): %v", raw, err)
		}
	}

	invalid := []string{
		"http://identity.example.test/token",
		"https://user:password@identity.example.test/token",
		"https://identity.example.test/token#fragment",
		"https://localhost/token",
		"https://login.localhost/token",
		"https://127.0.0.1/token",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.1/token",
		"https://[::1]/token",
	}
	for _, raw := range invalid {
		if err := ValidateHTTPSProviderEndpoint(raw); err == nil {
			t.Errorf("ValidateHTTPSProviderEndpoint(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestOIDCDoesNotFallBackToOAuth2UserInfo(t *testing.T) {
	cfg := Config{
		Kind: "oidc", UserInfoURL: "https://identity.example.test/me",
		IssuerURL: "https://identity.example.test", JWKSURL: "https://identity.example.test/keys",
	}
	if _, err := cfg.FetchUserInfo(context.Background(), Tokens{AccessToken: "access-token"}, "nonce"); err == nil ||
		!strings.Contains(err.Error(), "no id_token") {
		t.Fatalf("OIDC missing id_token error = %v", err)
	}
}
