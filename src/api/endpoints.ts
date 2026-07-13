/**
 * High-level endpoint wrappers. Each function returns the same shape the
 * backend returns, with a small typed helper signature. Group by feature so
 * the call sites stay readable.
 */
import { api, apiUrl, getAccessToken, ApiError, apiUpload, type UploadProgress } from './client'
import type {
  ApiAdminFile,
  ApiWorkspace,
  ApiWorkspaceMember,
  ApiAnalytics,
  ApiAuthResponse,
  ApiChannel,
  ApiConversation,
  ApiConversationFile,
  ApiCredits,
  ApiDocument,
  ApiKnowledgeBase,
  ApiMemory,
  ApiMessage,
  ApiModel,
  ApiModelTag,
  ApiModelQuota,
  ApiImageStyle,
  ApiAdminImage,
  ApiOAuthProvider,
  ApiProject,
  ApiRedeemCode,
  ApiRedeemRedemption,
  ApiRedeemResult,
  ApiSession,
  ApiUserGroup,
  ApiPublicOAuthProvider,
  ApiOAuthIdentity,
  ApiShareInfo,
  ApiSharedConversation,
  ApiSkill,
  ApiSkillAsset,
  ApiUsageRecord,
  ApiUser,
} from './types'
import { envNum } from '@/lib/env-config'

// ----- Auth ----------------------------------------------------------------

export const authApi = {
  signupOpen: () => api<{ open: boolean; captcha_required: boolean }>('/public/signup-open'),
  /** Fetch a fresh slider-puzzle captcha (drag the piece into the gap). */
  captcha: () =>
    api<{
      id: string
      background: string
      piece: string
      w: number
      h: number
      piece_size: number
      piece_y: number
    }>('/public/captcha'),
  /** Whether the deployment still needs its first-run setup (zero users). */
  needsSetup: () => api<{ needs_setup: boolean }>('/public/needs-setup'),
  /** Create the first account (admin) on a fresh deployment, then sign in. */
  setup: (name: string, email: string, password: string) =>
    api<ApiAuthResponse>('/setup', { method: 'POST', body: { name, email, password } }),
  me: () => api<ApiUser>('/me'),
  /** § user files page: storage meter + unified upload inventory. */
  myStorage: () => api<{ used_bytes: number; quota_bytes: number }>('/me/storage'),
  myFiles: (params: { search?: string; origin?: string; sort?: string; order?: string; limit?: number; offset?: number } = {}) => {
    const qs = new URLSearchParams()
    if (params.search) qs.set('search', params.search)
    if (params.origin && params.origin !== 'all') qs.set('origin', params.origin)
    if (params.sort) qs.set('sort', params.sort)
    if (params.order) qs.set('order', params.order)
    if (params.limit) qs.set('limit', String(params.limit))
    if (params.offset) qs.set('offset', String(params.offset))
    return api<{ files: ApiAdminFile[]; total: number; limit: number; offset: number }>(
      `/me/files${qs.toString() ? `?${qs}` : ''}`,
    )
  },
  deleteMyFiles: (items: Array<{ source: 'file' | 'document'; id: string }>) =>
    api<{ deleted: number }>('/me/files/delete', { method: 'POST', body: { items } }),
  myFileContentBlob: async (source: 'file' | 'document', id: string): Promise<Blob> => {
    const token = getAccessToken()
    const res = await fetch(apiUrl(`/me/files/content?source=${source}&id=${encodeURIComponent(id)}`), {
      credentials: 'include',
      headers: token ? { authorization: `Bearer ${token}` } : {},
    })
    if (!res.ok) throw new ApiError(res.status, `preview failed (${res.status})`, null)
    return res.blob()
  },
  /** Credit balance (timed pool + permanent pool) for the subscription page. */
  credits: () => api<ApiCredits>('/me/credits'),
  login: (email: string, password: string) =>
    api<ApiAuthResponse | { totp_required: true; ticket: string }>('/auth/login', {
      method: 'POST',
      body: { email, password },
    }),
  /** Complete a 2FA-gated login with the ticket from the password step. */
  loginTwoFactor: (ticket: string, code: string) =>
    api<ApiAuthResponse>('/auth/login/2fa', { method: 'POST', body: { ticket, code } }),
  /** Begin 2FA setup — returns the secret + otpauth URI for the authenticator. */
  setup2fa: () => api<{ secret: string; otpauth_url: string }>('/me/2fa/setup', { method: 'POST' }),
  enable2fa: (code: string) => api<{ ok: true }>('/me/2fa/enable', { method: 'POST', body: { code } }),
  disable2fa: (code: string) => api<{ ok: true }>('/me/2fa/disable', { method: 'POST', body: { code } }),
  register: (email: string, password: string, name: string, captchaToken?: string) =>
    api<ApiAuthResponse | { verification_required: boolean; email: string }>('/auth/register', {
      method: 'POST',
      body: { email, password, name, captcha_token: captchaToken },
    }),
  // Verify a slider-puzzle solution; on success returns a single-use pass token
  // the register call presents (lets the dialog show immediate green/red).
  captchaVerify: (id: string, fraction: number) =>
    api<{ ok: boolean; token?: string }>('/public/captcha/verify', { method: 'POST', body: { id, fraction } }),
  refresh: () => api<ApiAuthResponse>('/auth/refresh', { method: 'POST' }),
  logout: () => api<{ ok: true }>('/auth/logout', { method: 'POST' }),
  updateProfile: (patch: { name?: string; email?: string }) =>
    api<ApiUser>('/me', { method: 'PATCH', body: patch }),
  changePassword: (current_password: string, new_password: string) =>
    api<{ ok: true }>('/me/password', { method: 'PATCH', body: { current_password, new_password } }),
  /** Set the FIRST password for an OAuth account that has none (no current
   *  password required; the session stays valid). */
  setPassword: (new_password: string) =>
    api<{ ok: true }>('/me/password/set', { method: 'POST', body: { new_password } }),
  /** Upload a profile avatar (PNG/JPG). Returns the served URL to store in
   *  the user's settings (avatar_url). */
  uploadAvatar: (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return api<{ url: string; filename: string }>('/me/avatar', { method: 'POST', body: fd })
  },
  getSettings: () => api<Record<string, unknown>>('/me/settings'),
  updateSettings: (patch: Record<string, unknown>) =>
    api<Record<string, unknown>>('/me/settings', { method: 'PATCH', body: patch }),
  // Global announcement (§ announcement). enabled=false when none is active.
  // Pinned top bar (bar_*) is independent of the popup.
  announcement: () =>
    api<{
      enabled: boolean
      body: string
      image_url: string
      remember_dismiss: boolean
      updated_at: number
      bar_enabled: boolean
      bar_html: string
      bar_updated_at: number
    }>('/announcement'),
  // Cost is intentionally NOT exposed to users — only message volume.
  usage: () => api<{ days: number; messages: number }>('/me/usage'),
  // Active sessions (§ account → active sessions). `current` is the jti of the
  // session making the request, so the UI can mark "This device".
  sessions: () => api<{ sessions: ApiSession[]; current: string }>('/auth/sessions'),
  revokeSession: (id: string) =>
    api<{ ok: true }>(`/auth/sessions/${encodeURIComponent(id)}/revoke`, { method: 'POST' }),
  revokeOtherSessions: () =>
    api<{ ok: true }>('/auth/sessions/revoke-others', { method: 'POST' }),
  /** Permanently delete the user's account and all data. Requires password confirmation. */
  deleteAccount: (password: string) =>
    api<{ ok: true }>('/me', { method: 'DELETE', body: { password } }),
  // Email verification
  verifyEmail: (email: string, code: string) =>
    api<ApiAuthResponse>('/auth/verify-email', { method: 'POST', body: { email, code } }),
  sendCode: (email: string, purpose: 'verify' | 'reset') =>
    api<{ ok: true }>('/auth/send-code', { method: 'POST', body: { email, purpose } }),
  // Password reset
  forgotPassword: (email: string) =>
    api<{ ok: true }>('/auth/forgot-password', { method: 'POST', body: { email } }),
  resetPassword: (email: string, code: string, new_password: string) =>
    api<{ ok: true }>('/auth/reset-password', { method: 'POST', body: { email, code, new_password } }),
  // Enabled social-login providers for the login screen (no secrets). Empty
  // array → the UI hides the OAuth section entirely.
  oauthProviders: () => api<ApiPublicOAuthProvider[]>('/public/oauth-providers'),

  // ----- Identity linking (§ account → identity sources) -----
  /** Third-party accounts bound to the current user. */
  identities: () => api<ApiOAuthIdentity[]>('/me/identities'),
  /**
   * Begin a bind flow: returns the provider authorize URL for the SPA to
   * navigate to. The caller's identity is stashed server-side in the OAuth
   * state, so the shared callback links instead of logging in.
   */
  linkIdentityStart: (providerId: string) =>
    api<{ authorize_url: string }>(`/me/identities/${encodeURIComponent(providerId)}/link`, { method: 'POST' }),
  /** Remove a bound identity by its (provider, subject) key. */
  unlinkIdentity: (providerId: string, subject: string) =>
    api<{ ok: true }>(
      `/me/identities?provider_id=${encodeURIComponent(providerId)}&subject=${encodeURIComponent(subject)}`,
      { method: 'DELETE' },
    ),
}

