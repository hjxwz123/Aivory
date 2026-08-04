package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrInvalidRefreshToken means the presented token is missing, expired,
// revoked, belongs to another user, or predates a token-version rotation.
var ErrInvalidRefreshToken = errors.New("invalid refresh token")

// RotateRefreshToken consumes oldJTI exactly once and inserts its replacement
// in the same transaction. Locking the user row first establishes the same lock
// order as UpdateUserPassword, so a concurrent password reset either revokes the
// newly inserted token or makes this rotation fail its token-version check.
func RotateRefreshToken(
	ctx context.Context,
	db *sql.DB,
	oldJTI, userID string,
	expectedTokenVer int,
	newJTI string,
	newExpiresAt time.Time,
	meta SessionMeta,
) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// A no-op UPDATE is portable across SQLite/PostgreSQL and takes the user-row
	// write lock needed to serialize rotation with password/session invalidation.
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET token_ver=token_ver
		 WHERE id=? AND status='active' AND token_ver=?`, userID, expectedTokenVer)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", err
	} else if n != 1 {
		return "", ErrInvalidRefreshToken
	}

	now := time.Now().Unix()
	var (
		createdAt int64
		sessionID string
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT created_at,
		        CASE WHEN trim(session_id)<>'' THEN session_id ELSE jti END
		 FROM refresh_tokens
		 WHERE jti=? AND user_id=? AND revoked=0 AND expires_at>?`,
		oldJTI, userID, now).Scan(&createdAt, &sessionID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidRefreshToken
	} else if err != nil {
		return "", err
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked=1
		 WHERE jti=? AND user_id=? AND revoked=0 AND expires_at>?`,
		oldJTI, userID, now)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", err
	} else if n != 1 {
		return "", ErrInvalidRefreshToken
	}
	if meta.CreatedAt == 0 {
		meta.CreatedAt = createdAt
	}
	if strings.TrimSpace(meta.SessionID) != "" && meta.SessionID != sessionID {
		return "", errors.New("refresh session family cannot change during rotation")
	}
	meta.SessionID = sessionID
	_, err = tx.ExecContext(ctx,
		`INSERT INTO refresh_tokens(jti, session_id, user_id, expires_at, revoked, created_at, user_agent, ip, location, last_seen)
		 VALUES(?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
		newJTI, sessionID, userID, newExpiresAt.Unix(), meta.CreatedAt,
		meta.UserAgent, meta.IP, meta.Location, now)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return sessionID, nil
}

// A consumed JTI is intentionally rejected without revoking the whole family.
// A duplicate in-flight refresh is indistinguishable from token theft at this
// layer; revoking the family here would also revoke the legitimate successor
// that won the same rotation race. Callers can explicitly revoke the family via
// RevokeUserSession when a reuse detector has stronger evidence.
