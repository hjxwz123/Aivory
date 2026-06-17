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
  /** Unix seconds of last authenticated activity. Drives admin online status. */
  last_seen_at?: number
  /** Capability flags from the user's group (e.g. "research"). Populated on the
   *  /api/me response so the client can gate features. */
  features?: string[]
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
  created_at: number
}

/** The public read-only conversation served at /share/:token. */
export interface ApiSharedConversation {
  title: string
  messages: ApiSharedMessage[]
  created_at: number
}

/** Membership tier (§ user groups). */
export interface ApiUserGroup {
  id: string
  name: string
  description: string
  features: string[]
  price_usd: number
  price_cny: number
  /** Optional external purchase/upgrade link shown on the subscription page. */
  buy_url?: string
  is_default: boolean
  sort_order: number
  /** Max projects / knowledge bases a member may create. 0 = unlimited. */
  max_projects: number
  max_kbs: number
  created_at: number
  updated_at: number
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

/** Result of POST /api/me/redeem. */
export interface ApiRedeemResult {
  ok: true
  user: ApiUser
  group_id: string
  group_name: string
  expires_at: number
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

export interface ApiChannel {
  id: string
  name: string
  type: 'openai' | 'claude' | 'gemini'
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
  system_prompt: string
  param_controls: unknown
  /** OpenAI Responses hosted tools to enable; empty/absent = use system tools (§2.3-B). */
  official_tools?: string[]
  /** model_tags ids assigned to this model — drives the picker's tag filter (§ model tags). */
  tags?: string[]
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
  updated_at: number
  /** True when the model is restricted and the user's group has no grant (§ user groups). */
  locked?: boolean
}

export interface ApiSkill {
  id: string
  name: string
  description: string
  icon: string
  instructions: string
  assets: unknown
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
  status: 'streaming' | 'complete' | 'error'
  error: string
  /** User rating on an assistant message: "" | "like" | "dislike". */
  feedback?: string
  /** Wall-clock generation time for the turn, in ms. */
  gen_ms?: number
  created_at: number
  /** Sibling navigation (only on the path response). */
  branch_index?: number
  branch_count?: number
  siblings?: string[]
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

export interface ApiUsageReportRow {
  user_id: string
  user_email: string
  conversation_id: string
  conversation_title: string
  model_id: string
  purpose: string
  input_tokens: number
  output_tokens: number
  calls: number
  cost: number
  currency: string
}

/** SSE event shapes — matches §6.2. */
export type ApiSseEvent =
  | { type: 'message_start'; message_id: string }
  | { type: 'thinking_delta'; text: string }
  | { type: 'text_delta'; text: string }
  | { type: 'tool_start'; name: string; id?: string; input?: unknown }
  | { type: 'tool_input'; name?: string; id?: string; partial_json?: string; input?: unknown }
  | { type: 'tool_result'; name: string; id?: string; summary: string; status?: 'complete' | 'error' }
  | { type: 'citation'; citation: ApiCitation }
  | { type: 'artifact'; id?: string; url?: string; title?: string; summary?: string }
  | { type: 'rag'; status?: string; summary?: string }
  | { type: 'refusal'; message_id?: string; message?: string }
  | { type: 'error'; message: string }
  | { type: 'done'; stop_reason?: string; usage?: { input_tokens: number; output_tokens: number } }
  // Deep Research progress (§ deep-research mode).
  | { type: 'research_plan'; message_id?: string; text?: string; summary?: string }
  | { type: 'research_task'; id: string; text?: string; status?: string; name?: string }
  | { type: 'research_source'; id: string; url?: string; title?: string; summary?: string; status?: string }
  | { type: 'research_section'; id: string; title?: string; status?: string }