// ----- Models / skills -----------------------------------------------------

export const modelsApi = {
  list: () => api<{ models: ApiModel[]; default_id: string; verify_available?: boolean }>('/models'),
  listImage: () => api<{ models: ApiModel[]; default_id: string }>('/image-models'),
  listEmbedding: () => api<{ models: ApiModel[]; default_id: string }>('/embedding-models'),
  /** Model tags for the picker's filter chips (§ model tags). */
  tags: () => api<ApiModelTag[]>('/model-tags'),
}

// ----- Image generation (§4.20) --------------------------------------------

export const imageApi = {
  /** Enabled styles for the composer style picker (hidden prompt stripped). */
  styles: () => api<ApiImageStyle[]>('/image/styles'),
  /** The signed-in user's own generated-image gallery (§4.20). */
  myImages: (
    limit = envNum('VITE_AIVORY_IMAGE_API_MY_IMAGES_LIMIT', 60),
    offset = 0,
  ) => api<ApiAdminImage[]>(`/me/images?limit=${limit}&offset=${offset}`),
}

export const skillsApi = {
  list: () => api<ApiSkill[]>('/skills'),
}

// ----- User groups (membership tiers) --------------------------------------

export const groupsApi = {
  /** Groups visible to the signed-in user (subscription page). */
  list: () => api<ApiUserGroup[]>('/user-groups'),
  /** Public membership tiers for the landing page (no auth required). */
  publicList: () => api<ApiUserGroup[]>('/public/user-groups'),
}

// ----- Redeem codes (§ redeem codes) ---------------------------------------

export const redeemApi = {
  /** Apply a code on behalf of the signed-in user. Throws ApiError on failure
   *  with `error` field one of: code_invalid | code_expired | code_used |
   *  code_already_owned | code_disabled.
   *
   *  When the code grants a group DIFFERENT from the user's current one, the
   *  first call (confirm omitted) returns `{ requires_confirmation: true, … }`
   *  WITHOUT applying anything — call again with `confirm: true` to override the
   *  current group immediately (not a renewal). */
  redeem: (code: string, confirm = false) =>
    api<ApiRedeemResult>('/me/redeem', { method: 'POST', body: { code, confirm } }),
}

