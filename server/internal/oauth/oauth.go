// Package oauth implements the provider-agnostic Authorization Code flow used
// by the social-login handlers. It special-cases the three built-in providers
// (Google, GitHub, Apple), a generic OIDC provider, and a generic OAuth 2.0
// provider whose endpoints are supplied by the admin.
//
// ID tokens are authenticated independently of the token endpoint connection:
// their signature, issuer, audience, authorized party, lifetime and nonce are
// verified before any identity claim is used. GitHub and generic OAuth 2.0 do
// not treat access tokens as ID tokens; they resolve identity through a fixed
// HTTPS userinfo endpoint instead.
package oauth

import (
	"bytes"
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
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"aivory/server/internal/envcfg"
	"aivory/server/internal/netsafe"
)

// Config is the resolved settings for one provider. Build it from a stored
// row and pass through Resolve to fill in built-in defaults.
type Config struct {
	Kind         string
	ClientID     string
	ClientSecret string // Apple: the AuthKey .p8 private key (PEM)
	IssuerURL    string
	JWKSURL      string
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

// tokenEndpointError preserves only the protocol fields needed to classify a
// failed exchange. Authorization codes, credentials and returned tokens must
// never be attached to this error.
type tokenEndpointError struct {
	Status int
	Code   string
}

func (e *tokenEndpointError) Error() string {
	if e.Code != "" {
		return "token endpoint error: " + e.Code
	}
	if e.Status != 0 {
		return fmt.Sprintf("token endpoint returned HTTP %d", e.Status)
	}
	return "token endpoint error"
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

// A token request can fail before the provider processes the authorization
// code (for example, a stalled route while awaiting response headers). One
// fresh-connection retry is the only recovery available in that case. If the
// first request did reach the provider, the retry safely fails as a used code
// and the user starts a new flow, which was already required after losing the
// first response.
const tokenExchangeMaxAttempts = 2

// Generic OAuth endpoints are administrator supplied, so their server-side
// requests use a DNS-rebinding-safe client and may not redirect away from the
// configured token/userinfo URL. OAuth token and userinfo endpoints are
// expected to answer directly; following redirects could move credentials or
// bearer tokens to a different host.
var genericOAuth2HTTPClient = func() *http.Client {
	c := netsafe.PrivateBlockClient(httpClientTimeout)
	c.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("generic oauth2 endpoint redirects are not allowed")
	}
	return c
}()

// oauthProviderResponseBodyCap bounds a provider token/userinfo response read.
var oauthProviderResponseBodyCap = int64(1 << 20)

// appleClientSecretJwtExpiry is the lifetime of the generated Apple client-secret JWT.
var appleClientSecretJwtExpiry = envcfg.Dur("AIVORY_OAUTH_APPLE_CLIENT_SECRET_JWT_EXPIRY", 30*time.Minute)

// snippetMaxLen caps an error-body snippet included in error messages.
var snippetMaxLen = 200

// Resolve pins built-in providers to their official trust and protocol
// endpoints. Custom issuer/JWKS/auth/token hosts belong only to kind=oidc or
// kind=oauth2; a stale or malicious stored override must never move a built-in
// provider's signing-key trust root or authorization-code exchange to another
// host.
func Resolve(c Config) Config {
	switch c.Kind {
	case "google":
		c.IssuerURL = "https://accounts.google.com"
		c.JWKSURL = "https://www.googleapis.com/oauth2/v3/certs"
		c.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
		c.TokenURL = "https://oauth2.googleapis.com/token"
		c.UserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
		c.Scopes = orStr(c.Scopes, "openid email profile")
	case "github":
		c.IssuerURL = ""
		c.JWKSURL = ""
		c.AuthURL = "https://github.com/login/oauth/authorize"
		c.TokenURL = "https://github.com/login/oauth/access_token"
		c.UserInfoURL = "https://api.github.com/user"
		c.Scopes = orStr(c.Scopes, "read:user user:email")
	case "apple":
		c.IssuerURL = "https://appleid.apple.com"
		c.JWKSURL = "https://appleid.apple.com/auth/keys"
		c.AuthURL = "https://appleid.apple.com/auth/authorize"
		c.TokenURL = "https://appleid.apple.com/auth/token"
		c.UserInfoURL = ""
		c.Scopes = orStr(c.Scopes, "name email")
	case "oidc": // Generic OIDC relies on the admin-supplied endpoints.
		c.Scopes = orStr(c.Scopes, "openid email profile")
	case "oauth2":
		// OAuth 2.0 defines no standard identity scopes. Preserve exactly what
		// the administrator configured instead of assuming OIDC's openid scope.
	}
	return c
}

// UsesPKCE reports whether the kind should run PKCE. Enabled for the standards
// providers; GitHub's classic app flow and Apple's flow use state only.
func UsesPKCE(kind string) bool {
	return kind == "google" || kind == "oidc" || kind == "oauth2"
}

// UsesFormPost reports whether the authorization response is a cross-site POST.
// Apple always uses form_post. Generic OAuth/OIDC providers may declare the
// same response mode in their fixed authorization endpoint query.
func UsesFormPost(c Config) bool {
	if c.Kind == "apple" {
		return true
	}
	if c.Kind != "oidc" && c.Kind != "oauth2" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(c.AuthURL))
	if err != nil {
		return false
	}
	for _, mode := range u.Query()["response_mode"] {
		if strings.EqualFold(strings.TrimSpace(mode), "form_post") {
			return true
		}
	}
	return false
}

