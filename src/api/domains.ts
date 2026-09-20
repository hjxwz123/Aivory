import { api } from './client'
import type { ApiConversation, ApiMessage } from './types'

export interface DomainAccess {
  domain: string
  workspace_id: string
  locked: boolean
}
export interface RegistrationDomain {
  icon_url?: string
  description?: string
  subscription_purchase_disabled: boolean
  domain: string
  domains: string[]
  workspace_id: string
  workspace_name: string
  lock_personal: boolean
  email_verification_required: boolean
  initial_group_id: string
  initial_group_name: string
  enabled: boolean
  member_count: number
}
export interface DomainUser {
  user_id: string
  name: string
  email: string
  lock_override: boolean | null
  locked: boolean
}
export interface DomainUserCandidate {
  user_id: string
  name: string
  email: string
  status: string
  personal_conversation_count: number
}
export interface DomainPersonalDataStatus {
  needs_action: boolean
  domain: string
  workspace_id: string
  workspace_name: string
  personal_conversation_count: number
  can_migrate: boolean
  prompt_dismissed: boolean
}
export const domainsApi = {
  list: () => api<{ domains: RegistrationDomain[] }>('/admin/domains'),
  create: (body: Pick<RegistrationDomain, 'icon_url' | 'description' | 'domain' | 'domains' | 'workspace_id' | 'lock_personal' | 'email_verification_required' | 'subscription_purchase_disabled' | 'initial_group_id' | 'enabled'>) =>
    api('/admin/domains', { method: 'POST', body }),
  update: (body: RegistrationDomain) =>
    api(`/admin/domains/${encodeURIComponent(body.domain)}`, { method: 'PATCH', body }),
  remove: (domain: string) => api(`/admin/domains/${encodeURIComponent(domain)}`, { method: 'DELETE' }),
  users: (domain: string) => api<{ users: DomainUser[] }>(`/admin/domains/${encodeURIComponent(domain)}/users`),
  candidates: (domain: string, query = '') => {
    const search = query.trim() ? `?q=${encodeURIComponent(query.trim())}` : ''
    return api<{ users: DomainUserCandidate[] }>(`/admin/domains/${encodeURIComponent(domain)}/candidates${search}`)
  },
  enrollUsers: (domain: string, userIds: string[]) =>
    api<{ added: number; user_ids: string[] }>(`/admin/domains/${encodeURIComponent(domain)}/users`, { method: 'POST', body: { user_ids: userIds } }),
  updateUser: (domain: string, userId: string, lockOverride: boolean | null) =>
    api(`/admin/domains/${encodeURIComponent(domain)}/users/${encodeURIComponent(userId)}`, { method: 'PATCH', body: { lock_override: lockOverride } }),
  removeUser: (domain: string, userId: string) =>
    api<{ ok: true; workspace_membership_removed: boolean }>(`/admin/domains/${encodeURIComponent(domain)}/users/${encodeURIComponent(userId)}`, { method: 'DELETE' }),
}

export const domainDataApi = {
  status: () => api<DomainPersonalDataStatus>('/me/domain-data'),
  dismiss: () => api<{ ok: true }>('/me/domain-data/dismiss', { method: 'POST' }),
  migrate: () => api<{ migrated_conversations: number }>('/me/domain-data/migrate', { method: 'POST' }),
  conversations: (limit = 100, offset = 0) =>
    api<{ conversations: ApiConversation[]; limit: number; offset: number; has_more: boolean }>(
      `/me/domain-data/conversations?limit=${limit}&offset=${offset}`,
    ),
  messages: (conversationId: string) =>
    api<ApiMessage[]>(`/me/domain-data/conversations/${encodeURIComponent(conversationId)}/messages`),
}
