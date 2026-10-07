package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var modelChannelSelectionCounter atomic.Uint64

var (
	ErrInvalidModelChannelBinding = errors.New("invalid model channel binding")
	ErrUnsupportedChannelModel    = errors.New("channel does not advertise this request_id")
)

type ChannelModelHealth struct {
	ModelID             string `json:"model_id"`
	ModelLabel          string `json:"model_label"`
	RequestID           string `json:"request_id"`
	Role                string `json:"role"`
	DisabledUntil       int64  `json:"disabled_until"`
	ConsecutiveErrors   int    `json:"consecutive_errors"`
	ConsecutiveTimeouts int    `json:"consecutive_timeouts"`
}

// ListChannelModelHealth returns the binding-level quarantines for one
// channel. It is used by the administrator view and never sits on the user
// request path.
func ListChannelModelHealth(ctx context.Context, db *sql.DB, channelID string) ([]ChannelModelHealth, error) {
	rows, err := db.QueryContext(ctx, `SELECT m.id, m.label, m.request_id, b.role, b.disabled_until, b.consecutive_errors, b.consecutive_timeouts
		FROM model_channel_bindings b JOIN models m ON m.id=b.model_id
		WHERE b.channel_id=? ORDER BY lower(m.label), b.role`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ChannelModelHealth{}
	for rows.Next() {
		var item ChannelModelHealth
		if err := rows.Scan(&item.ModelID, &item.ModelLabel, &item.RequestID, &item.Role, &item.DisabledUntil, &item.ConsecutiveErrors, &item.ConsecutiveTimeouts); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ListChannelsModelHealth loads every binding's runtime state with one query
// for the administrator channel table.
func ListChannelsModelHealth(ctx context.Context, db *sql.DB) (map[string][]ChannelModelHealth, error) {
	rows, err := db.QueryContext(ctx, `SELECT b.channel_id, m.id, m.label, m.request_id, b.role, b.disabled_until, b.consecutive_errors, b.consecutive_timeouts
		FROM model_channel_bindings b JOIN models m ON m.id=b.model_id ORDER BY b.channel_id, lower(m.label), b.role`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]ChannelModelHealth{}
	for rows.Next() {
		var channelID string
		var item ChannelModelHealth
		if err := rows.Scan(&channelID, &item.ModelID, &item.ModelLabel, &item.RequestID, &item.Role, &item.DisabledUntil, &item.ConsecutiveErrors, &item.ConsecutiveTimeouts); err != nil {
			return nil, err
		}
		result[channelID] = append(result[channelID], item)
	}
	return result, rows.Err()
}

func normalizeBindingRole(role string) string {
	// The former fallback role now aliases the same priority queue. Keeping this
	// normalization also lets old callers recover or inspect legacy bindings.
	_ = role
	return "regular"
}

// backfillModelChannelConfiguration makes old single-channel rows immediately
// usable by the new resolver. It is idempotent and safe on every startup.
func backfillModelChannelConfiguration(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id, channel_id, request_id, label, description, kind, fallback_channel_id FROM models`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type legacyModel struct{ id, channelID, requestID, label, description, kind, fallbackID string }
	var models []legacyModel
	for rows.Next() {
		var m legacyModel
		if err := rows.Scan(&m.id, &m.channelID, &m.requestID, &m.label, &m.description, &m.kind, &m.fallbackID); err != nil {
			return err
		}
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range models {
		if strings.TrimSpace(m.channelID) == "" {
			continue
		}
		if err := ensureChannelModel(ctx, db, m.channelID, m.requestID, m.label, m.description, m.kind, "legacy"); err != nil {
			return err
		}
		if err := ensureBinding(ctx, db, m.id, m.channelID, "regular", 1, 100); err != nil {
			return err
		}
		if strings.TrimSpace(m.fallbackID) != "" && m.fallbackID != m.channelID {
			var fallbackExists int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM channels WHERE id=?`, m.fallbackID).Scan(&fallbackExists); err != nil {
				return err
			}
			if fallbackExists == 0 {
				continue
			}
			// Legacy rows only stored the fallback channel id. Advertise the
			// same request_id on that channel so the capability constraint is
			// true immediately after an upgrade.
			if err := ensureChannelModel(ctx, db, m.fallbackID, m.requestID, m.label, m.description, m.kind, "legacy"); err != nil {
				return err
			}
			var bound int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM model_channel_bindings WHERE model_id=? AND channel_id=?`, m.id, m.fallbackID).Scan(&bound); err != nil {
				return err
			}
			if bound > 0 {
				continue
			}
			var priority int
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(priority), 0) + 1 FROM model_channel_bindings WHERE model_id=?`, m.id).Scan(&priority); err != nil {
				return err
			}
			if err := ensureBinding(ctx, db, m.id, m.fallbackID, "regular", priority, 100); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrateFallbackModelChannels folds the former fallback role into the normal
// priority queue. Existing regular priorities stay intact; former fallback
// channels are placed after them.
func migrateFallbackModelChannels(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT model_id, channel_id, priority FROM model_channel_bindings WHERE role='fallback' ORDER BY model_id, priority, id`)
	if err != nil {
		return err
	}
	type binding struct {
		modelID, channelID string
		priority           int
	}
	legacy := []binding{}
	for rows.Next() {
		var item binding
		if err := rows.Scan(&item.modelID, &item.channelID, &item.priority); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	maxPriority := map[string]int{}
	for _, item := range legacy {
		if _, ok := maxPriority[item.modelID]; ok {
			continue
		}
		var priority int
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(priority), 0) FROM model_channel_bindings WHERE model_id=? AND role='regular'`, item.modelID).Scan(&priority); err != nil {
			return err
		}
		maxPriority[item.modelID] = priority
	}
	for _, item := range legacy {
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM model_channel_bindings WHERE model_id=? AND channel_id=? AND role='regular'`, item.modelID, item.channelID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			if _, err := db.ExecContext(ctx, `DELETE FROM model_channel_bindings WHERE model_id=? AND channel_id=? AND role='fallback'`, item.modelID, item.channelID); err != nil {
				return err
			}
			continue
		}
		priority := maxPriority[item.modelID] + max(item.priority, 1)
		if _, err := db.ExecContext(ctx, `UPDATE model_channel_bindings SET role='regular', priority=?, updated_at=? WHERE model_id=? AND channel_id=? AND role='fallback'`, priority, time.Now().Unix(), item.modelID, item.channelID); err != nil {
			return err
		}
	}
	_, err = db.ExecContext(ctx, `UPDATE models SET fallback_channel_id='' WHERE trim(fallback_channel_id)<>''`)
	return err
}

func migrateLegacyModelTTFT(ctx context.Context, db *sql.DB) error {
	var migrated string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='fallback_ttft_migrated' LIMIT 1`).Scan(&migrated); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='fallback_ttft_sec' LIMIT 1`).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_, _ = db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('fallback_ttft_migrated', 'true')`)
			return nil
		}
		return err
	}
	seconds, err := strconv.Atoi(strings.Trim(strings.TrimSpace(raw), `"`))
	if err != nil || seconds <= 0 {
		_, _ = db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('fallback_ttft_migrated', 'true')`)
		return nil
	}
	_, err = db.ExecContext(ctx, `UPDATE models SET fallback_ttft_sec=? WHERE fallback_ttft_sec=0`, seconds)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('fallback_ttft_migrated', 'true')`)
	return err
}

