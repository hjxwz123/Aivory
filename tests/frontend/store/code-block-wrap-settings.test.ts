import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

async function loadSettings(appearance: Record<string, unknown> = {}) {
  const values = new Map([['aivory.settings', JSON.stringify({ appearance })]])
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  })
  const { useSettings } = await import('@/store/settings')
  return { store: useSettings, values }
}

describe('code block wrapping settings', () => {
  beforeEach(() => vi.resetModules())
  afterEach(() => vi.unstubAllGlobals())

  it.each([undefined, false, 'true', 1, null])('defaults missing or invalid cache values to false: %s', async (value) => {
    const { store } = await loadSettings({ codeBlockWrap: value })
    expect(store.getState().appearance.codeBlockWrap).toBe(false)
  })

  it('restores and persists a valid browser preference', async () => {
    const { store, values } = await loadSettings({ codeBlockWrap: true })
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
    store.getState().setAppearance({ codeBlockWrap: false })
    expect(JSON.parse(values.get('aivory.settings')!).appearance.codeBlockWrap).toBe(false)
  })

  it('uses the cloud value and clears a cached preference when the new account has no field', async () => {
    const { store } = await loadSettings({ codeBlockWrap: true })
    store.getState().syncUserSettings({}, 'account-b')
    expect(store.getState().appearance.codeBlockWrap).toBe(false)
    store.getState().syncUserSettings({ code_block_wrap: true }, 'account-b')
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
  })

  it('protects a pending optimistic value from old profile snapshots without persisting request state', async () => {
    const { store, values } = await loadSettings()
    const requestId = store.getState().beginCodeBlockWrapSave(true, 'account-a')
    store.getState().syncUserSettings({ code_block_wrap: false }, 'account-a')
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
    expect(JSON.parse(values.get('aivory.settings')!)).not.toHaveProperty('codeBlockWrapPending')
    store.getState().finishCodeBlockWrapSave(requestId, true)
    expect(store.getState().codeBlockWrapPending).toBeNull()
  })

  it('drops the old account request and ignores its late rollback', async () => {
    const { store } = await loadSettings()
    const requestId = store.getState().beginCodeBlockWrapSave(true, 'account-a')
    store.getState().syncUserSettings({ code_block_wrap: true }, 'account-b')
    store.getState().finishCodeBlockWrapSave(requestId, false)
    expect(store.getState().appearance.codeBlockWrap).toBe(true)
    expect(store.getState().codeBlockWrapPending).toBeNull()
    expect(store.getState().codeBlockWrapAccountId).toBe('account-b')
  })

  it('cancels an optimistic save on logout and restores the previous value', async () => {
    const { store } = await loadSettings()
    store.getState().beginCodeBlockWrapSave(true, 'account-a')
    store.getState().cancelCodeBlockWrapSave()
    expect(store.getState().appearance.codeBlockWrap).toBe(false)
    expect(store.getState().codeBlockWrapAccountId).toBeNull()
    expect(store.getState().codeBlockWrapPending).toBeNull()
  })
})
