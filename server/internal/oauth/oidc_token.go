package oauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const oidcClockSkew = 60 * time.Second

var oidcJWKDefaultCacheTTL = 15 * time.Minute

type idTokenClaims struct {
	jwt.RegisteredClaims
	AuthorizedParty string `json:"azp"`
	Nonce           string `json:"nonce"`
	Email           string `json:"email"`
	EmailVerified   any    `json:"email_verified"`
	Name            string `json:"name"`
	Picture         string `json:"picture"`
}

type idTokenHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type jwkDocument struct {
	Keys []json.RawMessage `json:"keys"`
}

type rawJWK struct {
	KeyType string   `json:"kty"`
	KeyID   string   `json:"kid"`
	Use     string   `json:"use"`
	KeyOps  []string `json:"key_ops"`
	Alg     string   `json:"alg"`
	N       string   `json:"n"`
	E       string   `json:"e"`
	Curve   string   `json:"crv"`
	X       string   `json:"x"`
	Y       string   `json:"y"`
}

type verificationJWK struct {
	KeyID string
	Alg   string
	Key   any
}

type cachedJWKDocument struct {
	Keys      []verificationJWK
	ExpiresAt time.Time
}

var oidcJWKCache = struct {
	sync.Mutex
	Documents map[string]cachedJWKDocument
}{Documents: make(map[string]cachedJWKDocument)}

var allowedIDTokenAlgorithms = map[string]bool{
	"RS256": true,
	"RS384": true,
	"RS512": true,
	"PS256": true,
	"PS384": true,
	"PS512": true,
	"ES256": true,
	"ES384": true,
	"ES512": true,
	"EdDSA": true,
}

