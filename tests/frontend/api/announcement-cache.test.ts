import { afterEach, describe, expect, it, vi } from 'vitest'
import { authApi, invalidateAnnouncementCache } from '@/api'

const announcement = {
  enabled: true,
  title: 'Notice',
  body: 'Body',
  image_url: '',
  remember_dismiss: false,
  require_read: false,
  updated_at: 1,
  bar_enabled: true,
  bar_html: 'Pinned',
  bar_updated_at: 1,
}

afterEach(() => {
  invalidateAnnouncementCache()
  vi.unstubAllGlobals()
})

describe('announcement request cache', () => {
  it('coalesces the popup and pinned-bar request', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(announcement), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await Promise.all([authApi.announcement(), authApi.announcement()])

    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock).toHaveBeenCalledWith('/api/announcement', expect.any(Object))
  })

  it('requests fresh data after invalidation', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(announcement), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await authApi.announcement()
    invalidateAnnouncementCache()
    await authApi.announcement()

    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})
