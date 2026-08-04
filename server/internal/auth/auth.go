// Package auth issues and verifies short-lived access tokens and rotates
// refresh tokens, per design.md §8.1. Token rotation/realtime ban support is
// kept simple but compatible with the design: every access token carries a
// `tv` claim and a stable session-family `sid`; middleware compares both with
// authoritative database state, so a version bump or per-device revoke takes
// effect on the next request across every replica.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"aivory/server/internal/cache"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	accessTokenType  = "at+jwt"
	refreshTokenType = "rt+jwt"
	accessTokenUse   = "access"
	refreshTokenUse  = "refresh"
)

// Claims is the access-token payload.
type Claims struct {
	jwt.RegisteredClaims
	UID       string `json:"uid"`
	Role      string `json:"role"`
	TV        int    `json:"tv"`
	SessionID string `json:"sid,omitempty"`
	TokenUse  string `json:"token_use"`
}

// RefreshClaims is the refresh-token payload (sub == uid; jti = id).
type RefreshClaims struct {
	jwt.RegisteredClaims
	UID      string `json:"uid"`
	TV       int    `json:"tv"`
	TokenUse string `json:"token_use"`
}

// Service signs and verifies purpose-separated access and refresh tokens.
type Service struct {
	accessKey  []byte
	refreshKey []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	cache      cache.Cache
}

// New builds a new auth service.
func New(secret string, accessTTL, refreshTTL time.Duration, c cache.Cache) *Service {
	master := []byte(secret)
	return &Service{
		accessKey:  deriveSigningKey(master, accessTokenUse),
		refreshKey: deriveSigningKey(master, refreshTokenUse),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		cache:      c,
	}
}

// deriveSigningKey cryptographically separates access and refresh signatures
// while keeping one deployment secret in configuration. A token signed for one
// purpose cannot validate under the other parser even if its claims are altered.
func deriveSigningKey(master []byte, purpose string) []byte {
	h := hmac.New(sha256.New, master)
	_, _ = h.Write([]byte("aivory/jwt/" + purpose))
	return h.Sum(nil)
}

// IssueAccessForSession binds an access token to a stable refresh-session
// family. Revoking that family can then invalidate both its current refresh
// token and its already-issued access token without affecting other devices.
func (s *Service) IssueAccessForSession(uid, role string, tokenVer int, sessionID string) (string, time.Time, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", time.Time{}, errors.New("missing access-token session")
	}
	exp := time.Now().Add(s.accessTTL)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uid,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        uuid.NewString(),
		},
		UID:       uid,
		Role:      role,
		TV:        tokenVer,
		SessionID: sessionID,
		TokenUse:  accessTokenUse,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["typ"] = accessTokenType
	signed, err := tok.SignedString(s.accessKey)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// IssueRefresh returns a signed refresh token, its expiry, and its jti so the
// caller can record/revoke it in the DB.
func (s *Service) IssueRefresh(uid string, tokenVer int) (string, time.Time, string, error) {
	jti := uuid.NewString()
	exp := time.Now().Add(s.refreshTTL)
	claims := RefreshClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uid,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        jti,
		},
		UID:      uid,
		TV:       tokenVer,
		TokenUse: refreshTokenUse,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["typ"] = refreshTokenType
	signed, err := tok.SignedString(s.refreshKey)
	if err != nil {
		return "", time.Time{}, "", err
	}
	return signed, exp, jti, nil
}

// ParseAccess validates the access JWT and returns the claims.
func (s *Service) ParseAccess(token string) (*Claims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("missing token")
	}
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(_ *jwt.Token) (any, error) {
		return s.accessKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid || parsed.Header["typ"] != accessTokenType ||
		claims.TokenUse != accessTokenUse || claims.UID == "" ||
		claims.Subject != claims.UID || claims.ID == "" || claims.IssuedAt == nil ||
		strings.TrimSpace(claims.SessionID) == "" {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// ParseRefresh validates the refresh JWT and returns the claims.
func (s *Service) ParseRefresh(token string) (*RefreshClaims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("missing token")
	}
	parsed, err := jwt.ParseWithClaims(token, &RefreshClaims{}, func(_ *jwt.Token) (any, error) {
		return s.refreshKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*RefreshClaims)
	if !ok || !parsed.Valid || parsed.Header["typ"] != refreshTokenType ||
		claims.TokenUse != refreshTokenUse || claims.UID == "" ||
		claims.Subject != claims.UID || claims.ID == "" || claims.IssuedAt == nil {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// AccessTTL returns the configured access token lifetime.
func (s *Service) AccessTTL() time.Duration { return s.accessTTL }

// RefreshTTL returns the configured refresh token lifetime.
func (s *Service) RefreshTTL() time.Duration { return s.refreshTTL }
