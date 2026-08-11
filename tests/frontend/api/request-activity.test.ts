import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, setAccessToken } from '@/api/client'
import {
  __resetRequestActivityForTests,
  getRequestActivitySnapshot,
} from '@/lib/request-activity'

describe('API request activity integration', () => {
  beforeEach(() => {
    setAccessToken(null)
    __resetRequestActivityForTests()
  })

  afterEach(() => {
    __resetRequestActivityForTests()
    vi.unstubAllGlobals()
  })

  it('remains active until a deferred response has been parsed', async () => {
    let resolveFetch: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { resolveFetch = resolve })))

    const request = api<{ ok: boolean }>('/admin/deferred')
    expect(getRequestActivitySnapshot()).toEqual({ pending: 1, active: true, slow: false })

    resolveFetch?.(new Response(JSON.stringify({ ok: true }), { status: 200 }))
    await expect(request).resolves.toEqual({ ok: true })
    expect(getRequestActivitySnapshot()).toEqual({ pending: 0, active: false, slow: false })
  })

  it('clears foreground activity after a network error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline') }))

    await expect(api('/admin/failure')).rejects.toThrow('offline')
    expect(getRequestActivitySnapshot().active).toBe(false)
  })

  it('keeps explicitly background requests out of the foreground snapshot', async () => {
    let resolveFetch: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { resolveFetch = resolve })))

    const request = api('/admin/poll', { activity: 'background' })
    expect(getRequestActivitySnapshot()).toEqual({ pending: 0, active: false, slow: false })

    resolveFetch?.(new Response('{}', { status: 200 }))
    await request
  })
})
