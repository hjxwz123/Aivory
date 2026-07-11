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

	"aivory/server/internal/config"
	"aivory/server/internal/store/pgcompat"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

//go:embed schema_pg.sql
var schemaPGSQL string

// usePostgres records which dialect Open selected, so Migrate applies the
// matching schema. Set once at startup; the server opens a single database.
var usePostgres bool

// Postgres connection-pool tunables.
var (
	setMaxOpenConns    = 20
	setMaxIdleConns    = 10
	setConnMaxIdleTime = 5 * time.Minute
	setConnMaxLifetime = time.Hour
)

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
		db.SetMaxOpenConns(setMaxOpenConns)
		db.SetMaxIdleConns(setMaxIdleConns)
		db.SetConnMaxIdleTime(setConnMaxIdleTime)
		db.SetConnMaxLifetime(setConnMaxLifetime)
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
	// Per-model Deep Research exposure (§2.3-B).
	addResearchEnabled := `ALTER TABLE models ADD COLUMN research_enabled INTEGER NOT NULL DEFAULT 1`
	// Redeem-code-driven group membership window (§ redeem codes).
	addGroupExpires := `ALTER TABLE users ADD COLUMN group_expires_at INTEGER NOT NULL DEFAULT 0`
	addPrevGroup := `ALTER TABLE users ADD COLUMN previous_group_id TEXT NOT NULL DEFAULT ''`
	// Forced set-password for OAuth accounts (§ third-party login has no password).
	addPasswordSet := `ALTER TABLE users ADD COLUMN password_set INTEGER NOT NULL DEFAULT 1`
	// Last password change (§ account security row). 0 = never changed since signup.
	addPasswordChangedAt := `ALTER TABLE users ADD COLUMN password_changed_at INTEGER NOT NULL DEFAULT 0`
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
	addUserSortOrder := `ALTER TABLE users ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`
	addUsageCredits := `ALTER TABLE usage_logs ADD COLUMN credits REAL NOT NULL DEFAULT 0`
	addMsgCredits := `ALTER TABLE messages ADD COLUMN credits REAL NOT NULL DEFAULT 0`
	// Model label snapshot — preserves the human-readable model name on each
	// message so it remains visible even after the model is deleted from the catalog.
	addMsgModelLabel := `ALTER TABLE messages ADD COLUMN model_label TEXT NOT NULL DEFAULT ''`
	addMsgSearchText := `ALTER TABLE messages ADD COLUMN search_text TEXT NOT NULL DEFAULT ''`
	// §4.20 per-model image generation timeout (seconds; 0 = default).
	addImageTimeout := `ALTER TABLE models ADD COLUMN image_timeout_sec INTEGER NOT NULL DEFAULT 0`
	// §verify: per-message auditor (Verify mode) result JSON ('' = never audited).
	addMsgVerify := `ALTER TABLE messages ADD COLUMN verify TEXT NOT NULL DEFAULT ''`
	// Workspaces (§workspaces): '' = personal. Conversations/projects/KBs inside
	// a workspace are shared across members; messages record their AUTHOR so
	// shared conversations attribute each user turn; usage rows carry the
	// workspace for the usage pages; per-group cap on owned workspaces.
	addConvWorkspace := `ALTER TABLE conversations ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`
	addProjWorkspace := `ALTER TABLE projects ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`
	addKBWorkspace := `ALTER TABLE knowledge_bases ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`
	addMsgAuthor := `ALTER TABLE messages ADD COLUMN author_id TEXT NOT NULL DEFAULT ''`
	addUsageWorkspace := `ALTER TABLE usage_logs ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''`
	addGroupMaxWorkspaces := `ALTER TABLE user_groups ADD COLUMN max_workspaces INTEGER NOT NULL DEFAULT 0`
	// Whether the tier is listed on the public subscription page (§ user groups).
	addGroupIsPublic := `ALTER TABLE user_groups ADD COLUMN is_public INTEGER NOT NULL DEFAULT 1`
	// §fallback channel: per-model backup channel retried on primary request
	// failure ('' = none); usage rows record which channel served the request,
	// whether the fallback was used, and error requests are logged too.
	addModelFallbackChannel := `ALTER TABLE models ADD COLUMN fallback_channel_id TEXT NOT NULL DEFAULT ''`
	addUsageChannel := `ALTER TABLE usage_logs ADD COLUMN channel_id TEXT NOT NULL DEFAULT ''`
	addUsageFallback := `ALTER TABLE usage_logs ADD COLUMN fallback INTEGER NOT NULL DEFAULT 0`
	addUsageStatus := `ALTER TABLE usage_logs ADD COLUMN status TEXT NOT NULL DEFAULT 'ok'`
	addUsageError := `ALTER TABLE usage_logs ADD COLUMN error TEXT NOT NULL DEFAULT ''`
	addUsageRequestMethod := `ALTER TABLE usage_logs ADD COLUMN request_method TEXT NOT NULL DEFAULT ''`
	addUsageRequestURL := `ALTER TABLE usage_logs ADD COLUMN request_url TEXT NOT NULL DEFAULT ''`
	addUsageRequestHeaders := `ALTER TABLE usage_logs ADD COLUMN request_headers TEXT NOT NULL DEFAULT ''`
	addUsageRequestBody := `ALTER TABLE usage_logs ADD COLUMN request_body TEXT NOT NULL DEFAULT ''`
	// Composer uploads remain drafts until the user message carrying them is
	// persisted. This lets the client restore only unsent attachments on refresh.
	addFileDraft := `ALTER TABLE files ADD COLUMN draft INTEGER NOT NULL DEFAULT 0`
	// Persisted ingest heartbeat lets the RAG watchdog distinguish a live long-
	// running parse from a task abandoned by timeout, crash, or lease expiry.
	addDocumentIngestUpdatedAt := `ALTER TABLE documents ADD COLUMN ingest_updated_at INTEGER NOT NULL DEFAULT 0`
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
		addResearchEnabled = `ALTER TABLE models ADD COLUMN IF NOT EXISTS research_enabled INTEGER NOT NULL DEFAULT 1`
		addGroupExpires = `ALTER TABLE users ADD COLUMN IF NOT EXISTS group_expires_at BIGINT NOT NULL DEFAULT 0`
		addPrevGroup = `ALTER TABLE users ADD COLUMN IF NOT EXISTS previous_group_id TEXT NOT NULL DEFAULT ''`
		addPasswordSet = `ALTER TABLE users ADD COLUMN IF NOT EXISTS password_set INTEGER NOT NULL DEFAULT 1`
		addPasswordChangedAt = `ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at BIGINT NOT NULL DEFAULT 0`
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
		addUserSortOrder = `ALTER TABLE users ADD COLUMN IF NOT EXISTS sort_order INTEGER NOT NULL DEFAULT 0`
		addUsageCredits = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS credits REAL NOT NULL DEFAULT 0`
		addMsgCredits = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS credits DOUBLE PRECISION NOT NULL DEFAULT 0`
		addMsgModelLabel = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS model_label TEXT NOT NULL DEFAULT ''`
		addMsgSearchText = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_text TEXT NOT NULL DEFAULT ''`
		addImageTimeout = `ALTER TABLE models ADD COLUMN IF NOT EXISTS image_timeout_sec INTEGER NOT NULL DEFAULT 0`
		addMsgVerify = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS verify TEXT NOT NULL DEFAULT ''`
		addConvWorkspace = `ALTER TABLE conversations ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`
		addProjWorkspace = `ALTER TABLE projects ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`
		addKBWorkspace = `ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`
		addMsgAuthor = `ALTER TABLE messages ADD COLUMN IF NOT EXISTS author_id TEXT NOT NULL DEFAULT ''`
		addUsageWorkspace = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT ''`
		addGroupMaxWorkspaces = `ALTER TABLE user_groups ADD COLUMN IF NOT EXISTS max_workspaces INTEGER NOT NULL DEFAULT 0`
		addGroupIsPublic = `ALTER TABLE user_groups ADD COLUMN IF NOT EXISTS is_public INTEGER NOT NULL DEFAULT 1`
		addModelFallbackChannel = `ALTER TABLE models ADD COLUMN IF NOT EXISTS fallback_channel_id TEXT NOT NULL DEFAULT ''`
		addUsageChannel = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS channel_id TEXT NOT NULL DEFAULT ''`
		addUsageFallback = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS fallback INTEGER NOT NULL DEFAULT 0`
		addUsageStatus = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'ok'`
		addUsageError = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT ''`
		addUsageRequestMethod = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS request_method TEXT NOT NULL DEFAULT ''`
		addUsageRequestURL = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS request_url TEXT NOT NULL DEFAULT ''`
		addUsageRequestHeaders = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS request_headers TEXT NOT NULL DEFAULT ''`
		addUsageRequestBody = `ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS request_body TEXT NOT NULL DEFAULT ''`
		addFileDraft = `ALTER TABLE files ADD COLUMN IF NOT EXISTS draft INTEGER NOT NULL DEFAULT 0`
		addDocumentIngestUpdatedAt = `ALTER TABLE documents ADD COLUMN IF NOT EXISTS ingest_updated_at BIGINT NOT NULL DEFAULT 0`
	}
	if err := dedupeSkillNames(db); err != nil {
		return fmt.Errorf("dedupe skill names: %w", err)
	}
	if err := normalizeUniqueTextFields(db); err != nil {
		return fmt.Errorf("normalize unique text fields: %w", err)
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
		addResearchEnabled,
		addGroupExpires, addPrevGroup, addPasswordSet, addPasswordChangedAt, addLastSeen,
		addInlineSource, addInlineParent, addInlineQuote,
		addModelTags,
		addGroupMaxProjects, addGroupMaxKBs,
		addGroupCreditAllowance, addGroupCreditPeriod,
		addUserPermCredits, addUserSortOrder, addUsageCredits, addMsgCredits,
		addMsgModelLabel, addMsgSearchText,
		addImageTimeout,
		addMsgVerify,
		addConvWorkspace, addProjWorkspace, addKBWorkspace, addMsgAuthor, addUsageWorkspace, addGroupMaxWorkspaces, addGroupIsPublic,
		addModelFallbackChannel, addUsageChannel, addUsageFallback, addUsageStatus, addUsageError,
		addUsageRequestMethod, addUsageRequestURL, addUsageRequestHeaders, addUsageRequestBody,
		addFileDraft, addDocumentIngestUpdatedAt,
	} {
		_, _ = db.Exec(ddl)
	}
	if err := dropLegacyChunkEmbeddingColumn(db); err != nil {
		return fmt.Errorf("drop legacy chunks.embedding column: %w", err)
	}
	// Indexes that depend on additively-added columns must run AFTER the ALTERs
	// above (on an existing DB the CREATE TABLE is a no-op, so the column only
	// exists once the ALTER has run). Kept out of the schema file for that reason.
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_conv_inline ON conversations(inline_source_conv)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_files_conversation_id ON files(conversation_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_docs_ingest_state ON documents(status, ingest_updated_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_conv_created ON messages(conversation_id, created_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_conv_user_updated ON conversations(user_id, archived, pinned DESC, updated_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_users_sort_order ON users(sort_order, created_at DESC)`)
	// Workspace-scoped listings (§workspaces) — mirror the personal composites.
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_conv_workspace_updated ON conversations(workspace_id, archived, pinned DESC, updated_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_projects_workspace ON projects(workspace_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_kbs_workspace ON knowledge_bases(workspace_id)`)
	for _, ddl := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_name_unique ON channels(lower(trim(name)))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_providers_name_unique ON oauth_providers(lower(trim(name)))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_groups_name_unique ON user_groups(lower(trim(name)))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_model_tags_name_unique ON model_tags(lower(trim(name)))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_image_styles_name_unique ON image_styles(lower(trim(name)))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_models_channel_request_unique ON models(channel_id, lower(trim(request_id)))`,
		// §workspaces: names are unique per (user, space) — the same user may reuse
		// a name across their personal space and different workspaces. The old
		// two-column indexes are dropped and recreated with the workspace column.
		`DROP INDEX IF EXISTS idx_projects_user_name_unique`,
		`DROP INDEX IF EXISTS idx_kbs_user_name_unique`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name_unique ON projects(user_id, COALESCE(workspace_id,''), lower(trim(name)))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_kbs_user_name_unique ON knowledge_bases(user_id, COALESCE(workspace_id,''), lower(trim(name)))`,
	} {
		_, _ = db.Exec(ddl)
	}

	// Column-parity guard. The additive ALTER loop above discards every error by
	// design (a duplicate-column error on SQLite is expected on re-run), which also
	// SILENTLY swallows a genuine failure — leaving a column absent while the code's
	// SELECTs still list it, so the next read fails at runtime with "no such column"
	// on a live server. Assert here that every additively-migrated column actually
	// exists; a real failure now aborts startup loudly (mirrors the apply-schema
	// fatal) instead of surfacing as broken reads later. WHERE 1=0 makes each probe
	// O(1). If you add an ALTER above, add its column here.
	columnChecks := map[string][]string{
		"messages":        {"credits", "model_label", "search_text", "gen_ms", "feedback", "verify", "author_id"},
		"users":           {"group_id", "totp_secret", "totp_enabled", "group_expires_at", "previous_group_id", "password_set", "password_changed_at", "last_seen_at", "credits_permanent", "sort_order"},
		"usage_logs":      {"credits", "workspace_id", "channel_id", "fallback", "status", "error", "request_method", "request_url", "request_headers", "request_body"},
		"user_groups":     {"max_projects", "max_kbs", "credit_allowance", "credit_period_seconds", "max_workspaces", "is_public"},
		"models":          {"official_tools", "moderation_enabled", "moderation_mode", "tags", "image_timeout_sec", "research_enabled", "fallback_channel_id"},
		"refresh_tokens":  {"user_agent", "ip", "location", "last_seen"},
		"conversations":   {"inline_source_conv", "inline_parent_id", "inline_quote", "workspace_id"},
		"projects":        {"workspace_id"},
		"knowledge_bases": {"workspace_id"},
		"chunks":          {"image_ref"},
		"files":           {"draft"},
		"documents":       {"ingest_updated_at"},
	}
	for table, cols := range columnChecks {
		if _, err := db.Exec(fmt.Sprintf(`SELECT %s FROM %s WHERE 1=0`, strings.Join(cols, ", "), table)); err != nil {
			return fmt.Errorf("schema column check failed for %q (an additive migration may have silently failed): %w", table, err)
		}
	}
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
	// One-time backfill of messages.search_text for rows that predate the column,
	// so existing history is searchable. Keyset-paged, best-effort, runs once.
	var stBackfill string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key='msg_search_text_backfill_v1'`).Scan(&stBackfill)
	if stBackfill == "" {
		backfillSearchText(db)
		_, _ = db.Exec(`INSERT INTO settings(key, value) VALUES('msg_search_text_backfill_v1', '1') ON CONFLICT(key) DO NOTHING`)
	}
	// One-time backfill: existing users predate admin-defined ordering, so
	// preserve the old default order (newest created first) as explicit slots.
	var userSortBackfill string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key='user_sort_order_backfill_v1'`).Scan(&userSortBackfill)
	if userSortBackfill == "" {
		backfillUserSortOrder(db)
		_, _ = db.Exec(`INSERT INTO settings(key, value) VALUES('user_sort_order_backfill_v1', '1') ON CONFLICT(key) DO NOTHING`)
	}
	// One-time backfill: the welcome wizard is gated by users.settings.onboarded.
	// Accounts created before that flag existed are established users, so mark
	// them complete rather than showing first-login preferences on their next
	// refresh.
	var onboardBackfill string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key='user_onboarded_backfill_v1'`).Scan(&onboardBackfill)
	if onboardBackfill == "" {
		backfillUserOnboarded(db)
		_, _ = db.Exec(`INSERT INTO settings(key, value) VALUES('user_onboarded_backfill_v1', '1') ON CONFLICT(key) DO NOTHING`)
	}
	return nil
}

