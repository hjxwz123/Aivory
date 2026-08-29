import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  loadRememberedPassword,
  rememberPasswordPreference,
  setRememberPasswordPreference,
  storeRememberedPassword,
} from '@/lib/password-credentials'

describe('password credential storage', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('stores only the preference in localStorage and delegates the password to the browser', async () => {
    const storage = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
    })
    const store = vi.fn(async () => undefined)
    vi.stubGlobal('navigator', { credentials: { store, get: vi.fn() } })
    vi.stubGlobal('PasswordCredential', class {
      constructor(public data: unknown) {}
    })

    await storeRememberedPassword('user@example.test', 'secret-password')

    expect(rememberPasswordPreference()).toBe(true)
    expect([...storage.values()]).not.toContain('secret-password')
    expect(store).toHaveBeenCalledOnce()
  })

  it('loads credentials only while the preference is enabled', async () => {
    const storage = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
    })
    const get = vi.fn(async () => ({ id: 'user@example.test', password: 'saved', type: 'password' }))
    vi.stubGlobal('navigator', { credentials: { get, store: vi.fn(), preventSilentAccess: vi.fn() } })

    expect(await loadRememberedPassword()).toBeNull()
    expect(get).not.toHaveBeenCalled()

    setRememberPasswordPreference(true)
    expect(await loadRememberedPassword()).toEqual({ email: 'user@example.test', password: 'saved' })
  })
})
