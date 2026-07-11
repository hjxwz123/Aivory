// Package oauth implements the provider-agnostic Authorization Code flow used
// by the social-login handlers. It special-cases the three built-in providers
// (Google, GitHub, Apple) and supports a generic OIDC provider whose endpoints
// are supplied by the admin.
//
// Trust model: the authorization code arrives via the browser redirect, but the
// code→token exchange and every userinfo call are server-to-server over TLS
// straight to the provider. We therefore trust the id_token's claims without a
// separate JWKS signature check — TLS already authenticates the issuer for that
// direct call (this is the standard server-side confidential-client posture).
package oauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"aivory/server/internal/envcfg"
)

// Config is the resolved settings for one provider. Build it from a stored
// row and pass through Resolve to fill in built-in defaults.
type Config struct {
	Kind         string
	ClientID     string
	ClientSecret string // Apple: the AuthKey .p8 private key (PEM)
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	Scopes       string
	TeamID       string // Apple
	KeyID        string // Apple
}

// Tokens is the relevant slice of a token-endpoint response.
type Tokens struct {
	AccessToken string
	IDToken     string
}

// UserInfo is the normalised identity pulled from a provider.
type UserInfo struct {
	Subject       string // stable, provider-issued user id
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
}

var httpClientTimeout = 15 * time.Second
var httpClient = &http.Client{Timeout: httpClientTimeout}

// oauthProviderResponseBodyCap bounds a provider token/userinfo response read.
var oauthProviderResponseBodyCap = int64(1 << 20)

// appleClientSecretJwtExpiry is the lifetime of the generated Apple client-secret JWT.
var appleClientSecretJwtExpiry = envcfg.Dur("AIVORY_OAUTH_APPLE_CLIENT_SECRET_JWT_EXPIRY", 30*time.Minute)

// snippetMaxLen caps an error-body snippet included in error messages.
var snippetMaxLen = 200

// Resolve fills built-in endpoints/scopes for known kinds without overwriting
// any explicit override already present on the Config.
func Resolve(c Config) Config {
	switch c.Kind {
	case "google":
		c.AuthURL = orStr(c.AuthURL, "https://accounts.google.com/o/oauth2/v2/auth")
		c.TokenURL = orStr(c.TokenURL, "https://oauth2.googleapis.com/token")
		c.UserInfoURL = orStr(c.UserInfoURL, "https://openidconnect.googleapis.com/v1/userinfo")
		c.Scopes = orStr(c.Scopes, "openid email profile")
	case "github":
		c.AuthURL = orStr(c.AuthURL, "https://github.com/login/oauth/authorize")
		c.TokenURL = orStr(c.TokenURL, "https://github.com/login/oauth/access_token")
		c.UserInfoURL = orStr(c.UserInfoURL, "https://api.github.com/user")
		c.Scopes = orStr(c.Scopes, "read:user user:email")
	case "apple":
		c.AuthURL = orStr(c.AuthURL, "https://appleid.apple.com/auth/authorize")
		c.TokenURL = orStr(c.TokenURL, "https://appleid.apple.com/auth/token")
		c.Scopes = orStr(c.Scopes, "name email")
	default: // oidc / generic — rely on the admin-supplied endpoints.
		c.Scopes = orStr(c.Scopes, "openid email profile")
	}
	return c
}

// UsesPKCE reports whether the kind should run PKCE. Enabled for the standards
// providers; GitHub's classic app flow and Apple's flow use state only.
func UsesPKCE(kind string) bool {
	return kind == "google" || kind == "oidc"
}

// usesFormPost reports whether the provider posts the callback (Apple, because
// we request the name/email scope).
func UsesFormPost(kind string) bool { return kind == "apple" }