// verifyIDToken verifies an OIDC ID token before exposing any identity claim.
// The expected issuer and JWKS endpoint are administrator configuration for a
// generic provider and trusted built-in constants for Google and Apple.
func (c Config) verifyIDToken(ctx context.Context, rawToken, expectedNonce string) (UserInfo, error) {
	if strings.TrimSpace(c.ClientID) == "" {
		return UserInfo{}, errors.New("oidc: client_id is required")
	}
	if strings.TrimSpace(c.IssuerURL) == "" {
		return UserInfo{}, errors.New("oidc: issuer_url is required")
	}
	if strings.TrimSpace(c.JWKSURL) == "" {
		return UserInfo{}, errors.New("oidc: jwks_url is required")
	}
	if expectedNonce == "" {
		return UserInfo{}, errors.New("oidc: expected nonce is required")
	}

	header, err := parseIDTokenHeader(rawToken)
	if err != nil {
		return UserInfo{}, err
	}
	if !allowedIDTokenAlgorithms[header.Algorithm] {
		return UserInfo{}, fmt.Errorf("oidc: disallowed id_token algorithm %q", header.Algorithm)
	}

	claims, err := c.verifyIDTokenWithCachedKeys(ctx, rawToken, header, false)
	if err != nil && (errors.Is(err, jwt.ErrSignatureInvalid) || errors.Is(err, errJWKNotFound)) {
		// A provider may have rotated its signing key while the old JWKS is still
		// cached. Refresh once on an unknown key/signature and retry verification.
		claims, err = c.verifyIDTokenWithCachedKeys(ctx, rawToken, header, true)
	}
	if err != nil {
		return UserInfo{}, err
	}
	if claims.IssuedAt == nil {
		return UserInfo{}, errors.New("oidc: id_token missing iat")
	}
	if claims.Subject == "" {
		return UserInfo{}, errors.New("oidc: id_token missing subject")
	}
	issuerOK := claims.Issuer == c.IssuerURL
	// Google's documented issuer has historically appeared both with and
	// without the https:// prefix. Both values identify the same built-in,
	// pinned provider; generic OIDC issuers remain exact-match only.
	if c.Kind == "google" && c.IssuerURL == "https://accounts.google.com" && claims.Issuer == "accounts.google.com" {
		issuerOK = true
	}
	if !issuerOK {
		return UserInfo{}, errors.New("oidc: id_token issuer mismatch")
	}
	if len(claims.Audience) > 1 && claims.AuthorizedParty == "" {
		return UserInfo{}, errors.New("oidc: id_token with multiple audiences is missing azp")
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty != c.ClientID {
		return UserInfo{}, errors.New("oidc: id_token azp does not match client_id")
	}
	if len(claims.Nonce) != len(expectedNonce) ||
		subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 {
		return UserInfo{}, errors.New("oidc: id_token nonce mismatch")
	}

	return UserInfo{
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: truthy(claims.EmailVerified),
		Name:          claims.Name,
		AvatarURL:     claims.Picture,
	}, nil
}

var errJWKNotFound = errors.New("oidc: no matching jwk")

func (c Config) verifyIDTokenWithCachedKeys(ctx context.Context, rawToken string, header idTokenHeader, forceRefresh bool) (*idTokenClaims, error) {
	keys, err := c.loadVerificationJWKs(ctx, forceRefresh)
	if err != nil {
		return nil, err
	}
	key, err := selectVerificationJWK(keys, header)
	if err != nil {
		return nil, err
	}
	claims := &idTokenClaims{}
	parsed, err := jwt.ParseWithClaims(rawToken, claims, func(*jwt.Token) (any, error) {
		return key, nil
	},
		jwt.WithValidMethods([]string{header.Algorithm}),
		jwt.WithAudience(c.ClientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(oidcClockSkew),
		jwt.WithJSONNumber(),
		jwt.WithStrictDecoding(),
	)
	if err != nil {
		return nil, fmt.Errorf("oidc: verify id_token: %w", err)
	}
	if !parsed.Valid {
		return nil, errors.New("oidc: invalid id_token")
	}
	return claims, nil
}

func parseIDTokenHeader(rawToken string) (idTokenHeader, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 || len(parts[0]) > 4096 {
		return idTokenHeader{}, errors.New("oidc: malformed id_token")
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return idTokenHeader{}, errors.New("oidc: malformed id_token header")
	}
	var header idTokenHeader
	if err := json.Unmarshal(rawHeader, &header); err != nil || header.Algorithm == "" {
		return idTokenHeader{}, errors.New("oidc: malformed id_token header")
	}
	return header, nil
}

func (c Config) loadVerificationJWKs(ctx context.Context, forceRefresh bool) ([]verificationJWK, error) {
	jwksURL := c.JWKSURL
	now := time.Now()
	oidcJWKCache.Lock()
	if cached, ok := oidcJWKCache.Documents[jwksURL]; !forceRefresh && ok && now.Before(cached.ExpiresAt) {
		keys := append([]verificationJWK(nil), cached.Keys...)
		oidcJWKCache.Unlock()
		return keys, nil
	}
	oidcJWKCache.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("oidc: create jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	client := httpClient
	if c.Kind == "oidc" {
		if err := ValidateHTTPSProviderEndpoint(jwksURL); err != nil {
			return nil, fmt.Errorf("oidc: invalid custom jwks_url: %w", err)
		}
		client = genericOAuth2HTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthProviderResponseBodyCap+1))
	if err != nil {
		return nil, fmt.Errorf("oidc: read jwks: %w", err)
	}
	if int64(len(body)) > oauthProviderResponseBodyCap {
		return nil, errors.New("oidc: jwks response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oidc: jwks endpoint %d: %s", resp.StatusCode, snippet(body))
	}
	var document jwkDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("oidc: decode jwks: %w", err)
	}
	keys := make([]verificationJWK, 0, len(document.Keys))
	for _, encoded := range document.Keys {
		key, err := parseVerificationJWK(encoded)
		if err == nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("oidc: jwks contained no supported signing keys")
	}
	ttl := jwksCacheTTL(strings.Join(resp.Header.Values("Cache-Control"), ","))
	oidcJWKCache.Lock()
	if ttl > 0 {
		oidcJWKCache.Documents[jwksURL] = cachedJWKDocument{Keys: keys, ExpiresAt: now.Add(ttl)}
	} else {
		// no-store, no-cache, and max-age=0 all require the next verification
		// to fetch the provider's current keys. Delete an older cached document
		// as well, since this response may have been a forced rotation refresh.
		delete(oidcJWKCache.Documents, jwksURL)
	}
	oidcJWKCache.Unlock()
	return append([]verificationJWK(nil), keys...), nil
}

func jwksCacheTTL(cacheControl string) time.Duration {
	const maxTTL = 24 * time.Hour
	var parsedTTL time.Duration
	hasMaxAge := false
	for _, directive := range strings.Split(cacheControl, ",") {
		name, value, hasValue := strings.Cut(strings.TrimSpace(directive), "=")
		name = strings.TrimSpace(name)
		if strings.EqualFold(name, "no-store") || strings.EqualFold(name, "no-cache") {
			return 0
		}
		if !hasValue || !strings.EqualFold(name, "max-age") {
			continue
		}
		seconds, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(value), `"`), 10, 64)
		if err != nil || seconds < 0 {
			continue
		}
		ttl := time.Duration(0)
		if seconds >= int64(maxTTL/time.Second) {
			ttl = maxTTL
		} else {
			ttl = time.Duration(seconds) * time.Second
		}
		// Duplicate max-age directives make the header invalid, but choosing the
		// shortest valid value is the conservative behavior for key revocation.
		if !hasMaxAge || ttl < parsedTTL {
			parsedTTL = ttl
		}
		hasMaxAge = true
	}
	if hasMaxAge {
		return parsedTTL
	}
	return oidcJWKDefaultCacheTTL
}

