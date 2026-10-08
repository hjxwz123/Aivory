package store

import (
	"context"
	"database/sql"
	"time"
)

// SaveRefreshTokenForDesktopAuthorization serializes browser-session validation
// and desktop-session creation with account security changes and session revoke.
func SaveRefreshTokenForDesktopAuthorization(ctx context.Context, db *sql.DB, jti, userID, sourceSessionID string, tokenVer int, expiresAt time.Time, meta SessionMeta) error {
	if sourceSessionID == "" || tokenVer < 0 {
		return ErrLoginStateChanged
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE users SET token_ver=token_ver WHERE id=? AND status='active' AND token_ver=?`, userID, tokenVer)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrLoginStateChanged
	}
	// Lock the source family as well, so per-device revocation cannot race this
	// handoff. Refresh rotation keeps the family ID stable.
	res, err = tx.ExecContext(ctx, `UPDATE refresh_tokens SET last_seen=last_seen
		WHERE user_id=? AND revoked=0 AND expires_at>?
		AND CASE WHEN trim(session_id)<>'' THEN session_id ELSE jti END=?`, userID, time.Now().Unix(), sourceSessionID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrLoginStateChanged
	}
	if err := saveRefreshToken(ctx, tx, jti, userID, expiresAt, meta); err != nil {
		return err
	}
	return tx.Commit()
}
