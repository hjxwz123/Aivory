-- Aivory schema. SQLite-compatible; ports cleanly to Postgres (replace
-- AUTOINCREMENT with BIGSERIAL, JSON with JSONB, and add tsvector for
-- chunks). Mirrors design.md §5 — same table names and semantics. RAG vectors
-- live only in Qdrant; chunks stores text and retrieval metadata, not embeddings.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,                  -- JSON-encoded
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  email         TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  name          TEXT NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'user',
  status        TEXT NOT NULL DEFAULT 'active',
  token_ver     INTEGER NOT NULL DEFAULT 0,
  settings      TEXT NOT NULL DEFAULT '{}',
  group_id      TEXT NOT NULL DEFAULT 'ug_free',  -- membership tier (user_groups.id)
  totp_secret   TEXT NOT NULL DEFAULT '',         -- base32 TOTP secret (empty = no 2FA configured)
  totp_enabled  INTEGER NOT NULL DEFAULT 0,       -- 1 = login requires a 2FA code
  password_set  INTEGER NOT NULL DEFAULT 1,        -- 0 = OAuth account that never chose its own password
  password_changed_at INTEGER NOT NULL DEFAULT 0,  -- unix seconds of last password change (0 = never since signup)
  last_seen_at  INTEGER NOT NULL DEFAULT 0,        -- unix seconds of last authenticated activity (online status)
  credits_permanent REAL NOT NULL DEFAULT 0,       -- non-expiring credits (purchased / admin-set)
  sort_order    INTEGER NOT NULL DEFAULT 0,        -- admin-defined display order
  created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

-- Membership tiers. Exactly one row is the default (is_default=1, seeded as
-- ug_free). features is a JSON array of feature strings shown on the
-- subscription page; prices are display-only (no payment integration).
CREATE TABLE IF NOT EXISTS user_groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  features    TEXT NOT NULL DEFAULT '[]',
  price_usd   REAL NOT NULL DEFAULT 0,
  price_cny   REAL NOT NULL DEFAULT 0,
  is_default  INTEGER NOT NULL DEFAULT 0,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  max_projects INTEGER NOT NULL DEFAULT 0,
  max_kbs      INTEGER NOT NULL DEFAULT 0,
  -- Storage quota for non-image uploads, MB (0 = unlimited, § user files page).
  max_storage_mb INTEGER NOT NULL DEFAULT 0,
  credit_allowance      REAL NOT NULL DEFAULT 0,    -- timed credits granted each cycle
  credit_period_seconds INTEGER NOT NULL DEFAULT 0, -- refresh cycle length (0 = no timed credits)
  created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_groups_name_unique ON user_groups(lower(trim(name)));