// UsesIDToken reports whether the provider is expected to return an OIDC ID
// token. GitHub's classic OAuth App flow is intentionally excluded.
func UsesIDToken(kind string) bool {
	return kind == "google" || kind == "apple" || kind == "oidc"
}

// AuthCodeURL builds the provider authorize URL the browser is redirected to.
func (c Config) AuthCodeURL(redirectURI, state, codeChallenge, nonce string) string {
	q := url.Values{}
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	if strings.TrimSpace(c.Scopes) != "" {
		q.Set("scope", c.Scopes)
	}
	q.Set("state", state)
	if codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", "S256")
	}
	if UsesIDToken(c.Kind) && nonce != "" {
		q.Set("nonce", nonce)
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
		credentials := url.QueryEscape(c.ClientID) + ":" + url.QueryEscape(secret)
		authz := "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials))
		if tok2, _, err2 := c.postToken(ctx, cloneValues(form), authz); err2 == nil {
			return tok2, nil
		}
	}
	return Tokens{}, err
}

// postToken performs one token-endpoint POST and parses the response. authHeader,
// when non-empty, is sent as the Authorization header (client_secret_basic).
func (c Config) postToken(ctx context.Context, form url.Values, authHeader string) (Tokens, int, error) {
	client := httpClient
	if c.Kind == "oauth2" || c.Kind == "oidc" {
		if err := ValidateHTTPSProviderEndpoint(c.TokenURL); err != nil {
			return Tokens{}, 0, fmt.Errorf("invalid custom OAuth token_url: %w", err)
		}
		client = genericOAuth2HTTPClient
	}
	encodedForm := form.Encode()
	var lastErr error
	for attempt := 1; attempt <= tokenExchangeMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(encodedForm))
		if err != nil {
			return Tokens{}, 0, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json") // GitHub returns form-encoded otherwise
		if authHeader != "" {
			req.Header.Set("Authorization", authHeader)
		}
		resp, err := client.Do(req)
		if err == nil {
			return parseTokenEndpointResponse(resp)
		}
		lastErr = err
		if attempt == tokenExchangeMaxAttempts || ctx.Err() != nil || !retryableTokenTransportError(err) {
			break
		}
		// Avoid immediately selecting the same stale idle connection or route.
		client.CloseIdleConnections()
	}
	return Tokens{}, 0, lastErr
}

