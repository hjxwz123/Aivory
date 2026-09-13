import { afterEach, describe, expect, it, vi } from 'vitest'
import { authApi } from '@/api'
import { setAccessToken } from '@/api/client'

describe('passkey API protocol', () => {
  afterEach(() => {
    setAccessToken(null)
    vi.unstubAllGlobals()
  })

  it('runs the login ceremony over unsigned /auth/ endpoints with the ticket envelope', async () => {
    const options = { publicKey: { challenge: 'authch', allowCredentials: [], userVerification: 'required' } }
    const assertion = { id: 'c', rawId: 'c', type: 'public-key', response: { clientDataJSON: 'x', authenticatorData: 'y', signature: 'z', userHandle: null } }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ticket: 't1', options }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ user: { id: 'u1' }, access_token: 'jwt', request_signing_key: 'sk', expires_at: 1 }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    const begin = await authApi.beginPasskeyLogin()
    await authApi.verifyPasskeyLogin(begin.ticket, assertion)

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/auth/passkey/begin', expect.objectContaining({
      method: 'POST',
      credentials: 'include',
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/auth/passkey/verify', expect.objectContaining({
      method: 'POST',
      credentials: 'include',
      body: JSON.stringify({ ticket: 't1', response: assertion }),
    }))
  })

  it('posts the registration finish body verbatim (server parses it as the WebAuthn response)', async () => {
    const creation = { id: 'c', rawId: 'c', type: 'public-key', response: { clientDataJSON: 'x', attestationObject: 'y', transports: ['internal'] } }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ publicKey: { challenge: 'regch' } }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify([]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await authApi.beginPasskeyRegistration('iPhone')
    await authApi.finishPasskeyRegistration(creation)
    await authApi.passkeys()
    await authApi.deletePasskey('pk/odd id')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/me/passkeys/begin', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'iPhone' }),
    }))
    // finish sends the serialized credential itself as the whole body — the
    // server replays it into the WebAuthn parser.
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/me/passkeys/finish', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify(creation),
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/me/passkeys', expect.objectContaining({ method: 'GET' }))
    expect(fetchMock).toHaveBeenNthCalledWith(4, `/api/me/passkeys/${encodeURIComponent('pk/odd id')}`, expect.objectContaining({ method: 'DELETE' }))
  })
})
