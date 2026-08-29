const REMEMBER_PASSWORD_KEY = 'aivory.remember-password.v1'

type StoredPasswordCredential = {
  id: string
  password?: string
  type?: string
}

type PasswordCredentialManager = {
  get(options: { password: true; mediation: 'optional' }): Promise<StoredPasswordCredential | null>
  store(credential: unknown): Promise<unknown>
  preventSilentAccess?: () => Promise<void>
}

type PasswordCredentialConstructor = new (data: {
  id: string
  password: string
  name?: string
}) => unknown

function credentialManager(): PasswordCredentialManager | null {
  if (typeof navigator === 'undefined' || !navigator.credentials) return null
  return navigator.credentials as unknown as PasswordCredentialManager
}

export function rememberPasswordPreference(): boolean {
  if (typeof localStorage === 'undefined') return false
  try {
    return localStorage.getItem(REMEMBER_PASSWORD_KEY) === 'true'
  } catch {
    return false
  }
}

export function setRememberPasswordPreference(remember: boolean): void {
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(REMEMBER_PASSWORD_KEY, String(remember))
  } catch {
    /* Browser storage can be unavailable in private or locked-down contexts. */
  }
  if (!remember) {
    void credentialManager()?.preventSilentAccess?.().catch(() => {})
  }
}

export async function loadRememberedPassword(): Promise<{ email: string; password: string } | null> {
  if (!rememberPasswordPreference()) return null
  const manager = credentialManager()
  if (!manager) return null
  try {
    const credential = await manager.get({ password: true, mediation: 'optional' })
    if (!credential?.id || typeof credential.password !== 'string') return null
    return { email: credential.id, password: credential.password }
  } catch {
    return null
  }
}

export async function storeRememberedPassword(email: string, password: string): Promise<void> {
  setRememberPasswordPreference(true)
  const manager = credentialManager()
  const PasswordCredential = (globalThis as typeof globalThis & {
    PasswordCredential?: PasswordCredentialConstructor
  }).PasswordCredential
  if (!manager || !PasswordCredential) return
  try {
    await manager.store(new PasswordCredential({ id: email, password, name: email }))
  } catch {
    // The normal autocomplete=password flow remains available when the
    // Credential Management API is unsupported or the user declines storage.
  }
}
