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
  credits_permanent REAL NOT NULL DEFAULT 0,       -- compatibility/display mirror
  credits_permanent_micros INTEGER NOT NULL DEFAULT 0, -- authoritative fixed-point balance
  credit_cycle_anchor INTEGER NOT NULL DEFAULT (strftime('%s','now')), -- current group's timed-credit cycle origin
  quota_cycle_anchor INTEGER NOT NULL DEFAULT (strftime('%s','now')), -- current group's model-quota cycle origin
  sort_order    INTEGER NOT NULL DEFAULT 0,        -- admin-defined display order
  created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

-- One-time notices created by administrator permanent-credit adjustments.
-- claimed_at is set atomically when the signed-in user fetches the notice, so
-- refreshes and other devices cannot display the same adjustment twice.
CREATE TABLE IF NOT EXISTS credit_adjustment_notifications (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  direction     TEXT NOT NULL CHECK(direction IN ('add','remove')),
  amount_micros INTEGER NOT NULL CHECK(amount_micros > 0),
  reason        TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  claimed_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_credit_adjustment_notifications_user_pending
  ON credit_adjustment_notifications(user_id, claimed_at, created_at, id);

-- Immutable successful-login audit trail. Unlike refresh_tokens, these rows
-- survive logout/session rotation so administrators can review account access.
CREATE TABLE IF NOT EXISTS login_histories (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  login_at   INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  ip         TEXT NOT NULL DEFAULT '',
  location   TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  method     TEXT NOT NULL DEFAULT 'password'
);
CREATE INDEX IF NOT EXISTS idx_login_histories_user_time
  ON login_histories(user_id, login_at DESC, id DESC);

-- Membership tiers. Exactly one row is the default (is_default=1, seeded as
-- ug_free). features is a JSON array of feature strings shown on the
-- subscription page. Prices use the deployment-wide settlement currency and
-- are stored in that currency's smallest unit.
CREATE TABLE IF NOT EXISTS user_groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  features    TEXT NOT NULL DEFAULT '[]',
  monthly_price_amount_minor INTEGER NOT NULL DEFAULT 0,
  yearly_price_amount_minor  INTEGER NOT NULL DEFAULT 0,
  is_default  INTEGER NOT NULL DEFAULT 0,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  max_projects INTEGER NOT NULL DEFAULT 0,
  max_kbs      INTEGER NOT NULL DEFAULT 0,
  -- Storage quota for non-image uploads, MB (0 = unlimited, § user files page).
  max_storage_mb INTEGER NOT NULL DEFAULT 0,
  credit_allowance      REAL NOT NULL DEFAULT 0,    -- compatibility/display mirror
  credit_allowance_micros INTEGER NOT NULL DEFAULT 0, -- authoritative fixed-point allowance
  credit_period_seconds INTEGER NOT NULL DEFAULT 0, -- refresh cycle length (0 = no timed credits)
  is_purchasable INTEGER NOT NULL DEFAULT 1,      -- displayed tier may temporarily pause checkout
  permissions TEXT NOT NULL DEFAULT '{}',          -- normalized group capability/resource policy
  created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_groups_name_unique ON user_groups(lower(trim(name)));

-- Authoritative credit debit ledger. Unlike usage_logs, these rows are billing
-- records and are never deleted by the admin usage-log cleanup controls.
CREATE TABLE IF NOT EXISTS credit_ledger (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  group_id     TEXT NOT NULL,
  cycle_anchor INTEGER NOT NULL DEFAULT 0,
  cycle_start  INTEGER NOT NULL DEFAULT 0,
  kind         TEXT NOT NULL, -- timed_debit | permanent_debit
  amount       REAL NOT NULL,
  amount_micros INTEGER NOT NULL DEFAULT 0,
  source_type  TEXT NOT NULL DEFAULT '',
  source_id    TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_timed
  ON credit_ledger(user_id, group_id, cycle_anchor, cycle_start, kind);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_user_time
  ON credit_ledger(user_id, created_at);

CREATE TABLE IF NOT EXISTS credit_reservations (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  amount_micros   INTEGER NOT NULL CHECK(amount_micros > 0),
  actual_micros   INTEGER NOT NULL DEFAULT 0 CHECK(actual_micros >= 0),
  source_type     TEXT NOT NULL DEFAULT '',
  source_id       TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'reserved' CHECK(status IN ('reserved','settling','settled','released')),
  expires_at      INTEGER NOT NULL CHECK(expires_at > 0),
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
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
  cycle_anchor    INTEGER NOT NULL DEFAULT 0,
  window_start    INTEGER NOT NULL CHECK(window_start > 0),
  limit_type      TEXT NOT NULL CHECK(limit_type IN ('count','cost')),
  reserved_micros INTEGER NOT NULL DEFAULT 0 CHECK(reserved_micros >= 0),
  actual_micros   INTEGER NOT NULL DEFAULT 0 CHECK(actual_micros >= 0),
  status          TEXT NOT NULL DEFAULT 'reserved' CHECK(status IN ('reserved','finalized','released')),
  expires_at      INTEGER NOT NULL CHECK(expires_at > window_start),
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
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
  cost_micros     INTEGER NOT NULL DEFAULT 0 CHECK(cost_micros >= 0),
  images_count    INTEGER NOT NULL DEFAULT 0 CHECK(images_count >= 0),
  input_tokens    INTEGER NOT NULL DEFAULT 0 CHECK(input_tokens >= 0),
  output_tokens   INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens >= 0),
  currency        TEXT NOT NULL DEFAULT 'USD' CHECK(length(trim(currency)) > 0),
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_billing_usage_message
  ON billing_usage(message_id, purpose, created_at);
CREATE INDEX IF NOT EXISTS idx_billing_usage_user_time
  ON billing_usage(user_id, created_at);

-- Administrator-defined permanent-credit top-up packages. Prices use the
-- deployment-wide settlement currency and are stored in its smallest unit.
CREATE TABLE IF NOT EXISTS credit_packages (
  id                 TEXT PRIMARY KEY,
  name               TEXT NOT NULL,
  description        TEXT NOT NULL DEFAULT '',
  credits            REAL NOT NULL,
  price_amount_minor INTEGER NOT NULL DEFAULT 0,
  enabled            INTEGER NOT NULL DEFAULT 1,
  sort_order         INTEGER NOT NULL DEFAULT 0,
  created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at         INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_credit_packages_order ON credit_packages(sort_order, name);

-- Payment-provider accounts. config is the provider-specific JSON document
-- (including credentials); it is stored verbatim and masked only by the API.
CREATE TABLE IF NOT EXISTS payment_channels (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  provider   TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT 'live',
  config     TEXT NOT NULL DEFAULT '{}',
  enabled    INTEGER NOT NULL DEFAULT 1,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_channels_name_unique ON payment_channels(lower(trim(name)));
CREATE INDEX IF NOT EXISTS idx_payment_channels_order ON payment_channels(sort_order, name);

-- User-selectable methods exposed by a payment channel (for example Stripe
-- Checkout or an EPay Alipay route). Channel deletion is deliberately
-- restricted until its methods have been removed explicitly.
CREATE TABLE IF NOT EXISTS payment_methods (
  id         TEXT PRIMARY KEY,
  channel_id TEXT NOT NULL REFERENCES payment_channels(id) ON DELETE RESTRICT,
  name       TEXT NOT NULL,
  type       TEXT NOT NULL,
  icon       TEXT NOT NULL DEFAULT '',
  config     TEXT NOT NULL DEFAULT '{}',
  enabled    INTEGER NOT NULL DEFAULT 1,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_methods_channel_name_unique ON payment_methods(channel_id, lower(trim(name)));
CREATE INDEX IF NOT EXISTS idx_payment_methods_order ON payment_methods(channel_id, sort_order, name);

-- Immutable commercial snapshot plus mutable processing state. Provider,
-- channel/method, product name, amount/currency and entitlement fields are
-- copied at creation so later administrator edits cannot change an order.
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
  amount_minor      INTEGER NOT NULL,
  paid_amount_minor INTEGER NOT NULL DEFAULT 0,
  tax_amount_minor  INTEGER NOT NULL DEFAULT 0,
  currency          TEXT NOT NULL,
  provider_amount_minor INTEGER NOT NULL DEFAULT 0,
  provider_currency TEXT NOT NULL DEFAULT '',
  conversion_rate   TEXT NOT NULL DEFAULT '',
  credits           REAL NOT NULL DEFAULT 0,
  user_group_id     TEXT NOT NULL DEFAULT '',
  billing_cycle     TEXT NOT NULL DEFAULT '',
  provider_order_id TEXT NOT NULL DEFAULT '',
  provider_payment_id TEXT NOT NULL DEFAULT '',
  checkout_session_id TEXT NOT NULL DEFAULT '',
  checkout_url      TEXT NOT NULL DEFAULT '',
  checkout_expires_at INTEGER NOT NULL DEFAULT 0,
  last_reconciled_at INTEGER NOT NULL DEFAULT 0,
  reconcile_error   TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'pending',
  failure_code      TEXT NOT NULL DEFAULT '',
  failure_message   TEXT NOT NULL DEFAULT '',
  paid_at           INTEGER NOT NULL DEFAULT 0,
  fulfilled_at      INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at        INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_payment_orders_user_created ON payment_orders(user_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_payment_orders_channel_status ON payment_orders(channel_id, status, created_at);
CREATE INDEX IF NOT EXISTS idx_payment_orders_status_created ON payment_orders(status, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_orders_provider_order_unique
  ON payment_orders(provider, channel_id, provider_order_id) WHERE provider_order_id<>'';

-- Provider-facing checkout attempts belonging to one commercial order. EPay
-- resumes reuse the outstanding merchant_order_id. The integration does not
-- issue a replacement without trusted proof that the gateway reference ended;
-- ambiguous legacy orders with multiple references fail closed.
CREATE TABLE IF NOT EXISTS payment_order_attempts (
  merchant_order_id TEXT PRIMARY KEY,
  order_id          TEXT NOT NULL REFERENCES payment_orders(id) ON DELETE CASCADE,
  provider          TEXT NOT NULL,
  channel_id        TEXT NOT NULL,
  provider_order_id TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL DEFAULT 'issued',
  paid_at           INTEGER NOT NULL DEFAULT 0,
  created_at        INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at        INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_payment_order_attempts_order_created
  ON payment_order_attempts(order_id, created_at, merchant_order_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_order_attempts_provider_order_unique
  ON payment_order_attempts(provider, channel_id, provider_order_id) WHERE provider_order_id<>'';

-- Raw, verified provider notifications. The composite unique key is the first
-- idempotency barrier; fulfillment also locks/checks the order so a provider
-- emitting two different success event ids still cannot grant value twice.
CREATE TABLE IF NOT EXISTS payment_events (
  id           TEXT PRIMARY KEY,
  provider     TEXT NOT NULL,
  channel_id   TEXT NOT NULL,
  event_id     TEXT NOT NULL,
  order_id     TEXT NOT NULL REFERENCES payment_orders(id) ON DELETE CASCADE,
  event_type   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  processed_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE(provider, channel_id, event_id)
);
CREATE INDEX IF NOT EXISTS idx_payment_events_order_created ON payment_events(order_id, created_at, id);

-- Per-model, per-group access + usage cap. A model with NO rows here is open to
-- everyone (unlimited). Once a model has any row, only listed groups may use it;
-- each row caps usage within a fixed window: limit_type 'cost' (in the model's
-- currency) or 'count' (calls), limit_value 0 = granted but unlimited.
CREATE TABLE IF NOT EXISTS model_group_quotas (
  model_id       TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  group_id       TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  period_seconds INTEGER NOT NULL DEFAULT 604800 CHECK(period_seconds > 0),
  limit_type     TEXT NOT NULL DEFAULT 'count' CHECK(limit_type IN ('cost','count')),  -- cost | count
  limit_value    REAL NOT NULL DEFAULT 0 CHECK(limit_value >= 0),
  PRIMARY KEY (model_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_mgq_group ON model_group_quotas(group_id);

-- Redeem codes (§ redeem codes). Admin creates codes that grant a specific
-- user_group for `duration_days` (0 = permanent). `expires_at` is the deadline
-- by which the code itself must be redeemed (0 = no deadline). `max_uses=1`
-- (default) makes codes single-use; admins can bump it for shared promo codes.
-- enabled=0 lets an admin revoke an unredeemed code without deleting the row
-- (preserves audit history).
-- kind='credits' codes grant `credits` permanent credits instead of a group;
-- their group_id holds the default group as an FK-satisfying placeholder and
-- is never applied to the redeemer.
CREATE TABLE IF NOT EXISTS redeem_codes (
  id            TEXT PRIMARY KEY,
  code          TEXT UNIQUE NOT NULL,
  kind          TEXT NOT NULL DEFAULT 'group',
  group_id      TEXT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  duration_days INTEGER NOT NULL DEFAULT 30 CHECK(duration_days >= 0),
  credits       REAL NOT NULL DEFAULT 0 CHECK(credits >= 0),
  max_uses      INTEGER NOT NULL DEFAULT 1 CHECK(max_uses > 0),
  used_count    INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0 AND used_count <= max_uses),
  expires_at    INTEGER NOT NULL DEFAULT 0 CHECK(expires_at >= 0),
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
  credits         REAL NOT NULL DEFAULT 0,
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

-- Administrator-managed MCP Streamable HTTP services. Request headers may
-- contain credentials and are therefore stored verbatim but only exposed by
-- the administrator API in masked form. discovered_tools is the last
-- successful tools/list snapshot used by the runtime registry.
CREATE TABLE IF NOT EXISTS mcp_servers (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL,
  icon             TEXT NOT NULL DEFAULT '',
  description      TEXT NOT NULL DEFAULT '',
  url              TEXT NOT NULL,
  headers          TEXT NOT NULL DEFAULT '{}',
  enabled          INTEGER NOT NULL DEFAULT 0,
  discovered_tools TEXT NOT NULL DEFAULT '[]',
  protocol_version TEXT NOT NULL DEFAULT '',
  last_error       TEXT NOT NULL DEFAULT '',
  last_synced_at   INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at       INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_servers_name_unique ON mcp_servers(lower(trim(name)));
CREATE INDEX IF NOT EXISTS idx_mcp_servers_enabled ON mcp_servers(enabled, name);

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
  fast              INTEGER NOT NULL DEFAULT 0, -- §fast-mode: THE fast model (only one; hidden from the advanced picker, Deep Research forced off)
  system_prompt     TEXT NOT NULL DEFAULT '',
  param_controls    TEXT NOT NULL DEFAULT '[]',
  extra_params      TEXT NOT NULL DEFAULT '{}', -- admin-only upstream request defaults; native request fields win
  official_tools    TEXT NOT NULL DEFAULT '[]', -- provider-hosted [{name,icon,request}]; legacy string arrays are migrated
  builtin_tools     TEXT DEFAULT NULL, -- local-tool defaults; NULL=all (backwards compatible), []=none
  mcp_server_ids    TEXT DEFAULT NULL, -- MCP service defaults; NULL/[]=none, explicit ids=selected services
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
  compaction_token_threshold INTEGER NOT NULL DEFAULT 0,
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
  display_description TEXT NOT NULL DEFAULT '', -- catalog-only copy; description remains model-facing trigger metadata
  icon         TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL,
  assets       TEXT NOT NULL DEFAULT '[]',
  enabled      INTEGER NOT NULL DEFAULT 1,
  sort_order   INTEGER NOT NULL DEFAULT 0,
  updated_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_skills_name_unique ON skills(lower(trim(name)));

-- Administrator prompt templates. Prompt content is copied into user_prompts;
-- the public catalog only exposes name/description/icon metadata.
CREATE TABLE IF NOT EXISTS prompts (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  icon        TEXT NOT NULL DEFAULT '',
  content     TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_prompts_name_unique ON prompts(lower(trim(name)));

-- Private user Agent Skills. Deliberately no assets/storage columns: private
-- skills are instruction-only and can never stage files into the sandbox.
CREATE TABLE IF NOT EXISTS user_skills (
  id              TEXT PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  workspace_id    TEXT NOT NULL DEFAULT '',
  name            TEXT NOT NULL,
  description     TEXT NOT NULL,
  icon            TEXT NOT NULL DEFAULT '',
  instructions    TEXT NOT NULL,
  source_skill_id TEXT REFERENCES skills(id) ON DELETE SET NULL,
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_user_skills_user ON user_skills(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_skills_workspace ON user_skills(workspace_id, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_skills_user_name_unique ON user_skills(user_id, lower(trim(name))) WHERE workspace_id='';
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_skills_workspace_name_unique ON user_skills(workspace_id, lower(trim(name))) WHERE workspace_id<>'';
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_skills_source_unique ON user_skills(user_id, source_skill_id) WHERE workspace_id='' AND source_skill_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_skills_workspace_source_unique ON user_skills(workspace_id, source_skill_id) WHERE workspace_id<>'' AND source_skill_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS user_prompts (
  id               TEXT PRIMARY KEY,
  user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  workspace_id     TEXT NOT NULL DEFAULT '',
  name             TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  content          TEXT NOT NULL,
  source_prompt_id TEXT REFERENCES prompts(id) ON DELETE SET NULL,
  created_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at       INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_user_prompts_user ON user_prompts(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_prompts_workspace ON user_prompts(workspace_id, updated_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_prompts_user_name_unique ON user_prompts(user_id, lower(trim(name))) WHERE workspace_id='';
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_prompts_workspace_name_unique ON user_prompts(workspace_id, lower(trim(name))) WHERE workspace_id<>'';
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_prompts_source_unique ON user_prompts(user_id, source_prompt_id) WHERE workspace_id='' AND source_prompt_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_prompts_workspace_source_unique ON user_prompts(workspace_id, source_prompt_id) WHERE workspace_id<>'' AND source_prompt_id IS NOT NULL;

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
  created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  -- §workspace RBAC: 1 = shared with the workspace (members + guests);
  -- 0 = private to the creator and workspace admins. Personal rows ignore it.
  is_public          INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_kbs_user ON knowledge_bases(user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_kbs_user_name_unique ON knowledge_bases(user_id, lower(trim(name)));

CREATE TABLE IF NOT EXISTS knowledge_base_shares (
  kb_id      TEXT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL CHECK(role IN ('read','write')),
  created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  PRIMARY KEY (kb_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_kb_shares_user ON knowledge_base_shares(user_id, updated_at DESC);

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
  updated_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  -- §workspace RBAC: 1 = shared with the workspace; 0 = private to the
  -- creator and workspace admins. Personal rows ignore it.
  is_public        INTEGER NOT NULL DEFAULT 1
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
  -- Inline-thread linkage (§ text-selection sub-conversations). Non-empty
  -- inline_source_conv marks this row as a sub-conversation hidden from the list.
  inline_source_conv TEXT NOT NULL DEFAULT '',
  inline_parent_id   TEXT NOT NULL DEFAULT '',
  inline_quote       TEXT NOT NULL DEFAULT '',
  -- Workspace conversations are private to their creator by default. Personal
  -- conversations ignore this flag and remain owner-scoped.
  is_public      INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_conv_user ON conversations(user_id);
CREATE INDEX IF NOT EXISTS idx_conv_project ON conversations(project_id);
CREATE INDEX IF NOT EXISTS idx_conv_user_updated ON conversations(user_id, archived, pinned DESC, updated_at DESC);

-- A database-backed lease keeps context compaction exclusive even when several
-- application replicas are running without a shared Redis cache.
CREATE TABLE IF NOT EXISTS conversation_compaction_leases (
  conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
  owner_token     TEXT NOT NULL,
  expires_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversation_compaction_leases_expires
  ON conversation_compaction_leases(expires_at);

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
  input_tokens       INTEGER NOT NULL DEFAULT 0,
  context_tokens     INTEGER NOT NULL DEFAULT 0,
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
CREATE INDEX IF NOT EXISTS idx_messages_role_created ON messages(role, created_at);
CREATE INDEX IF NOT EXISTS idx_messages_model_role_created ON messages(model_id, role, created_at);

-- One feedback row per (assistant message, evaluating user). Catalog ids are
-- snapshots rather than foreign keys so deleting a model/channel/workspace does
-- not erase the quality history; the owning message/conversation/user remain
-- authoritative and cascade their feedback when deleted.
CREATE TABLE IF NOT EXISTS message_feedback (
  id              TEXT PRIMARY KEY,
  message_id      TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  workspace_id    TEXT NOT NULL DEFAULT '',
  model_id        TEXT NOT NULL DEFAULT '',
  channel_id      TEXT NOT NULL DEFAULT '',
  rating          TEXT NOT NULL CHECK(rating IN ('like','dislike')),
  reasons         TEXT NOT NULL DEFAULT '[]',
  comment         TEXT NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  updated_at      INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  UNIQUE(message_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_message_feedback_updated ON message_feedback(updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_message_feedback_model_updated ON message_feedback(model_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_feedback_rating_updated ON message_feedback(rating, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_message_feedback_conversation ON message_feedback(conversation_id);
CREATE INDEX IF NOT EXISTS idx_message_feedback_user_message ON message_feedback(user_id, message_id);

-- User-submitted product issue reports. Unlike message_feedback (model-quality
-- ratings), these records carry a required description and an optional page
-- screenshot for support triage. Conversation metadata is snapshotted so a
-- later message/thread deletion does not make the report unintelligible.
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
  screenshot          BLOB,
  screenshot_mime     TEXT NOT NULL DEFAULT '',
  screenshot_width    INTEGER NOT NULL DEFAULT 0,
  screenshot_height   INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_user_feedback_created ON user_feedback(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_user_feedback_user_created ON user_feedback(user_id, created_at DESC);

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
  uploaded_by_user_id TEXT NOT NULL DEFAULT '',
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

-- Append-only successful-call facts. usage_logs is a deletable diagnostic copy;
-- no relationship points back to it, so pruning logs cannot change analytics.
-- Catalog/conversation ids are immutable snapshots. Only user_id is a foreign
-- key so account deletion anonymizes attribution while preserving global/model
-- history.
CREATE TABLE IF NOT EXISTS usage_stats (
  source_log_id       INTEGER PRIMARY KEY,
  user_id             TEXT REFERENCES users(id) ON DELETE SET NULL,
  conversation_id     TEXT,
  message_id          TEXT,
  model_id            TEXT NOT NULL,
  purpose             TEXT NOT NULL,
  input_tokens        INTEGER NOT NULL DEFAULT 0,
  output_tokens       INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens   INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens  INTEGER NOT NULL DEFAULT 0,
  images_count        INTEGER NOT NULL DEFAULT 0,
  cost                REAL NOT NULL DEFAULT 0,
  currency            TEXT NOT NULL DEFAULT 'USD',
  credits             REAL NOT NULL DEFAULT 0,
  workspace_id        TEXT NOT NULL DEFAULT '',
  channel_id          TEXT NOT NULL DEFAULT '',
  fallback            INTEGER NOT NULL DEFAULT 0,
  ttft_fallback_model TEXT NOT NULL DEFAULT '',
  created_at           INTEGER NOT NULL
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
  size_bytes   INTEGER NOT NULL DEFAULT 0,
  source       TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_artifacts_message ON artifacts(message_id);

CREATE TABLE IF NOT EXISTS refresh_tokens (
  jti        TEXT PRIMARY KEY,
  session_id TEXT NOT NULL DEFAULT '',
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
-- is generic OpenID Connect and kind=oauth2 is generic OAuth 2.0 UserInfo. Their
-- endpoints come from the row. client_secret is plaintext like channel api_key;
-- for Apple it holds the
-- AuthKey .p8 private key used to mint the client-secret JWT.
CREATE TABLE IF NOT EXISTS oauth_providers (
  id            TEXT PRIMARY KEY,                -- "oa_<hex>"
  kind          TEXT NOT NULL,                   -- google | github | apple | oidc | oauth2
  name          TEXT NOT NULL,                   -- label shown on the login button
  icon          TEXT NOT NULL DEFAULT '',        -- emoji / uploaded URL (custom providers)
  client_id     TEXT NOT NULL DEFAULT '',
  client_secret TEXT NOT NULL DEFAULT '',        -- apple: the .p8 private key
  issuer_url    TEXT NOT NULL DEFAULT '',        -- expected OIDC iss (generic providers)
  jwks_url      TEXT NOT NULL DEFAULT '',        -- trusted signing-key set URL
  auth_url      TEXT NOT NULL DEFAULT '',        -- oidc/oauth2 only (built-ins use defaults)
  token_url     TEXT NOT NULL DEFAULT '',
  userinfo_url  TEXT NOT NULL DEFAULT '',
  scopes        TEXT NOT NULL DEFAULT '',        -- space-separated override
  team_id       TEXT NOT NULL DEFAULT '',        -- apple
  key_id        TEXT NOT NULL DEFAULT '',        -- apple
  subject_namespace TEXT NOT NULL DEFAULT '',    -- internal trust-domain generation
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
-- 空间内的 conversations 默认仅创建者可见，可显式公开给成员；projects/knowledge_bases 仍为成员共享。
-- invite_token 是 192-bit 能力令牌(仅通过邀请链接加入);轮换即作废旧链接。
CREATE TABLE IF NOT EXISTS workspaces (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  owner_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  invite_token TEXT NOT NULL UNIQUE,
  deleting     INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_workspaces_owner ON workspaces(owner_id);

CREATE TABLE IF NOT EXISTS workspace_members (
  workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id                  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role                     TEXT NOT NULL DEFAULT 'member',
  can_create_projects      INTEGER NOT NULL DEFAULT 1,
  can_private_conversations INTEGER NOT NULL DEFAULT 1,
  can_create_skills_prompts INTEGER NOT NULL DEFAULT 1,
  can_create_kb            INTEGER NOT NULL DEFAULT 1,
  can_add_kb_files         INTEGER NOT NULL DEFAULT 1,
  can_delete_kb_content    INTEGER NOT NULL DEFAULT 1,
  joined_at                INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_ws_members_user ON workspace_members(user_id);

-- Per-library workspace overrides. Missing rows deliberately mean "allowed" so
-- existing workspaces and newly invited members retain the historical shared
-- editing behavior until a library manager explicitly restricts them.
CREATE TABLE IF NOT EXISTS workspace_kb_member_permissions (
  kb_id              TEXT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  can_add_files      INTEGER NOT NULL DEFAULT 1,
  can_delete_content INTEGER NOT NULL DEFAULT 1,
  updated_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
  PRIMARY KEY (kb_id, user_id)
);

-- §workspace RBAC phase 3 — scoped invitation records. The obsolete
-- workspaces.invite_token is retained only for schema compatibility and is
-- never accepted by the application. purpose is internal lifecycle metadata:
-- "manual" is an administrator-created invite; "quick_link" is the bounded
-- single-use link returned by the legacy rotate endpoint.
CREATE TABLE IF NOT EXISTS workspace_invites (
  id           TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  token        TEXT NOT NULL UNIQUE,
  email        TEXT NOT NULL DEFAULT '',
  role         TEXT NOT NULL DEFAULT 'guest',
  expires_at   INTEGER NOT NULL DEFAULT 0,
  max_uses     INTEGER NOT NULL DEFAULT 1,
  used_count   INTEGER NOT NULL DEFAULT 0,
  created_by   TEXT REFERENCES users(id) ON DELETE SET NULL,
  purpose      TEXT NOT NULL DEFAULT 'manual',
  revoked_at   INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_ws_invites_workspace ON workspace_invites(workspace_id, created_at DESC);

-- §workspace RBAC phase 4 — per-workspace capability policy. Empty id arrays
-- mean "everything the platform offers"; the policy can only narrow.
CREATE TABLE IF NOT EXISTS workspace_policies (
  workspace_id                TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  allowed_model_ids           TEXT NOT NULL DEFAULT '[]',
  allowed_tool_ids            TEXT NOT NULL DEFAULT '[]',
  allowed_mcp_server_ids      TEXT NOT NULL DEFAULT '[]',
  allow_sandbox               INTEGER NOT NULL DEFAULT 1,
  allow_image_generation      INTEGER NOT NULL DEFAULT 1,
  allow_knowledge_bases       INTEGER NOT NULL DEFAULT 1,
  allow_file_upload           INTEGER NOT NULL DEFAULT 1,
  member_monthly_credit_limit REAL NOT NULL DEFAULT 0,
  updated_by                  TEXT NOT NULL DEFAULT '',
  updated_at                  INTEGER NOT NULL DEFAULT 0
);

-- §workspace RBAC phase 5 — lightweight audit trail. No FK on workspace_id:
-- rows deliberately OUTLIVE workspace deletion. metadata NEVER contains
-- invite tokens, API keys, request bodies or document content.
CREATE TABLE IF NOT EXISTS workspace_audit_logs (
  id            TEXT PRIMARY KEY,
  workspace_id  TEXT NOT NULL,
  actor_user_id TEXT NOT NULL,
  action        TEXT NOT NULL,
  target_type   TEXT NOT NULL DEFAULT '',
  target_id     TEXT NOT NULL DEFAULT '',
  metadata      TEXT NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE INDEX IF NOT EXISTS idx_ws_audit_workspace ON workspace_audit_logs(workspace_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ws_kb_permissions_user ON workspace_kb_member_permissions(user_id);
