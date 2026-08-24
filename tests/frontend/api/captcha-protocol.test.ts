import { afterEach, describe, expect, it, vi } from 'vitest'
import { authApi } from '@/api'
import { setAccessToken } from '@/api/client'

describe('captcha API protocol', () => {
  afterEach(() => {
    setAccessToken(null)
    vi.unstubAllGlobals()
  })

  it('binds challenge issuance to a purpose and sends the bounded interaction trace', async () => {
    const challenge = {
      id: 'challenge-id',
      background: 'data:image/png;base64,bg',
      piece: 'data:image/png;base64,piece',
      w: 280,
      h: 160,
      piece_size: 52,
      piece_y: 40,
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(challenge), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true, token: 'pass' }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(authApi.captcha('login')).resolves.toEqual(challenge)
    const solution = {
      fraction: 0.51,
      interaction: {
        mode: 'pointer' as const,
        points: [{ x: 0, t: 0 }, { x: 0.22, t: 95 }, { x: 0.51, t: 240 }],
      },
    }
    await expect(authApi.captchaVerify(challenge.id, solution)).resolves.toEqual({ ok: true, token: 'pass' })

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/public/captcha?purpose=login', expect.objectContaining({
      method: 'GET',
      credentials: 'include',
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/public/captcha/verify', expect.objectContaining({
      method: 'POST',
      credentials: 'include',
      body: JSON.stringify({ id: challenge.id, ...solution }),
    }))
  })
})