func parseTokenEndpointResponse(resp *http.Response) (Tokens, int, error) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, oauthProviderResponseBodyCap))
	tr, decodeErr := decodeTokenEndpointResponse(body)
	if tr.Error != "" {
		return Tokens{}, resp.StatusCode, &tokenEndpointError{
			Status: resp.StatusCode,
			Code:   strings.TrimSpace(tr.Error),
		}
	}
	if resp.StatusCode >= 400 {
		// Never attach an unstructured body: a broken endpoint could return a
		// token-shaped payload with an error status, and callback logs must not
		// copy that value.
		return Tokens{}, resp.StatusCode, &tokenEndpointError{Status: resp.StatusCode}
	}
	if decodeErr != nil {
		return Tokens{}, resp.StatusCode, fmt.Errorf("decode token response: %w", decodeErr)
	}
	if tr.AccessToken == "" && tr.IDToken == "" {
		return Tokens{}, resp.StatusCode, errors.New("token endpoint returned no tokens")
	}
	return Tokens{AccessToken: tr.AccessToken, IDToken: tr.IDToken}, resp.StatusCode, nil
}

type tokenEndpointResponse struct {
	AccessToken      string `json:"access_token"`
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// decodeTokenEndpointResponse accepts JSON and the legacy form-encoded OAuth
// response shape. GitHub honors the JSON Accept header, but parsing both forms
// keeps failures deterministic if an intermediary rewrites that header.
func decodeTokenEndpointResponse(body []byte) (tokenEndpointResponse, error) {
	var tr tokenEndpointResponse
	jsonErr := json.Unmarshal(body, &tr)
	if jsonErr == nil {
		return tr, nil
	}
	form, formErr := url.ParseQuery(strings.TrimSpace(string(body)))
	if formErr != nil {
		return tokenEndpointResponse{}, jsonErr
	}
	tr = tokenEndpointResponse{
		AccessToken:      form.Get("access_token"),
		IDToken:          form.Get("id_token"),
		Error:            form.Get("error"),
		ErrorDescription: form.Get("error_description"),
	}
	if tr.AccessToken == "" && tr.IDToken == "" && tr.Error == "" {
		return tokenEndpointResponse{}, jsonErr
	}
	return tr, nil
}

func retryableTokenTransportError(err error) bool {
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
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
	var endpointErr *tokenEndpointError
	if errors.As(err, &endpointErr) {
		switch strings.ToLower(strings.TrimSpace(endpointErr.Code)) {
		case "invalid_client", "incorrect_client_credentials":
			return true
		}
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid_client")
}

// TokenExchangeFailureReason maps provider and network failures to a stable
// public allowlist instead of forwarding provider text into a redirect URL.
func TokenExchangeFailureReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "oauth_provider_timeout"
	}
	var endpointErr *tokenEndpointError
	if errors.As(err, &endpointErr) {
		switch strings.ToLower(strings.TrimSpace(endpointErr.Code)) {
		case "invalid_client", "incorrect_client_credentials", "unauthorized_client":
			return "oauth_credentials_invalid"
		case "bad_verification_code", "invalid_grant", "expired_token":
			return "oauth_code_invalid"
		case "redirect_uri_mismatch":
			return "oauth_redirect_uri_mismatch"
		case "slow_down", "rate_limited", "rate_limit_exceeded":
			return "oauth_provider_rate_limited"
		}
		if endpointErr.Status == http.StatusTooManyRequests {
			return "oauth_provider_rate_limited"
		}
		if endpointErr.Status >= http.StatusInternalServerError {
			return "oauth_provider_unreachable"
		}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		if networkErr.Timeout() {
			return "oauth_provider_timeout"
		}
		return "oauth_provider_unreachable"
	}
	return "token_exchange_failed"
}

