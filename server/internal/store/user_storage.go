package store

import (
	"context"
	"database/sql"
)

// User storage accounting (§ user files page). Only NON-IMAGE uploads count
// against the quota: files rows with kind<>'image' plus documents the user
// owns (KB docs via the KB, conversation docs via the conversation) that do
// not share a storage path with a files row (those are the same bytes as the
// files twin and must not be double-counted).

// UserStorageUsage returns the user's quota-relevant bytes.
func UserStorageUsage(ctx context.Context, db *sql.DB, userID string) (int64, error) {
	var n sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(size_bytes), 0) FROM (
			SELECT f.size_bytes FROM files f WHERE f.user_id=? AND f.kind<>'image'
			UNION ALL
			SELECT d.size_bytes
			  FROM documents d
			  LEFT JOIN knowledge_bases k ON k.id = d.kb_id
			  LEFT JOIN conversations c ON c.id = d.conversation_id
			 WHERE COALESCE(k.user_id, c.user_id, '') = ?
			   AND NOT EXISTS (SELECT 1 FROM files f2 WHERE f2.storage_path = d.storage_path)
		) t`, userID, userID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// StorageQuotaBytes resolves the user's group storage cap in bytes.
// 0 = unlimited (no group, group without a cap, or lookup failure fails open —
// quota is a soft product limit, not a security boundary).
func StorageQuotaBytes(ctx context.Context, db *sql.DB, userID string) (int64, error) {
	var mb sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(g.max_storage_mb, 0)
		  FROM users u LEFT JOIN user_groups g ON g.id = u.group_id
		 WHERE u.id=?`, userID).Scan(&mb)
	if err != nil {
		return 0, err
	}
	return mb.Int64 * 1024 * 1024, nil
}
