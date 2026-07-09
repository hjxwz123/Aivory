-- Aurelia schema — PostgreSQL dialect (production).
--
-- Mirrors schema.sql (SQLite) table-for-table and column-for-column so the Go
-- store layer runs unchanged. Differences from the SQLite file:
--   * strftime('%s','now')        -> (extract(epoch from now())::bigint)
--   * INTEGER timestamps/bytes    -> BIGINT (avoid 2038 + large token sums)
--   * AUTOINCREMENT               -> BIGSERIAL
--   * BLOB                        -> BYTEA
--   * REAL                        -> DOUBLE PRECISION
-- Boolean-ish flag columns stay INTEGER 0/1 on purpose: the store layer reads
-- them through `int` locals (`x == 1`) and writes them via boolInt()/literals,
-- never binding a Go bool, so INTEGER is the portable choice.

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  group_id      TEXT NOT NULL DEFAULT 'ug_free',
  totp_secret   TEXT NOT NULL DEFAULT '',
  totp_enabled  INTEGER NOT NULL DEFAULT 0,
  password_set  INTEGER NOT NULL DEFAULT 1,
  password_changed_at BIGINT NOT NULL DEFAULT 0,
  last_seen_at  BIGINT NOT NULL DEFAULT 0,
  credits_permanent REAL NOT NULL DEFAULT 0,
  sort_order    INTEGER NOT NULL DEFAULT 0,
  created_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);

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
  credit_allowance      REAL NOT NULL DEFAULT 0,
  credit_period_seconds INTEGER NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at  BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_groups_name_unique ON user_groups(lower(trim(name)));

-- NOTE: model_group_quotas REFERENCES models(id) — it is created AFTER the models
-- table below. Postgres rejects a forward FK reference in a single-batch Exec, so
-- this table MUST stay after `models`; do not move it earlier.

