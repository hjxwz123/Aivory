/**
 * Wire-format types shared between the Go backend and the frontend. Keep
 * field names snake_case to match the backend JSON tags directly — frontend
 * code uses helpers in `lib/format.ts` to convert to display strings when
 * needed.
 */
import type { FeedbackReason } from '@/types/chat'

export interface ApiError {
  error: string
  retry_after?: number
}

/** Live timed-credit balance for the user's current refresh window. */
export interface ApiTimedCredits {
  remaining: number
  allowance: number
  period_seconds: number
  /** Unix seconds at which the current timed-credit window resets. */
  resets_at: number
}

export interface ApiUser {
  id: string
  email: string
  name: string
  role: 'user' | 'admin'
  status: 'active' | 'banned' | 'disabled' | 'deleting'
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
  /** Effective deployment authentication policy, attached to /me responses. */
  password_login_enabled?: boolean
  oauth_initial_password_policy?: OAuthInitialPasswordPolicy
  /** Administrator-controlled default tool policy for new conversations. */
  tool_mode_default?: 'auto' | 'disabled' | 'enabled'
  /** Unix seconds of the last password change (change/reset/first set).
   *  0 or absent = never changed since the account was created. */
  password_changed_at?: number
  /** Unix seconds of last authenticated activity. Drives admin online status. */
  last_seen_at?: number
  /** Non-expiring credit balance (§ credits) — admin-adjustable on the users page. */
  credits_permanent?: number
  /** Live timed-credit balance. Populated by the admin user-detail endpoint,
   *  not by the paginated users list. */
  credits_timed?: ApiTimedCredits
  /** Spendable timed + permanent credits after any recorded overage debt. */
  credits_available?: number
  /** Admin-defined order for the users page. */
  sort_order?: number
  /** Capability flags from the user's group (e.g. "research"). Populated on the
   *  /api/me response so the client can gate features. */
  features?: string[]
  /** Global admin master switch for long-term memory. When false, no user may
   *  enable memory and the per-user toggle is hidden. Transient (auth/me only).
   *  Absent ⇒ treat as available (older backend). */
  memory_available?: boolean
  permissions?: ApiUserGroupPermissions
  created_at: number
}

/** Admin analytics (§ admin → analytics). */
export interface ApiUsageTotals {
  calls: number
  turns: number
  credit_charged_turns: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  images_count: number
  cost: number
  credits: number
  turn_cost: number
  credit_charged_cost: number
  users: number
  credit_charged_users: number
  conversations: number
  workspaces: number
}
export interface ApiUsageTrendPoint {
  bucket_start: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  images_count: number
  calls: number
  turns: number
  users: number
  cost: number
  credits: number
}
export interface ApiUsageBreakdownRow {
  key: string
  label: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  images_count: number
  calls: number
  turns: number
  credit_charged_turns: number
  users: number
  conversations: number
  cost: number
  credits: number
}
export type ApiAnalyticsDimension = 'model' | 'user' | 'workspace' | 'purpose' | 'channel'
export interface ApiAnalytics {
  days: number
  bucket: number
  generated_at: number
  period_start: number
  period_end: number
  previous_period_start: number
  previous_period_end: number
  totals: ApiUsageTotals
  previous_totals: ApiUsageTotals
  trend: ApiUsageTrendPoint[]
  previous_trend: ApiUsageTrendPoint[]
  breakdowns: Record<ApiAnalyticsDimension, ApiUsageBreakdownRow[]>
  filter_options: Record<ApiAnalyticsDimension, ApiUsageBreakdownRow[]>
}

export type ApiMessageFeedbackRating = 'like' | 'dislike'

export interface ApiAdminMessageFeedbackSummary {
  total: number
  likes: number
  dislikes: number
  positive_rate: number
  assistant_messages: number
  rated_messages: number
  coverage: number
}

export interface ApiAdminMessageFeedbackModel {
  model_id: string
  model_label: string
  total: number
  likes: number
  dislikes: number
  positive_rate: number
  top_reason: FeedbackReason | ''
  sample_sufficient: boolean
}

