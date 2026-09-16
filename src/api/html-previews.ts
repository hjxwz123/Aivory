import { api, apiUrl } from './client'
import { buildPublicHtmlPreviewDocument } from '@/lib/html-preview-document'

interface CreatedHTMLPreview {
  id: string
  url: string
  created_at: number
}

export const htmlPreviewsApi = {
  async create(html: string): Promise<CreatedHTMLPreview & { absoluteUrl: string }> {
    const created = await api<CreatedHTMLPreview>('/html-previews', {
      method: 'POST',
      body: { html: buildPublicHtmlPreviewDocument(html) },
    })
    const publicPath = `/public/html-previews/${encodeURIComponent(created.id)}`
    return {
      ...created,
      absoluteUrl: new URL(apiUrl(publicPath), window.location.origin).href,
    }
  },
}
