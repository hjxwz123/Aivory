// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { safeAuthRedirect } from '@/lib/auth-redirect'
import { clearDesktopAuthorization, pendingDesktopAuthorization, rememberDesktopAuthorization } from '@/lib/desktop'

describe('desktop authorization survives the website login and OAuth redirect', () => {
  beforeEach(() => sessionStorage.clear())
  afterEach(() => vi.unstubAllGlobals())

  it('resumes a valid consent route until explicitly completed', () => {
    const id = 'a'.repeat(43)
    rememberDesktopAuthorization('/desktop/authorize', `?request_id=${id}`)
    expect(pendingDesktopAuthorization()).toBe(`/desktop/authorize?request_id=${id}`)
    rememberDesktopAuthorization('/login', '')
    expect(pendingDesktopAuthorization()).toBe(`/desktop/authorize?request_id=${id}`)
    clearDesktopAuthorization()
    expect(pendingDesktopAuthorization()).toBeNull()
  })

  it('ignores external redirects and expires abandoned requests', () => {
    rememberDesktopAuthorization('/desktop/authorize', '?request_id=https://evil.test')
    expect(pendingDesktopAuthorization()).toBeNull()
    sessionStorage.setItem('aivory.desktop.authorization', JSON.stringify({ id: 'a'.repeat(43), expiresAt: 1 }))
    expect(pendingDesktopAuthorization()).toBeNull()
  })

  it('keeps website consent continuation separate from the native desktop session', () => {
    const id = 'a'.repeat(43)
    rememberDesktopAuthorization('/desktop/authorize', `?request_id=${id}`)
    vi.stubGlobal('window', { aivoryDesktop: {} })
    expect(pendingDesktopAuthorization()).toBeNull()
    clearDesktopAuthorization()
    rememberDesktopAuthorization('/desktop/authorize', `?request_id=${id}`)
    expect(sessionStorage.getItem('aivory.desktop.authorization')).toBeNull()
  })

  it('preserves internal destinations and rejects external or malformed login redirects', () => {
    expect(safeAuthRedirect('/settings/account?section=passkeys')).toBe('/settings/account?section=passkeys')
    for (const from of ['https://evil.test', '//evil.test', '/\\evil.test', '/\nevil.test', '', null, 123]) {
      expect(safeAuthRedirect(from)).toBe('/')
    }
  })
})