export interface ApiAdminMessageFeedbackItem {
  id: string
  message_id: string
  question_id: string
  conversation_id: string
  conversation_title: string
  conversation_owner_id: string
  user_id: string
  user_name?: string
  user_email: string
  workspace_id: string
  workspace_name: string
  model_id: string
  model_label: string
  channel_id: string
  channel_name: string
  rating: ApiMessageFeedbackRating
  reasons: FeedbackReason[]
  comment: string
  question: string
  response: string
  provider: string
  gen_ms: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  cost: number
  currency: string
  credits: number
  has_tools: boolean
  has_files: boolean
  has_rag: boolean
  tool_names?: string[]
  file_names?: string[]
  citation_titles?: string[]
  fallback: boolean
  status: string
  error?: string
  message_created_at: number
  created_at: number
  updated_at: number
}

export interface ApiAdminMessageFeedbackPage {
  summary: ApiAdminMessageFeedbackSummary
  by_model: ApiAdminMessageFeedbackModel[]
  items: ApiAdminMessageFeedbackItem[]
  total: number
  limit: number
  offset: number
}

/** One active sign-in (§ account → active sessions). `id` is the stable
 * refresh-session family id, used as the opaque handle to revoke it. `location` is best-effort and may
 * be empty when no geo-providing proxy is in front of the server. */
export interface ApiSession {
  id: string
  ip: string
  user_agent: string
  location: string
  created_at: number
  last_seen: number
}

/** One successful authentication event in an administrator's user audit view. */
export interface ApiAdminLoginHistoryEntry {
  id: string
  user_id: string
  login_at: number
  ip: string
  location: string
  user_agent: string
  /** Known methods are listed explicitly; keep string extensibility so a newer
   * server method still renders instead of being dropped by the client. */
  method: 'password' | 'password_2fa' | 'oauth' | 'oauth_2fa' | (string & {})
}

export interface ApiAdminLoginHistoryPage {
  items: ApiAdminLoginHistoryEntry[]
  total: number
  limit: number
  offset: number
}

/** Owner-facing descriptor of a conversation's public share (§ sharing). */
export interface ApiShareInfo {
  id: string
  created_at: number
}

/** One message in a public share snapshot. It is cost-stripped and carries only
 *  the display identity needed by the transcript; ids/email/provider details
 *  remain private. Identity fields are optional for legacy snapshots. */
export interface ApiSharedMessage {
  role: 'user' | 'assistant'
  blocks: ApiBlock[]
  citations: ApiCitation[]
  /** Uploaded attachments (id/filename/kind/url). Absent on snapshots created
   *  before shares carried assets — re-share to include uploads. */
  attachments?: ApiAttachment[]
  created_at: number
  author_name?: string
  author_avatar?: string
  model_label?: string
  model_icon?: string
  /** Fast turns preserve the product-wide model-identity masking contract. */
  fast?: boolean
}

/** The public read-only conversation served at /share/:token. */
export interface ApiSharedConversation {
  title: string
  messages: ApiSharedMessage[]
  created_at: number
}

/** Workspace (§workspaces) — fully-isolated collaborative space. */
export type ApiWorkspaceRole = 'admin' | 'member' | 'guest'
export interface ApiWorkspace {
  id: string
  name: string
  owner_id: string
  /** Deprecated compatibility field. Workspace joins use governed invite records. */
	invite_token?: string
  created_at: number
  /** Requesting user's role (owner rows read as 'admin'). */
  role?: ApiWorkspaceRole
  /** Whether the requesting user is the canonical workspace owner. */
  is_owner?: boolean
  member_count?: number
  owner_name?: string
  can_create_projects: boolean
  can_private_conversations: boolean
  can_create_skills_prompts: boolean
  can_create_kb: boolean
  can_add_kb_files: boolean
  can_delete_kb_content: boolean
  can_delete_conversations: boolean
}

export interface ApiWorkspaceMember {
  user_id: string
  role: ApiWorkspaceRole
  /** Canonical workspace owner marker (their role is always 'admin'). */
  is_owner: boolean
  joined_at: number
  name: string
  email: string
  avatar_url: string
  can_create_projects: boolean
  can_private_conversations: boolean
  can_create_skills_prompts: boolean
  can_create_kb: boolean
  can_add_kb_files: boolean
  can_delete_kb_content: boolean
  can_delete_conversations: boolean
}

export interface ApiWorkspaceMemberPermissions {
  can_create_projects: boolean
  can_private_conversations: boolean
  can_create_skills_prompts: boolean
  can_create_kb: boolean
  can_add_kb_files: boolean
  can_delete_kb_content: boolean
  can_delete_conversations: boolean
}

