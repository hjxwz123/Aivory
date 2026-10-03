import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiUser } from '@/api/types'

const mocks = vi.hoisted(() => ({ me: vi.fn(), updateSettings: vi.fn() }))
vi.mock('@/api', () => ({
  authApi: mocks,
  ApiError: class ApiError extends Error {},
  resetAuthFailureState: vi.fn(),
  setAccessToken: vi.fn(),
}))
vi.mock('@/api/client', () => ({
  isAuthRefreshSuppressed: () => false,
  setAuthLostHandler: vi.fn(),
  setBannedHandler: vi.fn(),
  setInitialPasswordRequiredHandler: vi.fn(),
  setRefreshHandler: vi.fn(),
}))

import { useAuth } from '@/store/auth'
import { persistUserSettings } from '@/lib/user-settings'

const user = { id: 'account-a', name: 'User', email: 'user@example.test', role: 'user', status: 'active', created_at: 1, settings: { code_block_wrap: false } } as ApiUser

describe('profile refresh during preference saves', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAuth.setState({ user, status: 'authenticated' })
  })

  it('keeps the confirmed preference when an older profile request finishes later', async () => {
    let resolveProfile!: (value: ApiUser) => void
    mocks.me.mockReturnValue(new Promise((resolve) => { resolveProfile = resolve }))
    const refreshing = useAuth.getState().refreshProfile()
    mocks.updateSettings.mockResolvedValue({ code_block_wrap: true })
    await persistUserSettings({ code_block_wrap: true })
    resolveProfile({ ...user, name: 'Updated profile', settings: { code_block_wrap: false } })
    await refreshing
    expect(useAuth.getState().user?.settings.code_block_wrap).toBe(true)
    expect(useAuth.getState().user?.name).toBe('Updated profile')
  })

  it('accepts a newer server preference from another device', async () => {
    mocks.me.mockResolvedValue({ ...user, settings: { code_block_wrap: true } })
    await useAuth.getState().refreshProfile()
    expect(useAuth.getState().user?.settings.code_block_wrap).toBe(true)
  })

  it('does not apply an old account profile to a new account', async () => {
    let resolveProfile!: (value: ApiUser) => void
    mocks.me.mockReturnValue(new Promise((resolve) => { resolveProfile = resolve }))
    const refreshing = useAuth.getState().refreshProfile()
    useAuth.setState({ user: { ...user, id: 'account-b', settings: { code_block_wrap: true } } })
    resolveProfile(user)
    await refreshing
    expect(useAuth.getState().user?.id).toBe('account-b')
    expect(useAuth.getState().user?.settings.code_block_wrap).toBe(true)
  })
})
