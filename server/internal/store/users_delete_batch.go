package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrLastAdmin is returned by MarkUserDeleting when deleting the account
// would leave zero active admins.
var ErrLastAdmin = errors.New("cannot delete the last remaining admin")

// Batched deletion helpers for the async user-deletion job (§ async user
// delete). The heavy tables (messages via conversations, usage_logs) are
// drained in short per-batch transactions BEFORE the final DeleteUser sweep,
// so SQLite's single writer is never blocked by one huge transaction and
// Postgres avoids a long-running lock.

// ConversationIDsByUser returns up to limit conversation ids owned by the user.
// Callers loop until it returns an empty slice.
func ConversationIDsByUser(ctx context.Context, db *sql.DB, userID string, limit int) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM conversations WHERE user_id=? LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeleteConversationRows removes a batch of conversations and their messages
// in one short transaction. Child tables hanging off messages/conversations
// (artifacts, documents, chunks, conversation_shares) go via FK cascade.
func DeleteConversationRows(ctx context.Context, db *sql.DB, ids []string) error {
	ids = cleanIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	ph := idPlaceholders(len(ids))
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE conversation_id IN (`+ph+`)`, anySlice(ids)...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id IN (`+ph+`)`, anySlice(ids)...); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUsageLogsBatch removes up to limit usage rows for the user and reports
// how many went. Callers loop until it returns 0.
func DeleteUsageLogsBatch(ctx context.Context, db *sql.DB, userID string, limit int) (int64, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM usage_logs WHERE id IN (SELECT id FROM usage_logs WHERE user_id=? LIMIT ?)`,
		userID, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UsersMarkedDeleting lists accounts stuck in status='deleting' — used on
// startup to resume deletion jobs that died with the previous process.
func UsersMarkedDeleting(ctx context.Context, db *sql.DB) ([]User, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, email, name FROM users WHERE status='deleting'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// InsertPendingStorageCleanup persists the storage paths a deletion job is
// about to orphan, BEFORE any destructive row delete. Duplicate paths are
// ignored so job retries are idempotent.
func InsertPendingStorageCleanup(ctx context.Context, db *sql.DB, userID string, paths []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO pending_storage_cleanup(path, user_id, created_at) VALUES(?,?,?) ON CONFLICT(path) DO NOTHING`,
			p, userID, time.Now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeletePendingStorageCleanup marks one path as physically removed.
func DeletePendingStorageCleanup(ctx context.Context, db *sql.DB, path string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM pending_storage_cleanup WHERE path=?`, path)
	return err
}

// ListPendingStorageCleanup returns every path still awaiting physical
// deletion — the startup sweep uses this to finish work a crash abandoned.
func ListPendingStorageCleanup(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT path FROM pending_storage_cleanup`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkUserDeleting flips the account into the terminal 'deleting' state with
// the last-admin guard folded into the UPDATE itself, closing the TOCTOU
// where two admins deleting each other could leave zero active admins.
// Returns (false, nil) when the row is already 'deleting' (idempotent), and
// ErrLastAdmin when the guard blocked the transition.
func MarkUserDeleting(ctx context.Context, db *sql.DB, userID string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE users SET status='deleting'
		 WHERE id=? AND status<>'deleting'
		   AND (role<>'admin' OR EXISTS (
		     SELECT 1 FROM users u2 WHERE u2.role='admin' AND u2.status='active' AND u2.id<>users.id
		   ))`, userID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var status string
		if qerr := db.QueryRowContext(ctx, `SELECT status FROM users WHERE id=?`, userID).Scan(&status); qerr != nil {
			return false, qerr
		}
		if status == "deleting" {
			return false, nil
		}
		return false, ErrLastAdmin
	}
	// Same instant lockout a ban performs (§8.1).
	if err := BumpTokenVersion(ctx, db, userID); err != nil {
		return true, err
	}
	_, err = db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=?`, userID)
	return true, err
}

// SetUserStatusGuarded is SetUserStatus for ban/unban paths: it refuses to
// touch an account mid-purge (atomic — no check-then-act window). Returns
// false when the row was 'deleting' or missing.
func SetUserStatusGuarded(ctx context.Context, db *sql.DB, userID, status string) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE users SET status=? WHERE id=? AND status<>'deleting'`, status, userID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if status != "active" {
		if err := BumpTokenVersion(ctx, db, userID); err != nil {
			return true, err
		}
		if _, err := db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=?`, userID); err != nil {
			return true, err
		}
	}
	return true, nil
}
