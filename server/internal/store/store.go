// Package store wraps the SQLite database, schema migration, and seed.
//
// Designed so the storage layer can be swapped for Postgres without touching
// business code — every query is plain SQL string and every model is a flat
// struct with JSON-tagged exports.
package store

import (
	"database/sql"
	_ "embed"
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
	addGroupID := `ALTER TABLE users ADD COLUMN group_id TEXT NOT NULL DEFAULT 'ug_free'`
	addTotpSecret := `ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT ''`
	addTotpEnabled := `ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0`
	addFeedback := `ALTER TABLE messages ADD COLUMN feedback TEXT NOT NULL DEFAULT ''`
	addGenMs := `ALTER TABLE messages ADD COLUMN gen_ms INTEGER NOT NULL DEFAULT 0`
	// Active-sessions context on refresh tokens (§ account → active sessions).
	addSessUA := `ALTER TABLE refresh_tokens ADD COLUMN user_agent TEXT NOT NULL DEFAULT ''`
	addSessIP := `ALTER TABLE refresh_tokens ADD COLUMN ip TEXT NOT NULL DEFAULT ''`
	addSessLoc := `ALTER TABLE refresh_tokens ADD COLUMN location TEXT NOT NULL DEFAULT ''`
	addSessSeen := `ALTER TABLE refresh_tokens ADD COLUMN last_seen INTEGER NOT NULL DEFAULT 0`
	// Per-model content moderation (§ moderation).
	addModEnabled := `ALTER TABLE models ADD COLUMN moderation_enabled INTEGER NOT NULL DEFAULT 0`
	addModMode := `ALTER TABLE models ADD COLUMN moderation_mode TEXT NOT NULL DEFAULT 'keyword'`
	// Redeem-code-driven group membership window (§ redeem codes).
	addGroupExpires := `ALTER TABLE users ADD COLUMN group_expires_at INTEGER NOT NULL DEFAULT 0`
	addPrevGroup := `ALTER TABLE users ADD COLUMN previous_group_id TEXT NOT NULL DEFAULT ''`
	// Forced set-password for OAuth accounts (§ third-party login has no password).
	addPasswordSet := `ALTER TABLE users ADD COLUMN password_set INTEGER NOT NULL DEFAULT 1`
	// Online status / last-seen (§ admin → users).
	addLastSeen := `ALTER TABLE users ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0`
	// Inline-thread linkage (§ text-selection sub-conversations).
	addInlineSource := `ALTER TABLE conversations ADD COLUMN inline_source_conv TEXT NOT NULL DEFAULT ''`
	addInlineParent := `ALTER TABLE conversations ADD COLUMN inline_parent_id TEXT NOT NULL DEFAULT ''`
	addInlineQuote := `ALTER TABLE conversations ADD COLUMN inline_quote TEXT NOT NULL DEFAULT ''`
	// Model tags (§ model tags) — JSON id array on models.
	addModelTags := `ALTER TABLE models ADD COLUMN tags TEXT NOT NULL DEFAULT '[]'`
	// Per-group resource caps (§ user groups) — max projects / KBs a member may
	// create. 0 = unlimited.
	addGroupMaxProjects := `ALTER TABLE user_groups ADD COLUMN max_projects INTEGER NOT NULL DEFAULT 0`
	addGroupMaxKBs := `ALTER TABLE user_groups ADD COLUMN max_kbs INTEGER NOT NULL DEFAULT 0`
	// Credit system (§ credits): per-group timed allowance + refresh cycle;
	// per-user non-expiring balance; per-row charge. (The USD↔credit rate and the
	// purchase links are global settings, not columns.)
	addGroupCreditAllowance := `ALTER TABLE user_groups ADD COLUMN credit_allowance REAL NOT NULL DEFAULT 0`
	addGroupCreditPeriod := `ALTER TABLE user_groups ADD COLUMN credit_period_seconds INTEGER NOT NULL DEFAULT 0`
	addUserPermCredits := `ALTER TABLE users ADD COLUMN credits_permanent REAL NOT NULL DEFAULT 0`
	addUsageCredits := `ALTER TABLE usage_logs ADD COLUMN credits REAL NOT NULL DEFAULT 0`
	if usePostgres {
		schema = schemaPGSQL
		addImageRef = `ALTER TABLE chunks ADD COLUMN IF NOT EXISTS image_ref TEXT`
		addOfficialTools = `ALTER TABLE models ADD COLUMN IF NOT EXISTS official_tools TEXT NOT NULL DEFAULT '[]'`
		addGroupID = `ALTER TABLE users ADD COLUMN IF NOT EXISTS group_id TEXT NOT NULL DEFAULT 'ug_free'`
		addTotpSecret = `ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT NOT NULL DEFAULT ''`
		addTotpEnabled = `ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled INTEGER NOT NULL DEFAULT 0`
		addFeedback = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS feedback TEXT NOT NULL DEFAULT ''`
		addGenMs = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS gen_ms BIGINT NOT NULL DEFAULT 0`
		addSessUA = `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT ''`
		addSessIP = `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS ip TEXT NOT NULL DEFAULT ''`
		addSessLoc = `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS location TEXT NOT NULL DEFAULT ''`
		addSessSeen = `ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS last_seen BIGINT NOT NULL DEFAULT 0`
		addModEnabled = `ALTER TABLE models ADD COLUMN IF NOT EXISTS moderation_enabled INTEGER NOT NULL DEFAULT 0`
		addModMode = `ALTER TABLE models ADD COLUMN IF NOT EXISTS moderation_mode TEXT NOT NULL DEFAULT 'keyword'`
		addGroupExpires = `ALTER TABLE users ADD COLUMN IF NOT EXISTS group_expires_at BIGINT NOT NULL DEFAULT 0`
		addPrevGroup = `ALTER TABLE users ADD COLUMN IF NOT EXISTS previous_group_id TEXT NOT NULL DEFAULT ''`
		addPasswordSet = `ALTER TABLE users ADD COLUMN IF NOT EXISTS password_set INTEGER NOT NULL DEFAULT 1`
		addLastSeen = `ALTER TABLE users ADD COLUMN IF NOT EXISTS last_seen_at BIGINT NOT NULL DEFAULT 0`
		addInlineSource = `ALTER TABLE conversations ADD COLUMN IF NOT EXISTS inline_source_conv TEXT NOT NULL DEFAULT ''`
		addInlineParent = `ALTER TABLE conversations ADD COLUMN IF NOT EXISTS inline_parent_id TEXT NOT NULL DEFAULT ''`
		addInlineQuote = `ALTER TABLE conversations ADD COLUMN IF NOT EXISTS inline_quote TEXT NOT NULL DEFAULT ''`
		addModelTags = `ALTER TABLE models ADD COLUMN IF NOT EXISTS tags TEXT NOT NULL DEFAULT '[]'`
		addGroupMaxProjects = `ALTER TABLE user_groups ADD COLUMN IF NOT EXISTS max_projects INTEGER NOT NULL DEFAULT 0`
		addGroupMaxKBs = `ALTER TABLE user_groups ADD COLUMN IF NOT EXISTS max_kbs INTEGER NOT NULL DEFAULT 0`
		addGroupCreditAllowance = `ALTER TABLE user_groups ADD COLUMN IF NOT EXISTS credit_allowance REAL NOT NULL DEFAULT 0`
		addGroupCreditPeriod = `ALTER TABLE user_groups ADD COLUMN IF NOT EXISTS credit_period_seconds INTEGER NOT NULL DEFAULT 0`
		addUserPermCredits = `ALTER TABLE users ADD COLUMN IF NOT EXISTS credits_permanent REAL NOT NULL DEFAULT 0`
		addUsageCredits = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS credits REAL NOT NULL DEFAULT 0`
	}
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	// Best-effort additive migrations for existing databases — CREATE TABLE
	// IF NOT EXISTS won't add columns to a pre-existing table. On SQLite a
	// duplicate-column error is expected and ignored; Postgres uses IF NOT
	// EXISTS so it's a clean no-op.
	for _, ddl := range []string{
		addImageRef, addOfficialTools, addGroupID, addTotpSecret, addTotpEnabled, addFeedback, addGenMs,
		addSessUA, addSessIP, addSessLoc, addSessSeen,
		addModEnabled, addModMode,
		addGroupExpires, addPrevGroup, addPasswordSet, addLastSeen,
		addInlineSource, addInlineParent, addInlineQuote,
		addModelTags,
		addGroupMaxProjects, addGroupMaxKBs,
		addGroupCreditAllowance, addGroupCreditPeriod,
		addUserPermCredits, addUsageCredits,
	} {
		_, _ = db.Exec(ddl)
	}
	// Indexes that depend on additively-added columns must run AFTER the ALTERs
	// above (on an existing DB the CREATE TABLE is a no-op, so the column only
	// exists once the ALTER has run). Kept out of the schema file for that reason.
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_conv_inline ON conversations(inline_source_conv)`)
	// One-time backfill: accounts that exist only because of an OAuth login were
	// created with a random password they never chose, so mark them as
	// password-unset to force them through the set-password gate. Guarded by a
	// settings flag so it runs exactly once — re-running would re-prompt users
	// who have since set their own password.
	var pwBackfill string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key='oauth_pwset_backfill_v1'`).Scan(&pwBackfill)
	if pwBackfill == "" {
		_, _ = db.Exec(`UPDATE users SET password_set=0 WHERE id IN (SELECT user_id FROM oauth_identities)`)
		_, _ = db.Exec(`INSERT INTO settings(key, value) VALUES('oauth_pwset_backfill_v1', '1') ON CONFLICT(key) DO NOTHING`)
	}
	return nil
}

// Seed installs the bootstrap admin user + default global settings. The system
// ships with NO mock provider — an admin must configure a real channel + model
// and set the default/task model before chat works.
func Seed(db *sql.DB, cfg config.Config) error {
	// No admin is seeded from the environment. A brand-new deployment starts with
	// ZERO users; the first account is created through the first-run setup flow
	// (POST /api/setup) and becomes the admin (§ first-run setup).

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
		// Anti-abuse registration controls. register_ip_daily_limit caps how many
		// accounts one client IP may create per calendar day (0 = unlimited).
		// register_captcha_required gates signup behind the arithmetic captcha
		// (a text math question — no image/OCR).
		"register_ip_daily_limit":   `0`,
		"register_captcha_required": `false`,
		// Global credit conversion rate (§ credits): 1 USD of model cost = N
		// credits. Shared by every group; 0 disables credits platform-wide.
		"credits_per_usd": `0`,
		// Global purchase links (§ credits / user groups): one tier-upgrade link
		// and one permanent-credit top-up link, shared by every group.
		"group_buy_url":         `""`,
		"credit_buy_url":        `""`,
		"sandbox_base_url":      `""`,
		"sandbox_api_key":       `""`,
		"moderation_keywords":   `[]`,
		"moderation_model_id":   `""`,
		"moderation_categories": `["politics","pornography","violence or gore","terrorism","illegal activity","hate speech","self-harm"]`,
		"moderation_message":    `"Your message was blocked by content moderation. Please rephrase and try again."`,
		// § announcement: a single global notice shown to users on load. image_url
		// non-empty → image announcement (image left, text right). remember_dismiss
		// false → re-show every visit; updated_at doubles as the dismiss version.
		"announcement": `{"enabled":false,"body":"","image_url":"","remember_dismiss":true,"updated_at":0}`,
	} {
		if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO NOTHING`, k, v); err != nil {
			return fmt.Errorf("seed setting %s: %w", k, err)
		}
	}

	// Default "Free" membership group — always present. New users and any
	// legacy user without a group resolve to it (§ user groups).
	if _, err := db.Exec(`INSERT INTO user_groups(id, name, description, features, price_usd, price_cny, is_default, sort_order)
		VALUES('ug_free', 'Free', 'Default access tier.', '[]', 0, 0, 1, 0)
		ON CONFLICT(id) DO NOTHING`); err != nil {
		return fmt.Errorf("seed free group: %w", err)
	}
	// Backfill any user whose group no longer resolves (NULL/empty/dangling) to
	// the free group.
	if _, err := db.Exec(`UPDATE users SET group_id='ug_free'
		WHERE group_id IS NULL OR group_id='' OR group_id NOT IN (SELECT id FROM user_groups)`); err != nil {
		return fmt.Errorf("backfill user groups: %w", err)
	}
	return nil
}