// ----- Audio (speech-to-text) ----------------------------------------------

export const audioApi = {
  /** Transcribe a recorded audio blob via the admin-configured voice model. */
  transcribe: (file: Blob, filename = 'audio.webm') => {
    const fd = new FormData()
    fd.append('file', file, filename)
    return api<{ text: string }>('/audio/transcriptions', { method: 'POST', body: fd })
  },
}

// ----- Projects ------------------------------------------------------------

export const projectsApi = {
  list: (workspaceId?: string) =>
    api<ApiProject[]>(`/projects${workspaceId ? `?workspace_id=${encodeURIComponent(workspaceId)}` : ''}`),
  get: (id: string) =>
    api<{ project: ApiProject; documents: ApiDocument[]; conversations: ApiConversation[] }>(
      `/projects/${encodeURIComponent(id)}`,
    ),
  create: (body: Partial<ApiProject>) => api<ApiProject>('/projects', { method: 'POST', body }),
  update: (id: string, patch: Partial<ApiProject>) =>
    api<ApiProject>(`/projects/${encodeURIComponent(id)}`, { method: 'PATCH', body: patch }),
  remove: (id: string) => api<{ ok: true }>(`/projects/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  listDocs: (id: string) => api<ApiDocument[]>(`/projects/${encodeURIComponent(id)}/documents`),
  addDoc: (id: string, body: { filename: string; content: string; mime_type?: string }) =>
    api<ApiDocument>(`/projects/${encodeURIComponent(id)}/documents`, { method: 'POST', body }),
  /** Upload a real file (multipart) into the project's knowledge library. */
  uploadDoc: (id: string, file: File, opts: { onProgress?: (progress: UploadProgress) => void } = {}) => {
    const fd = new FormData()
    fd.append('file', file)
    return apiUpload<ApiDocument>(`/projects/${encodeURIComponent(id)}/documents`, fd, opts)
  },
  removeDoc: (id: string, docId: string) =>
    api<{ ok: true }>(`/projects/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}`, {
      method: 'DELETE',
    }),
  renameDoc: (id: string, docId: string, filename: string) =>
    api<{ ok: true }>(`/projects/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}`, {
      method: 'PATCH',
      body: { filename },
    }),
}

// ----- Search --------------------------------------------------------------

export interface SearchHit {
  conversation_id: string
  title: string
  message_id?: string
  role?: string
  snippet?: string
  created_at: number
  updated_at: number
}

// ----- Workspaces (§workspaces) ---------------------------------------------

export const workspacesApi = {
  list: () => api<{ workspaces: ApiWorkspace[] }>('/workspaces'),
  create: (name: string) => api<ApiWorkspace>('/workspaces', { method: 'POST', body: { name } }),
  remove: (id: string) => api<{ ok: true }>(`/workspaces/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  members: (id: string) => api<{ members: ApiWorkspaceMember[] }>(`/workspaces/${encodeURIComponent(id)}/members`),
  kick: (id: string, userId: string) =>
    api<{ ok: true }>(`/workspaces/${encodeURIComponent(id)}/members/${encodeURIComponent(userId)}`, { method: 'DELETE' }),
  leave: (id: string) => api<{ ok: true }>(`/workspaces/${encodeURIComponent(id)}/leave`, { method: 'POST' }),
  rotateInvite: (id: string) =>
    api<{ invite_token: string }>(`/workspaces/${encodeURIComponent(id)}/invite/rotate`, { method: 'POST' }),
  inviteInfo: (token: string) =>
    api<{ id: string; name: string; owner_name: string; member_count: number }>(
      `/workspaces/join/${encodeURIComponent(token)}`,
    ),
  join: (token: string) =>
    api<{ id: string; name: string }>(`/workspaces/join/${encodeURIComponent(token)}`, { method: 'POST' }),
  adminList: (
    limit = envNum('VITE_AIVORY_WORKSPACES_API_ADMIN_LIST_LIMIT', 200),
    offset = 0,
  ) => api<{ workspaces: ApiWorkspace[] }>(`/admin/workspaces?limit=${limit}&offset=${offset}`),
  adminDetail: (id: string) =>
    api<{
      workspace: ApiWorkspace
      members: ApiWorkspaceMember[]
      conversations: ApiConversation[]
      projects: ApiProject[]
      kbs: ApiKnowledgeBase[]
    }>(`/admin/workspaces/${encodeURIComponent(id)}`),
  adminRemove: (id: string) =>
    api<{ ok: true }>(`/admin/workspaces/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}

export const searchApi = {
  /** Full-text search over conversation titles + message content — scoped to the
   *  active workspace when one is set (§workspaces). */
  query: (q: string, workspaceId?: string) =>
    api<{ query: string; titles: SearchHit[]; messages: SearchHit[] }>(
      `/search?q=${encodeURIComponent(q)}${workspaceId ? `&workspace_id=${encodeURIComponent(workspaceId)}` : ''}`,
    ),
}

// ----- Conversations + messages -------------------------------------------

export const conversationsApi = {
  list: (
    projectId?: string,
    limit = envNum('VITE_AIVORY_CONVERSATIONS_API_LIST_LIMIT', 200),
    offset = 0,
    workspaceId?: string,
  ) =>
    api<{ conversations: ApiConversation[]; limit: number; offset: number; has_more: boolean }>(
      `/conversations?limit=${limit}&offset=${offset}${projectId ? `&project_id=${encodeURIComponent(projectId)}` : ''}${workspaceId ? `&workspace_id=${encodeURIComponent(workspaceId)}` : ''}`,
    ),
  listArchived: (
    limit = envNum('VITE_AIVORY_CONVERSATIONS_API_LIST_ARCHIVED_LIMIT', 200),
    offset = 0,
  ) =>
    api<{ conversations: ApiConversation[]; limit: number; offset: number; has_more: boolean }>(
      `/conversations?archived=only&limit=${limit}&offset=${offset}`,
    ),
  get: (id: string, opts?: { limit?: number; before?: string }) => {
    const qs = new URLSearchParams()
    if (opts?.limit) qs.set('limit', String(opts.limit))
    if (opts?.before) qs.set('before', opts.before)
    const q = qs.toString()
    return api<{ conversation: ApiConversation; messages: ApiMessage[]; has_more?: boolean; next_before?: string }>(
      `/conversations/${encodeURIComponent(id)}${q ? `?${q}` : ''}`,
    )
  },
  create: (body: { model_id?: string; project_id?: string; title?: string; workspace_id?: string }) =>
    api<ApiConversation>('/conversations', { method: 'POST', body }),
  // Bulk-import conversation trees from another platform's export OR our own
  // privacy-page "Export all data" file. History + titles (+ model for our own
  // format) only; the server bypasses the orchestrator (no model calls / quota).
  importConversations: (body: {
    conversations: {
      title: string
      active_leaf_id: string
      messages: { id: string; parent_id: string; role: string; content: string }[]
      model_id?: string
    }[]
  }) =>
    api<{ imported: number; failed: number; conversation_ids: string[] }>('/conversations/import', {
      method: 'POST',
      body,
    }),
  update: (id: string, patch: Partial<ApiConversation>) =>
    api<ApiConversation>(`/conversations/${encodeURIComponent(id)}`, { method: 'PATCH', body: patch }),
  remove: (id: string) => api<{ ok: true }>(`/conversations/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  messages: (id: string, mode: 'path' | 'tree' = 'path') =>
    api<ApiMessage[]>(
      `/conversations/${encodeURIComponent(id)}/messages${mode === 'tree' ? '?mode=tree' : ''}`,
    ),
  editMessage: (id: string, msgId: string, text: string) =>
    api<ApiMessage>(
      `/conversations/${encodeURIComponent(id)}/messages/${encodeURIComponent(msgId)}`,
      { method: 'PATCH', body: { text } },
    ),
  // Delete one round (the user message + all its assistant answers) by any
  // message id inside it; branch-safe. Returns the refreshed active path.
  deleteMessage: (id: string, msgId: string) =>
    api<{ ok: true; active_leaf_id: string; messages: ApiMessage[] }>(
      `/conversations/${encodeURIComponent(id)}/messages/${encodeURIComponent(msgId)}`,
      { method: 'DELETE' },
    ),
  feedback: (id: string, msgId: string, feedback: 'like' | 'dislike' | '') =>
    api<{ ok: true }>(
      `/conversations/${encodeURIComponent(id)}/messages/${encodeURIComponent(msgId)}/feedback`,
      { method: 'POST', body: { feedback } },
    ),
  stop: (id: string) => api<{ ok: true }>(`/conversations/${encodeURIComponent(id)}/stop`, { method: 'POST' }),
  setActiveLeaf: (id: string, leaf_id: string) =>
    api<{ conversation: ApiConversation; messages: ApiMessage[] }>(
      `/conversations/${encodeURIComponent(id)}/active-leaf`,
      { method: 'PATCH', body: { leaf_id } },
    ),
  fork: (id: string, body: { leaf_id?: string; title?: string }) =>
    api<ApiConversation>(`/conversations/${encodeURIComponent(id)}/fork`, { method: 'POST', body }),
  // Inline (text-selection) sub-conversations anchored to a quoted excerpt of a
  // message. The list drives the inline-thread markers; create opens a new one.
  inlineThreads: (id: string) =>
    api<ApiConversation[]>(`/conversations/${encodeURIComponent(id)}/inline-threads`),
  createInlineThread: (id: string, body: { message_id: string; quote: string }) =>
    api<ApiConversation>(`/conversations/${encodeURIComponent(id)}/inline-threads`, { method: 'POST', body }),
  promoteDoc: (id: string, docId: string) =>
    api<{ ok: true }>(`/conversations/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}/promote`, {
      method: 'POST',
    }),
  retryDoc: (id: string, docId: string) =>
    api<{ ok: true }>(`/conversations/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}/retry`, {
      method: 'POST',
    }),
  // Conversation-scoped documents + their ingest status — polled by the composer
  // to show upload/parse progress and block the first send until 'ready'.
  listDocs: (id: string) =>
    api<ApiDocument[]>(`/conversations/${encodeURIComponent(id)}/documents`),
  // Conversation files drawer (§ conversation files): the authoritative set of
  // files the conversation references, and remove (detach + drop RAG).
  listFiles: (id: string) =>
    api<ApiConversationFile[]>(`/conversations/${encodeURIComponent(id)}/files`),
  listDraftFiles: (id: string) =>
    api<ApiConversationFile[]>(`/conversations/${encodeURIComponent(id)}/files?draft=1`),
  removeFile: (id: string, fileId: string) =>
    api<{ ok: true }>(`/conversations/${encodeURIComponent(id)}/files/${encodeURIComponent(fileId)}`, {
      method: 'DELETE',
    }),
  // Public read-only sharing (§ sharing).
  getShare: (id: string) =>
    api<{ share: ApiShareInfo | null }>(`/conversations/${encodeURIComponent(id)}/share`),
  createShare: (id: string) =>
    api<ApiShareInfo>(`/conversations/${encodeURIComponent(id)}/share`, { method: 'POST' }),
  deleteShare: (id: string) =>
    api<{ ok: true }>(`/conversations/${encodeURIComponent(id)}/share`, { method: 'DELETE' }),
}

