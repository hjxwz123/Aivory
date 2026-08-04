package oauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func useOIDCJWKSTestResponse(t *testing.T, jwksURL string, body []byte) {
	t.Helper()
	oidcJWKCache.Lock()
	delete(oidcJWKCache.Documents, jwksURL)
	oidcJWKCache.Unlock()
	useGenericOAuth2TestClient(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != jwksURL {
			t.Fatalf("JWKS request = %s %s, want GET %s", req.Method, req.URL, jwksURL)
		}
		resp := oauth2JSONResponse(string(body))
		resp.Header.Set("Cache-Control", "public, max-age=60")
		return resp, nil
	})
}

func TestFetchUserInfoVerifiesOIDCIDToken(t *testing.T) {
	privateKey := mustRSAKey(t)
	issuer := "https://issuer.example.test"
	clientID := "client-123"
	nonce := "nonce-123"
	jwksURL := "https://jwks-verify.example.test/keys"
	jwksBody, _ := json.Marshal(testJWKS(privateKey, "key-1", "RS256"))
	useOIDCJWKSTestResponse(t, jwksURL, jwksBody)
	cfg := Config{Kind: "oidc", ClientID: clientID, IssuerURL: issuer, JWKSURL: jwksURL}
	claims := validTestIDTokenClaims(issuer, clientID, nonce)
	raw := signTestIDToken(t, privateKey, "key-1", jwt.SigningMethodRS256, claims)
	info, err := cfg.FetchUserInfo(context.Background(), Tokens{IDToken: raw}, nonce)
	if err != nil {
		t.Fatalf("FetchUserInfo: %v", err)
	}
	if info.Subject != "subject-1" || info.Email != "user@example.test" || !info.EmailVerified ||
		info.Name != "Test User" || info.AvatarURL != "https://cdn.example.test/avatar.png" {
		t.Fatalf("verified user info = %+v", info)
	}
}

func TestFetchUserInfoRejectsInvalidOIDCIDTokenClaims(t *testing.T) {
	privateKey := mustRSAKey(t)
	otherKey := mustRSAKey(t)
	issuer := "https://issuer.example.test"
	clientID := "client-123"
	nonce := "nonce-123"
	jwksURL := "https://jwks-invalid-claims.example.test/keys"
	jwksBody, _ := json.Marshal(testJWKS(privateKey, "key-1", "RS256"))
	useOIDCJWKSTestResponse(t, jwksURL, jwksBody)
	cfg := Config{Kind: "oidc", ClientID: clientID, IssuerURL: issuer, JWKSURL: jwksURL}

	tests := []struct {
		name        string
		mutate      func(jwt.MapClaims)
		signingKey  *rsa.PrivateKey
		expectNonce string
	}{
		{name: "wrong issuer", mutate: func(c jwt.MapClaims) { c["iss"] = "https://attacker.example" }},
		{name: "wrong audience", mutate: func(c jwt.MapClaims) { c["aud"] = "other-client" }},
		{name: "wrong authorized party", mutate: func(c jwt.MapClaims) { c["azp"] = "other-client" }},
		{name: "multiple audiences without authorized party", mutate: func(c jwt.MapClaims) {
			c["aud"] = []string{clientID, "other-client"}
			delete(c, "azp")
		}},
		{name: "expired", mutate: func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-5 * time.Minute).Unix() }},
		{name: "missing expiration", mutate: func(c jwt.MapClaims) { delete(c, "exp") }},
		{name: "future issued at", mutate: func(c jwt.MapClaims) { c["iat"] = time.Now().Add(5 * time.Minute).Unix() }},
		{name: "missing issued at", mutate: func(c jwt.MapClaims) { delete(c, "iat") }},
		{name: "missing subject", mutate: func(c jwt.MapClaims) { delete(c, "sub") }},
		{name: "wrong nonce claim", mutate: func(c jwt.MapClaims) { c["nonce"] = "wrong" }},
		{name: "wrong expected nonce", expectNonce: "wrong"},
		{name: "untrusted signature", signingKey: otherKey},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := validTestIDTokenClaims(issuer, clientID, nonce)
			if tc.mutate != nil {
				tc.mutate(claims)
			}
			key := tc.signingKey
			if key == nil {
				key = privateKey
			}
			expectedNonce := tc.expectNonce
			if expectedNonce == "" {
				expectedNonce = nonce
			}
			raw := signTestIDToken(t, key, "key-1", jwt.SigningMethodRS256, claims)
			if _, err := cfg.FetchUserInfo(context.Background(), Tokens{IDToken: raw}, expectedNonce); err == nil {
				t.Fatal("invalid id_token was accepted")
			}
		})
	}
}

