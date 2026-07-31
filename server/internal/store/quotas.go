package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"
)

var defaultQuotaPeriodSeconds = 604800

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
	existing := map[string]ModelGroupQuota{}
	rows, err := tx.QueryContext(ctx,
		`SELECT model_id, group_id, period_seconds, limit_type, limit_value FROM model_group_quotas WHERE model_id=?`, modelID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var q ModelGroupQuota
		if err := rows.Scan(&q.ModelID, &q.GroupID, &q.PeriodSeconds, &q.LimitType, &q.LimitValue); err != nil {
			_ = rows.Close()
			return err
		}
		existing[q.GroupID] = q
	}
	if err := rows.Close(); err != nil {
		return err
	}
	normalized := make([]ModelGroupQuota, 0, len(quotas))
	for _, q := range quotas {
		if q.GroupID == "" {
			continue
		}
		if q.LimitType != "cost" && q.LimitType != "count" {
			return ErrInvalidCreditConfig
		}
		if q.PeriodSeconds <= 0 {
			q.PeriodSeconds = defaultQuotaPeriodSeconds
		}
		if err := ValidateModelGroupQuota(q); err != nil {
			return err
		}
		q.ModelID = modelID
		normalized = append(normalized, q)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_group_quotas WHERE model_id=?`, modelID); err != nil {
		return err
	}
	changedGroups := map[string]bool{}
	for _, q := range normalized {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO model_group_quotas(model_id, group_id, period_seconds, limit_type, limit_value) VALUES(?, ?, ?, ?, ?)`,
			modelID, q.GroupID, q.PeriodSeconds, q.LimitType, q.LimitValue); err != nil {
			return err
		}
		old, ok := existing[q.GroupID]
		if !ok || old.PeriodSeconds != q.PeriodSeconds || old.LimitType != q.LimitType || old.LimitValue != q.LimitValue {
			changedGroups[q.GroupID] = true
		}
		delete(existing, q.GroupID)
	}
	for groupID := range existing {
		changedGroups[groupID] = true
	}
	if len(changedGroups) > 0 {
		now := time.Now().Unix()
		for groupID := range changedGroups {
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET quota_cycle_anchor=CASE WHEN quota_cycle_anchor>=? THEN quota_cycle_anchor+1 ELSE ? END WHERE group_id=?`,
				now, now, groupID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

const (
	QuotaScopeModelChat  = "model_chat"
	QuotaScopeModelImage = "model_image"
	QuotaScopeDailyImage = "daily_image"
	QuotaScopeDailyToken = "daily_token"

	QuotaStatusReserved  = "reserved"
	QuotaStatusFinalized = "finalized"
	QuotaStatusReleased  = "released"
)

var (
	ErrDailyTokenQuotaExceeded = errors.New("daily token quota reached")
	ErrQuotaConfigChanged      = errors.New("quota configuration changed")
)

type UserQuotaScope struct {
	GroupID string
	Anchor  int64
}

type QuotaReservation struct {
	ID            string
	UserID        string
	ScopeType     string
	ModelID       string
	GroupID       string
	CycleAnchor   int64
	WindowStart   int64
	LimitType     string
	ReservedValue float64
	ActualValue   float64
	Status        string
	ExpiresAt     int64
}

func GetUserQuotaScope(ctx context.Context, db *sql.DB, userID string) (UserQuotaScope, error) {
	u, err := FindUserByID(ctx, db, userID)
	if err != nil {
		return UserQuotaScope{}, err
	}
	var anchor int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(quota_cycle_anchor,0) FROM users WHERE id=?`, userID).Scan(&anchor); err != nil {
		return UserQuotaScope{}, err
	}
	if anchor <= 0 {
		anchor = time.Now().Unix()
	}
	return UserQuotaScope{GroupID: u.GroupID, Anchor: anchor}, nil
}

func quotaValueToMicros(value float64) (int64, error) {
	micros, err := CreditsToMicros(value)
	if err != nil {
		return 0, ErrInvalidCreditConfig
	}
	return micros, nil
}

func ValidateModelGroupQuota(q ModelGroupQuota) error {
	if q.PeriodSeconds <= 0 || q.LimitType != "cost" && q.LimitType != "count" {
		return ErrInvalidCreditConfig
	}
	micros, err := quotaValueToMicros(q.LimitValue)
	if err != nil || q.LimitValue > 0 && micros == 0 {
		return ErrInvalidCreditConfig
	}
	return nil
}