CREATE TABLE IF NOT EXISTS redeem_codes (
  id            TEXT PRIMARY KEY,
  code          TEXT UNIQUE NOT NULL,
  group_id      TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  duration_days INTEGER NOT NULL DEFAULT 30,
  max_uses      INTEGER NOT NULL DEFAULT 1,
  used_count    INTEGER NOT NULL DEFAULT 0,
  expires_at    BIGINT NOT NULL DEFAULT 0,
  enabled       INTEGER NOT NULL DEFAULT 1,
  note          TEXT NOT NULL DEFAULT '',
  batch_name    TEXT NOT NULL DEFAULT '',
  created_by    TEXT NOT NULL DEFAULT '',
  created_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_redeem_codes_code ON redeem_codes(code);
CREATE INDEX IF NOT EXISTS idx_redeem_codes_batch ON redeem_codes(batch_name);

CREATE TABLE IF NOT EXISTS redeem_redemptions (
  id              TEXT PRIMARY KEY,
  code_id         TEXT NOT NULL REFERENCES redeem_codes(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  group_id        TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  previous_group_id TEXT NOT NULL DEFAULT '',
  granted_at      BIGINT NOT NULL,
  expires_at      BIGINT NOT NULL,
  UNIQUE(code_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_redemptions_user ON redeem_redemptions(user_id);
CREATE INDEX IF NOT EXISTS idx_redemptions_code ON redeem_redemptions(code_id);

CREATE TABLE IF NOT EXISTS channels (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  type        TEXT NOT NULL,
  api_format  TEXT NOT NULL DEFAULT '',
  base_url    TEXT NOT NULL DEFAULT '',
  api_key     TEXT NOT NULL DEFAULT '',
  enabled     INTEGER NOT NULL DEFAULT 1,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  updated_at  BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_channels_name_unique ON channels(lower(trim(name)));

CREATE TABLE IF NOT EXISTS models (
  id                TEXT PRIMARY KEY,
  channel_id        TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  kind              TEXT NOT NULL DEFAULT 'chat',
  request_id        TEXT NOT NULL,
  label             TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',
  icon              TEXT NOT NULL DEFAULT '',
  fallback_channel_id TEXT NOT NULL DEFAULT '',      -- retried when a primary request fails ('' = none, §fallback channel)
  enabled           INTEGER NOT NULL DEFAULT 1,
  sort_order        INTEGER NOT NULL DEFAULT 0,
  tool_mode         TEXT NOT NULL DEFAULT 'native',
  vision            INTEGER NOT NULL DEFAULT 1,
  stream            INTEGER NOT NULL DEFAULT 1,
  research_enabled  INTEGER NOT NULL DEFAULT 1, -- expose Deep Research for this chat model
  system_prompt     TEXT NOT NULL DEFAULT '',
  param_controls    TEXT NOT NULL DEFAULT '[]',
  official_tools    TEXT NOT NULL DEFAULT '[]', -- OpenAI Responses hosted tools; [] = use system tools (§2.3-B)
  tags              TEXT NOT NULL DEFAULT '[]', -- model_tags ids for the picker filter (§ model tags)
  moderation_enabled INTEGER NOT NULL DEFAULT 0,      -- screen prompts before generation (§ moderation)
  moderation_mode   TEXT NOT NULL DEFAULT 'keyword',  -- keyword | model
  price_input       DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_output      DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_cache_read  DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_cache_write DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_per_image   DOUBLE PRECISION NOT NULL DEFAULT 0,
  currency          TEXT NOT NULL DEFAULT 'USD',
  dim               INTEGER NOT NULL DEFAULT 0,
  image_timeout_sec INTEGER NOT NULL DEFAULT 0,
  updated_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);

CREATE INDEX IF NOT EXISTS idx_models_channel ON models(channel_id);
CREATE INDEX IF NOT EXISTS idx_models_kind ON models(kind, enabled);
CREATE UNIQUE INDEX IF NOT EXISTS idx_models_channel_request_unique ON models(channel_id, lower(trim(request_id)));

-- Per-(model, group) free quota. Declared AFTER models because it has a FK to
-- models(id) and Postgres resolves FK targets eagerly within the schema batch
-- (a forward reference aborts the whole migration). See the note above redeem_codes.
CREATE TABLE IF NOT EXISTS model_group_quotas (
  model_id       TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  group_id       TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  period_seconds INTEGER NOT NULL DEFAULT 604800,
  limit_type     TEXT NOT NULL DEFAULT 'count',
  limit_value    REAL NOT NULL DEFAULT 0,
  PRIMARY KEY (model_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_mgq_group ON model_group_quotas(group_id);

-- Model tags (§ model tags). Admin-managed labels; each model stores the tag ids
-- it carries in models.tags (a JSON array), and the picker filters by them.
CREATE TABLE IF NOT EXISTS model_tags (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  updated_at   BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  project_id         TEXT,
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  created_at       BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at       BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  inline_source_conv TEXT NOT NULL DEFAULT '',
  inline_parent_id   TEXT NOT NULL DEFAULT '',
  inline_quote       TEXT NOT NULL DEFAULT '',
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  input_tokens       BIGINT NOT NULL DEFAULT 0,
  output_tokens      BIGINT NOT NULL DEFAULT 0,
  cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
  cache_write_tokens BIGINT NOT NULL DEFAULT 0,
  cost               DOUBLE PRECISION NOT NULL DEFAULT 0,
  currency           TEXT NOT NULL DEFAULT 'USD',
  credits            DOUBLE PRECISION NOT NULL DEFAULT 0,
  status             TEXT NOT NULL DEFAULT 'complete',
  error              TEXT NOT NULL DEFAULT '',
  gen_ms             BIGINT NOT NULL DEFAULT 0,
  search_text        TEXT NOT NULL DEFAULT '',
  -- §verify: secondary auditor (Verify mode) result for this assistant turn —
  -- JSON {verdict,findings:[{severity,quote,issue}],...}. '' = never audited.
  verify             TEXT NOT NULL DEFAULT '',
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(parent_id);
CREATE INDEX IF NOT EXISTS idx_messages_conv_created ON messages(conversation_id, created_at);

-- Public read-only conversation shares (cost-stripped snapshot; revoke = delete).
CREATE TABLE IF NOT EXISTS conversation_shares (
  id              TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title           TEXT NOT NULL DEFAULT '',
  snapshot        TEXT NOT NULL DEFAULT '[]',
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_conv_shares_conv ON conversation_shares(conversation_id);
CREATE INDEX IF NOT EXISTS idx_conv_shares_user ON conversation_shares(user_id);

CREATE TABLE IF NOT EXISTS files (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
  filename        TEXT NOT NULL,
  mime_type       TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes      BIGINT NOT NULL DEFAULT 0,
  storage_path    TEXT NOT NULL,
  kind            TEXT NOT NULL DEFAULT 'other',
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_files_user ON files(user_id);

CREATE TABLE IF NOT EXISTS documents (
  id              TEXT PRIMARY KEY,
  kb_id           TEXT REFERENCES knowledge_bases(id) ON DELETE CASCADE,
  conversation_id TEXT REFERENCES conversations(id) ON DELETE CASCADE,
  filename        TEXT NOT NULL,
  mime_type       TEXT NOT NULL,
  size_bytes      BIGINT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending',
  error           TEXT NOT NULL DEFAULT '',
  chunk_count     INTEGER NOT NULL DEFAULT 0,
  storage_path    TEXT NOT NULL DEFAULT '',
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  chunk_type      TEXT NOT NULL DEFAULT 'text',
  content         TEXT NOT NULL,
  image_ref       TEXT,
  meta            TEXT NOT NULL DEFAULT '{}',
  embedding       BYTEA,
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
  confidence         DOUBLE PRECISION NOT NULL DEFAULT 0.8,
  source_message_ids TEXT NOT NULL DEFAULT '[]',
  supersedes         TEXT NOT NULL DEFAULT '[]',
  superseded_by      TEXT NOT NULL DEFAULT '[]',
  affected_domains   TEXT NOT NULL DEFAULT '[]',
  reason             TEXT NOT NULL DEFAULT '',
  valid_from         BIGINT,
  valid_until        BIGINT,
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_memories_user_status ON memories(user_id, status);
CREATE INDEX IF NOT EXISTS idx_memories_user_slot ON memories(user_id, slot);

CREATE TABLE IF NOT EXISTS usage_logs (
  id                 BIGSERIAL PRIMARY KEY,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id    TEXT,
  message_id         TEXT,
  model_id           TEXT NOT NULL,
  purpose            TEXT NOT NULL,
  input_tokens       BIGINT NOT NULL DEFAULT 0,
  output_tokens      BIGINT NOT NULL DEFAULT 0,
  cache_read_tokens  BIGINT NOT NULL DEFAULT 0,
  cache_write_tokens BIGINT NOT NULL DEFAULT 0,
  images_count       INTEGER NOT NULL DEFAULT 0,
  cost               DOUBLE PRECISION NOT NULL DEFAULT 0,
  currency           TEXT NOT NULL DEFAULT 'USD',
  credits            DOUBLE PRECISION NOT NULL DEFAULT 0,
  channel_id         TEXT NOT NULL DEFAULT '',   -- channel that served the request (§fallback channel)
  fallback           INTEGER NOT NULL DEFAULT 0, -- 1 = served via the model's fallback channel
  status             TEXT NOT NULL DEFAULT 'ok', -- ok | error (error requests are logged too, §usage errors)
  error              TEXT NOT NULL DEFAULT '',   -- upstream failure detail for status='error' rows (admin-only)
  request_method     TEXT NOT NULL DEFAULT '',   -- sanitized upstream request diagnostics for status='error'
  request_url        TEXT NOT NULL DEFAULT '',
  request_headers    TEXT NOT NULL DEFAULT '',
  request_body       TEXT NOT NULL DEFAULT '',
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_usage_user_time ON usage_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_model_time ON usage_logs(model_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_user_model_time ON usage_logs(user_id, model_id, created_at);

CREATE TABLE IF NOT EXISTS artifacts (
  id           TEXT PRIMARY KEY,
  message_id   TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  filename     TEXT NOT NULL,
  storage_path TEXT NOT NULL,
  mime_type    TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes   BIGINT NOT NULL DEFAULT 0,
  created_at   BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_artifacts_message ON artifacts(message_id);

CREATE TABLE IF NOT EXISTS refresh_tokens (
  jti        TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at BIGINT NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  -- Device/network context for the "active sessions" view (see schema.sql).
  user_agent TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT '',
  location   TEXT NOT NULL DEFAULT '',
  last_seen  BIGINT NOT NULL DEFAULT 0
);

-- OAuth / social login providers (see schema.sql for the full rationale).
CREATE TABLE IF NOT EXISTS oauth_providers (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL,
  name          TEXT NOT NULL,
  icon          TEXT NOT NULL DEFAULT '',
  client_id     TEXT NOT NULL DEFAULT '',
  client_secret TEXT NOT NULL DEFAULT '',
  auth_url      TEXT NOT NULL DEFAULT '',
  token_url     TEXT NOT NULL DEFAULT '',
  userinfo_url  TEXT NOT NULL DEFAULT '',
  scopes        TEXT NOT NULL DEFAULT '',
  team_id       TEXT NOT NULL DEFAULT '',
  key_id        TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  sort_order    INTEGER NOT NULL DEFAULT 0,
  updated_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_providers_name_unique ON oauth_providers(lower(trim(name)));

CREATE TABLE IF NOT EXISTS oauth_identities (
  provider_id TEXT NOT NULL,
  subject     TEXT NOT NULL,
  user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email       TEXT NOT NULL DEFAULT '',
  created_at  BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  PRIMARY KEY (provider_id, subject)
);
CREATE INDEX IF NOT EXISTS idx_oauth_identities_user ON oauth_identities(user_id);

-- §4.20 Image Generation Studio (Postgres dialect — see schema.sql for notes).
CREATE TABLE IF NOT EXISTS image_styles (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  example_image_url TEXT NOT NULL DEFAULT '',
  hidden_prompt     TEXT NOT NULL DEFAULT '',
  enabled           INTEGER NOT NULL DEFAULT 1,
  sort_order        INTEGER NOT NULL DEFAULT 0,
  created_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
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
  created_at   BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_workspaces_owner ON workspaces(owner_id);

CREATE TABLE IF NOT EXISTS workspace_members (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role         TEXT NOT NULL DEFAULT 'member',
  joined_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_ws_members_user ON workspace_members(user_id);