-- Per-model, per-group access + usage cap. A model with NO rows here is open to
-- everyone (unlimited). Once a model has any row, only listed groups may use it;
-- each row caps usage within a fixed window: limit_type 'cost' (in the model's
-- currency) or 'count' (calls), limit_value 0 = granted but unlimited.
CREATE TABLE IF NOT EXISTS model_group_quotas (
  model_id       TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  group_id       TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  period_seconds INTEGER NOT NULL DEFAULT 604800,
  limit_type     TEXT NOT NULL DEFAULT 'count',  -- cost | count
  limit_value    REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (model_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_mgq_group ON model_group_quotas(group_id);

-- Redeem codes (§ redeem codes). Admin creates codes that grant a specific
-- user_group for `duration_days` (0 = permanent). `expires_at` is the deadline
-- by which the code itself must be redeemed (0 = no deadline). `max_uses=1`
-- (default) makes codes single-use; admins can bump it for shared promo codes.
-- enabled=0 lets an admin revoke an unredeemed code without deleting the row
-- (preserves audit history).
CREATE TABLE IF NOT EXISTS redeem_codes (
  id            TEXT PRIMARY KEY,
  code          TEXT UNIQUE NOT NULL,
  group_id      TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  duration_days INTEGER NOT NULL DEFAULT 30,
  max_uses      INTEGER NOT NULL DEFAULT 1,
  used_count    INTEGER NOT NULL DEFAULT 0,
  expires_at    INTEGER NOT NULL DEFAULT 0,
  enabled       INTEGER NOT NULL DEFAULT 1,
  note          TEXT NOT NULL DEFAULT '',
  batch_name    TEXT NOT NULL DEFAULT '',
  created_by    TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_redeem_codes_code ON redeem_codes(code);
CREATE INDEX IF NOT EXISTS idx_redeem_codes_batch ON redeem_codes(batch_name);

-- One row per successful redemption — audit trail + the basis for the user's
-- group membership window. user_id+code_id is unique so a single user can't
-- double-redeem the same multi-use code.
CREATE TABLE IF NOT EXISTS redeem_redemptions (
  id              TEXT PRIMARY KEY,
  code_id         TEXT NOT NULL REFERENCES redeem_codes(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  group_id        TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  previous_group_id TEXT NOT NULL DEFAULT '',
  granted_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL,
  UNIQUE(code_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_redemptions_user ON redeem_redemptions(user_id);
CREATE INDEX IF NOT EXISTS idx_redemptions_code ON redeem_redemptions(code_id);

CREATE TABLE IF NOT EXISTS channels (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  type        TEXT NOT NULL,                 -- openai | claude | gemini | mock
  api_format  TEXT NOT NULL DEFAULT '',      -- chat | responses (openai)
  base_url    TEXT NOT NULL DEFAULT '',
  api_key     TEXT NOT NULL DEFAULT '',
  enabled     INTEGER NOT NULL DEFAULT 1,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_name_unique ON channels(lower(trim(name)));

CREATE TABLE IF NOT EXISTS models (
  id                TEXT PRIMARY KEY,
  channel_id        TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  kind              TEXT NOT NULL DEFAULT 'chat',   -- chat | image | embedding
  request_id        TEXT NOT NULL,                  -- ID sent to upstream
  label             TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',
  icon              TEXT NOT NULL DEFAULT '',
  fallback_channel_id TEXT NOT NULL DEFAULT '',      -- retried when a primary request fails ('' = none, §fallback channel)
  enabled           INTEGER NOT NULL DEFAULT 1,
  sort_order        INTEGER NOT NULL DEFAULT 0,
  tool_mode         TEXT NOT NULL DEFAULT 'native', -- native | prompt | none
  vision            INTEGER NOT NULL DEFAULT 1,
  stream            INTEGER NOT NULL DEFAULT 1,
  research_enabled  INTEGER NOT NULL DEFAULT 1, -- expose Deep Research for this chat model
  system_prompt     TEXT NOT NULL DEFAULT '',
  param_controls    TEXT NOT NULL DEFAULT '[]',
  official_tools    TEXT NOT NULL DEFAULT '[]', -- OpenAI Responses hosted tools; [] = use system tools (§2.3-B)
  tags              TEXT NOT NULL DEFAULT '[]', -- model_tags ids for the picker filter (§ model tags)
  moderation_enabled INTEGER NOT NULL DEFAULT 0,      -- screen prompts before generation (§ moderation)
  moderation_mode   TEXT NOT NULL DEFAULT 'keyword',  -- keyword | model
  price_input       REAL NOT NULL DEFAULT 0,
  price_output      REAL NOT NULL DEFAULT 0,
  price_cache_read  REAL NOT NULL DEFAULT 0,
  price_cache_write REAL NOT NULL DEFAULT 0,
  price_per_image   REAL NOT NULL DEFAULT 0,
  currency          TEXT NOT NULL DEFAULT 'USD',
  dim               INTEGER NOT NULL DEFAULT 0,
  image_timeout_sec INTEGER NOT NULL DEFAULT 0,
  updated_at        INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_models_channel ON models(channel_id);
CREATE INDEX IF NOT EXISTS idx_models_kind ON models(kind, enabled);
CREATE UNIQUE INDEX IF NOT EXISTS idx_models_channel_request_unique ON models(channel_id, lower(trim(request_id)));

-- Model tags (§ model tags). Admin-managed labels; each model stores the tag ids
-- it carries in models.tags (a JSON array), and the picker filters by them.
CREATE TABLE IF NOT EXISTS model_tags (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_model_tags_name_unique ON model_tags(lower(trim(name)));

CREATE TABLE IF NOT EXISTS skills (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  description  TEXT NOT NULL,
  icon         TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL,
  assets       TEXT NOT NULL DEFAULT '[]',
  enabled      INTEGER NOT NULL DEFAULT 1,
  sort_order   INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_name_unique ON skills(lower(trim(name)));

CREATE TABLE IF NOT EXISTS model_skills (
  model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  skill_id TEXT NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  PRIMARY KEY (model_id, skill_id)
);

CREATE TABLE IF NOT EXISTS knowledge_bases (
  id                 TEXT PRIMARY KEY,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name               TEXT NOT NULL,
  description        TEXT NOT NULL DEFAULT '',
  embedding_model_id TEXT NOT NULL REFERENCES models(id),
  embedding_dim      INTEGER NOT NULL,
  project_id         TEXT,                          -- non-null when KB belongs to a project
  created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_kbs_user ON knowledge_bases(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_kbs_user_name_unique ON knowledge_bases(user_id, lower(trim(name)));

CREATE TABLE IF NOT EXISTS projects (
  id               TEXT PRIMARY KEY,
  user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  instructions     TEXT NOT NULL DEFAULT '',
  accent           TEXT NOT NULL DEFAULT 'violet',
  emoji            TEXT NOT NULL DEFAULT '',
  pinned           INTEGER NOT NULL DEFAULT 0,
  kb_id            TEXT REFERENCES knowledge_bases(id) ON DELETE SET NULL,
  auto_add_uploads INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at       INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_projects_user ON projects(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name_unique ON projects(user_id, lower(trim(name)));

CREATE TABLE IF NOT EXISTS conversations (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project_id      TEXT REFERENCES projects(id) ON DELETE SET NULL,
  title           TEXT NOT NULL DEFAULT '新对话',
  provider        TEXT NOT NULL DEFAULT '',
  model_id        TEXT NOT NULL DEFAULT '',
  kb_ids          TEXT NOT NULL DEFAULT '[]',
  rag_mode        TEXT NOT NULL DEFAULT 'auto',
  summary_blocks  TEXT NOT NULL DEFAULT '[]',
  active_leaf_id  TEXT,
  provider_state  TEXT NOT NULL DEFAULT '{}',
  pinned          INTEGER NOT NULL DEFAULT 0,
  archived        INTEGER NOT NULL DEFAULT 0,
  starred         INTEGER NOT NULL DEFAULT 0,
  -- Inline-thread linkage (§ text-selection sub-conversations). Non-empty
  -- inline_source_conv marks this row as a sub-conversation hidden from the list.
  inline_source_conv TEXT NOT NULL DEFAULT '',
  inline_parent_id   TEXT NOT NULL DEFAULT '',
  inline_quote       TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_conv_user ON conversations(user_id);
CREATE INDEX IF NOT EXISTS idx_conv_project ON conversations(project_id);
CREATE INDEX IF NOT EXISTS idx_conv_user_updated ON conversations(user_id, archived, pinned DESC, updated_at DESC);

CREATE TABLE IF NOT EXISTS messages (
  id                 TEXT PRIMARY KEY,
  conversation_id    TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  parent_id          TEXT REFERENCES messages(id) ON DELETE CASCADE,
  role               TEXT NOT NULL,
  provider           TEXT NOT NULL DEFAULT '',
  model_id           TEXT NOT NULL DEFAULT '',
  model_label        TEXT NOT NULL DEFAULT '',
  blocks             TEXT NOT NULL DEFAULT '[]',
  raw                TEXT,
  stop_reason        TEXT,
  attachments        TEXT NOT NULL DEFAULT '[]',
  citations          TEXT NOT NULL DEFAULT '[]',
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  cost               REAL NOT NULL DEFAULT 0,
  currency           TEXT NOT NULL DEFAULT 'USD',
  credits            REAL NOT NULL DEFAULT 0,           -- credits charged for this turn (0 = free / credits disabled)
  status             TEXT NOT NULL DEFAULT 'complete', -- complete | streaming | error
  error              TEXT NOT NULL DEFAULT '',
  gen_ms             INTEGER NOT NULL DEFAULT 0,        -- wall-clock generation time (ms)
  -- Plain visible text (the `text` blocks only) projected at write time, so
  -- content search scans a small column instead of LOWER()-ing the whole blocks
  -- JSON (which also holds large thinking/tool text). Excludes reasoning/tool/
  -- image data on purpose.
  search_text        TEXT NOT NULL DEFAULT '',
  -- §verify: secondary auditor (Verify mode) result for this assistant turn —
  -- JSON {verdict,findings:[{severity,quote,issue}],...}. '' = never audited.
  verify             TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(parent_id);
CREATE INDEX IF NOT EXISTS idx_messages_conv_created ON messages(conversation_id, created_at);

-- Public read-only conversation shares. id is the public token used in the
-- /share/:id link. snapshot is a frozen, cost-stripped JSON copy of the active
-- message path at share time, so revoking (deleting the row) fully cuts access
-- and later private messages never leak. At most one live share per conversation.
CREATE TABLE IF NOT EXISTS conversation_shares (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title           TEXT NOT NULL DEFAULT '',
  snapshot        TEXT NOT NULL DEFAULT '[]',
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_conv_shares_conv ON conversation_shares(conversation_id);
CREATE INDEX IF NOT EXISTS idx_conv_shares_user ON conversation_shares(user_id);

CREATE TABLE IF NOT EXISTS files (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
  filename        TEXT NOT NULL,
  mime_type       TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes      INTEGER NOT NULL DEFAULT 0,
  storage_path    TEXT NOT NULL,
  kind            TEXT NOT NULL DEFAULT 'other',
  draft           INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_files_user ON files(user_id);
CREATE INDEX IF NOT EXISTS idx_files_conversation_id ON files(conversation_id);

CREATE TABLE IF NOT EXISTS documents (
  id              TEXT PRIMARY KEY,
  kb_id           TEXT REFERENCES knowledge_bases(id) ON DELETE CASCADE,
  conversation_id TEXT REFERENCES conversations(id) ON DELETE CASCADE,
  filename        TEXT NOT NULL,
  mime_type       TEXT NOT NULL,
  size_bytes      INTEGER NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending', -- pending | parsing | embedding | ready | failed
  error           TEXT NOT NULL DEFAULT '',
  chunk_count     INTEGER NOT NULL DEFAULT 0,
  storage_path    TEXT NOT NULL DEFAULT '',
  ingest_updated_at INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_docs_kb ON documents(kb_id);
CREATE INDEX IF NOT EXISTS idx_docs_conv ON documents(conversation_id);

CREATE TABLE IF NOT EXISTS chunks (
  id              TEXT PRIMARY KEY,
  document_id     TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  kb_id           TEXT,
  conversation_id TEXT,
  seq             INTEGER NOT NULL,
  parent_id       TEXT,
  chunk_type      TEXT NOT NULL DEFAULT 'text',        -- text | parent | table | image_caption
  content         TEXT NOT NULL,
  image_ref       TEXT,                                -- original image ref for image_caption chunks
  meta            TEXT NOT NULL DEFAULT '{}',
  embedding_model TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks(document_id);
CREATE INDEX IF NOT EXISTS idx_chunks_kb ON chunks(kb_id);
CREATE INDEX IF NOT EXISTS idx_chunks_conv ON chunks(conversation_id);

CREATE TABLE IF NOT EXISTS memories (
  id                 TEXT PRIMARY KEY,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  memory_text        TEXT NOT NULL,
  memory_type        TEXT NOT NULL DEFAULT '',
  slot               TEXT NOT NULL DEFAULT '',
  value              TEXT NOT NULL DEFAULT '',
  status             TEXT NOT NULL DEFAULT 'ACTIVE',
  confidence         REAL NOT NULL DEFAULT 0.8,
  source_message_ids TEXT NOT NULL DEFAULT '[]',
  supersedes         TEXT NOT NULL DEFAULT '[]',
  superseded_by      TEXT NOT NULL DEFAULT '[]',
  affected_domains   TEXT NOT NULL DEFAULT '[]',
  reason             TEXT NOT NULL DEFAULT '',
  valid_from         INTEGER,
  valid_until        INTEGER,
  created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at         INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_memories_user_status ON memories(user_id, status);
CREATE INDEX IF NOT EXISTS idx_memories_user_slot ON memories(user_id, slot);

CREATE TABLE IF NOT EXISTS usage_logs (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id    TEXT,
  message_id         TEXT,
  model_id           TEXT NOT NULL,
  purpose            TEXT NOT NULL,            -- chat | task | image | embedding
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  output_tokens      INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  images_count       INTEGER NOT NULL DEFAULT 0,
  cost               REAL NOT NULL DEFAULT 0,
  currency           TEXT NOT NULL DEFAULT 'USD',
  credits            REAL NOT NULL DEFAULT 0,   -- credits charged for this row (0 = free / unconverted)
  channel_id         TEXT NOT NULL DEFAULT '',   -- channel that served the request (§fallback channel)
  fallback           INTEGER NOT NULL DEFAULT 0, -- 1 = served via the model's fallback channel
  status             TEXT NOT NULL DEFAULT 'ok', -- ok | error (error requests are logged too, §usage errors)
  error              TEXT NOT NULL DEFAULT '',   -- upstream failure detail for status='error' rows (admin-only)
  request_method     TEXT NOT NULL DEFAULT '',   -- sanitized upstream request diagnostics for status='error'
  request_url        TEXT NOT NULL DEFAULT '',
  request_headers    TEXT NOT NULL DEFAULT '',
  request_body       TEXT NOT NULL DEFAULT '',
  ttft_fallback_model TEXT NOT NULL DEFAULT '', -- non-empty = TTFT timeout model-fallback served this row (§4.6-C); value is the fallback model's display name
  created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_usage_user_time ON usage_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_model_time ON usage_logs(model_id, created_at);
-- Per-model-per-user windowed quota aggregate (authoritative fallback when the
-- cache counter is cold).
CREATE INDEX IF NOT EXISTS idx_usage_user_model_time ON usage_logs(user_id, model_id, created_at);

CREATE TABLE IF NOT EXISTS artifacts (
  id           TEXT PRIMARY KEY,
  message_id   TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  filename     TEXT NOT NULL,
  storage_path TEXT NOT NULL,
  mime_type    TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes   INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_artifacts_message ON artifacts(message_id);

CREATE TABLE IF NOT EXISTS refresh_tokens (
  jti        TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  -- Device/network context for the "active sessions" view. ip/location are
  -- best-effort (location is derived from reverse-proxy geo headers, if any).
  user_agent TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT '',
  location   TEXT NOT NULL DEFAULT '',
  last_seen  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id);

-- OAuth / social login providers, configured by the admin. Built-in kinds
-- (google | github | apple) fill their endpoints from code defaults; kind=oidc
-- is a generic OAuth2/OIDC provider whose endpoints come from the row. The
-- client_secret is plaintext like channel api_key; for Apple it holds the
-- AuthKey .p8 private key used to mint the client-secret JWT.
CREATE TABLE IF NOT EXISTS oauth_providers (
  id            TEXT PRIMARY KEY,                -- "oa_<hex>"
  kind          TEXT NOT NULL,                   -- google | github | apple | oidc
  name          TEXT NOT NULL,                   -- label shown on the login button
  icon          TEXT NOT NULL DEFAULT '',        -- emoji / uploaded URL (custom providers)
  client_id     TEXT NOT NULL DEFAULT '',
  client_secret TEXT NOT NULL DEFAULT '',        -- apple: the .p8 private key
  auth_url      TEXT NOT NULL DEFAULT '',        -- oidc only (built-ins use defaults)
  token_url     TEXT NOT NULL DEFAULT '',
  userinfo_url  TEXT NOT NULL DEFAULT '',
  scopes        TEXT NOT NULL DEFAULT '',        -- space-separated override
  team_id       TEXT NOT NULL DEFAULT '',        -- apple
  key_id        TEXT NOT NULL DEFAULT '',        -- apple
  enabled       INTEGER NOT NULL DEFAULT 1,
  sort_order    INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_providers_name_unique ON oauth_providers(lower(trim(name)));

-- Links a provider identity (provider row + stable subject) to a local user.
-- Keyed on (provider_id, subject) so the link survives email changes — re-login
-- matches on the provider's immutable subject, never on the email.
-- Storage paths awaiting physical deletion (§8.1-A async user delete). Rows
-- are written BEFORE any destructive SQL delete and removed one by one as the
-- bytes are actually unlinked, so a crash mid-purge never orphans disk or
-- object-storage bytes: startup sweeps whatever is left.
CREATE TABLE IF NOT EXISTS pending_storage_cleanup (
  path       TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE TABLE IF NOT EXISTS oauth_identities (
  provider_id TEXT NOT NULL,
  subject     TEXT NOT NULL,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email       TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  PRIMARY KEY (provider_id, subject)
);
CREATE INDEX IF NOT EXISTS idx_oauth_identities_user ON oauth_identities(user_id);

-- §4.20 Image Generation Studio. Admin-managed styles carry a hidden prompt
-- composed server-side and NEVER returned to non-admin users.
CREATE TABLE IF NOT EXISTS image_styles (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  example_image_url TEXT NOT NULL DEFAULT '',
  hidden_prompt     TEXT NOT NULL DEFAULT '',
  enabled           INTEGER NOT NULL DEFAULT 1,
  sort_order        INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at        INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_image_styles_sort ON image_styles(sort_order);
CREATE UNIQUE INDEX IF NOT EXISTS idx_image_styles_name_unique ON image_styles(lower(trim(name)));

-- 工作空间(§workspaces):完全独立的协作空间。个人数据 workspace_id=''(空串);
-- 空间内的 conversations/projects/knowledge_bases 记 workspace_id,所有成员共享可见。
-- invite_token 是 192-bit 能力令牌(仅通过邀请链接加入);轮换即作废旧链接。
CREATE TABLE IF NOT EXISTS workspaces (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  owner_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  invite_token TEXT NOT NULL UNIQUE,
  created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_workspaces_owner ON workspaces(owner_id);

CREATE TABLE IF NOT EXISTS workspace_members (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role         TEXT NOT NULL DEFAULT 'member',
  joined_at    INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_ws_members_user ON workspace_members(user_id);
