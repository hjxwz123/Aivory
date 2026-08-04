package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrLastAdmin is returned when an account mutation would leave no active
// administrator able to manage the deployment.
var ErrLastAdmin = errors.New("cannot remove the last remaining active admin")

var (
	// ErrWorkspaceOwnership prevents account deletion from implicitly destroying
	// a collaborative workspace that still has another member.
	ErrWorkspaceOwnership = errors.New("transfer or remove all other workspace members before deleting this account")
	// ErrWorkspaceOwnerDeleting closes the race where a member's resources would
	// be transferred to an owner whose own deletion is already in progress.
	ErrWorkspaceOwnerDeleting = errors.New("workspace owner is being deleted; retry after that deletion completes")
	// ErrUserCredentialsChanged binds self-deletion to the password hash that was
	// verified by the request before the deletion transaction acquired its lock.
	ErrUserCredentialsChanged = errors.New("account credentials changed; confirm the current password again")
)

// Batched deletion helpers for the async user-deletion job (§ async user
// delete). The heavy tables (messages via conversations, usage_logs) are
// drained in short per-batch transactions BEFORE the final DeleteUser sweep,
// so SQLite's single writer is never blocked by one huge transaction and
// Postgres avoids a long-running lock.

// ConversationIDsByUser returns a deletion-scoped batch: personal/orphaned
// conversations plus all conversations in sole-member workspaces owned by the
// user. Collaborative conversations in another live workspace are excluded.
func ConversationIDsByUser(ctx context.Context, db *sql.DB, userID string, limit int) ([]string, error) {
	if err := validateUserDeletionReadState(ctx, db, userID); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT deletion_conversation.id
		   FROM conversations deletion_conversation
		  WHERE `+userDeletionResourceScope("deletion_conversation")+`
		  ORDER BY deletion_conversation.id LIMIT ?`,
		append(userDeletionResourceScopeArgs(userID), limit)...)
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

// DeleteConversationRows removes only ids that are still inside userID's
// deletion scope. Revalidation and workspace locks prevent an asynchronously
// selected batch from crossing into collaborative state before the write.
func DeleteConversationRows(ctx context.Context, db *sql.DB, userID string, ids []string) error {
	ids = cleanIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	workspaces, err := lockUserDeletionWorkspacesTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	exists, err := lockUserDeletionUsersTx(ctx, tx, userID, workspaces)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if err := validateLockedUserDeletionWorkspaces(ctx, tx, userID, workspaces); err != nil {
		return err
	}
	ph := idPlaceholders(len(ids))
	scopedIDs := `SELECT deletion_conversation.id FROM conversations deletion_conversation
		WHERE deletion_conversation.id IN (` + ph + `)
		  AND ` + userDeletionResourceScope("deletion_conversation")
	args := anySlice(ids)
	args = append(args, userDeletionResourceScopeArgs(userID)...)
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE conversation_id IN (`+scopedIDs+`)`, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE conversation_id IN (`+scopedIDs+`)`, args...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id IN (`+scopedIDs+`)`, args...); err != nil {
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

// MarkUserDeleting flips the account into the terminal 'deleting' state.
// Payment-order creation and account deletion both lock the user row, so a
// checkout can never be inserted after the pending-order check. Active admin
// rows are locked in a stable order to keep the last-admin guard safe when two
// administrators try to delete accounts concurrently.
// Returns (false, nil) when the row is already 'deleting' (idempotent), and
// ErrLastAdmin when the guard blocked the transition.
func MarkUserDeleting(ctx context.Context, db *sql.DB, userID string, expectedPasswordHash ...string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck

	workspaces, err := lockUserDeletionWorkspacesTx(ctx, tx, userID)
	if err != nil {
		return false, err
	}
	activeAdminIDs, err := lockActiveAdminIDs(ctx, tx)
	if err != nil {
		return false, err
	}
	exists, err := lockUserDeletionUsersTx(ctx, tx, userID, workspaces)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, sql.ErrNoRows
	}
	var status, role, currentPasswordHash string
	if err := tx.QueryRowContext(ctx,
		`SELECT status, role, password_hash FROM users WHERE id=?`, userID,
	).Scan(&status, &role, &currentPasswordHash); err != nil {
		return false, err
	}
	if err := validateLockedUserDeletionWorkspaces(ctx, tx, userID, workspaces); err != nil {
		return false, err
	}
	hasExpectedPassword := len(expectedPasswordHash) > 0
	expectedHash := ""
	if hasExpectedPassword {
		expectedHash = expectedPasswordHash[0]
		if expectedHash == "" || currentPasswordHash != expectedHash {
			return false, ErrUserCredentialsChanged
		}
	}
	if status == "deleting" {
		if err := transferUserWorkspaceResourcesTx(ctx, tx, userID, workspaces); err != nil {
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if pending, err := hasPendingPaymentOrdersForUser(ctx, tx, userID); err != nil {
		return false, err
	} else if pending {
		return false, ErrPaymentOrdersPendingForUser
	}
	if role == "admin" {
		hasOtherActiveAdmin := false
		for _, id := range activeAdminIDs {
			if id != userID {
				hasOtherActiveAdmin = true
				break
			}
		}
		if !hasOtherActiveAdmin {
			return false, ErrLastAdmin
		}
	}
	if err := transferUserWorkspaceResourcesTx(ctx, tx, userID, workspaces); err != nil {
		return false, err
	}

	updateQuery := `UPDATE users SET status='deleting', token_ver=token_ver+1 WHERE id=? AND status<>'deleting'`
	updateArgs := []any{userID}
	if hasExpectedPassword {
		updateQuery += ` AND password_hash=?`
		updateArgs = append(updateArgs, expectedHash)
	}
	res, err := tx.ExecContext(ctx, updateQuery, updateArgs...)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if hasExpectedPassword {
			return false, ErrUserCredentialsChanged
		}
		return false, ErrLastAdmin
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=?`, userID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// SetUserStatusGuarded is SetUserStatus for ban/unban paths: it refuses to
// touch an account mid-purge (atomic — no check-then-act window). Returns
// false when the row was 'deleting' or missing.
func SetUserStatusGuarded(ctx context.Context, db *sql.DB, userID, status string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	activeAdminIDs, err := lockActiveAdminIDs(ctx, tx)
	if err != nil {
		return false, err
	}
	query := `SELECT role, status FROM users WHERE id=?`
	if usePostgres {
		query += ` FOR UPDATE`
	}
	var role, currentStatus string
	if err := tx.QueryRowContext(ctx, query, userID).Scan(&role, &currentStatus); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if currentStatus == "deleting" {
		return false, nil
	}
	if role == "admin" && currentStatus == "active" && status != "active" && !hasOtherActiveAdmin(activeAdminIDs, userID) {
		return false, ErrLastAdmin
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE users
		    SET status=?, token_ver=CASE WHEN ?<>'active' THEN token_ver+1 ELSE token_ver END
		  WHERE id=? AND status<>'deleting'`, status, status, userID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if status != "active" {
		if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked=1 WHERE user_id=? AND revoked=0`, userID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
