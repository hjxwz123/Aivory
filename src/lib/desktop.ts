export type DesktopLoginStatus = 'authorized' | 'denied' | 'expired' | 'failed' | 'cancelled' | 'busy'

declare global {
  interface Window {
    aivoryDesktop?: {
      getInfo: () => Promise<{ version: string; platform: string }>
      loginInBrowser: () => Promise<{ status: DesktopLoginStatus }>
      cancelBrowserLogin: () => Promise<{ status: 'cancelled' }>
      checkUpdates: () => Promise<{ status: 'available' | 'current' | 'failed'; version?: string }>
    }
  }
}

const pendingKey = 'aivory.desktop.authorization'
const requestPattern = /^[A-Za-z0-9_-]{43}$/

export function rememberDesktopAuthorization(path: string, search: string) {
  if (path !== '/desktop/authorize') return
  const id = new URLSearchParams(search).get('request_id')
  if (!id || !requestPattern.test(id)) return
  try {
    const current = JSON.parse(sessionStorage.getItem(pendingKey) || 'null')
    if (current?.id === id && current.expiresAt > Date.now()) return
    sessionStorage.setItem(pendingKey, JSON.stringify({ id, expiresAt: Date.now() + 5 * 60 * 1000 }))
  } catch { /* Authorization still works through the login route's from state. */ }
}

export function pendingDesktopAuthorization(): string | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(pendingKey) || 'null')
    if (value && requestPattern.test(value.id) && value.expiresAt > Date.now()) {
      return `/desktop/authorize?request_id=${encodeURIComponent(value.id)}`
    }
    sessionStorage.removeItem(pendingKey)
  } catch { /* Storage may be unavailable in private browsing. */ }
  return null
}

export function clearDesktopAuthorization() {
  try { sessionStorage.removeItem(pendingKey) } catch { /* Best effort. */ }
}
