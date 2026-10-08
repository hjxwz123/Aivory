import { api } from './client'

export interface SiteNotification {
  id: string
  title: string
  body?: string
  enabled: boolean
  version: string
  created_at: number
  updated_at: number
  unread: boolean
  should_popup: boolean
}
export interface NotificationPage { notifications: SiteNotification[]; total: number }
export type NotificationDraft = Pick<SiteNotification, 'title' | 'enabled'> & { body: string }

function query(offset = 0, search = '') {
  const params = new URLSearchParams({ limit: '50', offset: String(offset) })
  if (search.trim()) params.set('search', search.trim())
  return `?${params}`
}

export const notificationsApi = {
  list: (offset = 0) => api<NotificationPage>(`/notifications${query(offset)}`, { activity: 'background' }),
  get: (id: string) => api<SiteNotification>(`/notifications/${encodeURIComponent(id)}`),
  read: (id: string, version: string, dismiss = false, read = true) => api<{ ok: true }>(`/notifications/${encodeURIComponent(id)}/read`, { method: 'POST', body: { version, dismiss, read }, activity: 'background' }),
  adminList: (offset = 0, search = '') => api<NotificationPage>(`/admin/notifications${query(offset, search)}`),
  adminGet: (id: string) => api<SiteNotification>(`/admin/notifications/${encodeURIComponent(id)}`),
  save: (draft: NotificationDraft, id?: string) => api<SiteNotification>(`/admin/notifications${id ? `/${encodeURIComponent(id)}` : ''}`, { method: id ? 'PUT' : 'POST', body: draft }),
  delete: (id: string) => api<{ ok: true }>(`/admin/notifications/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}