func dropLegacyChunkEmbeddingColumn(db *sql.DB) error {
	exists, err := columnExists(db, "chunks", "embedding")
	if err != nil || !exists {
		return err
	}
	if usePostgres {
		_, err := db.Exec(`ALTER TABLE chunks DROP COLUMN IF EXISTS embedding`)
		return err
	}
	if _, err := db.Exec(`ALTER TABLE chunks DROP COLUMN embedding`); err == nil || isMissingColumnErr(err) {
		return nil
	}
	return rebuildSQLiteChunksWithoutEmbedding(db)
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	if _, err := db.Exec(fmt.Sprintf(`SELECT %s FROM %s WHERE 1=0`, column, table)); err != nil {
		if isMissingColumnErr(err) || isMissingTableErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func rebuildSQLiteChunksWithoutEmbedding(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`ALTER TABLE chunks RENAME TO chunks__legacy_embedding`); err != nil {
		return err
	}
	for _, ddl := range []string{
		`DROP INDEX IF EXISTS idx_chunks_doc`,
		`DROP INDEX IF EXISTS idx_chunks_kb`,
		`DROP INDEX IF EXISTS idx_chunks_conv`,
		`CREATE TABLE chunks (
			id              TEXT PRIMARY KEY,
			document_id     TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			kb_id           TEXT,
			conversation_id TEXT,
			seq             INTEGER NOT NULL,
			parent_id       TEXT,
			chunk_type      TEXT NOT NULL DEFAULT 'text',
			content         TEXT NOT NULL,
			image_ref       TEXT,
			meta            TEXT NOT NULL DEFAULT '{}',
			embedding_model TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO chunks(id, document_id, kb_id, conversation_id, seq, parent_id, chunk_type, content, image_ref, meta, embedding_model)
		 SELECT id, document_id, kb_id, conversation_id, seq, parent_id, COALESCE(NULLIF(chunk_type,''), 'text'), content, image_ref, COALESCE(meta, '{}'), COALESCE(embedding_model, '')
		 FROM chunks__legacy_embedding`,
		`DROP TABLE chunks__legacy_embedding`,
	} {
		if _, err := tx.Exec(ddl); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type duplicateSkill struct {
	id     string
	keeper string
}

func dedupeSkillNames(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, name, lower(trim(name)) FROM skills ORDER BY lower(trim(name)), sort_order, updated_at, id`)
	if err != nil {
		if isMissingTableErr(err) {
			return nil
		}
		return err
	}
	defer rows.Close()

	keepers := map[string]string{}
	duplicates := []duplicateSkill{}
	needsTrim := false
	for rows.Next() {
		var id, name, norm string
		if err := rows.Scan(&id, &name, &norm); err != nil {
			return err
		}
		if name != strings.TrimSpace(name) {
			needsTrim = true
		}
		if keeper := keepers[norm]; keeper != "" {
			duplicates = append(duplicates, duplicateSkill{id: id, keeper: keeper})
			continue
		}
		keepers[norm] = id
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !needsTrim && len(duplicates) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if needsTrim {
		if _, err := tx.Exec(`UPDATE skills SET name=trim(name)`); err != nil {
			return err
		}
	}

	mergeSQL := `INSERT OR IGNORE INTO model_skills(model_id, skill_id) SELECT model_id, ? FROM model_skills WHERE skill_id=?`
	if usePostgres {
		mergeSQL = `INSERT INTO model_skills(model_id, skill_id) SELECT model_id, ? FROM model_skills WHERE skill_id=? ON CONFLICT DO NOTHING`
	}
	for _, dup := range duplicates {
		if _, err := tx.Exec(mergeSQL, dup.keeper, dup.id); err != nil && !isMissingTableErr(err) {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM model_skills WHERE skill_id=?`, dup.id); err != nil && !isMissingTableErr(err) {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM skills WHERE id=?`, dup.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type uniqueTextSpec struct {
	table     string
	idCol     string
	valueCol  string
	scopeCols []string
	fallback  string
}

type uniqueTextRow struct {
	id     string
	value  string
	scopes []string
}

func normalizeUniqueTextFields(db *sql.DB) error {
	specs := []uniqueTextSpec{
		{table: "channels", idCol: "id", valueCol: "name", fallback: "Untitled channel"},
		{table: "oauth_providers", idCol: "id", valueCol: "name", fallback: "Untitled provider"},
		{table: "user_groups", idCol: "id", valueCol: "name", fallback: "Untitled group"},
		{table: "model_tags", idCol: "id", valueCol: "name", fallback: "Untitled tag"},
		{table: "image_styles", idCol: "id", valueCol: "name", fallback: "Untitled style"},
		{table: "models", idCol: "id", valueCol: "request_id", scopeCols: []string{"channel_id"}, fallback: "untitled-model"},
		{table: "projects", idCol: "id", valueCol: "name", scopeCols: []string{"user_id"}, fallback: "Untitled project"},
		{table: "knowledge_bases", idCol: "id", valueCol: "name", scopeCols: []string{"user_id"}, fallback: "Untitled library"},
	}
	for _, spec := range specs {
		if err := normalizeUniqueTextField(db, spec); err != nil {
			return err
		}
	}
	return nil
}

func normalizeUniqueTextField(db *sql.DB, spec uniqueTextSpec) error {
	selectCols := []string{spec.idCol, spec.valueCol}
	for _, col := range spec.scopeCols {
		selectCols = append(selectCols, "COALESCE("+col+", '')")
	}
	orderParts := append([]string{}, spec.scopeCols...)
	orderParts = append(orderParts, "lower(trim("+spec.valueCol+"))", spec.idCol)
	rows, err := db.Query(fmt.Sprintf(
		`SELECT %s FROM %s ORDER BY %s`,
		strings.Join(selectCols, ", "), spec.table, strings.Join(orderParts, ", "),
	))
	if err != nil {
		if isMissingTableErr(err) || isMissingColumnErr(err) {
			return nil
		}
		return err
	}
	defer rows.Close()

	records := []uniqueTextRow{}
	for rows.Next() {
		var id, value string
		scopeVals := make([]string, len(spec.scopeCols))
		dest := make([]any, 0, 2+len(scopeVals))
		dest = append(dest, &id, &value)
		for i := range scopeVals {
			dest = append(dest, &scopeVals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		records = append(records, uniqueTextRow{id: id, value: value, scopes: scopeVals})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(records) == 0 {
		return nil
	}

	usedByScope := map[string]map[string]bool{}
	updates := map[string]string{}
	for _, row := range records {
		scopeKey := strings.Join(row.scopes, "\x00")
		if usedByScope[scopeKey] == nil {
			usedByScope[scopeKey] = map[string]bool{}
		}
		used := usedByScope[scopeKey]
		next := strings.TrimSpace(row.value)
		if next == "" || used[normalizeTextForUnique(next)] {
			base := next
			if base == "" {
				base = spec.fallback
			}
			next = uniqueTextCandidate(base, row.id, used)
		}
		used[normalizeTextForUnique(next)] = true
		if next != row.value {
			updates[row.id] = next
		}
	}
	if len(updates) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	updateSQL := fmt.Sprintf(`UPDATE %s SET %s=? WHERE %s=?`, spec.table, spec.valueCol, spec.idCol)
	for id, next := range updates {
		if _, err := tx.Exec(updateSQL, next, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func uniqueTextCandidate(base, id string, used map[string]bool) string {
	tail := shortIDForName(id)
	for i := 0; ; i++ {
		suffix := tail
		if i > 0 {
			suffix = fmt.Sprintf("%s-%d", tail, i+1)
		}
		candidate := strings.TrimSpace(fmt.Sprintf("%s (%s)", base, suffix))
		if !used[normalizeTextForUnique(candidate)] {
			return candidate
		}
	}
}

func shortIDForName(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

func normalizeTextForUnique(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func isMissingTableErr(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "no such table") ||
		strings.Contains(low, "does not exist") ||
		strings.Contains(low, "undefined_table") ||
		strings.Contains(low, "42p01")
}

func isMissingColumnErr(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "no such column") ||
		strings.Contains(low, "undefined_column") ||
		strings.Contains(low, "42703")
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
		"default_model_id":      `""`,
		"task_model_id":         `""`,
		"image_prompt_model_id": `""`,
		"verify_model_id":       `""`,
		// §4.11-B RAG injection knobs (admin → Documents). A conversation doc at/below
		// rag_full_text_threshold (est. tokens) is injected in full; above it, it's
		// vectorised and only chunks are retrieved (rag_top_k of them, or — when
		// rag_dynamic_topk is on — every chunk with cosine sim ≥ rag_similarity_threshold).
		"rag_full_text_threshold":  `8000`,
		"rag_top_k":                `8`,
		"rag_dynamic_topk":         `false`,
		"rag_similarity_threshold": `0.5`,
		// §credits pre-flight: on a credit-charged turn, estimate the assembled
		// prompt's tokens before generating and refuse if the user can't afford it.
		"credit_preflight_enabled":    `true`,
		"keep_recent_rounds":          `6`,
		"summary_max_tokens":          `8192`,
		"compaction_token_trigger":    `32000`,
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
		"group_buy_url":    `""`,
		"credit_buy_url":   `""`,
		"sandbox_base_url": `""`,
		"sandbox_api_key":  `""`,
		// §4.5-F default sandbox archiving to the zero-dependency local backend so
		// a fresh deployment persists /workspace across the idle reaper with no
		// external object store. This is only meaningful because archives are keyed
		// by the conversation id (§4.5-C G2 fix), so restore survives session
		// recycle. Fail-safe: without SANDBOX_LOCAL_STORAGE_DIR mounted, `local` is
		// inert (reaped = gone). GC below bounds growth. Insert-if-absent, so an
		// admin who later picks s3/aliyun_oss (or "" to disable) is never overwritten.
		"storage_provider": `"local"`,
		// §4.5-A prune archived workspaces untouched for this many days so the
		// default-on local store can't grow without bound. An active conversation
		// re-archives (mtime bumps) each recycle, so only truly-abandoned ones age
		// out. 0 / "" = keep forever.
		"storage_archive_ttl_days": `30`,
		// §4.6 default image upload cap: 5 MB. Non-image files have no seeded cap
		// (blank → the MAX_UPLOAD_BYTES env ceiling). Admins tune both in
		// /admin/documents; the env ceiling remains the absolute maximum.
		"max_image_upload_mb":   `5`,
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
