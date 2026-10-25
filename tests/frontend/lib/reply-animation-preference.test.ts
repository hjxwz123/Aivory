import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  updateSettings: vi.fn(),
  auth: { status: 'authenticated', user: { id: 'account-a', settings: { font_family: 'serif' } as Record<string, unknown> } as { id: string; settings: Record<string, unknown> } | null },
}))
vi.mock('@/api', () => ({ authApi: { updateSettings: mocks.updateSettings } }))
vi.mock('@/store/auth', () => ({ useAuth: { getState: () => ({ ...mocks.auth, setUser: (user: typeof mocks.auth.user) => { mocks.auth.user = user } }) } }))

async function setup(cached: unknown = undefined) {
  const values = new Map([['aivory.settings', JSON.stringify({ appearance: { replyAnimation: cached } })]])
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  })
  const { useSettings } = await import('@/store/settings')
  const { saveReplyAnimationPreference } = await import('@/lib/reply-animation-preference')
  return { store: useSettings, save: saveReplyAnimationPreference, values }
}

describe('reply animation account preference', () => {
  beforeEach(() => {
    vi.resetModules()
    mocks.updateSettings.mockReset()
    mocks.auth.status = 'authenticated'
    mocks.auth.user = { id: 'account-a', settings: { font_family: 'serif' } }
  })
  afterEach(() => vi.unstubAllGlobals())

  it.each([undefined, null, 'unknown', true, 1])('uses paper plane for an absent or invalid cached choice: %s', async (cached) => {
    const { store } = await setup(cached)
    expect(store.getState().appearance.replyAnimation).toBe('paper-plane')
  })

  it('restores the browser cache, then lets the signed-in account override it', async () => {
    const { store, values } = await setup('origami')
    expect(store.getState().appearance.replyAnimation).toBe('origami')
    store.getState().syncUserSettings({ reply_animation: 'ink' }, 'account-a')
    expect(store.getState().appearance.replyAnimation).toBe('ink')
    store.getState().syncUserSettings({}, 'account-b')
    expect(store.getState().appearance.replyAnimation).toBe('paper-plane')
    expect(JSON.parse(values.get('aivory.settings')!).appearance.replyAnimation).toBe('paper-plane')
  })

  it('saves the selected animation without overwriting unrelated profile settings', async () => {
    const { store, save, values } = await setup()
    mocks.updateSettings.mockResolvedValue({ reply_animation: 'sprout', font_family: 'default' })
    await save('sprout')
    expect(mocks.updateSettings).toHaveBeenCalledWith({ reply_animation: 'sprout' })
    expect(store.getState().appearance.replyAnimation).toBe('sprout')
    expect(store.getState().replyAnimationPending).toBeNull()
    expect(JSON.parse(values.get('aivory.settings')!).appearance.replyAnimation).toBe('sprout')
    expect(mocks.auth.user?.settings).toEqual({ font_family: 'serif', reply_animation: 'sprout' })
  })

  it('rolls back on failure and when an older server silently rejects the setting', async () => {
    const { store, save } = await setup('orbit')
    mocks.updateSettings.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce({})
    await expect(save('prism')).rejects.toThrow('offline')
    expect(store.getState().appearance.replyAnimation).toBe('orbit')
    await expect(save('ink')).rejects.toThrow('did not save')
    expect(store.getState().appearance.replyAnimation).toBe('orbit')
    expect(store.getState().replyAnimationPending).toBeNull()
  })

  it('protects the live choice during a stale profile refresh and prevents overlapping writes', async () => {
    const { store, save } = await setup()
    let resolve!: (settings: Record<string, unknown>) => void
    mocks.updateSettings.mockReturnValue(new Promise((done) => { resolve = done }))
    const pending = save('origami')
    store.getState().syncUserSettings({ reply_animation: 'paper-plane' }, 'account-a')
    expect(store.getState().appearance.replyAnimation).toBe('origami')
    await expect(save('ink')).rejects.toThrow('already being saved')
    resolve({ reply_animation: 'origami' })
    await pending
    expect(store.getState().appearance.replyAnimation).toBe('origami')
  })

  it('ignores a late response from the previous account', async () => {
    const { store, save } = await setup()
    let reject!: (error: Error) => void
    mocks.updateSettings.mockReturnValue(new Promise((_resolve, fail) => { reject = fail }))
    const pending = save('origami')
    mocks.auth.user = { id: 'account-b', settings: { reply_animation: 'prism' } }
    store.getState().syncUserSettings(mocks.auth.user.settings, 'account-b')
    reject(new Error('old request'))
    await expect(pending).resolves.toBeUndefined()
    expect(store.getState().appearance.replyAnimation).toBe('prism')
  })

  it('cancels an optimistic choice on logout', async () => {
    const { store } = await setup('ink')
    const requestId = store.getState().beginReplyAnimationSave('prism', 'account-a')
    store.getState().cancelReplyAnimationSave()
    store.getState().finishReplyAnimationSave(requestId, 'prism')
    expect(store.getState().appearance.replyAnimation).toBe('ink')
    expect(store.getState().replyAnimationPending).toBeNull()
  })

  it('keeps a local preference without an API request when signed out', async () => {
    const { store, save } = await setup()
    mocks.auth.status = 'unauthenticated'
    mocks.auth.user = null
    await save('orbit')
    expect(store.getState().appearance.replyAnimation).toBe('orbit')
    expect(mocks.updateSettings).not.toHaveBeenCalled()
  })
})
