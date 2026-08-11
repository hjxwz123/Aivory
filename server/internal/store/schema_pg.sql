-- Aivory schema — PostgreSQL dialect (production).
--
-- Mirrors schema.sql (SQLite) table-for-table and column-for-column so the Go
-- store layer runs unchanged. Differences from the SQLite file:
--   * strftime('%s','now')        -> (extract(epoch from now())::bigint)
--   * INTEGER timestamps/bytes    -> BIGINT (avoid 2038 + large token sums)
--   * AUTOINCREMENT               -> BIGSERIAL
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
  credits_permanent_micros BIGINT NOT NULL DEFAULT 0,
  credit_cycle_anchor BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  quota_cycle_anchor BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  sort_order    INTEGER NOT NULL DEFAULT 0,
  created_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);

CREATE TABLE IF NOT EXISTS credit_adjustment_notifications (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  direction     TEXT NOT NULL CHECK(direction IN ('add','remove')),
  amount_micros BIGINT NOT NULL CHECK(amount_micros > 0),
  reason        TEXT NOT NULL DEFAULT '',
  created_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  claimed_at    BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_credit_adjustment_notifications_user_pending
  ON credit_adjustment_notifications(user_id, claimed_at, created_at, id);

-- Immutable successful-login audit trail (see schema.sql).
CREATE TABLE IF NOT EXISTS login_histories (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  login_at   BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  ip         TEXT NOT NULL DEFAULT '',
  location   TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  method     TEXT NOT NULL DEFAULT 'password'
);
CREATE INDEX IF NOT EXISTS idx_login_histories_user_time
  ON login_histories(user_id, login_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS user_groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  features    TEXT NOT NULL DEFAULT '[]',
  monthly_price_amount_minor BIGINT NOT NULL DEFAULT 0,
  yearly_price_amount_minor  BIGINT NOT NULL DEFAULT 0,
  is_default  INTEGER NOT NULL DEFAULT 0,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  max_projects INTEGER NOT NULL DEFAULT 0,
  max_kbs      INTEGER NOT NULL DEFAULT 0,
  -- Storage quota for non-image uploads, MB (0 = unlimited, § user files page).
  max_storage_mb INTEGER NOT NULL DEFAULT 0,
  credit_allowance      REAL NOT NULL DEFAULT 0,
  credit_allowance_micros BIGINT NOT NULL DEFAULT 0,
  credit_period_seconds INTEGER NOT NULL DEFAULT 0,
  is_purchasable INTEGER NOT NULL DEFAULT 1,
  created_at  BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at  BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_groups_name_unique ON user_groups(lower(trim(name)));

CREATE TABLE IF NOT EXISTS credit_ledger (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  group_id     TEXT NOT NULL,
  cycle_anchor BIGINT NOT NULL DEFAULT 0,
  cycle_start  BIGINT NOT NULL DEFAULT 0,
  kind         TEXT NOT NULL,
  amount       DOUBLE PRECISION NOT NULL,
  amount_micros BIGINT NOT NULL DEFAULT 0,
  source_type  TEXT NOT NULL DEFAULT '',
  source_id    TEXT NOT NULL DEFAULT '',
  created_at   BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_timed
  ON credit_ledger(user_id, group_id, cycle_anchor, cycle_start, kind);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_user_time
  ON credit_ledger(user_id, created_at);

CREATE TABLE IF NOT EXISTS credit_reservations (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  amount_micros   BIGINT NOT NULL CHECK(amount_micros > 0),
  actual_micros   BIGINT NOT NULL DEFAULT 0 CHECK(actual_micros >= 0),
  source_type     TEXT NOT NULL DEFAULT '',
  source_id       TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'reserved' CHECK(status IN ('reserved','settling','settled','released')),
  expires_at      BIGINT NOT NULL CHECK(expires_at > 0),
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  UNIQUE(source_type, source_id)
);
CREATE INDEX IF NOT EXISTS idx_credit_reservations_user_status
  ON credit_reservations(user_id, status, expires_at);

CREATE TABLE IF NOT EXISTS quota_ledger (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  scope_type      TEXT NOT NULL CHECK(length(trim(scope_type)) > 0),
  model_id        TEXT NOT NULL DEFAULT '',
  group_id        TEXT NOT NULL DEFAULT '',
  cycle_anchor    BIGINT NOT NULL DEFAULT 0,
  window_start    BIGINT NOT NULL CHECK(window_start > 0),
  limit_type      TEXT NOT NULL CHECK(limit_type IN ('count','cost')),
  reserved_micros BIGINT NOT NULL DEFAULT 0 CHECK(reserved_micros >= 0),
  actual_micros   BIGINT NOT NULL DEFAULT 0 CHECK(actual_micros >= 0),
  status          TEXT NOT NULL DEFAULT 'reserved' CHECK(status IN ('reserved','finalized','released')),
  expires_at      BIGINT NOT NULL CHECK(expires_at > window_start),
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_quota_ledger_scope
  ON quota_ledger(user_id, scope_type, model_id, group_id, cycle_anchor, window_start, status);

CREATE TABLE IF NOT EXISTS billing_usage (
  id              TEXT PRIMARY KEY,
  user_id         TEXT REFERENCES users(id) ON DELETE SET NULL,
  conversation_id TEXT NOT NULL DEFAULT '',
  message_id      TEXT NOT NULL DEFAULT '',
  model_id        TEXT NOT NULL DEFAULT '',
  purpose         TEXT NOT NULL DEFAULT '',
  cost_micros     BIGINT NOT NULL DEFAULT 0 CHECK(cost_micros >= 0),
  images_count    INTEGER NOT NULL DEFAULT 0 CHECK(images_count >= 0),
  input_tokens    INTEGER NOT NULL DEFAULT 0 CHECK(input_tokens >= 0),
  output_tokens   INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens >= 0),
  currency        TEXT NOT NULL DEFAULT 'USD' CHECK(length(trim(currency)) > 0),
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_billing_usage_message
  ON billing_usage(message_id, purpose, created_at);
CREATE INDEX IF NOT EXISTS idx_billing_usage_user_time
  ON billing_usage(user_id, created_at);

CREATE TABLE IF NOT EXISTS credit_packages (
  id                 TEXT PRIMARY KEY,
  name               TEXT NOT NULL,
  description        TEXT NOT NULL DEFAULT '',
  credits            REAL NOT NULL,
  price_amount_minor BIGINT NOT NULL DEFAULT 0,
  enabled            INTEGER NOT NULL DEFAULT 1,
  sort_order         INTEGER NOT NULL DEFAULT 0,
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_credit_packages_order ON credit_packages(sort_order, name);

CREATE TABLE IF NOT EXISTS payment_channels (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  provider   TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT 'live',
  config     TEXT NOT NULL DEFAULT '{}',
  enabled    INTEGER NOT NULL DEFAULT 1,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_channels_name_unique ON payment_channels(lower(trim(name)));
CREATE INDEX IF NOT EXISTS idx_payment_channels_order ON payment_channels(sort_order, name);

CREATE TABLE IF NOT EXISTS payment_methods (
  id         TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL REFERENCES payment_channels(id) ON DELETE RESTRICT,
  name       TEXT NOT NULL,
  type       TEXT NOT NULL,
  icon       TEXT NOT NULL DEFAULT '',
  config     TEXT NOT NULL DEFAULT '{}',
  enabled    INTEGER NOT NULL DEFAULT 1,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_methods_channel_name_unique ON payment_methods(channel_id, lower(trim(name)));
CREATE INDEX IF NOT EXISTS idx_payment_methods_order ON payment_methods(channel_id, sort_order, name);

CREATE TABLE IF NOT EXISTS payment_orders (
  id                TEXT PRIMARY KEY,
  user_id           TEXT REFERENCES users(id) ON DELETE SET NULL,
  user_email        TEXT NOT NULL,
  provider          TEXT NOT NULL,
  environment       TEXT NOT NULL DEFAULT 'live',
  channel_id        TEXT NOT NULL,
  channel_name      TEXT NOT NULL,
  method_id         TEXT NOT NULL,
  method_name       TEXT NOT NULL,
  method_type       TEXT NOT NULL,
  method_config     TEXT NOT NULL DEFAULT '{}',
  product_type      TEXT NOT NULL,
  product_id        TEXT NOT NULL,
  product_name      TEXT NOT NULL,
  amount_minor      BIGINT NOT NULL,
  paid_amount_minor BIGINT NOT NULL DEFAULT 0,
  tax_amount_minor  BIGINT NOT NULL DEFAULT 0,
  currency          TEXT NOT NULL,
  provider_amount_minor BIGINT NOT NULL DEFAULT 0,
  provider_currency TEXT NOT NULL DEFAULT '',
  conversion_rate   TEXT NOT NULL DEFAULT '',
  credits           DOUBLE PRECISION NOT NULL DEFAULT 0,
  user_group_id     TEXT NOT NULL DEFAULT '',
  billing_cycle     TEXT NOT NULL DEFAULT '',
  provider_order_id TEXT NOT NULL DEFAULT '',
  provider_payment_id TEXT NOT NULL DEFAULT '',
  checkout_session_id TEXT NOT NULL DEFAULT '',
  checkout_url      TEXT NOT NULL DEFAULT '',
  checkout_expires_at BIGINT NOT NULL DEFAULT 0,
  last_reconciled_at BIGINT NOT NULL DEFAULT 0,
  reconcile_error   TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'pending',
  failure_code      TEXT NOT NULL DEFAULT '',
  failure_message   TEXT NOT NULL DEFAULT '',
  paid_at           BIGINT NOT NULL DEFAULT 0,
  fulfilled_at      BIGINT NOT NULL DEFAULT 0,
  created_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_payment_orders_user_created ON payment_orders(user_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_payment_orders_channel_status ON payment_orders(channel_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_payment_orders_status_created ON payment_orders(status, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_orders_provider_order_unique
  ON payment_orders(provider, channel_id, provider_order_id) WHERE provider_order_id<>'';

CREATE TABLE IF NOT EXISTS payment_order_attempts (
  merchant_order_id TEXT PRIMARY KEY,
  order_id          TEXT NOT NULL REFERENCES payment_orders(id) ON DELETE CASCADE,
  provider          TEXT NOT NULL,
  channel_id        TEXT NOT NULL,
  provider_order_id TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'issued',
  paid_at           BIGINT NOT NULL DEFAULT 0,
  created_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at        BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_payment_order_attempts_order_created
  ON payment_order_attempts(order_id, created_at, merchant_order_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_order_attempts_provider_order_unique
  ON payment_order_attempts(provider, channel_id, provider_order_id) WHERE provider_order_id<>'';

CREATE TABLE IF NOT EXISTS payment_events (
  id           TEXT PRIMARY KEY,
  provider     TEXT NOT NULL,
  channel_id   TEXT NOT NULL,
  event_id     TEXT NOT NULL,
  order_id     TEXT NOT NULL REFERENCES payment_orders(id) ON DELETE CASCADE,
  event_type   TEXT NOT NULL DEFAULT '',
  created_at   BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  processed_at BIGINT NOT NULL DEFAULT 0,
  UNIQUE(provider, channel_id, event_id)
);
CREATE INDEX IF NOT EXISTS idx_payment_events_order_created ON payment_events(order_id, created_at, id);

-- NOTE: model_group_quotas REFERENCES models(id) — it is created AFTER the models
-- table below. Postgres rejects a forward FK reference in a single-batch Exec, so
-- this table MUST stay after `models`; do not move it earlier.

CREATE TABLE IF NOT EXISTS redeem_codes (
  id            TEXT PRIMARY KEY,
  code          TEXT UNIQUE NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'group',
  group_id      TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  duration_days INTEGER NOT NULL DEFAULT 30 CHECK(duration_days >= 0),
  credits       REAL NOT NULL DEFAULT 0 CHECK(credits >= 0),
  max_uses      INTEGER NOT NULL DEFAULT 1 CHECK(max_uses > 0),
  used_count    INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses),
  expires_at    BIGINT NOT NULL DEFAULT 0 CHECK(expires_at >= 0),
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
  credits         REAL NOT NULL DEFAULT 0,
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
  fast              INTEGER NOT NULL DEFAULT 0, -- §fast-mode: THE fast model (only one; hidden from the advanced picker, Deep Research forced off)
  system_prompt     TEXT NOT NULL DEFAULT '',
  param_controls    TEXT NOT NULL DEFAULT '[]',
  extra_params      TEXT NOT NULL DEFAULT '{}', -- admin-only upstream request defaults; native request fields win
  official_tools    TEXT NOT NULL DEFAULT '[]', -- provider-hosted [{name,icon,request}]; legacy string arrays are migrated
  builtin_tools     TEXT DEFAULT NULL, -- local-tool allowlist; NULL=all (backwards compatible), []=none
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
  period_seconds INTEGER NOT NULL DEFAULT 604800 CHECK(period_seconds > 0),
  limit_type     TEXT NOT NULL DEFAULT 'count' CHECK(limit_type IN ('cost','count')),
  limit_value    REAL NOT NULL DEFAULT 0 CHECK(limit_value >= 0),
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
  display_description TEXT NOT NULL DEFAULT '', -- catalog-only copy; description remains model-facing trigger metadata
  icon         TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL,
  assets       TEXT NOT NULL DEFAULT '[]',
  enabled      INTEGER NOT NULL DEFAULT 1,
  sort_order   INTEGER NOT NULL DEFAULT 0,
  updated_at   BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_name_unique ON skills(lower(trim(name)));

CREATE TABLE IF NOT EXISTS prompts (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  icon        TEXT NOT NULL DEFAULT '',
  content     TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  updated_at  BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_prompts_name_unique ON prompts(lower(trim(name)));

-- Private user Agent Skills are instruction-only. The absence of assets and
-- storage columns is an intentional sandbox security boundary.
CREATE TABLE IF NOT EXISTS user_skills (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name            TEXT NOT NULL,
  description     TEXT NOT NULL,
  icon            TEXT NOT NULL DEFAULT '',
  instructions    TEXT NOT NULL,
  source_skill_id TEXT REFERENCES skills(id) ON DELETE SET NULL,
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_user_skills_user ON user_skills(user_id, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_skills_user_name_unique ON user_skills(user_id, lower(trim(name)));
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_skills_source_unique ON user_skills(user_id, source_skill_id);

CREATE TABLE IF NOT EXISTS user_prompts (
  id               TEXT PRIMARY KEY,
  user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  content          TEXT NOT NULL,
  source_prompt_id TEXT REFERENCES prompts(id) ON DELETE SET NULL,
  created_at       BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at       BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_user_prompts_user ON user_prompts(user_id, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_prompts_user_name_unique ON user_prompts(user_id, lower(trim(name)));
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_prompts_source_unique ON user_prompts(user_id, source_prompt_id);

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
  fast            INTEGER NOT NULL DEFAULT 0, -- §fast-mode: conversation runs in fast mode (model resolved server-side from the admin's fast model; name hidden from the user)
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
  -- Workspace conversations are private to their creator by default. Personal
  -- conversations ignore this flag and remain owner-scoped.
  is_public      INTEGER NOT NULL DEFAULT 0,
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
  fast               INTEGER NOT NULL DEFAULT 0, -- §fast-mode: this turn ran in fast mode; mask model_id/model_label/provider at the user boundary
  blocks             TEXT NOT NULL DEFAULT '[]',
  raw                TEXT,
  stop_reason        TEXT,
  attachments        TEXT NOT NULL DEFAULT '[]',
  selected_user_skill_ids TEXT NOT NULL DEFAULT '[]', -- private skill ids applied to this user turn
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
CREATE INDEX IF NOT EXISTS idx_messages_role_created ON messages(role, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_model_role_created ON messages(model_id, role, created_at);

-- One feedback row per (assistant message, evaluating user). Catalog ids are
-- immutable snapshots here: deleting catalog records must not erase history.
CREATE TABLE IF NOT EXISTS message_feedback (
  id              TEXT PRIMARY KEY,
  message_id      TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  workspace_id    TEXT NOT NULL DEFAULT '',
  model_id        TEXT NOT NULL DEFAULT '',
  channel_id      TEXT NOT NULL DEFAULT '',
  rating          TEXT NOT NULL CHECK(rating IN ('like','dislike')),
  reasons         JSONB NOT NULL DEFAULT '[]'::jsonb,
  comment         TEXT NOT NULL DEFAULT '',
  created_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  updated_at      BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint),
  UNIQUE(message_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_message_feedback_updated ON message_feedback(updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_message_feedback_model_updated ON message_feedback(model_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_feedback_rating_updated ON message_feedback(rating, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_feedback_conversation ON message_feedback(conversation_id);
CREATE INDEX IF NOT EXISTS idx_message_feedback_user_message ON message_feedback(user_id, message_id);

-- User-submitted product issue reports, separate from model-quality ratings.
-- The optional raster screenshot is kept in BYTEA so local and object-storage
-- deployments have identical retention and backup behavior.
CREATE TABLE IF NOT EXISTS user_feedback (
  id                 TEXT PRIMARY KEY,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id         TEXT REFERENCES messages(id) ON DELETE SET NULL,
  conversation_id    TEXT REFERENCES conversations(id) ON DELETE SET NULL,
  conversation_title TEXT NOT NULL DEFAULT '',
  description        TEXT NOT NULL,
  page_path           TEXT NOT NULL DEFAULT '',
  user_agent          TEXT NOT NULL DEFAULT '',
  viewport_width      INTEGER NOT NULL DEFAULT 0,
  viewport_height     INTEGER NOT NULL DEFAULT 0,
  screenshot          BYTEA,
  screenshot_mime     TEXT NOT NULL DEFAULT '',
  screenshot_width    INTEGER NOT NULL DEFAULT 0,
  screenshot_height   INTEGER NOT NULL DEFAULT 0,
  created_at          BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_user_feedback_created ON user_feedback(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_user_feedback_user_created ON user_feedback(user_id, created_at DESC);

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
  draft           INTEGER NOT NULL DEFAULT 0,
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
  ingest_updated_at BIGINT NOT NULL DEFAULT 0,
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
  ttft_fallback_model TEXT NOT NULL DEFAULT '', -- non-empty = TTFT timeout model-fallback served this row (§4.6-C); value is the fallback model's display name
  created_at         BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE INDEX IF NOT EXISTS idx_usage_user_time ON usage_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_model_time ON usage_logs(model_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_user_model_time ON usage_logs(user_id, model_id, created_at);

-- Append-only successful-call facts. The source log id is an idempotency key,
-- not a foreign key: usage_logs remains freely deletable diagnostic data.
CREATE TABLE IF NOT EXISTS usage_stats (
  source_log_id       BIGINT PRIMARY KEY,
  user_id             TEXT REFERENCES users(id) ON DELETE SET NULL,
  conversation_id     TEXT,
  message_id          TEXT,
  model_id            TEXT NOT NULL,
  purpose             TEXT NOT NULL,
  input_tokens        BIGINT NOT NULL DEFAULT 0,
  output_tokens       BIGINT NOT NULL DEFAULT 0,
  cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
  cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
  images_count        INTEGER NOT NULL DEFAULT 0,
  cost                DOUBLE PRECISION NOT NULL DEFAULT 0,
  currency            TEXT NOT NULL DEFAULT 'USD',
  credits             DOUBLE PRECISION NOT NULL DEFAULT 0,
  workspace_id        TEXT NOT NULL DEFAULT '',
  channel_id          TEXT NOT NULL DEFAULT '',
  fallback            INTEGER NOT NULL DEFAULT 0,
  ttft_fallback_model TEXT NOT NULL DEFAULT '',
  created_at           BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_stats_time ON usage_stats(created_at);
CREATE INDEX IF NOT EXISTS idx_usage_stats_user_time ON usage_stats(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_stats_model_time ON usage_stats(model_id, created_at);
CREATE INDEX IF NOT EXISTS idx_usage_stats_message ON usage_stats(message_id, purpose, source_log_id);

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
  session_id TEXT NOT NULL DEFAULT '',
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
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id);

-- OAuth / social login providers (see schema.sql for the full rationale).
CREATE TABLE IF NOT EXISTS oauth_providers (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL,
  name          TEXT NOT NULL,
  icon          TEXT NOT NULL DEFAULT '',
  client_id     TEXT NOT NULL DEFAULT '',
  client_secret TEXT NOT NULL DEFAULT '',
  issuer_url    TEXT NOT NULL DEFAULT '',
  jwks_url      TEXT NOT NULL DEFAULT '',
  auth_url      TEXT NOT NULL DEFAULT '',
  token_url     TEXT NOT NULL DEFAULT '',
  userinfo_url  TEXT NOT NULL DEFAULT '',
  scopes        TEXT NOT NULL DEFAULT '',
  team_id       TEXT NOT NULL DEFAULT '',
  key_id        TEXT NOT NULL DEFAULT '',
  subject_namespace TEXT NOT NULL DEFAULT '',
  enabled       INTEGER NOT NULL DEFAULT 1,
  sort_order    INTEGER NOT NULL DEFAULT 0,
  updated_at    BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_providers_name_unique ON oauth_providers(lower(trim(name)));

-- Storage paths awaiting physical deletion (§8.1-A async user delete). Rows
-- are written BEFORE any destructive SQL delete and removed one by one as the
-- bytes are actually unlinked, so a crash mid-purge never orphans disk or
-- object-storage bytes: startup sweeps whatever is left.
CREATE TABLE IF NOT EXISTS pending_storage_cleanup (
  path       TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  created_at BIGINT NOT NULL DEFAULT (extract(epoch from now())::bigint)
);

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
-- 空间内的 conversations 默认仅创建者可见，可显式公开给成员；projects/knowledge_bases 仍为成员共享。
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
