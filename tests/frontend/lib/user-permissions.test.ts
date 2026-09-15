import { describe, expect, it } from 'vitest'
import { DEFAULT_USER_PERMISSIONS, userCan } from '@/lib/user-permissions'

describe('user group capabilities', () => {
  it('keeps private chat enabled for legacy profiles without permissions', () => {
    expect(userCan({ role: 'user' }, 'allow_private_chat')).toBe(true)
  })

  it('honors the private chat capability for regular users', () => {
    expect(userCan({
      role: 'user',
      permissions: { ...DEFAULT_USER_PERMISSIONS, allow_private_chat: false },
    }, 'allow_private_chat')).toBe(false)
  })

  it('keeps administrator access independent of group restrictions', () => {
    expect(userCan({
      role: 'admin',
      permissions: { ...DEFAULT_USER_PERMISSIONS, allow_private_chat: false },
    }, 'allow_private_chat')).toBe(true)
  })
})
