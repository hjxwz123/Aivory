import { afterEach, describe, expect, it, vi } from 'vitest'
import { conversationsApi } from '@/api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('conversation list API pagination', () => {
  it('requests 20 conversation summaries by default', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(
        JSON.stringify({ conversations: [], limit: 20, offset: 0, has_more: false }),
        { status: 200, headers: { 'content-type': 'application/json' } },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    await conversationsApi.list()

    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/conversations?limit=20&offset=0')
  })
})