/** §workspace RBAC phase 3 — invite record (admin-only surface). */
export interface ApiWorkspaceInvite {
  id: string
  workspace_id: string
  token: string
  email: string
  role: ApiWorkspaceRole
  expires_at: number
  max_uses: number
  used_count: number
  created_by: string
  creator_name?: string
  revoked_at: number
  created_at: number
}

/** §workspace RBAC phase 4 — workspace capability policy. */
export interface ApiWorkspacePolicy {
  WorkspaceID: string
  AllowedModelIDs: string[]
  AllowedToolIDs: string[]
  AllowedMCPServerIDs: string[]
  AllowSandbox: boolean
  AllowImageGeneration: boolean
  AllowKnowledgeBases: boolean
  AllowFileUpload: boolean
  MemberMonthlyCreditLimit: number
  UpdatedBy: string
  UpdatedAt: number
}

export interface ApiWorkspaceUsageRow {
  user_id: string
  name: string
  email: string
  messages: number
  input_tokens: number
  output_tokens: number
  credits: number
}

/** §workspace RBAC phase 5 — audit trail row (admin-only surface). */
export interface ApiWorkspaceAuditLog {
  id: string
  workspace_id: string
  actor_user_id: string
  actor_name: string
  action: string
  target_type: string
  target_id: string
  metadata: Record<string, unknown>
  created_at: number
}

/** Membership tier (§ user groups). */
export interface ApiUserGroup {
  id: string
  name: string
  description: string
  features: string[]
  /** Monthly price in the deployment settlement currency's smallest unit. */
  monthly_price_amount_minor: number
  /** Yearly price in the deployment settlement currency's smallest unit. */
  yearly_price_amount_minor: number
  /** Global, read-only settlement currency attached to each group response. */
  settlement_currency: string
  is_default: boolean
  sort_order: number
  /** Max projects / knowledge bases a member may create. 0 = unlimited. */
  max_projects: number
  max_kbs: number
  /** Credit system (§ credits): per-group timed allowance + refresh cycle (unused
   *  voided). The internal USD→credit rate is a global setting. */
  credit_allowance: number
  credit_period_seconds: number
  created_at: number
  updated_at: number
  max_workspaces?: number
  /** Storage cap for non-image uploads, MB. 0 = unlimited (§ user files page). */
  max_storage_mb?: number
  /** Listed on the public subscription page. */
  is_public?: boolean
  /** Whether users may purchase this group while it is listed. */
  is_purchasable?: boolean
  permissions?: ApiUserGroupPermissions
}

export type ApiResourceAccessMode = 'all' | 'selected' | 'none'
export interface ApiResourceAccessPolicy {
  mode: ApiResourceAccessMode
  ids: string[]
}
export interface ApiUserGroupPermissions {
  prompts: ApiResourceAccessPolicy
  skills: ApiResourceAccessPolicy
  tools: ApiResourceAccessPolicy
  allow_sharing: boolean
  allow_knowledge_bases: boolean
  allow_knowledge_base_sharing: boolean
  allow_file_upload: boolean
  allow_conversation_export: boolean
  allow_conversation_deletion: boolean
  allow_voice_transcription: boolean
  allow_memory: boolean
  allow_drawing: boolean
}

export interface ApiKnowledgeBaseShare {
  kb_id: string
  user_id: string
  role?: 'read' | 'write'
  name: string
  email: string
  avatar_url?: string
  created_at?: number
  updated_at?: number
}

export interface ApiKnowledgeBaseUploader {
  user_id: string
  name: string
  email: string
  avatar_url?: string
}

export interface ApiWorkspaceKnowledgeBaseMemberPermission {
  kb_id: string
  user_id: string
  role: ApiWorkspaceRole
  is_owner: boolean
  name: string
  email: string
  avatar_url?: string
  can_add_files: boolean
  can_delete_content: boolean
  total_can_add_kb_files: boolean
  total_can_delete_kb_content: boolean
  locked: boolean
}

export interface ApiGroupUsersPage {
  users: ApiGroupUserSummary[]
  total: number
  limit: number
  offset: number
}

export interface ApiGroupUserSummary {
  id: string
  email: string
  name: string
  role: ApiUser['role']
  status: ApiUser['status']
}

