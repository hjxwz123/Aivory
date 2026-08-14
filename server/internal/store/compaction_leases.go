package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// TryAcquireConversationCompactionLease atomically claims the exclusive
// context-compaction lease for one conversation. The database is authoritative
// so the exclusion still holds when application replicas use independent
// in-memory caches. Expired leases are replaced by the caller that wins the
// insert; release remains owner-token guarded so an old worker cannot delete a
// newer worker's lease.
func TryAcquireConversationCompactionLease(ctx context.Context, db *sql.DB, conversationID, ownerToken string, ttl time.Duration) (bool, error) {
	if db == nil {
		return false, errors.New("database is not initialized")
	}
	conversationID = strings.TrimSpace(conversationID)
	ownerToken = strings.TrimSpace(ownerToken)
	if conversationID == "" || ownerToken == "" {
		return false, errors.New("conversation compaction lease requires conversation and owner ids")
	}
	if ttl <= 0 {
		return false, errors.New("conversation compaction lease ttl must be positive")
	}

	now := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// Only clear the row for this conversation. A global expired-row sweep here
	// would turn every compaction attempt into avoidable write contention.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_compaction_leases WHERE conversation_id=? AND expires_at<=?`,
		conversationID, now.UnixNano(),
	); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO conversation_compaction_leases(conversation_id, owner_token, expires_at)
		 VALUES(?,?,?) ON CONFLICT(conversation_id) DO NOTHING`,
		conversationID, ownerToken, now.Add(ttl).UnixNano(),
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return rows == 1, nil
}

// ReleaseConversationCompactionLease releases a lease only when ownerToken is
// still the owner. This makes a late cleanup harmless after a timed-out lease
// was claimed by another worker.
func ReleaseConversationCompactionLease(ctx context.Context, db *sql.DB, conversationID, ownerToken string) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	conversationID = strings.TrimSpace(conversationID)
	ownerToken = strings.TrimSpace(ownerToken)
	if conversationID == "" || ownerToken == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`DELETE FROM conversation_compaction_leases WHERE conversation_id=? AND owner_token=?`,
		conversationID, ownerToken,
	)
	return err
}
