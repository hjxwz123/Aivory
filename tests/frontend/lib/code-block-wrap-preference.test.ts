import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  updateSettings: vi.fn(),
  auth: { status: 'authenticated', user: { id: 'account-a', settings: { font_family: 'serif' } as Record<string, unknown> } as { id: string; settings: Record<string, unknown> } | null },
}))

vi.mock('@/api', () => ({ authApi: { updateSettings: mocks.updateSettings } }))
vi.mock('@/store/auth', () => ({
  useAuth: {
    getState: () => ({
      ...mocks.auth,
      setUser: (user: typeof mocks.auth.user) => { mocks.auth.user = user },
    }),
  },
}))

async function setup() {
  const { useSettings } = await import('@/store/settings')
  const { saveCodeBlockWrapPreference } = await import('@/lib/code-block-wrap-preference')
  return { store: useSettings, save: saveCodeBlockWrapPreference }
}

describe('saving the code wrapping preference', () => {
  beforeEach(() => {
    vi.resetModules()
    mocks.updateSettings.mockReset()
    mocks.auth.status = 'authenticated'
    mocks.auth.user = { id: 'account-a', settings: { font_family: 'serif' } }
    vi.stubGlobal('localStorage', { getItem: () => null, setItem: vi.fn() })
  })
  afterEach(() => vi.unstubAllGlobals())

  it('confirms cloud persistence while preserving newer unrelated local profile fields', async () => {
    const { store, save } = await setup()
    mocks.updateSettings.mockResolvedValue({ code_block_wrap: true, font_family: 'default' })
    await save(true)
    expect(mocks.updateSettings).toHaveBeenCalledWith({ code_block_wrap: true })
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
    expect(store.getState().codeBlockWrapPending).toBeNull()
    expect(mocks.auth.user?.settings).toEqual({ font_family: 'serif', code_block_wrap: true })
  })

  it.each([{}, { code_block_wrap: false }, { code_block_wrap: 'true' }])('rolls back a server that did not accept the value: %s', async (response) => {
    const { store, save } = await setup()
    mocks.updateSettings.mockResolvedValue(response)
    await expect(save(true)).rejects.toThrow('did not save')
    expect(store.getState().appearance.codeBlockWrap).toBe(false)
    expect(store.getState().codeBlockWrapPending).toBeNull()
  })

  it('rolls back a network failure', async () => {
    const { store, save } = await setup()
    mocks.updateSettings.mockRejectedValue(new Error('offline'))
    await expect(save(true)).rejects.toThrow('offline')
    expect(store.getState().appearance.codeBlockWrap).toBe(false)
  })

  it('saves locally without a request when signed out', async () => {
    const { store, save } = await setup()
    mocks.auth.status = 'unauthenticated'
    mocks.auth.user = null
    await save(true)
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
    expect(mocks.updateSettings).not.toHaveBeenCalled()
  })

  it('prevents overlapping saves and protects the optimistic value during profile synchronization', async () => {
    const { store, save } = await setup()
    let resolveRequest!: (value: Record<string, unknown>) => void
    mocks.updateSettings.mockReturnValue(new Promise((resolve) => { resolveRequest = resolve }))
    const pending = save(true)
    store.getState().syncUserSettings({ code_block_wrap: false }, 'account-a')
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
    await expect(save(false)).rejects.toThrow('already being saved')
    resolveRequest({ code_block_wrap: true })
    await pending
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
  })

  it('ignores an old account failure after switching accounts', async () => {
    const { store, save } = await setup()
    let rejectRequest!: (error: Error) => void
    mocks.updateSettings.mockReturnValue(new Promise((_resolve, reject) => { rejectRequest = reject }))
    const pending = save(true)
    mocks.auth.user = { id: 'account-b', settings: { code_block_wrap: true } }
    store.getState().syncUserSettings(mocks.auth.user.settings, 'account-b')
    rejectRequest(new Error('old account request failed'))
    await expect(pending).resolves.toBeUndefined()
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
  })
})