/** Purchasable package of permanent (non-expiring) credits. */
export interface ApiCreditPackage {
  id: string
  name: string
  description: string
  credits: number
  /** Price in the deployment settlement currency's smallest unit. */
  price_amount_minor: number
  /** Global, read-only settlement currency attached to each package response. */
  settlement_currency: string
  enabled: boolean
  sort_order: number
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
 *  for `duration_days` (0 = permanent), or — when `kind` is 'credits' — a
 *  fixed amount of permanent credits. `code` is the human-typeable string. */
export interface ApiRedeemCode {
  id: string
  code: string
  /** 'group' grants a membership tier; 'credits' adds permanent credits. */
  kind: 'group' | 'credits'
  /** For 'credits' codes this holds a placeholder (default group) — ignore it. */
  group_id: string
  duration_days: number
  /** Permanent credits granted per redemption ('credits' codes only). */
  credits: number
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
  /** Permanent credits added ('credits' codes only; 0 for group codes). */
  credits: number
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
  /** 'credits' when the code added permanent credits instead of a group. */
  kind?: 'group' | 'credits'
  /** Permanent credits added (credits redemptions only). */
  credits?: number
  group_id?: string
  group_name?: string
  expires_at?: number
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

/** Browser session probe. Logged-out is a normal result, not an HTTP error. */
export interface ApiAuthSessionResponse {
  authenticated: boolean
  user?: ApiUser
  access_token?: string
  expires_at?: number
}

export type AuthEntryMode = 'login_page' | 'provider_picker' | 'auto_redirect'
export type OAuthInitialPasswordPolicy = 'required' | 'optional' | 'disabled'

/** Public authentication policy used before a session exists. */
export interface ApiAuthPolicy {
  password_login_enabled: boolean
  entry_mode: AuthEntryMode
  default_provider: ApiPublicOAuthProvider | null
  oauth_initial_password_policy: OAuthInitialPasswordPolicy
  oauth_auto_provision_enabled: boolean
  providers: ApiPublicOAuthProvider[]
}

export type OAuthKind = 'google' | 'github' | 'apple' | 'oauth2' | 'oidc'

/** Full provider record (admin view). client_secret is never returned. */
export interface ApiOAuthProvider {
  id: string
  kind: OAuthKind
  name: string
  icon: string
  client_id: string
  has_secret: boolean
  issuer_url: string
  jwks_url: string
  auth_url: string
  token_url: string
  userinfo_url: string
  scopes: string
  team_id: string
  key_id: string
  enabled: boolean
  sort_order: number
  updated_at: number
  /** Canonical URI computed by the server, including OAUTH_CALLBACK_BASE_URL. */
  redirect_uri: string
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

export interface ApiChannelModelImportResult {
  discovered: number
  created: number
  skipped_existing: number
  skipped_unsupported: number
}

export type ApiChannelModelKind = 'chat' | 'image' | 'embedding'

export interface ApiChannelModelCandidate {
  request_id: string
  label: string
  description: string
  kind: ApiChannelModelKind
}

export interface ApiChannelModelDiscoveryResult {
  models: ApiChannelModelCandidate[]
  discovered: number
  skipped_unsupported: number
}

export interface ApiChannelModelBatchResult {
  requested: number
  created: number
  skipped_existing: number
  skipped_duplicate: number
}

/** One configuration check returned by the per-administrator setup guide. */
export interface ApiAdminOnboardingStep {
  id: 'channel' | 'chat_model' | 'default_model' | 'task_model' | 'tool_route_model' | 'embedding' | 'search' | 'sandbox' | 'smtp'
  complete: boolean
}

/** Current administrator's setup-guide state plus live deployment readiness. */
export interface ApiAdminOnboarding {
  deployment_profile: 'personal' | 'full'
  status: 'unseen' | 'dismissed' | 'completed'
  required: ApiAdminOnboardingStep[]
  optional: ApiAdminOnboardingStep[]
  full_optional: ApiAdminOnboardingStep[]
}

export type ApiPaymentProvider = 'stripe' | 'epay' | 'waffo'
export type ApiPaymentEnvironment = 'live' | 'test'

/** Admin-only payment gateway credentials. Sensitive config values are returned
 * as the `••••••` sentinel; sending that value back keeps the saved secret. */
export interface ApiPaymentChannel {
  id: string
  name: string
  provider: ApiPaymentProvider
  environment: ApiPaymentEnvironment
  config: Record<string, unknown>
  enabled: boolean
  sort_order: number
  /** Absolute callback URL to register in the upstream provider console. */
  webhook_url?: string
  created_at: number
  updated_at: number
}

/** A user-facing payment choice bound to exactly one configured channel. */
export interface ApiPaymentMethod {
  id: string
  name: string
  icon: string
  channel_id: string
  provider: ApiPaymentProvider
  provider_method_config: Record<string, unknown>
  enabled: boolean
  sort_order: number
  created_at: number
  updated_at: number
}

/** Enabled payment choice exposed to signed-in buyers. Channel credentials and
 * provider-specific method configuration are intentionally admin-only. */
export interface ApiPublicPaymentMethod {
  id: string
  name: string
  icon: string
  provider: ApiPaymentProvider
}

export interface ApiPaymentCheckoutAction {
  type: 'redirect' | 'form_post'
  url: string
  fields?: Record<string, string>
}

export type ApiPaymentOrderStatus =
  | 'pending'
  | 'processing'
  | 'fulfilled'
  | 'failed'
  | 'expired'
  | 'cancelled'
export type ApiPaymentTargetType = 'credit_package' | 'user_group'

/** Public order state after the API maps internal `fulfilled` to `paid` and
 * internal `cancelled` to `expired`. */
export type ApiUserPaymentOrderStatus =
  | 'pending'
  | 'processing'
  | 'paid'
  | 'failed'
  | 'expired'

/** Immutable payment-order snapshot shown in the admin audit table. */
export interface ApiPaymentOrder {
  id: string
  user_email: string
  target_type: ApiPaymentTargetType
  target_name: string
  billing_cycle: '' | 'monthly' | 'yearly'
  amount_minor: number
  tax_amount_minor?: number
  currency: string
  channel_name: string
  method_name: string
  provider: ApiPaymentProvider
  environment: ApiPaymentEnvironment
  provider_order_id?: string
  checkout_session_id?: string
  checkout_expires_at?: number | null
  last_reconciled_at?: number | null
  reconcile_error?: string
  status: ApiPaymentOrderStatus
  can_delete?: boolean
  delete_requires_gateway_confirmation?: boolean
  created_at: number
  paid_at?: number | null
  fulfilled_at?: number | null
  failure_reason?: string
}

/** Buyer-visible order snapshot. It never exposes channel or method config. */
export interface ApiUserPaymentOrder {
  id: string
  status: ApiUserPaymentOrderStatus
  can_resume: boolean
  can_retry: boolean
  resume_mode?: 'original_session' | 'retry_submission'
  /** Compatibility field used by older payment backends. */
  resume_kind?: 'continue' | 'retry'
  target_type: ApiPaymentTargetType
  target_name: string
  billing_cycle: '' | 'monthly' | 'yearly'
  amount_minor: number
  tax_amount_minor?: number
  currency: string
  provider: ApiPaymentProvider
  method_name: string
  method_type: string
  failure_reason?: string
  checkout_expires_at?: number
  created_at: number
  paid_at?: number
  fulfilled_at?: number
}

/** An admin-managed model tag (§ model tags) used to filter the picker. */
export interface ApiModelTag {
  id: string
  name: string
  sort_order: number
  created_at: number
}

/** Admin-defined provider-hosted tool request fragment. This shape is returned
 * only by admin model endpoints; public model responses expose tools_available. */
export interface ApiOfficialToolDefinition {
  name: string
  icon: string
  request?: Record<string, unknown>
}

/** One platform-managed function-calling tool exposed to model administrators. */
export interface ApiBuiltinTool {
  name: string
  description: string
  /** Effective instance-level availability. Absent on older servers means enabled. */
  globally_enabled?: boolean
}

export interface ApiModel {
  id: string
  channel_id: string
  kind: 'chat' | 'image' | 'embedding'
  request_id: string
  label: string
  description: string
  icon: string
  /** Backup channel retried when a request on the primary channel fails; '' = none (§fallback channel). */
  fallback_channel_id?: string
  enabled: boolean
  sort_order: number
  tool_mode: 'native' | 'prompt' | 'none'
  vision: boolean
  stream: boolean
  /** Whether this chat model exposes Deep Research in the composer. Absent from older backends ⇒ enabled. */
  research_enabled?: boolean
  /** §fast-mode: THE fast model. Present only on the ADMIN model list (the public
   *  /api/models list filters the fast model out of the advanced picker). */
  fast?: boolean
  system_prompt: string
  param_controls: unknown
  /** Platform tool defaults. Admin responses use `null`/omitted to select all
   * registered tools (including future additions) by default and `[]` for none;
   * public responses return defaults after global and group policy filtering. */
  builtin_tools?: string[] | null
  /** MCP service defaults. Admin responses use `null`/omitted or `[]` for none;
   * only explicitly listed services are selected by default. Explicit arrays
   * retain unavailable IDs so a temporary outage or global disable never
   * silently rewrites the administrator's policy. */
  mcp_server_ids?: string[] | null
  /** Unified public capability bit. True when the administrator configured at
   * least one available local Function, provider-hosted tool, or MCP service. */
  tools_available?: boolean
  /** Optional chat-model JSON object merged into the upstream provider request. */
  extra_params?: Record<string, unknown>
  /** Provider-hosted tools offered by this model. Present only in admin model
   * responses; public responses expose the unified tools_available bit. */
  official_tools?: ApiOfficialToolDefinition[]
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
  /** Optional automatic-compaction trigger for this chat model. 0/absent uses
   * the global threshold, and positive overrides remain bounded by its cap. */
  compaction_token_threshold?: number
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

/** User-safe tool metadata for the per-model tool picker. The backend keeps
 * implementation/source details private so every tool is presented uniformly. */
export interface ApiSelectableTool {
  id: string
  name: string
  description: string
  icon: string
  /** False when the group may see this candidate but cannot select or invoke it. */
  allowed?: boolean
  /** True when the serving model selects this tool when the user has not saved
   * an explicit override. Availability and defaults are intentionally separate. */
  default_selected?: boolean
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
  /** Short catalog copy used only for display. The existing `description`
   * remains the model-facing trigger description. */
  display_description?: string
  description: string
  icon: string
  instructions: string
  assets: ApiSkillAsset[]
  enabled: boolean
  sort_order: number
  updated_at: number
}

/** Enabled administrator skill metadata visible to signed-in users. This
 * listing excludes trigger descriptions, instructions, and assets. */
export interface ApiPublicSkill {
  id: string
  name: string
  display_description: string
  icon: string
  enabled: boolean
  sort_order: number
}

export interface ApiPrompt {
  id: string
  name: string
  description: string
  content: string
  enabled: boolean
  sort_order: number
  updated_at: number
}

export interface ApiUserSkill {
  id: string
  workspace_id?: string
  can_manage: boolean
  name: string
  description: string
  /** Current administrator catalog copy for imported skills. When the
   * administrator leaves it empty, the API falls back to the source skill's
   * model-facing description. Personal skills omit this field. */
  display_description?: string
  icon: string
  instructions: string
  source_skill_id?: string
  created_at: number
  updated_at: number
}

export interface ApiUserPrompt {
  id: string
  workspace_id?: string
  can_manage: boolean
  name: string
  description: string
  content: string
  source_prompt_id?: string
  created_at: number
  updated_at: number
}

export interface ApiLibraryCatalogSkill {
  id: string
  name: string
  description?: string
  /** Omitted for migrated administrator skills that have not yet received
   * catalog copy. The catalog's optional `description` is also safe display metadata. */
  display_description?: string
  icon?: string
  source?: 'admin'
  added?: boolean
}

export interface ApiLibraryCatalogPrompt {
  id: string
  name: string
  description: string
  icon?: string
  added?: boolean
}

export interface ApiLibraryCatalog {
  skills: ApiLibraryCatalogSkill[]
  prompts: ApiLibraryCatalogPrompt[]
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
  /** §workspace RBAC: shared with the workspace (true) or creator-private. */
  is_public?: boolean
  /** Effective project-library capabilities. Detail responses always include
   * these; list/create/update responses may omit them. */
  can_upload_files?: boolean
  can_delete_content?: boolean
  can_delete?: boolean
}

export interface ApiKnowledgeBase {
  id: string
  user_id: string
  name: string
  description: string
  workspace_id?: string
  /** §workspace RBAC: shared with the workspace (true) or creator-private. */
  is_public?: boolean
  access_role?: 'owner' | 'read' | 'write' | 'workspace'
  owner_name?: string
  can_share?: boolean
  can_upload?: boolean
  can_delete?: boolean
  can_delete_content?: boolean
  can_manage_members?: boolean
  project_id?: string
  created_at: number
}

/** Full knowledge-base record returned only by administrator drill-down APIs. */
export interface ApiAdminKnowledgeBase extends ApiKnowledgeBase {
  embedding_model_id: string
  embedding_dim: number
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
  /** Stable machine code derived from the ingest error before the raw
   *  diagnostics are blanked (see DOCUMENT_PARSER_NOT_CONFIGURED). */
  error_code?: string
  chunk_count: number
  uploaded_by_user_id?: string
  uploaded_by_name?: string
  uploaded_by_email?: string
  can_delete?: boolean
  created_at: number
}

/** Shared paginated envelope for administrator-wide content inventories. */
export interface ApiAdminResourcePage<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

/** Administrator projection of one standalone knowledge base and its owner. */
export interface ApiAdminKnowledgeBaseResource {
  id: string
  name: string
  description: string
  creator_id: string
  creator_name: string
  creator_email: string
  creator_avatar_url?: string
  workspace_id?: string
  workspace_name?: string
  workspace_owner_id?: string
  workspace_owner_name?: string
  workspace_owner_email?: string
  embedding_model_id: string
  embedding_model_label: string
  embedding_model_enabled: boolean
  embedding_dim: number
  document_count: number
  ready_document_count: number
  failed_document_count: number
  processing_document_count: number
  total_size_bytes: number
  chunk_count: number
  share_count: number
  created_at: number
  last_activity_at: number
}

export interface ApiAdminKnowledgeBaseResourceDetail extends ApiAdminKnowledgeBaseResource {
  shares: ApiKnowledgeBaseShare[]
}

/** Administrator projection of one project and its dedicated knowledge base. */
export interface ApiAdminProjectResource {
  id: string
  name: string
  description: string
  accent: string
  emoji: string
  pinned: boolean
  auto_add_uploads: boolean
  creator_id: string
  creator_name: string
  creator_email: string
  creator_avatar_url?: string
  workspace_id?: string
  workspace_name?: string
  workspace_owner_id?: string
  workspace_owner_name?: string
  workspace_owner_email?: string
  kb_id?: string
  kb_name?: string
  kb_description?: string
  embedding_model_id?: string
  embedding_model_label?: string
  embedding_model_enabled: boolean
  embedding_dim: number
  document_count: number
  ready_document_count: number
  failed_document_count: number
  processing_document_count: number
  total_size_bytes: number
  chunk_count: number
  conversation_count: number
  active_conversation_count: number
  archived_conversation_count: number
  created_at: number
  updated_at: number
  last_activity_at: number
}

export interface ApiAdminProjectResourceDetail extends ApiAdminProjectResource {
  instructions: string
}

export interface ApiAdminProjectConversation {
  id: string
  creator_id: string
  creator_name: string
  creator_email: string
  creator_avatar_url?: string
  title: string
  provider: string
  model_id: string
  model_label: string
  fast: boolean
  pinned: boolean
  archived: boolean
  starred: boolean
  is_public: boolean
  workspace_id?: string
  created_at: number
  updated_at: number
}

export interface ApiAdminGeneratedImageResource {
  id: string
  conversation_id: string
  conversation_title: string
  message_id: string
  filename: string
  mime_type: string
  size_bytes: number
  created_at: number
  user_id: string
  user_email: string
  user_name: string
  workspace_id: string
  workspace_name: string
  model_id: string
  model_label: string
  prompt: string
  url: string
}

export interface ApiAdminGeneratedImageModel {
  id: string
  label: string
}

export interface ApiAdminGeneratedImagePage
  extends ApiAdminResourcePage<ApiAdminGeneratedImageResource> {
  models: ApiAdminGeneratedImageModel[]
}

/** Credit balance for the subscription page (§ credits). */
export interface ApiCredits {
  enabled: boolean
  timed?: ApiTimedCredits
  permanent: number
  /** Total amount that can be spent now; never negative. */
  available?: number
  settlement_currency: string
}

export interface ApiCreditAdjustmentNotification {
  id: string
  direction: 'add' | 'remove'
  amount: number
  reason: string
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
  draft: boolean
  document_id?: string
  document_status?: ApiDocument['status']
  document_error?: string
  /** Machine code for the draft document's ingest failure — survives page
   *  refresh so the composer chip can render the right localized reason. */
  document_error_code?: string
}

export interface ApiConversation {
  id: string
  user_id: string
  project_id: string
  title: string
  provider: string
  model_id: string
  /** §fast-mode: conversation runs in fast mode (model hidden). */
  fast?: boolean
  kb_ids: string[]
  rag_mode: 'auto' | 'inject'
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
  /** Optional for rolling upgrades and cached responses from older servers. */
  is_public?: boolean
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
  /** Total observable reasoning time, stored only on the first thinking block. */
  thinking_ms?: number
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
  document_id?: string
  /** The referenced conversation file was deleted after this message was sent. */
  deleted?: boolean
}

export interface ApiCitation {
  id: string
  index: number
  title: string
  url: string
  snippet: string
  source: 'web' | 'kb' | 'document'
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
  /** §fast-mode: this turn ran in fast mode; the server blanks model_id/model_label
   *  so the client renders "快速". */
  fast?: boolean
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
  feedback?: '' | 'like' | 'dislike'
  /** Optional structured context supplied with a dislike. */
  feedback_reasons?: FeedbackReason[]
  feedback_comment?: string
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

// One row of the admin file inventory (§ admin files): the union of the files
// table (conversation attachments) and documents (KB docs). A conversation
// document sharing its storage path with a files row is folded into that row.
export interface ApiAdminFile {
  id: string
  source: 'file' | 'document'
  origin: 'conversation' | 'kb'
  user_id: string
  user_email: string
  user_name: string
  filename: string
  mime_type: string
  size_bytes: number
  created_at: number
  conversation_id: string
  kb_id: string
  kb_name: string
}

/** A user-submitted product issue report. Screenshot bytes are fetched lazily. */
export interface ApiAdminUserFeedback {
  id: string
  user_id: string
  user_email: string
  user_name: string
  message_id: string
  conversation_id: string
  conversation_title: string
  description: string
  page_path: string
  user_agent: string
  viewport_width: number
  viewport_height: number
  screenshot_mime: string
  screenshot_width: number
  screenshot_height: number
  screenshot_size: number
  has_screenshot: boolean
  created_at: number
}

export interface ApiAdminUserFeedbackPage {
  items: ApiAdminUserFeedback[]
  total: number
  limit: number
  offset: number
}

// A single usage_logs row (one API call) for the admin usage list.
export interface ApiUsageRecord {
  id: number
  user_id: string
  user_name: string
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
  /** §fallback channel: which channel served the request, whether it was the
   *  model's fallback, and ok|error (error requests are logged too). */
  channel_id?: string
  channel_name?: string
  fallback?: boolean
  /** §4.6-C: display name of the model a TTFT timeout-fallback switched to for this
   *  row; '' = no model fallback. Distinct from `fallback` (same-model channel retry). */
  ttft_fallback_model?: string
  status?: string
  /** Upstream failure detail for status='error' rows (admin-only; may embed provider bodies). */
  error?: string
  /** Sanitized upstream request diagnostics for failed rows. */
  request_method?: string
  request_url?: string
  request_headers?: string
  request_body?: string
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
  | { type: 'rag'; status?: string; summary?: string; source_count?: number }
  | { type: 'refusal'; message_id?: string; message?: string }
  | { type: 'error'; message: string; code?: string; thinking_ms?: number }
  | {
      type: 'done'
      stop_reason?: string
      usage?: { input_tokens: number; output_tokens: number }
      credits?: number
      thinking_ms?: number
    }
  // Deep Research progress (§ deep-research mode).
  | { type: 'research_plan'; message_id?: string; text?: string; summary?: string }
  | { type: 'research_task'; id: string; text?: string; status?: string; name?: string }
  | { type: 'research_source'; id: string; url?: string; title?: string; summary?: string; status?: string }
  | { type: 'research_section'; id: string; title?: string; status?: string }
  // Verify mode (§verify): auditor lifecycle. started → N findings → done.
  | { type: 'verify_started'; message_id?: string }
  | { type: 'verify_finding'; message_id?: string; finding: ApiVerifyFinding }
  | { type: 'verify_done'; message_id?: string; verdict: 'clean' | 'issues' }
