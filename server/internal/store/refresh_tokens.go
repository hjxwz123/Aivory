package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	// ErrInvalidRefreshToken means the presented token is missing, expired,
	// belongs to another user, or predates a token-version rotation.
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	// ErrRefreshTokenReplay means a still-live refresh token was already
	// consumed. Callers must treat this as a credential-theft signal: its entire
	// session family has been revoked before this error is returned.
	ErrRefreshTokenReplay = errors.New("refresh token replay detected")
)

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
		// A revoked, unexpired JTI is an attempted reuse of a token that already
		// won a rotation race. The only safe response is to revoke the successor
		// too, so a copied refresh cookie cannot silently take over a session.
		var replaySessionID string
		replayErr := tx.QueryRowContext(ctx,
			`SELECT CASE WHEN trim(session_id)<>'' THEN session_id ELSE jti END
			 FROM refresh_tokens
			 WHERE jti=? AND user_id=? AND revoked=1 AND expires_at>?`,
			oldJTI, userID, now).Scan(&replaySessionID)
		if errors.Is(replayErr, sql.ErrNoRows) {
			return "", ErrInvalidRefreshToken
		}
		if replayErr != nil {
			return "", replayErr
		}
		if _, replayErr = tx.ExecContext(ctx,
			`UPDATE refresh_tokens SET revoked=1
			 WHERE user_id=? AND revoked=0
			   AND CASE WHEN trim(session_id)<>'' THEN session_id ELSE jti END=?`,
			userID, replaySessionID); replayErr != nil {
			return "", replayErr
		}
		if replayErr = tx.Commit(); replayErr != nil {
			return "", replayErr
		}
		return "", ErrRefreshTokenReplay
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

// A consumed, unexpired JTI revokes the entire family (see
// ErrRefreshTokenReplay). Browser clients serialize refreshes across tabs; a
// second device that presents a copied cookie is therefore treated as theft,
// not as a benign retry.
