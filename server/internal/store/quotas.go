package store

import (
	"context"
	"database/sql"
	"errors"
)

// ListModelQuotas returns every per-group quota row for a model.
func ListModelQuotas(ctx context.Context, db *sql.DB, modelID string) ([]ModelGroupQuota, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT model_id, group_id, period_seconds, limit_type, limit_value FROM model_group_quotas WHERE model_id=?`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelGroupQuota{}
	for rows.Next() {
		var q ModelGroupQuota
		if err := rows.Scan(&q.ModelID, &q.GroupID, &q.PeriodSeconds, &q.LimitType, &q.LimitValue); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// SetModelQuotas replaces ALL quota rows for a model in one transaction.
func SetModelQuotas(ctx context.Context, db *sql.DB, modelID string, quotas []ModelGroupQuota) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_group_quotas WHERE model_id=?`, modelID); err != nil {
		return err
	}
	for _, q := range quotas {
		if q.GroupID == "" {
			continue
		}
		lt := q.LimitType
		if lt != "cost" && lt != "count" {
			lt = "count"
		}
		ps := q.PeriodSeconds
		if ps <= 0 {
			ps = 604800
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO model_group_quotas(model_id, group_id, period_seconds, limit_type, limit_value) VALUES(?, ?, ?, ?, ?)`,
			modelID, q.GroupID, ps, lt, q.LimitValue); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetModelQuota returns the quota row for (model, group), or ErrNotFound.
func GetModelQuota(ctx context.Context, db *sql.DB, modelID, groupID string) (*ModelGroupQuota, error) {
	var q ModelGroupQuota
	err := db.QueryRowContext(ctx,
		`SELECT model_id, group_id, period_seconds, limit_type, limit_value FROM model_group_quotas WHERE model_id=? AND group_id=?`,
		modelID, groupID).Scan(&q.ModelID, &q.GroupID, &q.PeriodSeconds, &q.LimitType, &q.LimitValue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// ImageUsageInWindow aggregates a user's IMAGE generation for one model inside a
// fixed window — summed cost and image COUNT (images_count, not rows). §4.20:
// both the drawing-mode path and the chat tool-call path log purpose='image'
// against the same image model id, so this is the shared quota source for an
// image model regardless of how the generation was triggered.
func ImageUsageInWindow(ctx context.Context, db *sql.DB, userID, modelID string, sinceUnix int64) (cost float64, images int, err error) {
	err = db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost),0), COALESCE(SUM(images_count),0) FROM usage_logs
		 WHERE user_id=? AND model_id=? AND purpose='image' AND COALESCE(status,'ok')<>'error' AND created_at>=?`,
		userID, modelID, sinceUnix).Scan(&cost, &images)
	return cost, images, err
}

// ModelHasAnyQuota reports whether a model is restricted (has ≥1 quota row).
// A model with no rows is open to everyone, unlimited.
func ModelHasAnyQuota(ctx context.Context, db *sql.DB, modelID string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_group_quotas WHERE model_id=?`, modelID).Scan(&n)
	return n > 0, err
}

// RestrictedModelIDs returns the set of model ids that have ≥1 quota row, so
// callers can compute "locked" for many models with one query.
func RestrictedModelIDs(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT model_id FROM model_group_quotas`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// QuotasForGroup returns model_id → quota for one group (used to compute which
// restricted models a group can access, for the picker).
func QuotasForGroup(ctx context.Context, db *sql.DB, groupID string) (map[string]ModelGroupQuota, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT model_id, group_id, period_seconds, limit_type, limit_value FROM model_group_quotas WHERE group_id=?`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ModelGroupQuota{}
	for rows.Next() {
		var q ModelGroupQuota
		if err := rows.Scan(&q.ModelID, &q.GroupID, &q.PeriodSeconds, &q.LimitType, &q.LimitValue); err != nil {
			return nil, err
		}
		out[q.ModelID] = q
	}
	return out, rows.Err()
}

// UsageInWindow sums chat cost + call count for one user+model since a unix time
// — the authoritative fallback when the cache counter is cold. Error rows
// (status='error', logged so admin/usage can count failures) are excluded: a
// failed request produced nothing and must not burn a count-based quota.
func UsageInWindow(ctx context.Context, db *sql.DB, userID, modelID string, sinceUnix int64) (cost float64, count int, err error) {
	err = db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost),0), COUNT(*) FROM usage_logs
		 WHERE user_id=? AND model_id=? AND purpose='chat' AND COALESCE(status,'ok')<>'error' AND created_at>=?`,
		userID, modelID, sinceUnix).Scan(&cost, &count)
	return cost, count, err
}
