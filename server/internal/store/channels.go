package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ListChannels returns every channel. Admin endpoint shape.
func ListChannels(ctx context.Context, db *sql.DB) ([]Channel, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, type, api_format, base_url, api_key, enabled, sort_order, updated_at FROM channels ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		var en int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.APIFormat, &c.BaseURL, &c.APIKey, &en, &c.SortOrder, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Enabled = en == 1
		c.HasAPIKey = c.APIKey != ""
		c.APIKey = "" // never leak
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel returns one channel by id including the api_key (used by provider
// layer, never by handlers).
func GetChannel(ctx context.Context, db *sql.DB, id string) (*Channel, error) {
	var c Channel
	var en int
	err := db.QueryRowContext(ctx,
		`SELECT id, name, type, api_format, base_url, api_key, enabled, sort_order, updated_at FROM channels WHERE id=?`, id,
	).Scan(&c.ID, &c.Name, &c.Type, &c.APIFormat, &c.BaseURL, &c.APIKey, &en, &c.SortOrder, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = en == 1
	c.HasAPIKey = c.APIKey != ""
	return &c, nil
}

// GetChannelByName returns a channel by case-insensitive, trimmed name.
func GetChannelByName(ctx context.Context, db *sql.DB, name string) (*Channel, error) {
	var c Channel
	var en int
	err := db.QueryRowContext(ctx,
		`SELECT id, name, type, api_format, base_url, api_key, enabled, sort_order, updated_at FROM channels WHERE lower(trim(name))=lower(trim(?)) LIMIT 1`,
		name,
	).Scan(&c.ID, &c.Name, &c.Type, &c.APIFormat, &c.BaseURL, &c.APIKey, &en, &c.SortOrder, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = en == 1
	c.HasAPIKey = c.APIKey != ""
	return &c, nil
}

// CreateChannel inserts a row and returns it (with api_key stripped).
func CreateChannel(ctx context.Context, db *sql.DB, name, typ, apiFormat, baseURL, apiKey string) (*Channel, error) {
	id := genID("ch")
	name = strings.TrimSpace(name)
	baseURL = strings.TrimSpace(baseURL)
	_, err := db.ExecContext(ctx,
		`INSERT INTO channels(id, name, type, api_format, base_url, api_key, enabled, sort_order, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		id, name, typ, apiFormat, baseURL, apiKey, time.Now().Unix())
	if err != nil {
		if isUniqueIndexErr(err, "idx_channels_name_unique", "channels.name") {
			return nil, ErrChannelNameExists
		}
		return nil, err
	}
	c, err := GetChannel(ctx, db, id)
	if err != nil {
		return nil, err
	}
	c.APIKey = ""
	return c, nil
}

// ReorderChannels assigns sort_order = position for each id in one
// transaction. Ids not present are ignored, matching ReorderModels.
func ReorderChannels(ctx context.Context, db *sql.DB, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET sort_order=?, updated_at=? WHERE id=?`, i, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateChannel writes selective fields. An empty api_key argument leaves the
// stored key unchanged (so admins can edit other fields without re-entering
// the secret).
type ChannelPatch struct {
	Name      *string `json:"name"`
	Type      *string `json:"type"`
	APIFormat *string `json:"api_format"`
	BaseURL   *string `json:"base_url"`
	APIKey    *string `json:"api_key"`
	Enabled   *bool   `json:"enabled"`
	SortOrder *int    `json:"sort_order"`
}

func UpdateChannel(ctx context.Context, db *sql.DB, id string, patch ChannelPatch) (*Channel, error) {
	parts := []string{}
	args := []any{}
	if patch.Name != nil {
		parts = append(parts, "name=?")
		args = append(args, strings.TrimSpace(*patch.Name))
	}
	if patch.Type != nil {
		parts = append(parts, "type=?")
		args = append(args, *patch.Type)
	}
	if patch.APIFormat != nil {
		parts = append(parts, "api_format=?")
		args = append(args, *patch.APIFormat)
	}
	if patch.BaseURL != nil {
		parts = append(parts, "base_url=?")
		args = append(args, strings.TrimSpace(*patch.BaseURL))
	}
	if patch.APIKey != nil && *patch.APIKey != "" {
		parts = append(parts, "api_key=?")
		args = append(args, *patch.APIKey)
	}
	if patch.Enabled != nil {
		parts = append(parts, "enabled=?")
		if *patch.Enabled {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if patch.SortOrder != nil {
		parts = append(parts, "sort_order=?")
		args = append(args, *patch.SortOrder)
	}
	if len(parts) == 0 {
		return GetChannel(ctx, db, id)
	}
	parts = append(parts, "updated_at=?")
	args = append(args, time.Now().Unix())
	args = append(args, id)
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("UPDATE channels SET %s WHERE id=?", strings.Join(parts, ", ")),
		args...); err != nil {
		if isUniqueIndexErr(err, "idx_channels_name_unique", "channels.name") {
			return nil, ErrChannelNameExists
		}
		return nil, err
	}
	c, err := GetChannel(ctx, db, id)
	if err != nil {
		return nil, err
	}
	c.APIKey = ""
	return c, nil
}

// DeleteChannel cascades to its models via FK ON DELETE CASCADE.
func DeleteChannel(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM channels WHERE id=?", id)
	return err
}

// ListModels returns every model with optional kind filter (empty = all).
// onlyEnabled restricts to enabled rows.
func ListModels(ctx context.Context, db *sql.DB, kind string, onlyEnabled bool) ([]Model, error) {
	q := `SELECT id, channel_id, kind, request_id, label, description, icon, fallback_channel_id, enabled, sort_order, tool_mode, vision, stream, research_enabled, fast, system_prompt, param_controls, extra_params, official_tools, tags, moderation_enabled, moderation_mode, price_input, price_output, price_cache_read, price_cache_write, price_per_image, currency, dim, image_timeout_sec, updated_at FROM models WHERE 1=1`
	args := []any{}
	if kind != "" {
		q += " AND kind=?"
		args = append(args, kind)
	}
	if onlyEnabled {
		q += " AND enabled=1"
	}
	q += " ORDER BY sort_order, label"
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetModel returns one row.
func GetModel(ctx context.Context, db *sql.DB, id string) (*Model, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, channel_id, kind, request_id, label, description, icon, fallback_channel_id, enabled, sort_order, tool_mode, vision, stream, research_enabled, fast, system_prompt, param_controls, extra_params, official_tools, tags, moderation_enabled, moderation_mode, price_input, price_output, price_cache_read, price_cache_write, price_per_image, currency, dim, image_timeout_sec, updated_at FROM models WHERE id=?`, id)
	m, err := scanModel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func scanModel(s scanner) (Model, error) {
	var m Model
	var en, vi, st, researchEn, fastI, modEn int
	var paramControls, extraParams, officialTools, tags string
	if err := s.Scan(&m.ID, &m.ChannelID, &m.Kind, &m.RequestID, &m.Label, &m.Description, &m.Icon, &m.FallbackChannelID, &en, &m.SortOrder,
		&m.ToolMode, &vi, &st, &researchEn, &fastI, &m.SystemPrompt, &paramControls, &extraParams, &officialTools, &tags, &modEn, &m.ModerationMode,
		&m.PriceInput, &m.PriceOutput, &m.PriceCacheRead, &m.PriceCacheWrite, &m.PricePerImage, &m.Currency, &m.Dim, &m.ImageTimeoutSec, &m.UpdatedAt); err != nil {
		return m, err
	}
	m.Enabled = en == 1
	m.Vision = vi == 1
	m.Stream = st == 1
	m.ResearchEnabled = researchEn == 1
	m.Fast = fastI == 1
	m.ModerationEnabled = modEn == 1
	m.ParamControls = json.RawMessage(paramControls)
	m.ExtraParams = json.RawMessage(orDefault(extraParams, "{}"))
	m.OfficialTools = json.RawMessage(orDefault(officialTools, "[]"))
	m.Tags = json.RawMessage(orDefault(tags, "[]"))
	if m.ModerationMode == "" {
		m.ModerationMode = "keyword"
	}
	return m, nil
}

type scanner interface {
	Scan(dst ...any) error
}

// CreateModel inserts a row.
func CreateModel(ctx context.Context, db *sql.DB, m Model) (*Model, error) {
	m.RequestID = strings.TrimSpace(m.RequestID)
	m.Label = strings.TrimSpace(m.Label)
	m.Description = strings.TrimSpace(m.Description)
	m.Icon = strings.TrimSpace(m.Icon)
	m.FallbackChannelID = strings.TrimSpace(m.FallbackChannelID)
	m.SystemPrompt = strings.TrimSpace(m.SystemPrompt)
	if m.ID == "" {
		m.ID = genID("m")
	}
	if m.Currency == "" {
		m.Currency = "USD"
	}
	if m.Kind == "" {
		m.Kind = "chat"
	}
	if m.Kind == "chat" && !m.ResearchEnabledSet {
		m.ResearchEnabled = true
	}
	if m.ToolMode == "" {
		m.ToolMode = "native"
	}
	if len(m.ParamControls) == 0 {
		m.ParamControls = json.RawMessage("[]")
	}
	if extraParams, err := NormalizeModelExtraParams(m.ExtraParams); err != nil {
		return nil, err
	} else {
		m.ExtraParams = extraParams
	}
	if len(m.OfficialTools) == 0 {
		m.OfficialTools = json.RawMessage("[]")
	}
	if len(m.Tags) == 0 {
		m.Tags = json.RawMessage("[]")
	}
	if m.ModerationMode == "" {
		m.ModerationMode = "keyword"
	}
	if m.ImageTimeoutSec < 0 {
		m.ImageTimeoutSec = 0 // 0 = no cap; never store a negative
	}
	_, err := db.ExecContext(ctx, `INSERT INTO models(
		id, channel_id, kind, request_id, label, description, icon, fallback_channel_id, enabled, sort_order,
		tool_mode, vision, stream, research_enabled, system_prompt, param_controls, extra_params, official_tools, tags, moderation_enabled, moderation_mode,
		price_input, price_output, price_cache_read, price_cache_write, price_per_image, currency,
		dim, image_timeout_sec, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChannelID, m.Kind, m.RequestID, m.Label, m.Description, m.Icon, m.FallbackChannelID, boolInt(m.Enabled), m.SortOrder,
		m.ToolMode, boolInt(m.Vision), boolInt(m.Stream), boolInt(m.ResearchEnabled), m.SystemPrompt, string(m.ParamControls), string(m.ExtraParams), string(m.OfficialTools), string(m.Tags), boolInt(m.ModerationEnabled), m.ModerationMode,
		m.PriceInput, m.PriceOutput, m.PriceCacheRead, m.PriceCacheWrite, m.PricePerImage, m.Currency,
		m.Dim, m.ImageTimeoutSec, time.Now().Unix())
	if err != nil {
		if isUniqueIndexErr(err, "idx_models_channel_request_unique", "models.channel_id") {
			return nil, ErrModelRequestExists
		}
		return nil, err
	}
	return GetModel(ctx, db, m.ID)
}

func GetModelByChannelRequestID(ctx context.Context, db *sql.DB, channelID, requestID string) (*Model, error) {
	row := db.QueryRowContext(ctx,
		`SELECT id, channel_id, kind, request_id, label, description, icon, fallback_channel_id, enabled, sort_order, tool_mode, vision, stream, research_enabled, fast, system_prompt, param_controls, extra_params, official_tools, tags, moderation_enabled, moderation_mode, price_input, price_output, price_cache_read, price_cache_write, price_per_image, currency, dim, image_timeout_sec, updated_at
		 FROM models WHERE channel_id=? AND lower(trim(request_id))=lower(trim(?)) LIMIT 1`,
		channelID, requestID)
	m, err := scanModel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateModel writes selective fields.
func UpdateModel(ctx context.Context, db *sql.DB, id string, m Model) (*Model, error) {
	m.RequestID = strings.TrimSpace(m.RequestID)
	m.Label = strings.TrimSpace(m.Label)
	m.Description = strings.TrimSpace(m.Description)
	m.Icon = strings.TrimSpace(m.Icon)
	m.FallbackChannelID = strings.TrimSpace(m.FallbackChannelID)
	m.SystemPrompt = strings.TrimSpace(m.SystemPrompt)
	if extraParams, err := NormalizeModelExtraParams(m.ExtraParams); err != nil {
		return nil, err
	} else {
		m.ExtraParams = extraParams
	}
	if len(m.OfficialTools) == 0 {
		m.OfficialTools = json.RawMessage("[]")
	}
	if len(m.Tags) == 0 {
		m.Tags = json.RawMessage("[]")
	}
	if m.ModerationMode == "" {
		m.ModerationMode = "keyword"
	}
	if m.ImageTimeoutSec < 0 {
		m.ImageTimeoutSec = 0 // 0 = no cap; never store a negative
	}
	_, err := db.ExecContext(ctx, `UPDATE models SET
		channel_id=?, label=?, description=?, icon=?, fallback_channel_id=?, request_id=?, kind=?, enabled=?, sort_order=?,
		tool_mode=?, vision=?, stream=?, research_enabled=?, system_prompt=?, param_controls=?, extra_params=?, official_tools=?, tags=?, moderation_enabled=?, moderation_mode=?,
		price_input=?, price_output=?, price_cache_read=?, price_cache_write=?, price_per_image=?, currency=?,
		dim=?, image_timeout_sec=?, updated_at=?
		WHERE id=?`,
		m.ChannelID, m.Label, m.Description, m.Icon, m.FallbackChannelID, m.RequestID, m.Kind, boolInt(m.Enabled), m.SortOrder,
		m.ToolMode, boolInt(m.Vision), boolInt(m.Stream), boolInt(m.ResearchEnabled), m.SystemPrompt, string(m.ParamControls), string(m.ExtraParams), string(m.OfficialTools), string(m.Tags), boolInt(m.ModerationEnabled), m.ModerationMode,
		m.PriceInput, m.PriceOutput, m.PriceCacheRead, m.PriceCacheWrite, m.PricePerImage, m.Currency,
		m.Dim, m.ImageTimeoutSec, time.Now().Unix(), id)
	if err != nil {
		if isUniqueIndexErr(err, "idx_models_channel_request_unique", "models.channel_id") {
			return nil, ErrModelRequestExists
		}
		return nil, err
	}
	return GetModel(ctx, db, id)
}

// DeleteModel removes the row. FKs cascade to model_skills.
func DeleteModel(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM models WHERE id=?", id)
	return err
}

// SetFastModel marks exactly one model as THE fast model (§fast-mode), clearing
// the flag on every other model in the same transaction. Passing id=="" clears
// the fast model entirely. Marking a model fast also forces Deep Research off on
// it — a fast turn never runs Deep Research, and the admin can't re-enable it
// while the model is the fast one. Enforcing "at least one advanced model
// remains" is the caller's job (it needs a user-facing error).
func SetFastModel(ctx context.Context, db *sql.DB, id string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE models SET fast=0, updated_at=? WHERE fast=1`, now); err != nil {
		return err
	}
	if id != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE models SET fast=1, research_enabled=0, updated_at=? WHERE id=?`, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetFastModel returns the single enabled chat model marked fast (§fast-mode), or
// (nil, nil) when none is configured. This is the model a fast turn actually runs
// on — resolved server-side and never named to the user.
func GetFastModel(ctx context.Context, db *sql.DB) (*Model, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM models WHERE fast=1 AND enabled=1 AND kind='chat' LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return GetModel(ctx, db, id)
}

// CountAdvancedChatModels returns the number of enabled chat models that are NOT
// the fast model — the pool the "进阶/advanced" picker shows. The admin guard uses
// it to refuse marking the last advanced model as fast (which would empty the
// advanced picker).
func CountAdvancedChatModels(ctx context.Context, db *sql.DB, excludeID string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM models WHERE enabled=1 AND kind='chat' AND fast=0 AND id<>?`, excludeID).Scan(&n)
	return n, err
}

// ReorderModels assigns sort_order = position for each id in the given order, in
// one transaction. The admin list orders by sort_order, so this is what makes a
// drag / move-up-down persist. Ids not present are ignored.
func ReorderModels(ctx context.Context, db *sql.DB, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE models SET sort_order=?, updated_at=? WHERE id=?`, i, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SkillsForModel returns the skill ids associated with the model.
func SkillsForModel(ctx context.Context, db *sql.DB, modelID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT skill_id FROM model_skills WHERE model_id=?`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetSkillsForModel replaces the join rows for a model.
func SetSkillsForModel(ctx context.Context, db *sql.DB, modelID string, skillIDs []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM model_skills WHERE model_id=?", modelID); err != nil {
		return err
	}
	for _, sid := range skillIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO model_skills(model_id, skill_id) VALUES(?, ?)", modelID, sid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
