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

// CreateChannel inserts a row and returns it (with api_key stripped).
func CreateChannel(ctx context.Context, db *sql.DB, name, typ, apiFormat, baseURL, apiKey string) (*Channel, error) {
	id := genID("ch")
	_, err := db.ExecContext(ctx,
		`INSERT INTO channels(id, name, type, api_format, base_url, api_key, enabled, sort_order, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		id, name, typ, apiFormat, baseURL, apiKey, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	c, err := GetChannel(ctx, db, id)
	if err != nil {
		return nil, err
	}
	c.APIKey = ""
	return c, nil
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
		args = append(args, *patch.Name)
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
		args = append(args, *patch.BaseURL)
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
	q := `SELECT id, channel_id, kind, request_id, label, description, icon, enabled, sort_order, tool_mode, vision, stream, system_prompt, param_controls, price_input, price_output, price_cache_read, price_cache_write, price_per_image, currency, dim, updated_at FROM models WHERE 1=1`
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
		`SELECT id, channel_id, kind, request_id, label, description, icon, enabled, sort_order, tool_mode, vision, stream, system_prompt, param_controls, price_input, price_output, price_cache_read, price_cache_write, price_per_image, currency, dim, updated_at FROM models WHERE id=?`, id)
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
	var en, vi, st int
	var paramControls string
	if err := s.Scan(&m.ID, &m.ChannelID, &m.Kind, &m.RequestID, &m.Label, &m.Description, &m.Icon, &en, &m.SortOrder,
		&m.ToolMode, &vi, &st, &m.SystemPrompt, &paramControls,
		&m.PriceInput, &m.PriceOutput, &m.PriceCacheRead, &m.PriceCacheWrite, &m.PricePerImage, &m.Currency, &m.Dim, &m.UpdatedAt); err != nil {
		return m, err
	}
	m.Enabled = en == 1
	m.Vision = vi == 1
	m.Stream = st == 1
	m.ParamControls = json.RawMessage(paramControls)
	return m, nil
}

type scanner interface {
	Scan(dst ...any) error
}

// CreateModel inserts a row.
func CreateModel(ctx context.Context, db *sql.DB, m Model) (*Model, error) {
	if m.ID == "" {
		m.ID = genID("m")
	}
	if m.Currency == "" {
		m.Currency = "USD"
	}
	if m.Kind == "" {
		m.Kind = "chat"
	}
	if m.ToolMode == "" {
		m.ToolMode = "native"
	}
	if len(m.ParamControls) == 0 {
		m.ParamControls = json.RawMessage("[]")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO models(
		id, channel_id, kind, request_id, label, description, icon, enabled, sort_order,
		tool_mode, vision, stream, system_prompt, param_controls,
		price_input, price_output, price_cache_read, price_cache_write, price_per_image, currency,
		dim, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChannelID, m.Kind, m.RequestID, m.Label, m.Description, m.Icon, boolInt(m.Enabled), m.SortOrder,
		m.ToolMode, boolInt(m.Vision), boolInt(m.Stream), m.SystemPrompt, string(m.ParamControls),
		m.PriceInput, m.PriceOutput, m.PriceCacheRead, m.PriceCacheWrite, m.PricePerImage, m.Currency,
		m.Dim, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return GetModel(ctx, db, m.ID)
}

// UpdateModel writes selective fields.
func UpdateModel(ctx context.Context, db *sql.DB, id string, m Model) (*Model, error) {
	_, err := db.ExecContext(ctx, `UPDATE models SET
		label=?, description=?, icon=?, request_id=?, kind=?, enabled=?, sort_order=?,
		tool_mode=?, vision=?, stream=?, system_prompt=?, param_controls=?,
		price_input=?, price_output=?, price_cache_read=?, price_cache_write=?, price_per_image=?, currency=?,
		dim=?, updated_at=?
		WHERE id=?`,
		m.Label, m.Description, m.Icon, m.RequestID, m.Kind, boolInt(m.Enabled), m.SortOrder,
		m.ToolMode, boolInt(m.Vision), boolInt(m.Stream), m.SystemPrompt, string(m.ParamControls),
		m.PriceInput, m.PriceOutput, m.PriceCacheRead, m.PriceCacheWrite, m.PricePerImage, m.Currency,
		m.Dim, time.Now().Unix(), id)
	if err != nil {
		return nil, err
	}
	return GetModel(ctx, db, id)
}

// DeleteModel removes the row. FKs cascade to model_skills.
func DeleteModel(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, "DELETE FROM models WHERE id=?", id)
	return err
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
