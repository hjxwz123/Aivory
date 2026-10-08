// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import { clearDesktopAuthorization, pendingDesktopAuthorization, rememberDesktopAuthorization } from '@/lib/desktop'

describe('desktop authorization survives the website login and OAuth redirect', () => {
  beforeEach(() => sessionStorage.clear())

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
})
