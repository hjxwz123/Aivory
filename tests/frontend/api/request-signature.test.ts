import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, authenticatedRequestHeaders, setAccessToken } from '@/api/client'
import { hmacSha256, sha256 } from '@/lib/hmac-sha256'

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

function base64(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes))
}

describe('authenticated request proof', () => {
  afterEach(() => {
    setAccessToken(null)
    vi.unstubAllGlobals()
  })

  it('binds the bearer proof to method, query, device, and JSON payload', async () => {
    const jwt = 'frontend-request-proof-token'
    const body = JSON.stringify({ prompt: 'hello' })
    const requestKey = 'frontend-session-request-key'
    setAccessToken(jwt, requestKey)

    const headers = await authenticatedRequestHeaders(
      '/conversations/c1/messages?mode=tree&limit=20',
      'POST',
      body,
    )

    expect(headers.authorization).toBe(`Bearer ${jwt}`)
    expect(headers['x-device-id']).toBeTruthy()
    expect(headers['x-req-nonce']).toMatch(/^[A-Za-z0-9_-]{20,64}$/)
    expect(headers['x-req-content-sha256']).toBe(hex(sha256(new TextEncoder().encode(body))))

    const ts = Number(headers['x-req-ts'])
    const message = [
      'v2',
      String(ts),
      headers['x-req-nonce'],
      'POST',
      '/conversations/c1/messages?mode=tree&limit=20',
      headers['x-device-id'],
      headers['x-req-content-sha256'],
      hex(sha256(new TextEncoder().encode(jwt))),
    ].join('\x00')
    const encoder = new TextEncoder()
    const derived = hmacSha256(encoder.encode(requestKey), encoder.encode(String(Math.floor(ts / 3600))))
    const expected = base64(hmacSha256(derived, encoder.encode(message)))
    expect(headers['x-req-token']).toBe(expected)
  })

  it('uses a fresh nonce for every request and marks multipart payloads explicitly', async () => {
    setAccessToken('frontend-request-proof-token', 'frontend-session-request-key')

    const first = await authenticatedRequestHeaders('/files?conversation_id=c1', 'POST', '', true)
    const second = await authenticatedRequestHeaders('/files?conversation_id=c1', 'POST', '', true)

    expect(first['x-req-content-sha256']).toBe('UNSIGNED-PAYLOAD')
    expect(second['x-req-content-sha256']).toBe('UNSIGNED-PAYLOAD')
    expect(first['x-req-nonce']).not.toBe(second['x-req-nonce'])
    expect(first['x-req-token']).not.toBe(second['x-req-token'])
  })

  it('injects the proof into an actual api() request', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) =>
      new Response('{}', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    setAccessToken('frontend-request-proof-token', 'frontend-session-request-key')

    await api('/projects/p1?include=documents', {
      method: 'PATCH',
      body: { name: 'Signed project' },
    })

    expect(fetchMock).toHaveBeenCalledOnce()
    const init = fetchMock.mock.calls[0]?.[1]
    if (!init) throw new Error('missing fetch init')
    const headers = init.headers as Record<string, string>
    expect(headers.authorization).toBe('Bearer frontend-request-proof-token')
    expect(headers['x-device-id']).toMatch(/^dv-[A-Za-z0-9_-]{22}$/)
    expect(headers['x-client-id']).toMatch(/^tab-[A-Za-z0-9_-]{22}$/)
    expect(headers['x-req-token']).toBeTruthy()
    expect(headers['x-req-content-sha256']).toBe(
      hex(sha256(new TextEncoder().encode('{"name":"Signed project"}'))),
    )
  })
})
