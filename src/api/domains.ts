import { api } from './client'

export interface DomainAccess {
  domain: string
  workspace_id: string
  locked: boolean
}
export interface RegistrationDomain {
  domain: string
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
export const domainsApi = {
  list: () => api<{ domains: RegistrationDomain[] }>('/admin/domains'),
  create: (body: Pick<RegistrationDomain, 'domain' | 'workspace_id' | 'lock_personal' | 'email_verification_required' | 'initial_group_id' | 'enabled'>) =>
    api('/admin/domains', { method: 'POST', body }),
  update: (body: RegistrationDomain) =>
    api(`/admin/domains/${encodeURIComponent(body.domain)}`, { method: 'PATCH', body }),
  remove: (domain: string) => api(`/admin/domains/${encodeURIComponent(domain)}`, { method: 'DELETE' }),
  users: (domain: string) => api<{ users: DomainUser[] }>(`/admin/domains/${encodeURIComponent(domain)}/users`),
  updateUser: (domain: string, userId: string, lockOverride: boolean | null) =>
    api(`/admin/domains/${encodeURIComponent(domain)}/users/${encodeURIComponent(userId)}`, { method: 'PATCH', body: { lock_override: lockOverride } }),
}