func parseVerificationJWK(encoded json.RawMessage) (verificationJWK, error) {
	var raw rawJWK
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return verificationJWK{}, err
	}
	if raw.Use != "" && raw.Use != "sig" {
		return verificationJWK{}, errors.New("jwk is not a signing key")
	}
	if len(raw.KeyOps) > 0 && !containsString(raw.KeyOps, "verify") {
		return verificationJWK{}, errors.New("jwk cannot verify signatures")
	}
	if raw.Alg != "" && !allowedIDTokenAlgorithms[raw.Alg] {
		return verificationJWK{}, errors.New("jwk uses an unsupported algorithm")
	}

	var key any
	var err error
	switch raw.KeyType {
	case "RSA":
		key, err = parseRSAJWK(raw)
	case "EC":
		key, err = parseECJWK(raw)
	case "OKP":
		key, err = parseOKPJWK(raw)
	default:
		err = errors.New("unsupported jwk key type")
	}
	if err != nil {
		return verificationJWK{}, err
	}
	return verificationJWK{KeyID: raw.KeyID, Alg: raw.Alg, Key: key}, nil
}

func parseRSAJWK(raw rawJWK) (*rsa.PublicKey, error) {
	modulusBytes, err := base64.RawURLEncoding.DecodeString(raw.N)
	if err != nil || len(modulusBytes) == 0 {
		return nil, errors.New("invalid rsa modulus")
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(raw.E)
	if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
		return nil, errors.New("invalid rsa exponent")
	}
	exponent := 0
	for _, b := range exponentBytes {
		exponent = exponent<<8 | int(b)
	}
	modulus := new(big.Int).SetBytes(modulusBytes)
	if modulus.BitLen() < 2048 || exponent < 3 || exponent%2 == 0 {
		return nil, errors.New("unsafe rsa signing key")
	}
	return &rsa.PublicKey{N: modulus, E: exponent}, nil
}

func parseECJWK(raw rawJWK) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch raw.Curve {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, errors.New("unsupported ec curve")
	}
	xBytes, errX := base64.RawURLEncoding.DecodeString(raw.X)
	yBytes, errY := base64.RawURLEncoding.DecodeString(raw.Y)
	if errX != nil || errY != nil || len(xBytes) == 0 || len(yBytes) == 0 {
		return nil, errors.New("invalid ec coordinates")
	}
	x, y := new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes)
	if !curve.IsOnCurve(x, y) {
		return nil, errors.New("ec key is not on its declared curve")
	}
	return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
}

func parseOKPJWK(raw rawJWK) (ed25519.PublicKey, error) {
	if raw.Curve != "Ed25519" {
		return nil, errors.New("unsupported okp curve")
	}
	x, err := base64.RawURLEncoding.DecodeString(raw.X)
	if err != nil || len(x) != ed25519.PublicKeySize {
		return nil, errors.New("invalid ed25519 public key")
	}
	return ed25519.PublicKey(x), nil
}

func selectVerificationJWK(keys []verificationJWK, header idTokenHeader) (any, error) {
	matches := make([]verificationJWK, 0, 1)
	for _, key := range keys {
		if header.KeyID != "" && key.KeyID != header.KeyID {
			continue
		}
		if key.Alg != "" && key.Alg != header.Algorithm {
			continue
		}
		if !jwkSupportsAlgorithm(key.Key, header.Algorithm) {
			continue
		}
		matches = append(matches, key)
	}
	if len(matches) != 1 {
		return nil, errJWKNotFound
	}
	return matches[0].Key, nil
}

func jwkSupportsAlgorithm(key any, algorithm string) bool {
	switch typed := key.(type) {
	case *rsa.PublicKey:
		return strings.HasPrefix(algorithm, "RS") || strings.HasPrefix(algorithm, "PS")
	case *ecdsa.PublicKey:
		switch algorithm {
		case "ES256":
			return typed.Curve == elliptic.P256()
		case "ES384":
			return typed.Curve == elliptic.P384()
		case "ES512":
			return typed.Curve == elliptic.P521()
		default:
			return false
		}
	case ed25519.PublicKey:
		return algorithm == "EdDSA"
	default:
		return false
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
