/**
 * High-level endpoint wrappers. Each function returns the same shape the
 * backend returns, with a small typed helper signature. Group by feature so
 * the call sites stay readable.
 */
import { api } from './client'
import type {
  ApiAnalytics,
  ApiAuthResponse,
  ApiChannel,
  ApiConversation,
  ApiDocument,
  ApiKnowledgeBase,
  ApiMemory,
  ApiMessage,
  ApiModel,
  ApiModelQuota,
  ApiOAuthProvider,
  ApiProject,
  ApiRedeemCode,
  ApiRedeemRedemption,
  ApiRedeemResult,
  ApiSession,
  ApiUserGroup,
  ApiPublicOAuthProvider,
  ApiShareInfo,
  ApiSharedConversation,
  ApiSkill,
  ApiUsageReportRow,
  ApiUser,
} from './types'

// ----- Auth ----------------------------------------------------------------

export const authApi = {
  signupOpen: () => api<{ open: boolean }>('/public/signup-open'),
  me: () => api<ApiUser>('/me'),
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
  register: (email: string, password: string, name: string) =>
    api<ApiAuthResponse | { verification_required: boolean; email: string }>('/auth/register', { method: 'POST', body: { email, password, name } }),
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
  getSettings: () => api<Record<string, unknown>>('/me/settings'),
  updateSettings: (patch: Record<string, unknown>) =>
    api<Record<string, unknown>>('/me/settings', { method: 'PATCH', body: patch }),
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
}

// ----- Models / skills -----------------------------------------------------

export const modelsApi = {
  list: () => api<{ models: ApiModel[]; default_id: string }>('/models'),
  listImage: () => api<{ models: ApiModel[]; default_id: string }>('/image-models'),
  listEmbedding: () => api<{ models: ApiModel[]; default_id: string }>('/embedding-models'),
}

export const skillsApi = {
  list: () => api<ApiSkill[]>('/skills'),
}

// ----- User groups (membership tiers) --------------------------------------

export const groupsApi = {
  /** Groups visible to the signed-in user (subscription page). */
  list: () => api<ApiUserGroup[]>('/user-groups'),
}

// ----- Redeem codes (§ redeem codes) ---------------------------------------

export const redeemApi = {
  /** Apply a code on behalf of the signed-in user. Throws ApiError on failure
   *  with `error` field one of: code_invalid | code_expired | code_used |
   *  code_already_owned | code_disabled. */
  redeem: (code: string) =>
    api<ApiRedeemResult>('/me/redeem', { method: 'POST', body: { code } }),
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
  list: () => api<ApiProject[]>('/projects'),
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
  uploadDoc: (id: string, file: File) => {
    const fd = new FormData()
    fd.append('file', file)
    return api<ApiDocument>(`/projects/${encodeURIComponent(id)}/documents`, { method: 'POST', body: fd })
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

// ----- Conversations + messages -------------------------------------------

export const conversationsApi = {
  list: (projectId?: string) =>
    api<ApiConversation[]>(`/conversations${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ''}`),
  listArchived: () => api<ApiConversation[]>('/conversations?archived=only'),
  get: (id: string) =>
    api<{ conversation: ApiConversation; messages: ApiMessage[] }>(`/conversations/${encodeURIComponent(id)}`),
  create: (body: { model_id?: string; project_id?: string; title?: string }) =>
    api<ApiConversation>('/conversations', { method: 'POST', body }),
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
  promoteDoc: (id: string, docId: string) =>
    api<{ ok: true }>(`/conversations/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}/promote`, {
      method: 'POST',
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
}

// ----- Knowledge bases ----------------------------------------------------

export const kbsApi = {
  list: () => api<ApiKnowledgeBase[]>('/kbs'),
  create: (body: { name: string; description?: string; embedding_model_id?: string }) =>
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

  skills: () => api<ApiSkill[]>('/admin/skills'),
  createSkill: (body: Partial<ApiSkill>) => api<ApiSkill>('/admin/skills', { method: 'POST', body }),
  updateSkill: (id: string, body: Partial<ApiSkill>) =>
    api<ApiSkill>(`/admin/skills/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeSkill: (id: string) => api<{ ok: true }>(`/admin/skills/${encodeURIComponent(id)}`, { method: 'DELETE' }),

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
  updateUserGroup: (id: string, body: Partial<ApiUserGroup>) =>
    api<ApiUserGroup>(`/admin/user-groups/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  removeUserGroup: (id: string) =>
    api<{ ok: true }>(`/admin/user-groups/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  setUserGroup: (id: string, group_id: string) =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/group`, { method: 'POST', body: { group_id } }),
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

  users: () => api<ApiUser[]>('/admin/users'),
  createUser: (body: { email: string; name: string; password: string; role: 'user' | 'admin' }) =>
    api<ApiUser>('/admin/users', { method: 'POST', body }),
  setUserPassword: (id: string, new_password: string) =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/password`, { method: 'POST', body: { new_password } }),
  setUserRole: (id: string, role: 'user' | 'admin') =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/role`, { method: 'POST', body: { role } }),
  banUser: (id: string) => api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/ban`, { method: 'POST' }),
  unbanUser: (id: string) => api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/unban`, { method: 'POST' }),
  /** Reset (turn off) a user's 2FA — recovery for a lost authenticator (§ 2FA). */
  disableUser2fa: (id: string) =>
    api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/2fa/disable`, { method: 'POST' }),
  // §8.1 abuse-triage drill-down. Returns one user's conversations (all
  // statuses — admin can still inspect archived/banned content) and the
  // full message timeline of any single conversation, both bypassing the
  // per-user ownership filter on the server side.
  userConversations: (id: string) =>
    api<ApiConversation[]>(`/admin/users/${encodeURIComponent(id)}/conversations`),
  conversation: (id: string) =>
    api<ApiConversation>(`/admin/conversations/${encodeURIComponent(id)}`),
  conversationMessages: (id: string, mode?: 'tree') =>
    api<ApiMessage[]>(
      `/admin/conversations/${encodeURIComponent(id)}/messages${mode ? `?mode=${mode}` : ''}`,
    ),
  deleteConversation: (id: string) =>
    api<{ ok: true }>(`/admin/conversations/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  usage: (days = 30) => api<{ days: number; rows: ApiUsageReportRow[]; trend: { bucket_start: number; input_tokens: number; output_tokens: number; calls: number; cost: number }[] }>(`/admin/usage?days=${days}`),
  analytics: (days = 30) => api<ApiAnalytics>(`/admin/analytics?days=${days}`),

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
}
