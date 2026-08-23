import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, apiUpload, streamSSE, streamSSEGet } from '@/api/client'

describe('API network preflight', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('does not start JSON, upload, or SSE requests while the browser is offline', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('navigator', { onLine: false })
    vi.stubGlobal('fetch', fetchMock)

    await expect(api('/offline-check')).rejects.toMatchObject({
      status: 0,
      message: 'No network connection. Check your connection and try again.',
    })
    await expect(apiUpload('/offline-upload', new FormData())).rejects.toMatchObject({ status: 0 })
    await expect(streamSSE('/offline-stream', {}).next()).rejects.toMatchObject({ status: 0 })
    await expect(streamSSEGet('/offline-events').next()).rejects.toMatchObject({ status: 0 })

    expect(fetchMock).not.toHaveBeenCalled()
  })
})
