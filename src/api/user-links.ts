import { api } from './client'
import type { ApiUserLinksPage } from './types'

export const userLinksApi = {
  conversations: (limit = 20, offset = 0) => api<ApiUserLinksPage>(`/conversation-shares?limit=${limit}&offset=${offset}`),
  revokeConversation: (id: string) => api<{ ok: true }>(`/conversation-shares/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  htmlPreviews: (limit = 20, offset = 0) => api<ApiUserLinksPage>(`/html-previews?limit=${limit}&offset=${offset}`),
  revokeHTMLPreview: (id: string) => api<{ ok: true }>(`/html-previews/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}