func migrateModelTTFTToGlobalSetting(ctx context.Context, db *sql.DB) error {
	var migrated string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='model_ttft_global_migrated' LIMIT 1`).Scan(&migrated); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='fallback_ttft_sec' LIMIT 1`).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var current int
	if json.Unmarshal([]byte(raw), &current) != nil {
		var value string
		if json.Unmarshal([]byte(raw), &value) == nil {
			current, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if current <= 0 {
		var legacy sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT MAX(fallback_ttft_sec) FROM models`).Scan(&legacy); err != nil {
			return err
		}
		if legacy.Valid && legacy.Int64 > 0 {
			if _, err := db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('fallback_ttft_sec', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(legacy.Int64, 10)); err != nil {
				return err
			}
		}
	}
	_, err := db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('model_ttft_global_migrated', 'true')`)
	return err
}

func upsertChannelModel(ctx context.Context, db execer, channelID, requestID, label, description, kind, source string) error {
	requestID = strings.TrimSpace(requestID)
	if channelID == "" || requestID == "" {
		return nil
	}
	if kind == "" {
		kind = "chat"
	}
	if source == "" {
		source = "manual"
	}
	_, err := db.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, description, kind, enabled, source, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT DO UPDATE SET label=excluded.label, description=excluded.description, kind=excluded.kind, enabled=1, source=excluded.source, updated_at=excluded.updated_at`,
		genID("cm"), channelID, requestID, strings.TrimSpace(label), strings.TrimSpace(description), kind, source, time.Now().Unix())
	return err
}

// ensureChannelModel is used by the legacy migration. It must never rewrite
// an administrator's source/label or re-enable a capability that was removed
// deliberately from a channel's model list.
func ensureChannelModel(ctx context.Context, db execer, channelID, requestID, label, description, kind, source string) error {
	requestID = strings.TrimSpace(requestID)
	if channelID == "" || requestID == "" {
		return nil
	}
	if kind == "" {
		kind = "chat"
	}
	if source == "" {
		source = "manual"
	}
	_, err := db.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, description, kind, enabled, source, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT DO NOTHING`,
		genID("cm"), channelID, requestID, strings.TrimSpace(label), strings.TrimSpace(description), kind, source, time.Now().Unix())
	return err
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func ensureBinding(ctx context.Context, db execer, modelID, channelID, role string, priority, weight int) error {
	role = normalizeBindingRole(role)
	if modelID == "" || channelID == "" {
		return nil
	}
	if priority < 1 {
		priority = 1
	}
	if weight < 1 {
		weight = 100
	}
	_, err := db.ExecContext(ctx, `INSERT INTO model_channel_bindings(id, model_id, channel_id, role, priority, weight, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model_id, channel_id, role) DO NOTHING`, genID("mcb"), modelID, channelID, role, priority, weight, time.Now().Unix())
	return err
}

// ListChannelModels returns the advertised request ids for a channel.
func ListChannelModels(ctx context.Context, db *sql.DB, channelID string, onlyEnabled bool) ([]ChannelModel, error) {
	q := `SELECT id, channel_id, request_id, label, description, kind, enabled, source, updated_at FROM channel_models WHERE channel_id=?`
	if onlyEnabled {
		q += ` AND enabled=1`
	}
	q += ` ORDER BY lower(request_id)`
	rows, err := db.QueryContext(ctx, q, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChannelModel{}
	for rows.Next() {
		var m ChannelModel
		var enabled int
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.RequestID, &m.Label, &m.Description, &m.Kind, &enabled, &m.Source, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Enabled = enabled == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplaceChannelModels updates a channel's advertised request ids in one
// transaction. Existing rows are disabled first so removed upstream models do
// not remain selectable in the model editor.
func ReplaceChannelModels(ctx context.Context, db *sql.DB, channelID string, models []ChannelModel) ([]ChannelModel, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE channel_models SET enabled=0, updated_at=? WHERE channel_id=?`, time.Now().Unix(), channelID); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, model := range models {
		requestID := strings.TrimSpace(model.RequestID)
		key := strings.ToLower(requestID)
		if requestID == "" || seen[key] {
			continue
		}
		seen[key] = true
		if model.Kind == "" {
			model.Kind = "chat"
		}
		if model.Source == "" {
			model.Source = "manual"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO channel_models(id, channel_id, request_id, label, description, kind, enabled, source, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT DO UPDATE SET label=excluded.label, description=excluded.description, kind=excluded.kind, enabled=1, source=excluded.source, updated_at=excluded.updated_at`,
			model.IDOrNew(), channelID, requestID, strings.TrimSpace(model.Label), strings.TrimSpace(model.Description), model.Kind, model.Source, time.Now().Unix()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ListChannelModels(ctx, db, channelID, false)
}

func (m ChannelModel) IDOrNew() string {
	if strings.TrimSpace(m.ID) != "" {
		return m.ID
	}
	return genID("cm")
}

// ListModelChannelBindings includes channel metadata for the admin editor.
func ListModelChannelBindings(ctx context.Context, db *sql.DB, modelID, role string) ([]ModelChannelBinding, error) {
	q := `SELECT b.id, b.model_id, b.channel_id, b.role, b.priority, b.weight,
		c.name, c.type, c.enabled,
		c.auto_disable_errors, c.auto_disable_timeouts, c.auto_disable_minutes,
		c.auto_disabled_until, c.consecutive_errors, c.consecutive_timeouts,
		b.disabled_until, b.consecutive_errors, b.consecutive_timeouts, b.updated_at
		FROM model_channel_bindings b JOIN channels c ON c.id=b.channel_id WHERE b.model_id=?`
	args := []any{modelID}
	if strings.TrimSpace(role) != "" {
		q += ` AND b.role=?`
		args = append(args, normalizeBindingRole(role))
	}
	q += ` ORDER BY b.role, b.priority, b.id`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelChannelBinding{}
	for rows.Next() {
		var b ModelChannelBinding
		var enabled int
		if err := rows.Scan(&b.ID, &b.ModelID, &b.ChannelID, &b.Role, &b.Priority, &b.Weight, &b.ChannelName, &b.ChannelType, &enabled,
			&b.ChannelAutoDisableErrors, &b.ChannelAutoDisableTimeouts, &b.ChannelAutoDisableMinutes,
			&b.ChannelAutoDisabledUntil, &b.ChannelConsecutiveErrors, &b.ChannelConsecutiveTimeouts,
			&b.DisabledUntil, &b.ConsecutiveErrors, &b.ConsecutiveTimeouts, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.ChannelEnabled = enabled == 1
		out = append(out, b)
	}
	return out, rows.Err()
}

func ChannelSupportsRequestID(ctx context.Context, db *sql.DB, channelID, requestID string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM channel_models WHERE channel_id=? AND enabled=1 AND lower(trim(request_id))=lower(trim(?))`, channelID, requestID).Scan(&n)
	return n > 0, err
}

// ReplaceModelChannelBindings validates capabilities before replacing the
// model's single priority-ordered channel queue. The fallback slice remains an
// API compatibility input and is appended to regular bindings.
func ReplaceModelChannelBindings(ctx context.Context, db *sql.DB, model *Model, regular, fallback []ModelChannelBinding) ([]ModelChannelBinding, error) {
	if model == nil || strings.TrimSpace(model.ID) == "" {
		return nil, ErrInvalidModelChannelBinding
	}
	all := make([]ModelChannelBinding, 0, len(regular)+len(fallback))
	seenChannels := map[string]struct{}{}
	maxRegularPriority := 0
	for _, b := range regular {
		if b.Priority > maxRegularPriority {
			maxRegularPriority = b.Priority
		}
	}
	appendBinding := func(b ModelChannelBinding, priorityOffset int) error {
		b.Role = "regular"
		b.ModelID = model.ID
		if b.ChannelID == "" {
			return ErrInvalidModelChannelBinding
		}
		if _, exists := seenChannels[b.ChannelID]; exists {
			return fmt.Errorf("%w: duplicate channel %s", ErrInvalidModelChannelBinding, b.ChannelID)
		}
		seenChannels[b.ChannelID] = struct{}{}
		if b.Priority < 1 {
			b.Priority = 1
		}
		b.Priority += priorityOffset
		if b.Weight < 1 {
			b.Weight = 100
		}
		ok, err := ChannelSupportsRequestID(ctx, db, b.ChannelID, model.RequestID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnsupportedChannelModel, b.ChannelID)
		}
		all = append(all, b)
		return nil
	}
	for _, b := range regular {
		if err := appendBinding(b, 0); err != nil {
			return nil, err
		}
	}
	// Older admin clients still submit a separate fallback list. Preserve its
	// former semantics by placing it after every regular priority in this queue.
	for _, b := range fallback {
		if err := appendBinding(b, maxRegularPriority); err != nil {
			return nil, err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM model_channel_bindings WHERE model_id=?`, model.ID); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	for _, b := range all {
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_channel_bindings(id, model_id, channel_id, role, priority, weight, updated_at) VALUES(?, ?, ?, 'regular', ?, ?, ?)`, genID("mcb"), model.ID, b.ChannelID, b.Priority, b.Weight, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ListModelChannelBindings(ctx, db, model.ID, "")
}

// RecordModelChannelResult updates the per-binding consecutive counters. It
// is intentionally scoped to (model, channel, role), never the whole channel.
func RecordModelChannelResult(ctx context.Context, db *sql.DB, modelID, channelID, role string, kind string, threshold, disableMinutes int) error {
	role = normalizeBindingRole(role)
	if modelID == "" || channelID == "" {
		return nil
	}
	if kind != "timeout" {
		kind = "error"
	}
	// A zero threshold explicitly disables this trigger. Clear its previous
	// consecutive count so re-enabling the policy cannot inherit stale failures.
	if threshold <= 0 {
		column := "consecutive_errors"
		if kind == "timeout" {
			column = "consecutive_timeouts"
		}
		_, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE model_channel_bindings SET %s=0, updated_at=? WHERE model_id=? AND channel_id=? AND role=?`, column), time.Now().Unix(), modelID, channelID, role)
		return err
	}
	if disableMinutes < 0 {
		disableMinutes = 0
	}
	now := time.Now().Unix()
	if kind == "timeout" {
		_, err := db.ExecContext(ctx, `UPDATE model_channel_bindings SET consecutive_timeouts=CASE WHEN ? > 0 AND consecutive_timeouts+1 >= ? THEN 0 ELSE consecutive_timeouts+1 END, consecutive_errors=0,
			disabled_until=CASE WHEN ? > 0 AND consecutive_timeouts+1 >= ? AND disabled_until<=? THEN ? + (? * 60) ELSE disabled_until END,
			updated_at=? WHERE model_id=? AND channel_id=? AND role=?`, threshold, threshold, threshold, threshold, now, now, disableMinutes, now, modelID, channelID, role)
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE model_channel_bindings SET consecutive_errors=CASE WHEN ? > 0 AND consecutive_errors+1 >= ? THEN 0 ELSE consecutive_errors+1 END, consecutive_timeouts=0,
		disabled_until=CASE WHEN ? > 0 AND consecutive_errors+1 >= ? AND disabled_until<=? THEN ? + (? * 60) ELSE disabled_until END,
		updated_at=? WHERE model_id=? AND channel_id=? AND role=?`, threshold, threshold, threshold, threshold, now, now, disableMinutes, now, modelID, channelID, role)
	return err
}

// ResetModelChannelCounters clears consecutive results after a healthy request
// without shortening an automatic quarantine already in progress.
func ResetModelChannelCounters(ctx context.Context, db *sql.DB, modelID, channelID, role string) error {
	_, err := db.ExecContext(ctx, `UPDATE model_channel_bindings SET consecutive_errors=0, consecutive_timeouts=0, updated_at=? WHERE model_id=? AND channel_id=? AND role=?`, time.Now().Unix(), modelID, channelID, normalizeBindingRole(role))
	return err
}

// ResetModelChannelResult is the administrator's manual recovery operation.
func ResetModelChannelResult(ctx context.Context, db *sql.DB, modelID, channelID, role string) error {
	_, err := db.ExecContext(ctx, `UPDATE model_channel_bindings SET consecutive_errors=0, consecutive_timeouts=0, disabled_until=0, updated_at=? WHERE model_id=? AND channel_id=? AND role=?`, time.Now().Unix(), modelID, channelID, normalizeBindingRole(role))
	return err
}

// RecordChannelResult updates the channel-wide counters. Failures from all
// logical models bound to this channel share this quarantine state.
func RecordChannelResult(ctx context.Context, db *sql.DB, channelID, kind string, threshold, disableMinutes int) error {
	if strings.TrimSpace(channelID) == "" {
		return nil
	}
	if kind != "timeout" {
		kind = "error"
	}
	if threshold <= 0 {
		column := "consecutive_errors"
		if kind == "timeout" {
			column = "consecutive_timeouts"
		}
		_, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE channels SET %s=0, updated_at=? WHERE id=?`, column), time.Now().Unix(), channelID)
		return err
	}
	if disableMinutes < 0 {
		disableMinutes = 0
	}
	now := time.Now().Unix()
	if kind == "timeout" {
		_, err := db.ExecContext(ctx, `UPDATE channels SET consecutive_timeouts=CASE WHEN consecutive_timeouts+1>=? THEN 0 ELSE consecutive_timeouts+1 END,
			consecutive_errors=0, auto_disabled_until=CASE WHEN consecutive_timeouts+1>=? AND auto_disabled_until<=? THEN ?+(?*60) ELSE auto_disabled_until END,
			updated_at=? WHERE id=?`, threshold, threshold, now, now, disableMinutes, now, channelID)
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE channels SET consecutive_errors=CASE WHEN consecutive_errors+1>=? THEN 0 ELSE consecutive_errors+1 END,
		consecutive_timeouts=0, auto_disabled_until=CASE WHEN consecutive_errors+1>=? AND auto_disabled_until<=? THEN ?+(?*60) ELSE auto_disabled_until END,
		updated_at=? WHERE id=?`, threshold, threshold, now, now, disableMinutes, now, channelID)
	return err
}

// ResetChannelResult is the administrator's manual recovery operation. It
// intentionally leaves model binding quarantines untouched.
func ResetChannelResult(ctx context.Context, db *sql.DB, channelID string) error {
	_, err := db.ExecContext(ctx, `UPDATE channels SET consecutive_errors=0, consecutive_timeouts=0, auto_disabled_until=0, updated_at=? WHERE id=?`, time.Now().Unix(), channelID)
	return err
}

// ResetChannelCounters records a healthy attempt without overriding an active
// administrator-visible quarantine. The automatic quarantine expires by time
// or through ResetChannelResult (manual recovery).
func ResetChannelCounters(ctx context.Context, db *sql.DB, channelID string) error {
	_, err := db.ExecContext(ctx, `UPDATE channels SET consecutive_errors=0, consecutive_timeouts=0, updated_at=? WHERE id=?`, time.Now().Unix(), channelID)
	return err
}

// RecordChannelFailure records one completed upstream attempt using the policy
// already stored on the row. The update is atomic and avoids a policy SELECT
// on the request completion path.
func RecordChannelFailure(ctx context.Context, db *sql.DB, channelID, kind string) error {
	now := time.Now().Unix()
	if kind == "timeout" {
		_, err := db.ExecContext(ctx, `UPDATE channels SET
			consecutive_timeouts=CASE WHEN auto_disable_timeouts<=0 THEN 0 WHEN consecutive_timeouts+1>=auto_disable_timeouts THEN 0 ELSE consecutive_timeouts+1 END,
			consecutive_errors=0,
			auto_disabled_until=CASE WHEN auto_disable_timeouts>0 AND consecutive_timeouts+1>=auto_disable_timeouts AND auto_disabled_until<=? THEN ?+(auto_disable_minutes*60) ELSE auto_disabled_until END,
			updated_at=? WHERE id=?`, now, now, now, channelID)
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE channels SET
		consecutive_errors=CASE WHEN auto_disable_errors<=0 THEN 0 WHEN consecutive_errors+1>=auto_disable_errors THEN 0 ELSE consecutive_errors+1 END,
		consecutive_timeouts=0,
		auto_disabled_until=CASE WHEN auto_disable_errors>0 AND consecutive_errors+1>=auto_disable_errors AND auto_disabled_until<=? THEN ?+(auto_disable_minutes*60) ELSE auto_disabled_until END,
		updated_at=? WHERE id=?`, now, now, now, channelID)
	return err
}

