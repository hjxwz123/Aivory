/**
 * Stable browser-install and per-page identifiers.
 *
 * X-Device-Id is persisted locally and participates in authenticated request
 * proofs, giving rate limits and audit signals a stable browser identifier.
 * X-Client-Id is intentionally per page load and is used only to suppress a
 * tab's own realtime event echo; duplicated tabs must not share that value.
 */

const DEVICE_STORAGE_KEY = 'aivory-device-id-v1'

let cachedDevice = ''
let cachedClient = ''

function randomID(prefix: string): string {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  const value = btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/g, '')
  return `${prefix}-${value}`
}

export function getDeviceId(): string {
  if (cachedDevice) return cachedDevice
  try {
    const stored = localStorage.getItem(DEVICE_STORAGE_KEY)
    if (stored && /^dv-[A-Za-z0-9_-]{22}$/.test(stored)) {
      cachedDevice = stored
      return cachedDevice
    }
  } catch {
    // Storage can be unavailable in hardened/private browser contexts.
  }
  cachedDevice = randomID('dv')
  try {
    localStorage.setItem(DEVICE_STORAGE_KEY, cachedDevice)
  } catch {
    // Keep the in-memory identifier for this page load.
  }
  return cachedDevice
}

export function getClientInstanceId(): string {
  if (!cachedClient) cachedClient = randomID('tab')
  return cachedClient
}
