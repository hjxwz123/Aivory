import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiAuthPolicy, ApiUser } from '@/api/types'

const apiMocks = vi.hoisted(() => ({
  session: vi.fn(),
  authPolicy: vi.fn(),
  signupOpen: vi.fn(),
  needsSetup: vi.fn(),
}))

const clientMocks = vi.hoisted(() => ({
  setAccessToken: vi.fn(),
  resetAuthFailureState: vi.fn(),
}))

vi.mock('@/api', () => ({
  authApi: apiMocks,
  ApiError: class ApiError extends Error {},
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

import { DEFAULT_AUTH_POLICY, useAuth } from '@/store/auth'

const policy: ApiAuthPolicy = {
  password_login_enabled: false,
  entry_mode: 'provider_picker',
  default_provider: null,
  oauth_initial_password_policy: 'optional',
  oauth_auto_provision_enabled: true,
  providers: [],
}

const user = {
  id: 'user-1',
  email: 'user@example.test',
  name: 'User',
  role: 'user',
  status: 'active',
  settings: {},
  created_at: 1,
} as ApiUser

describe('auth startup request coalescing', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAuth.setState({
      user: null,
      status: 'idle',
      authPolicy: DEFAULT_AUTH_POLICY,
      authPolicyLoaded: false,
      signupOpen: true,
      captchaRequired: false,
      loginCaptchaRequired: false,
      needsSetup: false,
      setupProbed: false,
      error: null,
    })
    apiMocks.authPolicy.mockResolvedValue(policy)
    apiMocks.signupOpen.mockResolvedValue({ open: true, captcha_required: false, login_captcha_required: false })
    apiMocks.needsSetup.mockResolvedValue({ needs_setup: false })
  })

  it('uses the policy embedded in an authenticated session without public probes', async () => {
    apiMocks.session.mockResolvedValue({
      authenticated: true,
      user,
      access_token: 'access-token',
      request_signing_key: 'request-signing-key',
      expires_at: 2,
      auth_policy: policy,
    })

    await useAuth.getState().hydrate()

    expect(apiMocks.session).toHaveBeenCalledOnce()
    expect(apiMocks.authPolicy).not.toHaveBeenCalled()
    expect(apiMocks.signupOpen).not.toHaveBeenCalled()
    expect(apiMocks.needsSetup).not.toHaveBeenCalled()
    expect(clientMocks.setAccessToken).toHaveBeenCalledWith('access-token', 'request-signing-key')
    expect(useAuth.getState()).toMatchObject({
      user,
      status: 'authenticated',
      authPolicy: policy,
      authPolicyLoaded: true,
      setupProbed: true,
    })
  })

  it('runs only the setup and signup probes after a signed-out session', async () => {
    apiMocks.session.mockResolvedValue({ authenticated: false, auth_policy: policy })

    await useAuth.getState().hydrate()

    expect(apiMocks.authPolicy).not.toHaveBeenCalled()
    expect(apiMocks.signupOpen).toHaveBeenCalledOnce()
    expect(apiMocks.needsSetup).toHaveBeenCalledOnce()
    expect(useAuth.getState()).toMatchObject({
      status: 'unauthenticated',
      authPolicy: policy,
      authPolicyLoaded: true,
      setupProbed: true,
    })
  })

  it('falls back to the standalone policy endpoint during a rolling upgrade', async () => {
    apiMocks.session.mockResolvedValue({ authenticated: true, user, access_token: 'access-token', expires_at: 2 })

    await useAuth.getState().hydrate()

    expect(apiMocks.authPolicy).toHaveBeenCalledOnce()
    expect(useAuth.getState().authPolicy).toEqual(policy)
  })
})