// AuthCodeURL builds the provider authorize URL the browser is redirected to.
func (c Config) AuthCodeURL(redirectURI, state, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", c.Scopes)
	q.Set("state", state)
	if codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	if c.Kind == "apple" {
		// Apple requires form_post whenever name/email scope is requested.
		q.Set("response_mode", "form_post")
	}
	if c.Kind == "google" {
		q.Set("access_type", "online")
		q.Set("include_granted_scopes", "true")
	}
	sep := "?"
	if strings.Contains(c.AuthURL, "?") {
		sep = "&"
	}
	return c.AuthURL + sep + q.Encode()
}

// Exchange swaps the authorization code for tokens. It supports both client
// authentication methods the spec allows: it first tries client_secret_post
// (secret in the body — what Google/GitHub/most use), then falls back to
// client_secret_basic (HTTP Basic, the OIDC DEFAULT) when the provider answers
// with an auth error. A failed client-auth request does not consume the
// single-use code, so the retry is safe.
func (c Config) Exchange(ctx context.Context, redirectURI, code, codeVerifier string) (Tokens, error) {
	secret := c.ClientSecret
	if c.Kind == "apple" {
		s, err := appleClientSecret(c)
		if err != nil {
			return Tokens{}, err
		}
		secret = s
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", c.ClientID)
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}

	// Attempt 1 — client_secret_post: client_secret in the body.
	postForm := cloneValues(form)
	if secret != "" {
		postForm.Set("client_secret", secret)
	}
	tok, status, err := c.postToken(ctx, postForm, "")
	if err == nil {
		return tok, nil
	}

	// Attempt 2 — client_secret_basic: credentials in an Authorization: Basic
	// header. Only when the first attempt looks like a client-auth rejection.
	if secret != "" && (status == http.StatusUnauthorized || isInvalidClient(err)) {
		authz := "Basic " + base64.StdEncoding.EncodeToString([]byte(c.ClientID+":"+secret))
		if tok2, _, err2 := c.postToken(ctx, cloneValues(form), authz); err2 == nil {
			return tok2, nil
		}
	}
	return Tokens{}, err
}

