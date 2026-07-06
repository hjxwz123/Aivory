/**
 * Wire-format types shared between the Go backend and the frontend. Keep
 * field names snake_case to match the backend JSON tags directly — frontend
 * code uses helpers in `lib/format.ts` to convert to display strings when
 * needed.
 */

export interface ApiError {
  error: string
}

export interface ApiUser {
  id: string
  email: string
  name: string
  role: 'user' | 'admin'
  status: 'active' | 'banned' | 'disabled'
  settings: Record<string, unknown>
  group_id?: string
  /** Display name of the membership group (tier label shown in the sidebar).
   *  Transient — populated on the auth/me responses, not stored. */
  group_name?: string
  /** Unix seconds at which a redeem-code grant lapses back to previous_group_id. 0 = permanent. */
  group_expires_at?: number
  /** The tier to fall back to when group_expires_at hits. */
  previous_group_id?: string
  /** True when the account requires a 2FA code at login (§ 2FA). */
  totp_enabled?: boolean
  /** False for OAuth accounts that have never chosen their own password — the
   *  client forces a set-password step before letting them into the app. */
  has_password?: boolean
  /** Unix seconds of the last password change (change/reset/first set).
   *  0 or absent = never changed since the account was created. */
  password_changed_at?: number
  /** Unix seconds of last authenticated activity. Drives admin online status. */
  last_seen_at?: number
  /** Non-expiring credit balance (§ credits) — admin-editable on the users page. */
  credits_permanent?: number
  /** Admin-defined order for the users page. */
  sort_order?: number
  /** Capability flags from the user's group (e.g. "research"). Populated on the
   *  /api/me response so the client can gate features. */
  features?: string[]
  /** Global admin master switch for long-term memory. When false, no user may
   *  enable memory and the per-user toggle is hidden. Transient (auth/me only).
   *  Absent ⇒ treat as available (older backend). */
  memory_available?: boolean
  created_at: number
}

/** Admin analytics (§ admin → analytics). */
export interface ApiUsageTotals {
  calls: number
  input_tokens: number
  output_tokens: number
  cost: number
  users: number
}
export interface ApiUsageTrendPoint {
  bucket_start: number
  input_tokens: number
  output_tokens: number
  calls: number
  cost: number
}
export interface ApiUsageBreakdownRow {
  key: string
  label: string
  input_tokens: number
  output_tokens: number
  calls: number
  cost: number
}
export interface ApiUsageSeriesPoint {
  bucket_start: number
  key: string
  input_tokens: number
  output_tokens: number
  calls: number
  cost: number
}
export interface ApiAnalytics {
  days: number
  bucket: number
  totals: ApiUsageTotals
  trend: ApiUsageTrendPoint[]
  by_model: ApiUsageBreakdownRow[]
  by_user: ApiUsageBreakdownRow[]
  model_series: ApiUsageSeriesPoint[]
  user_series: ApiUsageSeriesPoint[]
}

/** One active sign-in (§ account → active sessions). `id` is the refresh-token
 * jti, used as the opaque handle to revoke it. `location` is best-effort and may
 * be empty when no geo-providing proxy is in front of the server. */
export interface ApiSession {
  id: string
  ip: string
  user_agent: string
  location: string
  created_at: number
  last_seen: number
}

/** Owner-facing descriptor of a conversation's public share (§ sharing). */
export interface ApiShareInfo {
  id: string
  created_at: number
}

/** One message in a public share snapshot — cost-stripped, identity-free. */
export interface ApiSharedMessage {
  role: 'user' | 'assistant'
  blocks: ApiBlock[]
  citations: ApiCitation[]
  /** Uploaded attachments (id/filename/kind/url). Absent on snapshots created
   *  before shares carried assets — re-share to include uploads. */
  attachments?: ApiAttachment[]
  created_at: number
}

/** The public read-only conversation served at /share/:token. */
export interface ApiSharedConversation {
  title: string
  messages: ApiSharedMessage[]
  created_at: number
}

/** Workspace (§workspaces) — fully-isolated collaborative space. */
export interface ApiWorkspace {
  id: string
  name: string
  owner_id: string
  /** Present only for the owner. */
  invite_token?: string
  created_at: number
  role?: 'owner' | 'member'
  member_count?: number
  owner_name?: string
}

export interface ApiWorkspaceMember {
  user_id: string
  role: 'owner' | 'member'
  joined_at: number
  name: string
  email: string
  avatar_url: string
}

