import { afterEach, describe, expect, it, vi } from 'vitest'

import { paymentsApi } from '@/api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('buyer payment order details API', () => {
  it('encodes the order id and forwards an abort signal', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response(JSON.stringify({ id: 'order / 42' }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const controller = new AbortController()

    await expect(paymentsApi.order('order / 42', controller.signal)).resolves.toEqual({ id: 'order / 42' })

    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/payments/orders/order%20%2F%2042')
    expect(options).toMatchObject({ method: 'GET', credentials: 'include', signal: controller.signal })
  })
})
