/**
 * High-level endpoint wrappers. Each function returns the same shape the
 * backend returns, with a small typed helper signature. Group by feature so
 * the call sites stay readable.
 */
import { api } from './client'
import type {
  ApiAuthResponse,
  ApiChannel,
  ApiConversation,
  ApiDocument,
  ApiKnowledgeBase,
  ApiMemory,
  ApiMessage,
  ApiModel,
  ApiProject,
  ApiSkill,
  ApiUsageReportRow,
  ApiUser,
} from './types'

// ----- Auth ----------------------------------------------------------------

export const authApi = {
  signupOpen: () => api<{ open: boolean }>('/public/signup-open'),
  me: () => api<ApiUser>('/me'),
  login: (email: string, password: string) =>
    api<ApiAuthResponse>('/auth/login', { method: 'POST', body: { email, password } }),
  register: (email: string, password: string, name: string) =>
    api<ApiAuthResponse>('/auth/register', { method: 'POST', body: { email, password, name } }),
  refresh: () => api<ApiAuthResponse>('/auth/refresh', { method: 'POST' }),
  logout: () => api<{ ok: true }>('/auth/logout', { method: 'POST' }),
  updateProfile: (patch: { name?: string; email?: string }) =>
    api<ApiUser>('/me', { method: 'PATCH', body: patch }),
  changePassword: (current_password: string, new_password: string) =>
    api<{ ok: true }>('/me/password', { method: 'PATCH', body: { current_password, new_password } }),
  getSettings: () => api<Record<string, unknown>>('/me/settings'),
  updateSettings: (patch: Record<string, unknown>) =>
    api<Record<string, unknown>>('/me/settings', { method: 'PATCH', body: patch }),
  usage: () => api<{ days: number; cost: number; messages: number }>('/me/usage'),
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
  removeDoc: (id: string, docId: string) =>
    api<{ ok: true }>(`/projects/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}`, {
      method: 'DELETE',
    }),
}

// ----- Conversations + messages -------------------------------------------

export const conversationsApi = {
  list: (projectId?: string) =>
    api<ApiConversation[]>(`/conversations${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ''}`),
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

  users: () => api<ApiUser[]>('/admin/users'),
  banUser: (id: string) => api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/ban`, { method: 'POST' }),
  unbanUser: (id: string) => api<{ ok: true }>(`/admin/users/${encodeURIComponent(id)}/unban`, { method: 'POST' }),
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

  usage: (days = 30) => api<{ days: number; rows: ApiUsageReportRow[]; trend: { bucket_start: number; input_tokens: number; output_tokens: number; calls: number; cost: number }[] }>(`/admin/usage?days=${days}`),

  settings: () => api<Record<string, unknown>>('/admin/settings'),
  updateSettings: (patch: Record<string, unknown>) =>
    api<Record<string, unknown>>('/admin/settings', { method: 'PATCH', body: patch }),
}
