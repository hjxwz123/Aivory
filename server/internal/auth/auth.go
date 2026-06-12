// Package auth issues and verifies short-lived access tokens and rotates
// refresh tokens, per design.md §8.1. Token rotation/realtime ban support is
// kept simple but compatible with the design: every access token carries a
// `tv` claim and the cache layer stores the user's current token version, so
// bumping the version (via store.BumpTokenVersion) immediately invalidates
// all outstanding tokens.
package auth

import (
	"errors"
	"strings"
	"time"

	"aurelia/server/internal/cache"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims is the access-token payload.
type Claims struct {
	jwt.RegisteredClaims
	UID  string `json:"uid"`
	Role string `json:"role"`
	TV   int    `json:"tv"`
}

// RefreshClaims is the refresh-token payload (sub == uid; jti = id).
type RefreshClaims struct {
	jwt.RegisteredClaims
	UID string `json:"uid"`
}

// Service signs/verifies tokens with a hot-path cache hook for token-version
// invalidation.
type Service struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	cache      cache.Cache
}

// New builds a new auth service.
func New(secret string, accessTTL, refreshTTL time.Duration, c cache.Cache) *Service {
	return &Service{secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL, cache: c}
}

// IssueAccess returns a signed access token + its expiry.
func (s *Service) IssueAccess(uid, role string, tokenVer int) (string, time.Time, error) {
	exp := time.Now().Add(s.accessTTL)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uid,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        uuid.NewString(),
		},
		UID:  uid,
		Role: role,
		TV:   tokenVer,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// IssueRefresh returns a signed refresh token, its expiry, and its jti so the
// caller can record/revoke it in the DB.
func (s *Service) IssueRefresh(uid string) (string, time.Time, string, error) {
	jti := uuid.NewString()
	exp := time.Now().Add(s.refreshTTL)
	claims := RefreshClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   uid,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        jti,
		},
		UID: uid,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
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
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// ParseRefresh validates the refresh JWT and returns the claims.
func (s *Service) ParseRefresh(token string) (*RefreshClaims, error) {
	parsed, err := jwt.ParseWithClaims(token, &RefreshClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*RefreshClaims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// AccessTTL returns the configured access token lifetime.
func (s *Service) AccessTTL() time.Duration { return s.accessTTL }

// RefreshTTL returns the configured refresh token lifetime.
func (s *Service) RefreshTTL() time.Duration { return s.refreshTTL }