/** Membership tier (§ user groups). */
export interface ApiUserGroup {
  id: string
  name: string
  description: string
  features: string[]
  price_usd: number
  price_cny: number
  is_default: boolean
  sort_order: number
  /** Max projects / knowledge bases a member may create. 0 = unlimited. */
  max_projects: number
  max_kbs: number
  /** Credit system (§ credits): per-group timed allowance + refresh cycle (unused
   *  voided). The USD→credit rate and purchase links are global settings. */
  credit_allowance: number
  credit_period_seconds: number
  created_at: number
  updated_at: number
  max_workspaces?: number
  /** Listed on the public subscription page. */
  is_public?: boolean
}

/** Per-model, per-group usage cap. */
export interface ApiModelQuota {
  model_id: string
  group_id: string
  period_seconds: number
  limit_type: 'cost' | 'count'
  limit_value: number
}

/** Redeem code (§ redeem codes). Admins issue these to grant a user_group
 *  for `duration_days` (0 = permanent). `code` is the human-typeable string. */
export interface ApiRedeemCode {
  id: string
  code: string
  group_id: string
  duration_days: number
  max_uses: number
  used_count: number
  /** Unix seconds after which an unredeemed code is rejected. 0 = no deadline. */
  expires_at: number
  enabled: boolean
  note: string
  batch_name: string
  created_by: string
  created_at: number
}

/** One row in the redeem audit trail. */
export interface ApiRedeemRedemption {
  id: string
  code_id: string
  user_id: string
  group_id: string
  previous_group_id: string
  granted_at: number
  expires_at: number
}

/** Result of POST /api/me/redeem.
 *
 *  On a successful redemption `ok` is true and `user` is the updated account.
 *  When the code grants a group different from the current one and `confirm`
 *  wasn't passed, the server applies nothing and returns
 *  `requires_confirmation: true` with both group names so the UI can warn that
 *  redeeming overrides the current group immediately (not a renewal). */
export interface ApiRedeemResult {
  ok?: true
  user?: ApiUser
  group_id: string
  group_name: string
  expires_at: number
  /** Set when the code would switch groups and needs an explicit confirm. */
  requires_confirmation?: boolean
  /** The group the user is currently on (only on the confirmation preview). */
  current_group_id?: string
  current_group_name?: string
}

export interface ApiAuthResponse {
  user: ApiUser
  access_token: string
  expires_at: number
}

export type OAuthKind = 'google' | 'github' | 'apple' | 'oidc'

/** Full provider record (admin view). client_secret is never returned. */
export interface ApiOAuthProvider {
  id: string
  kind: OAuthKind
  name: string
  icon: string
  client_id: string
  has_secret: boolean
  auth_url: string
  token_url: string
  userinfo_url: string
  scopes: string
  team_id: string
  key_id: string
  enabled: boolean
  sort_order: number
  updated_at: number
}

/** Minimal provider shape exposed to the public login page. */
export interface ApiPublicOAuthProvider {
  id: string
  kind: OAuthKind
  name: string
  icon: string
}

/** One third-party identity bound to the current user (§ identity linking). */
export interface ApiOAuthIdentity {
  provider_id: string
  subject: string
  email: string
  created_at: number
  provider_name: string
  provider_kind: OAuthKind
  provider_icon: string
  /** false when the admin disabled the provider — bound but not usable to log in. */
  provider_enabled: boolean
}

export interface ApiChannel {
  id: string
  name: string
  type: 'openai' | 'claude' | 'anthropic' | 'google' | 'gemini'
  api_format: 'chat' | 'responses' | ''
  base_url: string
  has_api_key: boolean
  enabled: boolean
  sort_order: number
  updated_at: number
}

/** An admin-managed model tag (§ model tags) used to filter the picker. */
export interface ApiModelTag {
  id: string
  name: string
  sort_order: number
  created_at: number
}

export interface ApiModel {
  id: string
  channel_id: string
  kind: 'chat' | 'image' | 'embedding'
  request_id: string
  label: string
  description: string
  icon: string
  enabled: boolean
  sort_order: number
  tool_mode: 'native' | 'prompt' | 'none'
  vision: boolean
  stream: boolean
  /** Whether this chat model exposes Deep Research in the composer. Absent from older backends ⇒ enabled. */
  research_enabled?: boolean
  system_prompt: string
  param_controls: unknown
  /** OpenAI Responses hosted tools to enable; empty/absent = use system tools (§2.3-B). */
  official_tools?: string[]
  /** model_tags ids assigned to this model — drives the picker's tag filter (§ model tags). */
  tags?: string[]
  /** skill ids bound to this model (model_skills) — these get listed in the
   *  system-prompt skill index; full instructions load on demand via use_skill (§4.17). */
  skills?: string[]
  /** Screen each user prompt before generation (§ moderation). */
  moderation_enabled?: boolean
  /** Which screen to use when moderation is on: keyword list or a model verdict. */
  moderation_mode?: 'keyword' | 'model'
  price_input: number
  price_output: number
  price_cache_read: number
  price_cache_write: number
  price_per_image: number
  currency: string
  dim: number
  /** §4.20 per-image-model: seconds cap for one generation/edit request. 0 = default. */
  image_timeout_sec?: number
  updated_at: number
  /** True when the model has no free allotment left for the user's group, so it's
   *  charged in credits (§ credits). The picker shows the multiplier, not a lock. */
  uses_credits?: boolean
  /** Relative credit rate = (price_input + price_output) / 5, one decimal (§ credits). */
  multiplier?: number
  /** Per-image cost in credits (price_per_image × credits_per_usd) for an image
   *  model that's credit-charged. The picker shows "N credits" after the name;
   *  0/absent for chat models, free image models, or when credits are off. */
  credits_per_image?: number
}

