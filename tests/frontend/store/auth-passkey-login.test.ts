import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiUser } from '@/api/types'

const apiMocks = vi.hoisted(() => ({
  session: vi.fn(),
  authPolicy: vi.fn(),
  signupOpen: vi.fn(),
  needsSetup: vi.fn(),
  beginPasskeyLogin: vi.fn(),
  verifyPasskeyLogin: vi.fn(),
}))

const clientMocks = vi.hoisted(() => ({
  setAccessToken: vi.fn(),
  resetAuthFailureState: vi.fn(),
}))

const passkeyMocks = vi.hoisted(() => {
  class PasskeyError extends Error {
    code: string
    constructor(code: string) {
      super(code)
      this.code = code
      this.name = 'PasskeyError'
    }
  }
  return { requestPasskeyAssertion: vi.fn(), PasskeyError }
})

vi.mock('@/api', () => ({
  authApi: apiMocks,
  ApiError: class ApiError extends Error {
    status: number
    body: unknown
    constructor(status: number, message: string, body: unknown) {
      super(message)
      this.status = status
      this.body = body
    }
  },
  setAccessToken: clientMocks.setAccessToken,
  resetAuthFailureState: clientMocks.resetAuthFailureState,
}))

vi.mock('@/api/client', () => ({
  isAuthRefreshSuppressed: () => false,
  setAuthLostHandler: vi.fn(),
  setBannedHandler: vi.fn(),
  setInitialPasswordRequiredHandler: vi.fn(),
  setRefreshHandler: vi.fn(),
}))

vi.mock('@/lib/passkey', () => ({
  isPasskeyAvailable: () => true,
  requestPasskeyAssertion: passkeyMocks.requestPasskeyAssertion,
  PasskeyError: passkeyMocks.PasskeyError,
}))

import { ApiError } from '@/api'
import { useAuth } from '@/store/auth'

const user = {
  id: 'user-1',
  email: 'user@example.test',
  name: 'User',
  role: 'user',
  status: 'active',
  settings: {},
  created_at: 1,
} as ApiUser

describe('passkey login', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAuth.setState({ user: null, status: 'idle', error: null, pendingTwoFactor: null })
    apiMocks.beginPasskeyLogin.mockResolvedValue({ ticket: 't1', options: { publicKey: { challenge: 'ch' } } })
    passkeyMocks.requestPasskeyAssertion.mockResolvedValue({ id: 'cred', type: 'public-key', response: {} })
  })

  it('completes the ceremony and stores the session', async () => {
    apiMocks.verifyPasskeyLogin.mockResolvedValue({
      user,
      access_token: 'jwt',
      request_signing_key: 'sk',
      expires_at: 123,
    })
    const ok = await useAuth.getState().loginWithPasskey()
    expect(ok).toBe(true)
    expect(passkeyMocks.requestPasskeyAssertion).toHaveBeenCalledWith({ publicKey: { challenge: 'ch' } })
    expect(apiMocks.verifyPasskeyLogin).toHaveBeenCalledWith('t1', { id: 'cred', type: 'public-key', response: {} })
    expect(clientMocks.setAccessToken).toHaveBeenCalledWith('jwt', 'sk')
    const state = useAuth.getState()
    expect(state.status).toBe('authenticated')
    expect(state.user?.id).toBe('user-1')
    expect(state.error).toBeNull()
  })

  it('stays silent when the user dismisses the biometric prompt', async () => {
    passkeyMocks.requestPasskeyAssertion.mockRejectedValue(new passkeyMocks.PasskeyError('passkey_cancelled'))
    const ok = await useAuth.getState().loginWithPasskey()
    expect(ok).toBe(false)
    const state = useAuth.getState()
    expect(state.status).toBe('unauthenticated')
    expect(state.error).toBeNull()
  })

  it('surfaces server error codes for the login page to translate', async () => {
    apiMocks.verifyPasskeyLogin.mockRejectedValue(new ApiError(401, 'passkey_login_failed', null))
    const ok = await useAuth.getState().loginWithPasskey()
    expect(ok).toBe(false)
    expect(useAuth.getState().error).toBe('passkey_login_failed')
    expect(useAuth.getState().status).toBe('unauthenticated')
  })
})