// ----- Public share view (no auth) ----------------------------------------

export const sharedApi = {
  get: (token: string) => api<ApiSharedConversation>(`/public/shared/${encodeURIComponent(token)}`),
  clone: (token: string) =>
    api<ApiConversation>(`/shared/${encodeURIComponent(token)}/clone`, { method: 'POST' }),
}

// ----- Knowledge bases ----------------------------------------------------

export const kbsApi = {
  list: (workspaceId?: string) =>
    api<ApiKnowledgeBase[]>(`/kbs${workspaceId ? `?workspace_id=${encodeURIComponent(workspaceId)}` : ''}`),
  create: (body: { name: string; description?: string; embedding_model_id?: string; workspace_id?: string }) =>
    api<ApiKnowledgeBase>('/kbs', { method: 'POST', body }),
  remove: (id: string) => api<{ ok: true }>(`/kbs/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  listDocs: (id: string) => api<ApiDocument[]>(`/kbs/${encodeURIComponent(id)}/documents`),
  addDoc: (id: string, body: { filename: string; content: string; mime_type?: string }) =>
    api<ApiDocument>(`/kbs/${encodeURIComponent(id)}/documents`, { method: 'POST', body }),
  removeDoc: (id: string, docId: string) =>
    api<{ ok: true }>(`/kbs/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}`, { method: 'DELETE' }),
}

// ----- Memories -----------------------------------------------------------

export const memoriesApi = {
  list: (status?: string) =>
    api<ApiMemory[]>(`/me/memories${status ? `?status=${encodeURIComponent(status)}` : ''}`),
  create: (body: { memory_text: string; slot?: string; value?: string }) =>
    api<ApiMemory>('/me/memories', { method: 'POST', body }),
  update: (id: string, body: { memory_text?: string; status?: string; reason?: string }) =>
    api<ApiMemory>(`/me/memories/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  remove: (id: string) => api<{ ok: true }>(`/me/memories/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}

// ----- Admin --------------------------------------------------------------

export const adminApi = {
  channels: () => api<ApiChannel[]>('/admin/channels'),
  createChannel: (body: Partial<ApiChannel> & { api_key?: string }) =>
    api<ApiChannel>('/admin/channels', { method: 'POST', body }),
  reorderChannels: (ids: string[]) =>
    api<{ ok: true }>('/admin/channels/reorder', { method: 'PATCH', body: { ids } }),
  updateChannel: (id: string, body: Partial<ApiChannel> & { api_key?: string }) =>
    api<ApiChannel>(`/admin/channels/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeChannel: (id: string) =>
    api<{ ok: true }>(`/admin/channels/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  models: (kind?: 'chat' | 'image' | 'embedding') =>
    api<ApiModel[]>(`/admin/models${kind ? `?kind=${encodeURIComponent(kind)}` : ''}`),
  createModel: (body: Partial<ApiModel>) => api<ApiModel>('/admin/models', { method: 'POST', body }),
  // Persist a new model order: `ids` is the full list in the desired order.
  reorderModels: (ids: string[]) =>
    api<{ ok: true }>('/admin/models/reorder', { method: 'PATCH', body: { ids } }),
  updateModel: (id: string, body: Partial<ApiModel>) =>
    api<ApiModel>(`/admin/models/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeModel: (id: string) => api<{ ok: true }>(`/admin/models/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  setModelSkills: (id: string, skillIds: string[]) =>
    api<{ ok: true }>(`/admin/models/${encodeURIComponent(id)}/skills`, {
      method: 'PUT',
      body: { skill_ids: skillIds },
    }),

  // Model tags (§ model tags): admin CRUD of the assignable label set.
  modelTags: () => api<ApiModelTag[]>('/admin/model-tags'),
  createModelTag: (name: string, sortOrder = 0) =>
    api<ApiModelTag>('/admin/model-tags', { method: 'POST', body: { name, sort_order: sortOrder } }),
  updateModelTag: (id: string, body: { name: string; sort_order?: number }) =>
    api<ApiModelTag>(`/admin/model-tags/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeModelTag: (id: string) =>
    api<{ ok: true }>(`/admin/model-tags/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // §4.20 Image styles — full row incl. hidden_prompt (admin only).
  imageStyles: () => api<ApiImageStyle[]>('/admin/image-styles'),
  createImageStyle: (body: Partial<ApiImageStyle>) =>
    api<ApiImageStyle>('/admin/image-styles', { method: 'POST', body }),
  updateImageStyle: (id: string, body: Partial<ApiImageStyle>) =>
    api<ApiImageStyle>(`/admin/image-styles/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeImageStyle: (id: string) =>
    api<{ ok: true }>(`/admin/image-styles/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  skills: () => api<ApiSkill[]>('/admin/skills'),
  createSkill: (body: Partial<ApiSkill>) => api<ApiSkill>('/admin/skills', { method: 'POST', body }),
  updateSkill: (id: string, body: Partial<ApiSkill>) =>
    api<ApiSkill>(`/admin/skills/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeSkill: (id: string) => api<{ ok: true }>(`/admin/skills/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // Upload one skill asset (template/script/data). Returns the descriptor to push
  // into the skill's `assets` array; storage_path is server-controlled (§4.17).
  uploadSkillAsset: (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return api<ApiSkillAsset>('/admin/skills/assets', { method: 'POST', body: fd })
  },

  // OAuth / social login providers. client_secret is write-only — send it on
  // create/update, never expect it back (has_secret flags whether one is set).
  oauthProviders: () => api<ApiOAuthProvider[]>('/admin/oauth-providers'),
  createOAuthProvider: (body: Partial<ApiOAuthProvider> & { client_secret?: string }) =>
    api<ApiOAuthProvider>('/admin/oauth-providers', { method: 'POST', body }),
  updateOAuthProvider: (id: string, body: Partial<ApiOAuthProvider> & { client_secret?: string }) =>
    api<ApiOAuthProvider>(`/admin/oauth-providers/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeOAuthProvider: (id: string) =>
    api<{ ok: true }>(`/admin/oauth-providers/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // User groups + per-model quotas (§ user groups).
  userGroups: () => api<ApiUserGroup[]>('/admin/user-groups'),
  createUserGroup: (body: Partial<ApiUserGroup>) =>
    api<ApiUserGroup>('/admin/user-groups', { method: 'POST', body }),
  reorderUserGroups: (ids: string[]) =>
    api<{ ok: true }>('/admin/user-groups/reorder', { method: 'PATCH', body: { ids } }),
  updateUserGroup: (id: string, body: Partial<ApiUserGroup>) =>
    api<ApiUserGroup>(`/admin/user-groups/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeUserGroup: (id: string) =>
    api<{ ok: true }>(`/admin/user-groups/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  /** Assign a membership group; group_expires_at is unix seconds (0 = permanent). */
  setUserGroup: (id: string, group_id: string, group_expires_at = 0) =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/group`, {
      method: 'POST',
      body: { group_id, group_expires_at },
    }),
  /** Overwrite a user's permanent (non-expiring) credit balance (§ credits). */
  setUserCredits: (id: string, credits_permanent: number) =>
    api<{ ok: true; credits_permanent: number }>(`/admin/users/${encodeURIComponent(id)}/credits`, {
      method: 'POST',
      body: { credits_permanent },
    }),
  modelQuotas: (id: string) => api<ApiModelQuota[]>(`/admin/models/${encodeURIComponent(id)}/quotas`),
  setModelQuotas: (id: string, quotas: ApiModelQuota[]) =>
    api<{ ok: true }>(`/admin/models/${encodeURIComponent(id)}/quotas`, { method: 'PUT', body: { quotas } }),

  // Redeem codes (§ redeem codes).
  redeemCodes: (params?: { batch?: string; status?: 'unused' | 'redeemed' | 'disabled' | 'expired'; limit?: number; offset?: number }) => {
    const q = new URLSearchParams()
    if (params?.batch) q.set('batch', params.batch)
    if (params?.status) q.set('status', params.status)
    if (params?.limit) q.set('limit', String(params.limit))
    if (params?.offset) q.set('offset', String(params.offset))
    const qs = q.toString()
    return api<ApiRedeemCode[]>(`/admin/redeem-codes${qs ? `?${qs}` : ''}`)
  },
  createRedeemCode: (body: {
    group_id: string
    duration_days: number
    max_uses?: number
    expires_at?: number
    note?: string
    batch_name?: string
    code?: string
    /** When > 1 a bulk batch is generated. */
    quantity?: number
  }) => api<ApiRedeemCode | ApiRedeemCode[]>('/admin/redeem-codes', { method: 'POST', body }),
  updateRedeemCode: (id: string, patch: {
    enabled?: boolean
    note?: string
    batch_name?: string
    expires_at?: number
    max_uses?: number
  }) => api<ApiRedeemCode>(`/admin/redeem-codes/${encodeURIComponent(id)}`, { method: 'PATCH', body: patch }),
  removeRedeemCode: (id: string) =>
    api<{ ok: true }>(`/admin/redeem-codes/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  redeemCodeRedemptions: (id: string) =>
    api<ApiRedeemRedemption[]>(`/admin/redeem-codes/${encodeURIComponent(id)}/redemptions`),
  removeRedeemBatch: (name: string) =>
    api<{ ok: true; removed: number }>(`/admin/redeem-batches/${encodeURIComponent(name)}`, { method: 'DELETE' }),

  users: (
    search = '',
    limit = envNum('VITE_AIVORY_ADMIN_API_USERS_LIMIT', 50),
    offset = 0,
  ) =>
    api<{ users: ApiUser[]; total: number; limit: number; offset: number }>(
      `/admin/users?search=${encodeURIComponent(search)}&limit=${limit}&offset=${offset}`,
    ),
  reorderUsers: (ids: string[]) =>
    api<{ ok: true }>('/admin/users/reorder', { method: 'PATCH', body: { ids } }),
  createUser: (body: { email: string; name: string; password: string; role: 'user' | 'admin' }) =>
    api<ApiUser>('/admin/users', { method: 'POST', body }),
  setUserPassword: (id: string, new_password: string) =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/password`, { method: 'POST', body: { new_password } }),
  setUserRole: (id: string, role: 'user' | 'admin') =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/role`, { method: 'POST', body: { role } }),
  banUser: (id: string) => api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/ban`, { method: 'POST' }),
  unbanUser: (id: string) => api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/unban`, { method: 'POST' }),
  /** Permanently delete a user and all their data (§ admin → users). */
  deleteUser: (id: string) =>
    api<{ ok: true; status?: string }>(`/admin/users/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  /** Background deletion jobs (§async user delete) — poll while a purge runs. */
  userDeletions: () =>
    api<{ jobs: Array<{ user_id: string; email: string; status: string; progress: string; error?: string }> }>(
      '/admin/users/deletions',
    ),
  /** Reset (turn off) a user's 2FA — recovery for a lost authenticator (§ 2FA). */
  disableUser2fa: (id: string) =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/2fa/disable`, { method: 'POST' }),
  // §8.1 abuse-triage drill-down. Returns one user's conversations (all
  // statuses — admin can still inspect archived/banned content) and the
  // full message timeline of any single conversation, both bypassing the
  // per-user ownership filter on the server side.
  userConversations: (id: string) =>
    api<ApiConversation[]>(`/admin/users/${encodeURIComponent(id)}/conversations`),
  userProjects: (id: string) =>
    api<ApiProject[]>(`/admin/users/${encodeURIComponent(id)}/projects`),
  // §4.20 a user's generated-image gallery (admin drill-down).
  userImages: (
    id: string,
    limit = envNum('VITE_AIVORY_ADMIN_API_USER_IMAGES_LIMIT', 60),
    offset = 0,
  ) => api<ApiAdminImage[]>(`/admin/users/${encodeURIComponent(id)}/images?limit=${limit}&offset=${offset}`),
  userKbs: (id: string) =>
    api<ApiKnowledgeBase[]>(`/admin/users/${encodeURIComponent(id)}/kbs`),
  kbDocuments: (kbId: string) =>
    api<ApiDocument[]>(`/admin/kbs/${encodeURIComponent(kbId)}/documents`),
  conversation: (id: string) =>
    api<ApiConversation>(`/admin/conversations/${encodeURIComponent(id)}`),
  conversationMessages: (id: string, mode?: 'tree') =>
    api<ApiMessage[]>(
      `/admin/conversations/${encodeURIComponent(id)}/messages${mode ? `?mode=${mode}` : ''}`,
    ),
  deleteConversation: (id: string) =>
    api<{ ok: true }>(`/admin/conversations/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // Sandbox inspector (§ admin tools): list / preview / clear a conversation's
  // sandbox workspace files.
  sandboxFiles: (id: string) =>
    api<{ session: string; files: { path: string; size: number }[] }>(
      `/admin/conversations/${encodeURIComponent(id)}/sandbox`,
    ),
  sandboxFileUrl: (id: string, path: string) =>
    apiUrl(`/admin/conversations/${encodeURIComponent(id)}/sandbox/file?path=${encodeURIComponent(path)}`),
  clearSandbox: (id: string) =>
    api<{ ok: true }>(`/admin/conversations/${encodeURIComponent(id)}/sandbox`, { method: 'DELETE' }),

  // Per-record usage list (one row per API call), filtered + paginated.
  usage: (params: { days?: number; user?: string; model?: string; status?: string; purpose?: string; page?: number; pageSize?: number } = {}) => {
    const qs = new URLSearchParams()
    if (params.days) qs.set('days', String(params.days))
    if (params.user) qs.set('user', params.user)
    if (params.model) qs.set('model', params.model)
    if (params.status) qs.set('status', params.status)
    if (params.purpose) qs.set('purpose', params.purpose)
    if (params.page) qs.set('page', String(params.page))
    if (params.pageSize) qs.set('page_size', String(params.pageSize))
    return api<{ records: ApiUsageRecord[]; total: number; total_cost: number; page: number; page_size: number }>(
      `/admin/usage${qs.toString() ? `?${qs}` : ''}`,
    )
  },
  deleteUsageRecord: (id: number) => api<{ ok: true }>(`/admin/usage/${id}`, { method: 'DELETE' }),
  deleteUsageFiltered: (params: { days?: number; user?: string; model?: string; status?: string; purpose?: string }) => {
    const qs = new URLSearchParams()
    if (params.days) qs.set('days', String(params.days))
    if (params.user) qs.set('user', params.user)
    if (params.model) qs.set('model', params.model)
    if (params.status) qs.set('status', params.status)
    if (params.purpose) qs.set('purpose', params.purpose)
    return api<{ deleted: number }>(`/admin/usage${qs.toString() ? `?${qs}` : ''}`, { method: 'DELETE' })
  },
  files: (params: { search?: string; userId?: string; user?: string; origin?: string; sort?: string; order?: string; limit?: number; offset?: number } = {}) => {
    const qs = new URLSearchParams()
    if (params.search) qs.set('search', params.search)
    if (params.userId) qs.set('user_id', params.userId)
    if (params.user) qs.set('user', params.user)
    if (params.origin && params.origin !== 'all') qs.set('origin', params.origin)
    if (params.sort) qs.set('sort', params.sort)
    if (params.order) qs.set('order', params.order)
    if (params.limit) qs.set('limit', String(params.limit))
    if (params.offset) qs.set('offset', String(params.offset))
    return api<{ files: ApiAdminFile[]; total: number; limit: number; offset: number }>(
      `/admin/files${qs.toString() ? `?${qs}` : ''}`,
    )
  },
  deleteFiles: (items: Array<{ source: 'file' | 'document'; id: string }>) =>
    api<{ deleted: number }>('/admin/files/delete', { method: 'POST', body: { items } }),
  // Raw bytes for the preview dialog. Same bearer-fetch pattern as
  // backupArchiveDownload: <img>/<iframe> tags can't carry the auth header.
  fileContentBlob: async (source: 'file' | 'document', id: string): Promise<Blob> => {
    const token = getAccessToken()
    const res = await fetch(apiUrl(`/admin/files/content?source=${source}&id=${encodeURIComponent(id)}`), {
      credentials: 'include',
      headers: token ? { authorization: `Bearer ${token}` } : {},
    })
    if (!res.ok) throw new ApiError(res.status, `preview failed (${res.status})`, null)
    return res.blob()
  },
  analytics: (days = envNum('VITE_AIVORY_ADMIN_API_ANALYTICS', 30)) =>
    api<ApiAnalytics>(`/admin/analytics?days=${days}`),

  settings: () => api<Record<string, unknown>>('/admin/settings'),
  updateSettings: (patch: Record<string, unknown>) =>
    api<Record<string, unknown>>('/admin/settings', { method: 'PATCH', body: patch }),

  // Icon upload — returns { url, filename } where `url` is a path the model's
  // icon column can store directly (e.g. "/api/icons/abc123.png"). Backend
  // enforces a 256 KiB cap, png/jpg/jpeg only, header sniff + structural
  // decode to reject polyglots — see admin_uploads.go.
  uploadIcon: (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return api<{ url: string; filename: string }>('/admin/icons', { method: 'POST', body: fd })
  },

  // Database backup / migration (§ admin → data migration). Export streams a
  // .zip the browser saves; import uploads one and REPLACES all data. The blob
  // path can't use the JSON `api()` helper, so it hand-rolls the fetch (still
  // sending the cookie + Bearer the rest of the client uses).
  backupExport: async (includeFiles: boolean): Promise<Blob> => {
    const token = getAccessToken()
    const res = await fetch(apiUrl(`/admin/backup/export${includeFiles ? '?files=1' : ''}`), {
      credentials: 'include',
      headers: token ? { authorization: `Bearer ${token}` } : {},
    })
    if (!res.ok) {
      let msg = `export failed (${res.status})`
      try {
        const j = (await res.json()) as { error?: string }
        if (j?.error) msg = j.error
      } catch {
        /* non-JSON error body */
      }
      throw new ApiError(res.status, msg, null)
    }
    return res.blob()
  },
  backupExportStart: (includeFiles: boolean) =>
    api<BackupExportState>('/admin/backup/export-jobs', {
      method: 'POST',
      body: { include_files: includeFiles },
    }),
  backupExportState: () => api<BackupExportState>('/admin/backup/export-jobs'),
  backupArchiveDownload: async (name: string): Promise<Blob> => {
    const token = getAccessToken()
    const res = await fetch(apiUrl(`/admin/backup/archives/${encodeURIComponent(name)}`), {
      credentials: 'include',
      headers: token ? { authorization: `Bearer ${token}` } : {},
    })
    if (!res.ok) {
      let msg = `download failed (${res.status})`
      try {
        const j = (await res.json()) as { error?: string }
        if (j?.error) msg = j.error
      } catch {
        /* non-JSON error body */
      }
      throw new ApiError(res.status, msg, null)
    }
    return res.blob()
  },
  backupImport: (file: File) => {
    const fd = new FormData()
    fd.append('confirm', 'REPLACE')
    fd.append('file', file)
    return api<BackupImportResult>('/admin/backup/import', { method: 'POST', body: fd })
  },

  // Configuration backup / migration. This is lighter than the full backup:
  // it exports admin configuration tables (settings, channels, models, skills,
  // OAuth providers, groups, image styles) plus admin assets, and imports by
  // upsert, leaving users, conversations, user uploads, KBs, sessions,
  // workspaces, and logs untouched.
  configExport: async (): Promise<Blob> => {
    const token = getAccessToken()
    const res = await fetch(apiUrl('/admin/config/export'), {
      credentials: 'include',
      headers: token ? { authorization: `Bearer ${token}` } : {},
    })
    if (!res.ok) {
      let msg = `export failed (${res.status})`
      try {
        const j = (await res.json()) as { error?: string }
        if (j?.error) msg = j.error
      } catch {
        /* non-JSON error body */
      }
      throw new ApiError(res.status, msg, null)
    }
    return res.blob()
  },
  configImport: (file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return api<ConfigImportResult>('/admin/config/import', { method: 'POST', body: fd })
  },

  // Vector index maintenance. Checks DB chunks against Qdrant and can rebuild
  // missing/empty vectors asynchronously.
  vectorMaintenanceState: () => api<VectorMaintenanceState>('/admin/vectors/jobs'),
  vectorCheckStart: () => api<VectorMaintenanceState>('/admin/vectors/check', { method: 'POST' }),
  vectorRebuildMissingStart: () =>
    api<VectorMaintenanceState>('/admin/vectors/rebuild-missing', { method: 'POST' }),
}

/** Result of a successful database import (§ admin → data migration). */
export interface BackupImportResult {
  ok: true
  /** Row count restored per table. */
  tables: Record<string, number>
  files_restored: number
  includes_files: boolean
  qdrant_restored?: number
  qdrant_error?: string
  /** The admin's session was invalidated by the restore — re-login required. */
  relogin_required: boolean
}

/** Async Docker/full backup export state. */
export interface BackupExportJob {
  id: string
  status: 'running' | 'completed' | 'failed'
  progress: string
  filename?: string
  error?: string
  started_at: number
  completed_at?: number
  size_bytes?: number
  include_files: boolean
  include_qdrant: boolean
  qdrant_points?: number
}

export interface BackupArchiveFile {
  name: string
  size_bytes: number
  created_at: number
}

export interface BackupExportState {
  running: BackupExportJob | null
  jobs: BackupExportJob[]
  archives: BackupArchiveFile[]
}

export interface VectorIssue {
  chunk_id: string
  document_id: string
  kb_id?: string
  conversation_id?: string
  filename: string
  embedding_model: string
  dim?: number
  reason: string
}

export interface VectorAuditModel {
  embedding_model: string
  dim: number
  total: number
  present: number
  missing: number
  empty: number
  skipped: number
}

export interface VectorAuditReport {
  total: number
  present: number
  missing: number
  empty: number
  skipped: number
  models: VectorAuditModel[]
  issues: VectorIssue[]
}

export interface VectorMaintenanceJob {
  id: string
  type: 'check' | 'rebuild'
  status: 'running' | 'completed' | 'failed'
  progress: string
  error?: string
  started_at: number
  completed_at?: number
  report?: VectorAuditReport
  rebuilt?: number
  failed?: number
}

export interface VectorMaintenanceState {
  running: VectorMaintenanceJob | null
  jobs: VectorMaintenanceJob[]
}

/** Result of a successful configuration import (§ admin → configuration migration). */
export interface ConfigImportResult {
  ok: true
  /** Row count merged per config table. */
  tables: Record<string, number>
  /** Admin-managed icon / skill-asset files restored from the archive. */
  assets_restored: number
  merge_mode: 'upsert' | 'settings'
  relogin_required: false
}