/** One file bundled with a skill (§4.17). use_skill stages these into the
 *  sandbox at /workspace/skills/<name>/. storage_path is server-controlled
 *  (returned by the upload endpoint) — the client only echoes it back on save. */
export interface ApiSkillAsset {
  filename: string
  storage_path: string
  mime_type?: string
  size_bytes?: number
}

export interface ApiSkill {
  id: string
  name: string
  description: string
  icon: string
  instructions: string
  assets: ApiSkillAsset[]
  enabled: boolean
  sort_order: number
  updated_at: number
}

export interface ApiProject {
  id: string
  user_id: string
  name: string
  description: string
  instructions: string
  accent: 'violet' | 'sage' | 'amber' | 'rose' | 'slate' | 'teal'
  emoji: string
  pinned: boolean
  kb_id: string
  auto_add_uploads: boolean
  created_at: number
  updated_at: number
  /** §workspaces */
  workspace_id?: string
}

export interface ApiKnowledgeBase {
  id: string
  user_id: string
  name: string
  description: string
  embedding_model_id: string
  embedding_dim: number
  project_id: string
  created_at: number
}

export interface ApiDocument {
  id: string
  kb_id: string
  conversation_id: string
  filename: string
  mime_type: string
  size_bytes: number
  status: 'pending' | 'parsing' | 'embedding' | 'ready' | 'failed'
  error: string
  chunk_count: number
  created_at: number
}

/** Credit balance for the subscription page (§ credits). */
export interface ApiCredits {
  enabled: boolean
  timed?: { remaining: number; allowance: number; period_seconds: number; resets_at: number }
  permanent: number
  /** Global permanent-credit top-up link. */
  buy_url?: string
  /** Global tier-purchase link (shown on every group card). */
  group_buy_url?: string
}

/** A file referenced by a conversation (§ conversation files drawer). */
export interface ApiConversationFile {
  id: string
  filename: string
  kind: string
  mime_type: string
  size_bytes: number
  created_at: number
  url: string
}

export interface ApiConversation {
  id: string
  user_id: string
  project_id: string
  title: string
  provider: string
  model_id: string
  kb_ids: string[]
  rag_mode: 'auto' | 'inject' | 'tool'
  summary_blocks: unknown[]
  active_leaf_id: string
  provider_state: Record<string, unknown>
  pinned: boolean
  archived: boolean
  starred: boolean
  created_at: number
  updated_at: number
  // Inline (text-selection) sub-conversation linkage. Non-empty
  // inline_source_conv marks this as a sub-conversation anchored to an excerpt.
  inline_source_conv?: string
  inline_parent_id?: string
  inline_quote?: string
  /** §workspaces */
  workspace_id?: string
  creator_name?: string
  creator_avatar?: string
}

export type ApiBlockKind =
  | 'text'
  | 'thinking'
  | 'tool_call'
  | 'tool_output'
  | 'citation'
  | 'image'
  | 'document'
  | 'artifact'
  | 'research'

export interface ApiBlock {
  kind: ApiBlockKind
  text?: string
  tool_name?: string
  tool_id?: string
  input?: unknown
  summary?: string
  url?: string
  title?: string
  file_ref?: string
}

export interface ApiAttachment {
  id: string
  filename: string
  mime_type: string
  kind: string
  url: string
}

export interface ApiCitation {
  id: string
  index: number
  title: string
  url: string
  snippet: string
  source: 'web' | 'kb'
}