// FetchUserInfo normalises the provider's identity. GitHub uses its pinned REST
// API, generic OAuth 2.0 uses its configured HTTPS userinfo API, and every OIDC
// provider must return a signed ID token bound to this login's nonce.
func (c Config) FetchUserInfo(ctx context.Context, tk Tokens, expectedNonce string) (UserInfo, error) {
	switch c.Kind {
	case "github":
		return c.githubUser(ctx, tk.AccessToken)
	case "oauth2":
		if strings.TrimSpace(tk.AccessToken) == "" {
			return UserInfo{}, errors.New("token endpoint returned no access_token")
		}
		if err := ValidateHTTPSProviderEndpoint(c.UserInfoURL); err != nil {
			return UserInfo{}, fmt.Errorf("invalid generic oauth2 userinfo_url: %w", err)
		}
		return c.oauth2UserInfo(ctx, tk.AccessToken)
	case "google", "apple", "oidc":
		if strings.TrimSpace(tk.IDToken) == "" {
			return UserInfo{}, errors.New("token endpoint returned no id_token")
		}
		return c.verifyIDToken(ctx, tk.IDToken, expectedNonce)
	default:
		return UserInfo{}, errors.New("unsupported oauth provider kind")
	}
}

func (c Config) oauth2UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.UserInfoURL, nil)
	if err != nil {
		return UserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := genericOAuth2HTTPClient.Do(req)
	if err != nil {
		return UserInfo{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, oauthProviderResponseBodyCap))
	if resp.StatusCode >= 400 {
		return UserInfo{}, fmt.Errorf("userinfo %d: %s", resp.StatusCode, snippet(body))
	}
	var m map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&m); err != nil {
		return UserInfo{}, err
	}
	// Match json.Unmarshal's single-value semantics. Decoder.Decode alone would
	// accept a valid profile followed by a second JSON value.
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return UserInfo{}, fmt.Errorf("decode userinfo trailing data: %w", err)
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

// SubjectNamespace identifies the exact trust domain behind one provider row.
// It covers every supported kind, including pinned built-ins: switching a row
// between built-in/custom kinds or changing an OIDC issuer/JWKS can therefore
// never make an equal-looking raw subject inherit an older identity.
func (c Config) SubjectNamespace() string {
	c = Resolve(c)
	// Only fields that participate in this kind's identity proof belong in its
	// trust generation. OIDC identity comes exclusively from the signed ID token,
	// while OAuth2 UserInfo has no issuer/JWKS relationship.
	if c.Kind == "oidc" {
		c.UserInfoURL = ""
	}
	if c.Kind == "oauth2" {
		c.IssuerURL = ""
		c.JWKSURL = ""
	}
	trust, _ := json.Marshal([]string{
		"oauth-subject-namespace-v1",
		strings.TrimSpace(c.Kind),
		strings.TrimSpace(c.ClientID),
		strings.TrimSpace(c.IssuerURL),
		strings.TrimSpace(c.JWKSURL),
		strings.TrimSpace(c.AuthURL),
		strings.TrimSpace(c.TokenURL),
		strings.TrimSpace(c.UserInfoURL),
	})
	digest := sha256.Sum256(trust)
	return "oauth:v1:" + base64.RawURLEncoding.EncodeToString(digest[:]) + ":"
}

// NamespaceSubject binds a raw provider subject to SubjectNamespace.
func (c Config) NamespaceSubject(raw string) string {
	if raw == "" {
		return ""
	}
	return c.SubjectNamespace() + raw
}

// ValidateHTTPSProviderEndpoint validates an administrator-supplied OAuth/OIDC
// endpoint without resolving DNS. The generic OAuth 2.0 HTTP client performs a
// second dial-time resolved-IP check, which closes DNS-rebinding and redirect
// routes to loopback, private networks, and cloud metadata services.
func ValidateHTTPSProviderEndpoint(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return errors.New("must be an absolute https URL without credentials or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(u.Hostname())), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") {
		return errors.New("must not target a local host")
	}
	if ip := net.ParseIP(host); ip != nil && !netsafe.IsPublicIP(ip) {
		return errors.New("must not target a private or reserved IP address")
	}
	return nil
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