func quotaValueFromMicros(value int64) float64 {
	return float64(value) / 1e6
}

// ReserveModelQuota atomically reserves a free allowance against the user's
// membership-anchored group window. For cost quotas, reserveRemaining asks for
// the entire remaining allowance so concurrent turns cannot all pass the same
// read-before-write gate; unused value is released when the turn finalizes.
func ReserveModelQuota(ctx context.Context, db *sql.DB, userID, modelID, scopeType string, q ModelGroupQuota, requested float64, reserveRemaining bool) (*QuotaReservation, bool, error) {
	if scopeType != QuotaScopeModelChat && scopeType != QuotaScopeModelImage {
		return nil, false, ErrInvalidCreditConfig
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if !usePostgres {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET quota_cycle_anchor=quota_cycle_anchor WHERE id=?`, userID); err != nil {
			return nil, false, err
		}
	}
	userQuery := `SELECT group_id, COALESCE(quota_cycle_anchor,0) FROM users WHERE id=?`
	if usePostgres {
		userQuery += ` FOR UPDATE`
	}
	var groupID string
	var anchor int64
	if err := tx.QueryRowContext(ctx, userQuery, userID).Scan(&groupID, &anchor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	if groupID != q.GroupID {
		return nil, false, ErrInvalidCreditConfig
	}
	var current ModelGroupQuota
	if err := tx.QueryRowContext(ctx,
		`SELECT model_id, group_id, period_seconds, limit_type, limit_value
		   FROM model_group_quotas WHERE model_id=? AND group_id=?`,
		modelID, groupID,
	).Scan(&current.ModelID, &current.GroupID, &current.PeriodSeconds, &current.LimitType, &current.LimitValue); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, ErrQuotaConfigChanged
		}
		return nil, false, err
	}
	if (q.ModelID != "" && current.ModelID != q.ModelID) || current.GroupID != q.GroupID ||
		current.PeriodSeconds != q.PeriodSeconds || current.LimitType != q.LimitType || current.LimitValue != q.LimitValue {
		return nil, false, ErrQuotaConfigChanged
	}
	now := time.Now().Unix()
	if anchor <= 0 {
		anchor = now
		if _, err := tx.ExecContext(ctx, `UPDATE users SET quota_cycle_anchor=? WHERE id=?`, anchor, userID); err != nil {
			return nil, false, err
		}
	}
	windowStart, windowEnd := CreditCycleStart(anchor, q.PeriodSeconds, now)
	limitMicros, err := quotaValueToMicros(q.LimitValue)
	if err != nil {
		return nil, false, err
	}
	requestedMicros, err := quotaValueToMicros(requested)
	if err != nil {
		return nil, false, err
	}
	var usedMicros int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN status=? THEN actual_micros ELSE reserved_micros END),0)
		   FROM quota_ledger
		  WHERE user_id=? AND scope_type=? AND model_id=? AND group_id=? AND cycle_anchor=? AND window_start=?
		    AND status IN (?, ?) AND expires_at>?`,
		QuotaStatusFinalized, userID, scopeType, modelID, groupID, anchor, windowStart,
		QuotaStatusReserved, QuotaStatusFinalized, now).Scan(&usedMicros); err != nil {
		return nil, false, err
	}
	remaining := limitMicros - usedMicros
	if remaining <= 0 {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	reserveMicros := requestedMicros
	if reserveRemaining {
		reserveMicros = remaining
	}
	if reserveMicros <= 0 || reserveMicros > remaining {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	reservation := &QuotaReservation{
		ID: genID("qr"), UserID: userID, ScopeType: scopeType, ModelID: modelID,
		GroupID: groupID, CycleAnchor: anchor, WindowStart: windowStart, LimitType: q.LimitType,
		ReservedValue: quotaValueFromMicros(reserveMicros), Status: QuotaStatusReserved, ExpiresAt: windowEnd,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO quota_ledger(id,user_id,scope_type,model_id,group_id,cycle_anchor,window_start,limit_type,reserved_micros,actual_micros,status,expires_at,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		reservation.ID, userID, reservation.ScopeType, modelID, groupID, anchor, windowStart, q.LimitType,
		reserveMicros, 0, QuotaStatusReserved, windowEnd, now, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return reservation, true, nil
}

// ReserveFixedQuota atomically reserves a deployment-wide count allowance such
// as the daily image cap. It shares quota_ledger with model allowances so
// analytics deletion and concurrent requests cannot reset or oversubscribe it.
func ReserveFixedQuota(ctx context.Context, db *sql.DB, userID, scopeType string, requested, limit int, windowStart, expiresAt int64) (*QuotaReservation, bool, error) {
	if requested <= 0 || limit <= 0 || windowStart <= 0 || expiresAt <= windowStart {
		return nil, false, ErrInvalidCreditConfig
	}
	if int64(requested) > math.MaxInt64/CreditMicrosPerUnit || int64(limit) > math.MaxInt64/CreditMicrosPerUnit {
		return nil, false, ErrInvalidCreditConfig
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if !usePostgres {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET quota_cycle_anchor=quota_cycle_anchor WHERE id=?`, userID); err != nil {
			return nil, false, err
		}
	}
	userQuery := `SELECT group_id, COALESCE(quota_cycle_anchor,0) FROM users WHERE id=?`
	if usePostgres {
		userQuery += ` FOR UPDATE`
	}
	var groupID string
	var anchor int64
	if err := tx.QueryRowContext(ctx, userQuery, userID).Scan(&groupID, &anchor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	requestedMicros := int64(requested) * CreditMicrosPerUnit
	limitMicros := int64(limit) * CreditMicrosPerUnit
	now := time.Now().Unix()
	var usedMicros int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN status=? THEN actual_micros ELSE reserved_micros END),0)
		   FROM quota_ledger
		  WHERE user_id=? AND scope_type=? AND window_start=? AND status IN (?, ?) AND expires_at>?`,
		QuotaStatusFinalized, userID, scopeType, windowStart, QuotaStatusReserved, QuotaStatusFinalized, now).
		Scan(&usedMicros); err != nil {
		return nil, false, err
	}
	if requestedMicros > limitMicros-usedMicros {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	reservation := &QuotaReservation{
		ID: genID("qr"), UserID: userID, ScopeType: scopeType, GroupID: groupID,
		CycleAnchor: anchor, WindowStart: windowStart, LimitType: "count",
		ReservedValue: float64(requested), Status: QuotaStatusReserved, ExpiresAt: expiresAt,
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO quota_ledger(id,user_id,scope_type,model_id,group_id,cycle_anchor,window_start,limit_type,reserved_micros,actual_micros,status,expires_at,created_at,updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		reservation.ID, userID, scopeType, "", groupID, anchor, windowStart, "count",
		requestedMicros, 0, QuotaStatusReserved, expiresAt, now, now); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return reservation, true, nil
}

// ReserveDailyTokenQuota atomically holds the estimated token budget for one
// user-owned provider call. The reservation is finalized with provider-reported
// tokens, so concurrent chat and internal task calls share the same hard gate.
func ReserveDailyTokenQuota(ctx context.Context, db *sql.DB, userID string, requested int) (*QuotaReservation, bool, error) {
	if userID == "" {
		return nil, true, nil
	}
	u, err := FindUserByID(ctx, db, userID)
	if err != nil {
		return nil, false, err
	}
	if u.Role == "admin" {
		return nil, true, nil
	}
	limit := 0
	if raw, err := GetSetting(db, "daily_token_limit"); err == nil {
		if json.Unmarshal(raw, &limit) != nil || limit < 0 {
			return nil, false, ErrInvalidCreditConfig
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	if limit <= 0 {
		return nil, true, nil
	}
	if requested <= 0 {
		requested = 1
	}
	dayStart := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	reservation, allowed, err := ReserveFixedQuota(
		ctx, db, userID, QuotaScopeDailyToken, requested, limit, dayStart, dayStart+24*60*60,
	)
	if err != nil || allowed {
		return reservation, allowed, err
	}
	return nil, false, ErrDailyTokenQuotaExceeded
}

// migrateDailyTokenQuotaLedger preserves the current UTC day's legacy token
// use when a deployment first adopts quota_ledger. The marker and baselines are
// committed together so concurrent startup and failed migrations cannot double
// count or leave a partially migrated day.
func migrateDailyTokenQuotaLedger(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO settings(key,value) VALUES('daily_token_quota_ledger_backfill_v1','1') ON CONFLICT(key) DO NOTHING`)
	if err != nil {
		return err
	}
	claimed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if claimed == 0 {
		return tx.Commit()
	}
	dayStart := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	rows, err := tx.QueryContext(ctx,
		`SELECT l.user_id,u.group_id,COALESCE(u.quota_cycle_anchor,0),SUM(l.input_tokens+l.output_tokens)
		   FROM usage_logs l JOIN users u ON u.id=l.user_id
		  WHERE l.created_at>=? AND l.status<>'error'
		  GROUP BY l.user_id,u.group_id,u.quota_cycle_anchor
		 HAVING SUM(l.input_tokens+l.output_tokens)>0`, dayStart)
	if err != nil {
		return err
	}
	type legacyUsage struct {
		userID, groupID string
		anchor          int64
		tokens          int64
	}
	var usages []legacyUsage
	for rows.Next() {
		var u legacyUsage
		if err := rows.Scan(&u.userID, &u.groupID, &u.anchor, &u.tokens); err != nil {
			_ = rows.Close()
			return err
		}
		usages = append(usages, u)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, u := range usages {
		if u.tokens <= 0 || u.tokens > math.MaxInt64/CreditMicrosPerUnit {
			return ErrInvalidCreditConfig
		}
		actualMicros := u.tokens * CreditMicrosPerUnit
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO quota_ledger(id,user_id,scope_type,model_id,group_id,cycle_anchor,window_start,limit_type,reserved_micros,actual_micros,status,expires_at,created_at,updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			genID("qr"), u.userID, QuotaScopeDailyToken, "", u.groupID, u.anchor, dayStart, "count",
			actualMicros, actualMicros, QuotaStatusFinalized, dayStart+24*60*60, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ModelQuotaUsage(ctx context.Context, db *sql.DB, userID, modelID, groupID, scopeType string, anchor, windowStart int64) (float64, error) {
	var usedMicros int64
	err := db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN status=? THEN actual_micros ELSE reserved_micros END),0)
		   FROM quota_ledger
		  WHERE user_id=? AND scope_type=? AND model_id=? AND group_id=? AND cycle_anchor=? AND window_start=?
		    AND status IN (?, ?) AND expires_at>?`,
		QuotaStatusFinalized, userID, scopeType, modelID, groupID, anchor, windowStart,
		QuotaStatusReserved, QuotaStatusFinalized, time.Now().Unix()).Scan(&usedMicros)
	if err != nil {
		return 0, err
	}
	return quotaValueFromMicros(usedMicros), nil
}

func FinalizeQuotaReservation(ctx context.Context, db *sql.DB, id string, actual float64) (float64, error) {
	actualMicros, err := quotaValueToMicros(actual)
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	query := `SELECT reserved_micros,actual_micros,status FROM quota_ledger WHERE id=?`
	if usePostgres {
		query += ` FOR UPDATE`
	}
	var reservedMicros int64
	var storedActualMicros int64
	var status string
	if err := tx.QueryRowContext(ctx, query, id).Scan(&reservedMicros, &storedActualMicros, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if status == QuotaStatusReleased {
		return 0, ErrNotFound
	}
	if status == QuotaStatusFinalized {
		actualMicros = storedActualMicros
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE quota_ledger SET actual_micros=?, status=?, updated_at=? WHERE id=?`,
			actualMicros, QuotaStatusFinalized, time.Now().Unix(), id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if actualMicros <= reservedMicros {
		return 0, nil
	}
	return quotaValueFromMicros(actualMicros - reservedMicros), nil
}

func ReleaseQuotaReservation(ctx context.Context, db *sql.DB, id string) error {
	if id == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE quota_ledger SET status=?, updated_at=? WHERE id=? AND status=?`,
		QuotaStatusReleased, time.Now().Unix(), id, QuotaStatusReserved)
	return err
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

// ModelHasAnyQuota reports whether any group has an explicit free allowance for
// the model. A model with no rows is credit-paid for every non-admin user.
func ModelHasAnyQuota(ctx context.Context, db *sql.DB, modelID string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_group_quotas WHERE model_id=?`, modelID).Scan(&n)
	return n > 0, err
}

// RestrictedModelIDs returns the model ids that have at least one explicit
// group free-allowance row.
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

// QuotasForGroup returns model_id → free allowance for one group. A missing
// model entry means that group's calls are paid with credits.
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
