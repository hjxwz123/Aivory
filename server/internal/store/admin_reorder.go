package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidReorder = errors.New("reorder list must include every item exactly once")

// reorderAdminRecords persists a complete display order in one transaction.
// Callers pass only the fixed table names below; the allowlist keeps the SQL
// identifier out of request-controlled data.
func reorderAdminRecords(ctx context.Context, db *sql.DB, table string, ids []string) error {
	switch table {
	case "image_styles", "skills", "prompts", "oauth_providers":
	default:
		return fmt.Errorf("unsupported reorder table %q", table)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		return err
	}
	if len(ids) != count {
		return ErrInvalidReorder
	}

	seen := make(map[string]struct{}, len(ids))
	now := time.Now().Unix()
	for index, id := range ids {
		if id == "" {
			return ErrInvalidReorder
		}
		if _, exists := seen[id]; exists {
			return ErrInvalidReorder
		}
		seen[id] = struct{}{}

		result, err := tx.ExecContext(ctx,
			`UPDATE `+table+` SET sort_order=?, updated_at=? WHERE id=?`, index, now, id)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return ErrInvalidReorder
		}
	}

	return tx.Commit()
}