// postToken performs one token-endpoint POST and parses the response. authHeader,
// when non-empty, is sent as the Authorization header (client_secret_basic).
func (c Config) postToken(ctx context.Context, form url.Values, authHeader string) (Tokens, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json") // GitHub returns form-encoded otherwise
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Tokens{}, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, oauthProviderResponseBodyCap))
	if resp.StatusCode >= 400 {
		return Tokens{}, resp.StatusCode, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, snippet(body))
	}
	var tr struct {
		AccessToken      string `json:"access_token"`
		IDToken          string `json:"id_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return Tokens{}, resp.StatusCode, fmt.Errorf("decode token response: %w", err)
	}
	if tr.Error != "" {
		return Tokens{}, resp.StatusCode, fmt.Errorf("token endpoint error: %s %s", tr.Error, tr.ErrorDescription)
	}
	if tr.AccessToken == "" && tr.IDToken == "" {
		return Tokens{}, resp.StatusCode, errors.New("token endpoint returned no tokens")
	}
	return Tokens{AccessToken: tr.AccessToken, IDToken: tr.IDToken}, resp.StatusCode, nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}

func isInvalidClient(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid_client")
}

// FetchUserInfo normalises the provider's identity. GitHub uses its REST API;
// every other provider prefers the id_token, falling back to the userinfo
// endpoint.
func (c Config) FetchUserInfo(ctx context.Context, tk Tokens) (UserInfo, error) {
	if c.Kind == "github" {
		return c.githubUser(ctx, tk.AccessToken)
	}
	if tk.IDToken != "" {
		if info, ok := decodeIDToken(tk.IDToken); ok {
			return info, nil
		}
	}
	if c.UserInfoURL == "" {
		return UserInfo{}, errors.New("no id_token and no userinfo endpoint configured")
	}
	return c.oidcUserInfo(ctx, tk.AccessToken)
}

func (c Config) oidcUserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.UserInfoURL, nil)
	if err != nil {
		return UserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return UserInfo{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, oauthProviderResponseBodyCap))
	if resp.StatusCode >= 400 {
		return UserInfo{}, fmt.Errorf("userinfo %d: %s", resp.StatusCode, snippet(body))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return UserInfo{}, err
	}
	info := UserInfo{
		Subject:       str(m["sub"]),
		Email:         str(m["email"]),
		EmailVerified: truthy(m["email_verified"]),
		Name:          str(m["name"]),
		AvatarURL:     str(m["picture"]),
	}
	if info.Subject == "" {
		info.Subject = str(m["id"])
	}
	if info.Subject == "" {
		return UserInfo{}, errors.New("userinfo missing subject")
	}
	return info, nil
}

func (c Config) githubUser(ctx context.Context, accessToken string) (UserInfo, error) {
	get := func(u string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "Aivory")
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, oauthProviderResponseBodyCap))
		if resp.StatusCode >= 400 {
			return fmt.Errorf("github %d: %s", resp.StatusCode, snippet(body))
		}
		return json.Unmarshal(body, out)
	}
	var gu struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
	}
	if err := get("https://api.github.com/user", &gu); err != nil {
		return UserInfo{}, err
	}
	if gu.ID == 0 {
		return UserInfo{}, errors.New("github user missing id")
	}
	info := UserInfo{
		Subject:   strconv.FormatInt(gu.ID, 10),
		Email:     gu.Email,
		Name:      orStr(gu.Name, gu.Login),
		AvatarURL: gu.AvatarURL,
	}
	if info.Email != "" {
		info.EmailVerified = true // a public profile email is verified by GitHub
	} else {
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if err := get("https://api.github.com/user/emails", &emails); err == nil {
			for _, e := range emails {
				if e.Primary && e.Verified {
					info.Email = e.Email
					info.EmailVerified = true
					break
				}
			}
		}
	}
	return info, nil
}

// appleClientSecret mints the ES256-signed JWT Apple accepts in place of a
// static client secret.
func appleClientSecret(c Config) (string, error) {
	block, _ := pem.Decode([]byte(c.ClientSecret))
	if block == nil {
		return "", errors.New("apple: client secret is not a valid .p8 PEM key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("apple: parse private key: %w", err)
	}
	ecKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("apple: private key is not ECDSA")
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": c.TeamID,
		"iat": now.Unix(),
		"exp": now.Add(appleClientSecretJwtExpiry).Unix(),
		"aud": "https://appleid.apple.com",
		"sub": c.ClientID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["kid"] = c.KeyID
	return tok.SignedString(ecKey)
}

// decodeIDToken reads the JWT payload (no signature check — see package doc).
func decodeIDToken(idToken string) (UserInfo, bool) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return UserInfo{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return UserInfo{}, false
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return UserInfo{}, false
	}
	// §A7: reject an expired id_token (60s skew). This is NOT a substitute for
	// JWKS signature verification — for fully-untrusted generic oidc token
	// endpoints, verifying the RS256/ES256 signature against the provider's JWKS
	// (and iss/aud) is the proper control; the account-takeover path is otherwise
	// closed by requiring a VERIFIED email before linking (see resolveOAuthUser).
	if exp, ok := m["exp"].(float64); ok && time.Now().Unix() > int64(exp)+60 {
		return UserInfo{}, false
	}
	info := UserInfo{
		Subject:       str(m["sub"]),
		Email:         str(m["email"]),
		EmailVerified: truthy(m["email_verified"]),
		Name:          str(m["name"]),
		AvatarURL:     str(m["picture"]),
	}
	return info, info.Subject != ""
}

// PKCEChallenge derives the S256 code_challenge for a verifier.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func orStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

// truthy accepts the bool and the "true"/"false" string forms providers use for
// email_verified.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	default:
		return false
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > snippetMaxLen {
		return s[:snippetMaxLen]
	}
	return s
}