// ModelChannelCandidateIDs returns the currently usable channel bindings in
// failover order. The first channel in each priority tier is selected by weight;
// remaining channels in that tier are kept next so a failed request can advance
// without skipping providers.
func ModelChannelCandidateIDs(ctx context.Context, db *sql.DB, modelID, requestID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT b.channel_id, b.priority, b.weight
		FROM model_channel_bindings b JOIN channels c ON c.id=b.channel_id
		JOIN channels anchor ON anchor.id=(SELECT channel_id FROM models WHERE id=b.model_id)
		JOIN channel_models cm ON cm.channel_id=b.channel_id AND lower(trim(cm.request_id))=lower(trim(?)) AND cm.enabled=1
		WHERE b.model_id=? AND b.role IN ('regular','fallback') AND (
			lower(trim(c.type))=lower(trim(anchor.type))
			OR (lower(trim(c.type)) IN ('anthropic','claude') AND lower(trim(anchor.type)) IN ('anthropic','claude'))
			OR (lower(trim(c.type)) IN ('google','gemini') AND lower(trim(anchor.type)) IN ('google','gemini'))
		)
			AND lower(trim(COALESCE(c.api_format,'')))=lower(trim(COALESCE(anchor.api_format,'')))
			AND c.enabled=1 AND trim(c.api_key)<>''
			AND (b.disabled_until=0 OR b.disabled_until<=?)
			AND (c.auto_disabled_until=0 OR c.auto_disabled_until<=?)
		ORDER BY b.priority ASC, b.channel_id`, requestID, modelID, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		id               string
		priority, weight int
	}
	all := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.priority, &item.weight); err != nil {
			return nil, err
		}
		if item.priority < 1 {
			item.priority = 1
		}
		if item.weight < 1 {
			item.weight = 1
		}
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, sql.ErrNoRows
	}
	ordered := make([]string, 0, len(all))
	for start := 0; start < len(all); {
		end := start + 1
		for end < len(all) && all[end].priority == all[start].priority {
			end++
		}
		pool := all[start:end]
		total := 0
		for _, item := range pool {
			total += item.weight
		}
		bucket := rand.IntN(total)
		selected := 0
		for i, item := range pool {
			if bucket < item.weight {
				selected = i
				break
			}
			bucket -= item.weight
		}
		for offset := range pool {
			ordered = append(ordered, pool[(selected+offset)%len(pool)].id)
		}
		start = end
	}
	return ordered, nil
}

// SelectModelChannelID resolves the first usable binding within a role. New
// request paths use ModelChannelCandidateIDs so all priority tiers are retained
// for retry; role stays here for older internal callers.
func SelectModelChannelID(ctx context.Context, db *sql.DB, modelID, requestID, role, excludeID string) (string, error) {
	if strings.TrimSpace(role) == "" {
		candidates, err := ModelChannelCandidateIDs(ctx, db, modelID, requestID)
		if err != nil {
			return "", err
		}
		for _, id := range candidates {
			if id != excludeID {
				return id, nil
			}
		}
		return "", sql.ErrNoRows
	}
	roleFilter := `b.role=?`
	rows, err := db.QueryContext(ctx, `SELECT b.channel_id, b.priority, b.weight
		FROM model_channel_bindings b JOIN channels c ON c.id=b.channel_id
		JOIN channel_models cm ON cm.channel_id=b.channel_id AND lower(trim(cm.request_id))=lower(trim(?)) AND cm.enabled=1
		WHERE b.model_id=? AND `+roleFilter+` AND c.enabled=1 AND trim(c.api_key)<>''
			AND (b.disabled_until=0 OR b.disabled_until<=?)
		AND (c.auto_disabled_until=0 OR c.auto_disabled_until<=?)
		ORDER BY b.priority ASC, b.weight DESC, b.id`, requestID, modelID, normalizeBindingRole(role), time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type candidate struct {
		id               string
		priority, weight int
	}
	all := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.priority, &c.weight); err != nil {
			return "", err
		}
		if c.id == excludeID {
			continue
		}
		if c.priority < 1 {
			c.priority = 1
		}
		if c.weight < 1 {
			c.weight = 1
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "", sql.ErrNoRows
	}
	bestPriority := all[0].priority
	pool := all[:0]
	for _, c := range all {
		if c.priority == bestPriority {
			pool = append(pool, c)
		}
	}
	if role == "fallback" || len(pool) == 1 {
		return pool[0].id, nil
	}
	total := 0
	for _, c := range pool {
		total += c.weight
	}
	if total <= 0 {
		return pool[0].id, nil
	}
	// A process-local counter keeps this selection allocation-free and avoids a
	// global PRNG lock on every request. Unlike a timestamp bucket it also keeps
	// back-to-back requests from repeatedly selecting the same channel when the
	// scheduler hands them the same clock tick.
	bucket := int(modelChannelSelectionCounter.Add(1) % uint64(total))
	for _, c := range pool {
		if bucket < c.weight {
			return c.id, nil
		}
		bucket -= c.weight
	}
	return pool[len(pool)-1].id, nil
}