func TestFetchUserInfoRejectsUnsafeIDTokenAlgorithm(t *testing.T) {
	claims := validTestIDTokenClaims("https://issuer.example.test", "client-123", "nonce-123")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = "key-1"
	raw, err := token.SignedString([]byte("attacker-controlled-secret"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Kind: "oidc", ClientID: "client-123", IssuerURL: "https://issuer.example.test", JWKSURL: "https://jwks-algorithm.example.test/keys"}
	if _, err := cfg.FetchUserInfo(context.Background(), Tokens{IDToken: raw}, "nonce-123"); err == nil {
		t.Fatal("HS256 id_token was accepted")
	}
}

func TestOIDCAuthorizationURLBindsNonce(t *testing.T) {
	oidcURL, err := url.Parse((Config{Kind: "oidc", AuthURL: "https://issuer.example/authorize", ClientID: "cid", Scopes: "openid"}).AuthCodeURL(
		"https://app.example/callback", "state", "challenge", "nonce",
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := oidcURL.Query().Get("nonce"); got != "nonce" {
		t.Fatalf("OIDC nonce = %q, want nonce", got)
	}
	githubURL, err := url.Parse((Config{Kind: "github", AuthURL: "https://github.example/authorize", ClientID: "cid"}).AuthCodeURL(
		"https://app.example/callback", "state", "", "must-not-leak",
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := githubURL.Query().Get("nonce"); got != "" {
		t.Fatalf("GitHub authorization URL unexpectedly contains nonce %q", got)
	}
}

func TestResolveUsesTrustedBuiltInOIDCEndpoints(t *testing.T) {
	google := Resolve(Config{
		Kind: "google", IssuerURL: "https://attacker.example/issuer", JWKSURL: "https://attacker.example/keys",
		AuthURL: "https://attacker.example/authorize", TokenURL: "https://attacker.example/token", UserInfoURL: "https://attacker.example/userinfo",
	})
	if google.IssuerURL != "https://accounts.google.com" || google.JWKSURL != "https://www.googleapis.com/oauth2/v3/certs" {
		t.Fatalf("Google OIDC endpoints = issuer %q jwks %q", google.IssuerURL, google.JWKSURL)
	}
	if google.AuthURL != "https://accounts.google.com/o/oauth2/v2/auth" || google.TokenURL != "https://oauth2.googleapis.com/token" || google.UserInfoURL != "https://openidconnect.googleapis.com/v1/userinfo" {
		t.Fatalf("Google protocol endpoints = auth %q token %q userinfo %q", google.AuthURL, google.TokenURL, google.UserInfoURL)
	}
	apple := Resolve(Config{
		Kind: "apple", IssuerURL: "https://attacker.example/issuer", JWKSURL: "https://attacker.example/keys",
		AuthURL: "https://attacker.example/authorize", TokenURL: "https://attacker.example/token",
	})
	if apple.IssuerURL != "https://appleid.apple.com" || apple.JWKSURL != "https://appleid.apple.com/auth/keys" {
		t.Fatalf("Apple OIDC endpoints = issuer %q jwks %q", apple.IssuerURL, apple.JWKSURL)
	}
	if apple.AuthURL != "https://appleid.apple.com/auth/authorize" || apple.TokenURL != "https://appleid.apple.com/auth/token" {
		t.Fatalf("Apple protocol endpoints = auth %q token %q", apple.AuthURL, apple.TokenURL)
	}
	github := Resolve(Config{
		Kind: "github", AuthURL: "https://attacker.example/authorize",
		TokenURL: "https://attacker.example/token", UserInfoURL: "https://attacker.example/userinfo",
	})
	if github.AuthURL != "https://github.com/login/oauth/authorize" || github.TokenURL != "https://github.com/login/oauth/access_token" || github.UserInfoURL != "https://api.github.com/user" {
		t.Fatalf("GitHub protocol endpoints = auth %q token %q userinfo %q", github.AuthURL, github.TokenURL, github.UserInfoURL)
	}
}

func TestResolvePreservesGenericOIDCTrustConfiguration(t *testing.T) {
	want := Config{
		Kind: "oidc", IssuerURL: "https://id.example.test", JWKSURL: "https://id.example.test/keys",
		AuthURL: "https://id.example.test/authorize", TokenURL: "https://id.example.test/token",
		UserInfoURL: "https://id.example.test/userinfo", Scopes: "openid custom",
	}
	got := Resolve(want)
	if got.IssuerURL != want.IssuerURL || got.JWKSURL != want.JWKSURL || got.AuthURL != want.AuthURL ||
		got.TokenURL != want.TokenURL || got.UserInfoURL != want.UserInfoURL || got.Scopes != want.Scopes {
		t.Fatalf("generic OIDC config changed: got %+v want %+v", got, want)
	}
}

func TestJWKSResponseSizeAndAlgorithmKeyTypeAreEnforced(t *testing.T) {
	jwksURL := "https://jwks-oversized.example.test/keys"
	useOIDCJWKSTestResponse(t, jwksURL, []byte(strings.Repeat("x", int(oauthProviderResponseBodyCap)+1)))
	key := mustRSAKey(t)
	claims := validTestIDTokenClaims("https://issuer.example.test", "client-123", "nonce-123")
	raw := signTestIDToken(t, key, "key-1", jwt.SigningMethodRS256, claims)
	cfg := Config{Kind: "oidc", ClientID: "client-123", IssuerURL: "https://issuer.example.test", JWKSURL: jwksURL}
	if _, err := cfg.FetchUserInfo(context.Background(), Tokens{IDToken: raw}, "nonce-123"); err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("oversized JWKS error = %v", err)
	}

	if _, err := selectVerificationJWK([]verificationJWK{{KeyID: "key-1", Key: &key.PublicKey}}, idTokenHeader{
		Algorithm: "ES256", KeyID: "key-1",
	}); !errors.Is(err, errJWKNotFound) {
		t.Fatalf("RSA key selected for ES256: %v", err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selectVerificationJWK([]verificationJWK{{KeyID: "key-2", Key: &ecKey.PublicKey}}, idTokenHeader{
		Algorithm: "ES256", KeyID: "key-2",
	}); !errors.Is(err, errJWKNotFound) {
		t.Fatalf("P-384 key selected for ES256: %v", err)
	}
}

func TestJWKSCacheControlTTL(t *testing.T) {
	tests := []struct {
		name         string
		cacheControl string
		want         time.Duration
	}{
		{name: "missing uses default", want: oidcJWKDefaultCacheTTL},
		{name: "positive max age", cacheControl: "public, max-age=60", want: time.Minute},
		{name: "quoted max age with whitespace", cacheControl: `public, MAX-AGE = "120"`, want: 2 * time.Minute},
		{name: "max age zero", cacheControl: "public, max-age=0", want: 0},
		{name: "no store overrides earlier max age", cacheControl: "max-age=3600, no-store", want: 0},
		{name: "no cache overrides earlier max age", cacheControl: `max-age=3600, NO-CACHE="Set-Cookie"`, want: 0},
		{name: "shortest duplicate max age wins", cacheControl: "max-age=3600, max-age=10", want: 10 * time.Second},
		{name: "negative max age uses default", cacheControl: "max-age=-1", want: oidcJWKDefaultCacheTTL},
		{name: "huge max age is capped without overflow", cacheControl: "max-age=9223372036854775807", want: 24 * time.Hour},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := jwksCacheTTL(tc.cacheControl); got != tc.want {
				t.Fatalf("jwksCacheTTL(%q) = %s, want %s", tc.cacheControl, got, tc.want)
			}
		})
	}
}

func TestJWKSNoCacheDirectivesRemoveOlderCachedKeys(t *testing.T) {
	key := mustRSAKey(t)
	body, err := json.Marshal(testJWKS(key, "current-key", "RS256"))
	if err != nil {
		t.Fatal(err)
	}

	for i, cacheControl := range []string{"no-store", "no-cache", "max-age=0"} {
		t.Run(cacheControl, func(t *testing.T) {
			jwksURL := fmt.Sprintf("https://jwks-no-cache-%d.example.test/keys", i)
			oidcJWKCache.Lock()
			oidcJWKCache.Documents[jwksURL] = cachedJWKDocument{
				Keys:      []verificationJWK{{KeyID: "revoked-key", Key: &key.PublicKey}},
				ExpiresAt: time.Now().Add(time.Hour),
			}
			oidcJWKCache.Unlock()
			t.Cleanup(func() {
				oidcJWKCache.Lock()
				delete(oidcJWKCache.Documents, jwksURL)
				oidcJWKCache.Unlock()
			})

			requests := 0
			useGenericOAuth2TestClient(t, func(req *http.Request) (*http.Response, error) {
				requests++
				resp := oauth2JSONResponse(string(body))
				resp.Header.Set("Cache-Control", "max-age=3600")
				resp.Header.Add("Cache-Control", cacheControl)
				return resp, nil
			})
			cfg := Config{Kind: "oidc", JWKSURL: jwksURL}
			if _, err := cfg.loadVerificationJWKs(t.Context(), true); err != nil {
				t.Fatalf("forced JWKS refresh: %v", err)
			}

			oidcJWKCache.Lock()
			_, cached := oidcJWKCache.Documents[jwksURL]
			oidcJWKCache.Unlock()
			if cached {
				t.Fatal("no-cache JWKS response left an older key document cached")
			}

			if _, err := cfg.loadVerificationJWKs(t.Context(), false); err != nil {
				t.Fatalf("second JWKS fetch: %v", err)
			}
			if requests != 2 {
				t.Fatalf("JWKS request count = %d, want 2", requests)
			}
		})
	}
}

func TestJWKSRefreshFailurePreservesPreviouslyAuthorizedCache(t *testing.T) {
	jwksURL := "https://jwks-refresh-failure.example.test/keys"
	key := mustRSAKey(t)
	expiresAt := time.Now().Add(time.Hour)
	oidcJWKCache.Lock()
	oidcJWKCache.Documents[jwksURL] = cachedJWKDocument{
		Keys:      []verificationJWK{{KeyID: "still-cached", Key: &key.PublicKey}},
		ExpiresAt: expiresAt,
	}
	oidcJWKCache.Unlock()
	t.Cleanup(func() {
		oidcJWKCache.Lock()
		delete(oidcJWKCache.Documents, jwksURL)
		oidcJWKCache.Unlock()
	})

	requests := 0
	useGenericOAuth2TestClient(t, func(req *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("provider unavailable")
	})
	cfg := Config{Kind: "oidc", JWKSURL: jwksURL}
	if _, err := cfg.loadVerificationJWKs(t.Context(), true); err == nil {
		t.Fatal("forced JWKS refresh unexpectedly fell back to cached keys")
	}

	oidcJWKCache.Lock()
	cached, exists := oidcJWKCache.Documents[jwksURL]
	oidcJWKCache.Unlock()
	if !exists || len(cached.Keys) != 1 || cached.Keys[0].KeyID != "still-cached" || !cached.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("failed refresh changed the prior cache entry: %+v, exists=%v", cached, exists)
	}
	keys, err := cfg.loadVerificationJWKs(t.Context(), false)
	if err != nil || len(keys) != 1 || keys[0].KeyID != "still-cached" {
		t.Fatalf("subsequent normal cache read keys=%+v err=%v", keys, err)
	}
	if requests != 1 {
		t.Fatalf("JWKS request count = %d, want 1", requests)
	}
}

func validTestIDTokenClaims(issuer, clientID, nonce string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":            issuer,
		"sub":            "subject-1",
		"aud":            clientID,
		"azp":            clientID,
		"exp":            now.Add(5 * time.Minute).Unix(),
		"iat":            now.Unix(),
		"nonce":          nonce,
		"email":          "user@example.test",
		"email_verified": true,
		"name":           "Test User",
		"picture":        "https://cdn.example.test/avatar.png",
	}
}

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

func signTestIDToken(t *testing.T, key *rsa.PrivateKey, kid string, method jwt.SigningMethod, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	token.Header["kid"] = kid
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign id_token: %v", err)
	}
	return raw
}

func testJWKS(key *rsa.PrivateKey, kid, alg string) map[string]any {
	return map[string]any{"keys": []map[string]any{{
		"kty": "RSA",
		"kid": kid,
		"use": "sig",
		"alg": alg,
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
}
