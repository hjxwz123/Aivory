import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiFetch } from '@/lib/api-fetch'

afterEach(() => vi.unstubAllGlobals())

describe('apiFetch desktop transport', () => {
  it('preserves web fetch and external requests without using the desktop bridge', async () => {
    const response = new Response('web')
    const fetch = vi.fn().mockResolvedValue(response)
    const startApiRequest = vi.fn()
    vi.stubGlobal('fetch', fetch)
    vi.stubGlobal('window', { location: { href: 'https://app.aivory.invalid/', origin: 'https://app.aivory.invalid' } })
    const init = { signal: new AbortController().signal }
    expect(await apiFetch('/api/test', init)).toBe(response)
    expect(fetch).toHaveBeenCalledWith('/api/test', init)
    Object.assign(window, { aivoryDesktop: { startApiRequest, abortApiRequest: vi.fn() } })
    expect(await apiFetch('https://external.example/image.png', init)).toBe(response)
    expect(startApiRequest).not.toHaveBeenCalled()
  })

  it('streams first bytes immediately and explicitly cancels the native request on abort', async () => {
    const startApiRequest = vi.fn()
    const abortApiRequest = vi.fn()
    const fetch = vi.fn().mockResolvedValue(new Response(new ReadableStream({
      start(controller) { controller.enqueue(new TextEncoder().encode('first')) },
    }), { headers: { 'content-type': 'text/event-stream' } }))
    vi.stubGlobal('window', { location: { href: 'https://app.aivory.invalid/', origin: 'https://app.aivory.invalid' },
      aivoryDesktop: { startApiRequest, abortApiRequest } })
    vi.stubGlobal('fetch', fetch)
    const controller = new AbortController()
    const response = await apiFetch('/api/chat', { signal: controller.signal, headers: { authorization: 'Bearer test' } })
    const requestId = startApiRequest.mock.calls[0][0]
    expect(fetch.mock.calls[0][1].headers.get('x-aivory-desktop-request')).toBe(requestId)
    expect(fetch.mock.calls[0][1].headers.get('authorization')).toBe('Bearer test')
    const reader = response.body!.getReader()
    expect(new TextDecoder().decode((await reader.read()).value)).toBe('first')
    controller.abort()
    expect(abortApiRequest).toHaveBeenCalledWith(requestId)
    await reader.cancel()
  })
})
