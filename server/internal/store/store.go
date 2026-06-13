// Package store wraps the SQLite database, schema migration, and seed.
//
// Designed so the storage layer can be swapped for Postgres without touching
// business code — every query is plain SQL string and every model is a flat
// struct with JSON-tagged exports.
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"aurelia/server/internal/config"
	"aurelia/server/internal/store/pgcompat"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

//go:embed schema_pg.sql
var schemaPGSQL string

// usePostgres records which dialect Open selected, so Migrate applies the
// matching schema. Set once at startup; the server opens a single database.
var usePostgres bool

// isPostgresDSN reports whether the data source addresses PostgreSQL. Accepts
// the libpq URL forms plus bare key=value strings.
func isPostgresDSN(dsn string) bool {
	l := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(l, "postgres://") ||
		strings.HasPrefix(l, "postgresql://") ||
		strings.Contains(l, "host=") && strings.Contains(l, "dbname=")
}

// Open opens the relational database. A postgres:// (or libpq key=value) data
// source selects PostgreSQL via the pgcompat driver (which accepts `?`
// placeholders); anything else opens SQLite for local development.
func Open(dataSource string) (*sql.DB, error) {
	if isPostgresDSN(dataSource) {
		usePostgres = true
		db, err := pgcompat.Open(dataSource)
		if err != nil {
			return nil, fmt.Errorf("pgcompat.Open: %w", err)
		}
		// A real connection pool — Postgres handles concurrency, unlike the
		// single-writer SQLite file.
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(10)
		db.SetConnMaxIdleTime(5 * time.Minute)
		db.SetConnMaxLifetime(time.Hour)
		if err := db.Ping(); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("db.Ping: %w", err)
		}
		return db, nil
	}

	usePostgres = false
	db, err := sql.Open("sqlite3", dataSource)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite serialises writes; keep contention low.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db.Ping: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable FK: %w", err)
	}
	return db, nil
}

// Migrate applies the embedded schema for the active dialect. Idempotent.
func Migrate(db *sql.DB) error {
	schema := schemaSQL
	addImageRef := `ALTER TABLE chunks ADD COLUMN image_ref TEXT`
	addOfficialTools := `ALTER TABLE models ADD COLUMN official_tools TEXT NOT NULL DEFAULT '[]'`
	if usePostgres {
		schema = schemaPGSQL
		addImageRef = `ALTER TABLE chunks ADD COLUMN IF NOT EXISTS image_ref TEXT`
		addOfficialTools = `ALTER TABLE models ADD COLUMN IF NOT EXISTS official_tools TEXT NOT NULL DEFAULT '[]'`
	}
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	// Best-effort additive migrations for existing databases — CREATE TABLE
	// IF NOT EXISTS won't add columns to a pre-existing table. On SQLite a
	// duplicate-column error is expected and ignored; Postgres uses IF NOT
	// EXISTS so it's a clean no-op.
	for _, ddl := range []string{addImageRef, addOfficialTools} {
		_, _ = db.Exec(ddl)
	}
	return nil
}

// Seed installs the bootstrap admin user + the always-on mock channel/model
// so the server is functional on a fresh database without provider keys.
func Seed(db *sql.DB, cfg config.Config) error {
	// Admin user.
	var existingID string
	err := db.QueryRow("SELECT id FROM users WHERE role='admin' LIMIT 1").Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := seedAdmin(db, cfg); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("check admin: %w", err)
	}

	// Mock channel + model — gives the system a working chat path before any
	// real keys are wired up. See internal/llm/mock_provider.go.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM channels WHERE type='mock'").Scan(&n); err != nil {
		return fmt.Errorf("check channels: %w", err)
	}
	if n == 0 && cfg.EnableMockKeys {
		if err := seedMock(db); err != nil {
			return err
		}
	}

	// Default global settings.
	for k, v := range map[string]string{
		"default_model_id":            `""`,
		"task_model_id":               `""`,
		"keep_recent_rounds":          `6`,
		"summary_max_tokens":          `2048`,
		"compaction_enabled":          `true`,
		"memory_enabled":              `true`,
		"daily_message_limit":         fmt.Sprintf("%d", cfg.DailyMessages),
		"daily_image_limit":           fmt.Sprintf("%d", cfg.DailyImages),
		"signup_open":                 `true`,
		"email_verification_required": `false`,
		"sandbox_base_url":            `""`,
		"sandbox_api_key":             `""`,
	} {
		if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO NOTHING`, k, v); err != nil {
			return fmt.Errorf("seed setting %s: %w", k, err)
		}
	}
	return nil
}

func seedAdmin(db *sql.DB, cfg config.Config) error {
	id := genID("u")
	hash, err := hashPassword(cfg.SeedAdminPass)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO users(id, email, password_hash, name, role, status, settings) VALUES(?, ?, ?, ?, 'admin', 'active', '{}')`,
		id, cfg.SeedAdminEmail, hash, "Aurelia Admin",
	)
	return err
}

func seedMock(db *sql.DB) error {
	channelID := "ch_mock"
	if _, err := db.Exec(
		`INSERT INTO channels(id, name, type, api_format, base_url, api_key, enabled, sort_order) VALUES(?, 'Mock provider', 'mock', '', '', '', 1, 0)
		 ON CONFLICT(id) DO NOTHING`, channelID); err != nil {
		return err
	}
	// One chat model.
	if _, err := db.Exec(
		`INSERT INTO models(id, channel_id, kind, request_id, label, description, icon, tool_mode, vision, stream, system_prompt, param_controls, currency)
		 VALUES('m_mock_chat', ?, 'chat', 'aurelia-mock-1', 'Aurelia Mock', 'Local mock streaming model. No external calls.', 'sparkles', 'native', 1, 1, '', '[]', 'USD')
		 ON CONFLICT(id) DO NOTHING`, channelID); err != nil {
		return err
	}
	// One embedding model so KBs can be created without external keys.
	if _, err := db.Exec(
		`INSERT INTO models(id, channel_id, kind, request_id, label, description, icon, dim, currency)
		 VALUES('m_mock_embed', ?, 'embedding', 'aurelia-mock-embed', 'Aurelia Local Embed', 'Lightweight local hash embedding. For dev only.', 'circuit-board', 256, 'USD')
		 ON CONFLICT(id) DO NOTHING`, channelID); err != nil {
		return err
	}
	// Set defaults if unset.
	if _, err := db.Exec(`UPDATE settings SET value=? WHERE key='default_model_id' AND value=?`, `"m_mock_chat"`, `""`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE settings SET value=? WHERE key='task_model_id' AND value=?`, `"m_mock_chat"`, `""`); err != nil {
		return err
	}
	return nil
}
