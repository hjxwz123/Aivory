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
  created_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);

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

CREATE TABLE IF NOT EXISTS models (
  id                TEXT PRIMARY KEY,
  channel_id        TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  kind              TEXT NOT NULL DEFAULT 'chat',
  request_id        TEXT NOT NULL,
  label             TEXT NOT NULL,
  description       TEXT NOT NULL DEFAULT '',
  icon              TEXT NOT NULL DEFAULT '',
  enabled           INTEGER NOT NULL DEFAULT 1,
  sort_order        INTEGER NOT NULL DEFAULT 0,
  tool_mode         TEXT NOT NULL DEFAULT 'native',
  vision            INTEGER NOT NULL DEFAULT 1,
  stream            INTEGER NOT NULL DEFAULT 1,
  system_prompt     TEXT NOT NULL DEFAULT '',
  param_controls    TEXT NOT NULL DEFAULT '[]',
  price_input       DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_output      DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_cache_read  DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_cache_write DOUBLE PRECISION NOT NULL DEFAULT 0,
  price_per_image   DOUBLE PRECISION NOT NULL DEFAULT 0,
  currency          TEXT NOT NULL DEFAULT 'USD',
  dim               INTEGER NOT NULL DEFAULT 0,
  updated_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);

CREATE INDEX IF NOT EXISTS idx_models_channel ON models(channel_id);
CREATE INDEX IF NOT EXISTS idx_models_kind ON models(kind, enabled);

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
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_conv_user ON conversations(user_id);
CREATE INDEX IF NOT EXISTS idx_conv_project ON conversations(project_id);

CREATE TABLE IF NOT EXISTS messages (
  id                 TEXT PRIMARY KEY,
  conversation_id    TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  parent_id          TEXT REFERENCES messages(id) ON DELETE CASCADE,
  role               TEXT NOT NULL,
  provider           TEXT NOT NULL DEFAULT '',
  model_id           TEXT NOT NULL DEFAULT '',
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
  status             TEXT NOT NULL DEFAULT 'complete',
  error              TEXT NOT NULL DEFAULT '',
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(parent_id);

CREATE TABLE IF NOT EXISTS files (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conversation_id TEXT REFERENCES conversations(id) ON DELETE SET NULL,
  filename        TEXT NOT NULL,
  mime_type       TEXT NOT NULL DEFAULT 'application/octet-stream',
  size_bytes      BIGINT NOT NULL DEFAULT 0,
  storage_path    TEXT NOT NULL,
  provider_refs   TEXT NOT NULL DEFAULT '{}',
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
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_usage_user_time ON usage_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_model_time ON usage_logs(model_id, created_at);

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
  created_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
