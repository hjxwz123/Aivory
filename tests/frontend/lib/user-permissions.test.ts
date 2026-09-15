import { describe, expect, it } from 'vitest'
import { DEFAULT_USER_PERMISSIONS, userCan, userCanUseMCPServer } from '@/lib/user-permissions'

describe('user group capabilities', () => {
  it.each([
    ['all', [], true],
    ['none', ['usermcp:*'], false],
    ['selected', ['usermcp:*'], true],
    ['selected', ['usermcp:mine'], true],
    ['selected', ['usermcp:another'], false],
    ['selected', ['mcp:mine'], false],
    ['selected', [], false],
  ] as const)('resolves user MCP access for %s / %j', (mode, ids, allowed) => {
    const user = { role: 'user' as const, permissions: { ...DEFAULT_USER_PERMISSIONS, tools: { mode, ids: [...ids] } } }
    expect(userCanUseMCPServer(user, 'mine')).toBe(allowed)
    expect(userCanUseMCPServer({ ...user, role: 'admin' }, 'mine')).toBe(true)
  })

  it.each(['allow_prompts', 'allow_skills', 'allow_workspace_deletion'] as const)(
    'defaults %s for legacy profiles and preserves explicit denials', (capability) => {
      expect(userCan({ role: 'user' }, capability)).toBe(true)
      const permissions = { ...DEFAULT_USER_PERMISSIONS, [capability]: false }
      expect(userCan({ role: 'user', permissions }, capability)).toBe(false)
      expect(userCan({ role: 'admin', permissions }, capability)).toBe(true)
    },
  )
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