export interface ApiMessage {
  id: string
  conversation_id: string
  parent_id: string
  role: 'user' | 'assistant' | 'system'
  provider: string
  model_id: string
  /** Human-readable model name snapshotted at message creation time. Remains populated even after the model is deleted. */
  model_label?: string
  blocks: ApiBlock[]
  attachments: ApiAttachment[]
  citations: ApiCitation[]
  stop_reason: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost: number
  currency: string
  /** Credits charged for this turn (user-facing; 0 = free / credits disabled). */
  credits?: number
  status: 'streaming' | 'complete' | 'error' | 'stopped'
  error: string
  /** User rating on an assistant message: "" | "like" | "dislike". */
  feedback?: string
  /** Wall-clock generation time for the turn, in ms. */
  gen_ms?: number
  /** Verify mode (§verify): persisted secondary-auditor result (snake_case from
   *  the Go json tags). Absent when the turn was never audited. */
  verify?: ApiVerifyResult
  created_at: number
  /** Sibling navigation (only on the path response). */
  branch_index?: number
  branch_count?: number
  siblings?: string[]
  /** §workspaces — author of a user turn in a shared conversation. */
  author_id?: string
  author_name?: string
  author_avatar?: string
}

export interface ApiMemory {
  id: string
  user_id: string
  memory_text: string
  memory_type: string
  slot: string
  value: string
  status: 'ACTIVE' | 'STALE' | 'UNKNOWN_CURRENT' | 'HISTORICAL_ONLY' | 'QUERY_DEPENDENT'
  confidence: number
  source_message_ids: string[]
  supersedes: string[]
  superseded_by: string[]
  affected_domains: string[]
  reason: string
  valid_from: number
  valid_until: number
  created_at: number
  updated_at: number
}

// A single usage_logs row (one API call) for the admin usage list.
export interface ApiUsageRecord {
  id: number
  user_id: string
  user_email: string
  conversation_id: string
  conversation_title: string
  /** True when the row's conversation was deleted — show "deleted", not the id. */
  conversation_deleted: boolean
  model_id: string
  purpose: string
  input_tokens: number
  output_tokens: number
  cost: number
  currency: string
  created_at: number
  /** §workspaces */
  workspace_id?: string
  workspace_name?: string
}

/** SSE event shapes — matches §6.2. */
// §4.20 Image Generation — admin-managed style. hidden_prompt is present only in
// admin responses; the user-facing list strips it.
export interface ApiImageStyle {
  id: string
  name: string
  example_image_url: string
  hidden_prompt?: string
  enabled: boolean
  sort_order: number
  created_at: number
  updated_at: number
}

// §8.1 admin drill-down: one of a user's generated images (links to its source
// conversation). url = /api/artifacts/<id>.
export interface ApiAdminImage {
  id: string
  conversation_id: string
  conversation_title: string
  message_id: string
  filename: string
  mime_type: string
  size_bytes: number
  created_at: number
  url: string
}

/** Verify mode (§verify): one auditor finding, snake_case as it arrives on the
 *  wire (SSE `verify_finding` + persisted `message.verify.findings`). */
export interface ApiVerifyFinding {
  severity: string
  quote: string
  issue: string
}

/** Verify mode (§verify): persisted auditor result on a message (snake_case). */
export interface ApiVerifyResult {
  verdict?: 'clean' | 'issues'
  findings?: ApiVerifyFinding[]
  auditor_model_id?: string
  auditor_label?: string
  at?: number
}

export type ApiSseEvent =
  | { type: 'message_start'; message_id: string }
  | { type: 'thinking_delta'; text: string }
  | { type: 'text_delta'; text: string }
  | { type: 'tool_start'; name: string; id?: string; input?: unknown }
  | { type: 'tool_input'; name?: string; id?: string; partial_json?: string; input?: unknown }
  | { type: 'tool_result'; name: string; id?: string; summary: string; status?: 'complete' | 'error' }
  | { type: 'citation'; citation: ApiCitation }
  | { type: 'artifact'; id?: string; url?: string; title?: string; summary?: string }
  // §4.20 image mode: drawing-phase status ('optimizing' | 'generating') driving
  // the dedicated generating UI.
  | { type: 'image_status'; message_id?: string; status?: string }
  | { type: 'rag'; status?: string; summary?: string }
  | { type: 'refusal'; message_id?: string; message?: string }
  | { type: 'error'; message: string }
  | { type: 'done'; stop_reason?: string; usage?: { input_tokens: number; output_tokens: number }; credits?: number }
  // Deep Research progress (§ deep-research mode).
  | { type: 'research_plan'; message_id?: string; text?: string; summary?: string }
  | { type: 'research_task'; id: string; text?: string; status?: string; name?: string }
  | { type: 'research_source'; id: string; url?: string; title?: string; summary?: string; status?: string }
  | { type: 'research_section'; id: string; title?: string; status?: string }
  // Verify mode (§verify): auditor lifecycle. started → N findings → done.
  | { type: 'verify_started'; message_id?: string }
  | { type: 'verify_finding'; message_id?: string; finding: ApiVerifyFinding }
  | { type: 'verify_done'; message_id?: string; verdict: 'clean' | 'issues' }
