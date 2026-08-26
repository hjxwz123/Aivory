import { afterEach, describe, expect, it, vi } from 'vitest'

import { adminApi } from '@/api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('administrator channel model import API', () => {
  it('posts to the encoded channel-scoped import endpoint and returns the import counts', async () => {
    const response = {
      discovered: 8,
      created: 5,
      skipped_existing: 2,
      skipped_unsupported: 1,
    }
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify(response), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(adminApi.importChannelModels('channel / one')).resolves.toEqual(response)

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/admin/channels/channel%20%2F%20one/models/import')
    expect(options).toMatchObject({ method: 'POST', credentials: 'include' })
  })
})
