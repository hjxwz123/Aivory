import { describe, expect, it } from 'vitest'
import {
  ADMIN_NAV_GROUPS,
  adminNavGroupActive,
  adminNavGroupForPath,
  adminNavItemActive,
  underAdminPath,
} from '@/lib/admin-navigation'

function activeItemPath(path: string): string | undefined {
  return adminNavGroupForPath(path)?.items.find((item) => adminNavItemActive(path, item))?.to
}

describe('administrator navigation', () => {
  it('keeps stable group destinations and unique tab routes', () => {
    const itemPaths = ADMIN_NAV_GROUPS.flatMap((group) => group.items.map((item) => item.to))

    expect(ADMIN_NAV_GROUPS.map((group) => [group.key, group.to])).toEqual([
      ['ai', '/admin/channels'],
      ['capabilities', '/admin/skills'],
      ['access', '/admin/users'],
      ['billing', '/admin/user-groups'],
      ['operations', '/admin/analytics'],
      ['platform', '/admin/announcement'],
    ])
    expect(new Set(itemPaths).size).toBe(itemPaths.length)
    for (const group of ADMIN_NAV_GROUPS) {
      expect(group.items.some((item) => item.to === group.to)).toBe(true)
    }
  })

  it.each([
    ['/admin/settings/model-policy', 'ai'],
    ['/admin/settings/context-memory', 'ai'],
    ['/admin/settings/registration', 'access'],
    ['/admin/credits', 'billing'],
    ['/admin/payment-channels', 'billing'],
    ['/admin/payment-methods', 'billing'],
    ['/admin/payment-orders', 'billing'],
    ['/admin/files', 'operations'],
    ['/admin/settings/email', 'platform'],
    ['/admin/storage', 'platform'],
    ['/admin/settings/legal', 'platform'],
    ['/admin/settings/logging', 'platform'],
    ['/admin/backup', 'platform'],
  ])('maps %s to the %s group', (path, groupKey) => {
    expect(adminNavGroupForPath(path)?.key).toBe(groupKey)
  })

  it('keeps model and user drill-down routes on their parent tabs', () => {
    expect(adminNavGroupForPath('/admin/models/model-1')?.key).toBe('ai')
    expect(activeItemPath('/admin/models/model-1')).toBe('/admin/models')

    expect(adminNavGroupForPath('/admin/model-tags')?.key).toBe('ai')
    expect(activeItemPath('/admin/model-tags')).toBe('/admin/models')

    expect(adminNavGroupForPath('/admin/users/user-1/conversations/chat-1')?.key).toBe('access')
    expect(activeItemPath('/admin/users/user-1/conversations/chat-1')).toBe('/admin/users')
  })

  it('matches path segment boundaries instead of similarly prefixed routes', () => {
    const access = ADMIN_NAV_GROUPS.find((group) => group.key === 'access')

    expect(underAdminPath('/admin/users/user-1', '/admin/users')).toBe(true)
    expect(underAdminPath('/admin/user-groups', '/admin/users')).toBe(false)
    expect(access && adminNavGroupActive('/admin/user-groups', access)).toBe(false)
    expect(adminNavGroupForPath('/admin/user-groups')?.key).toBe('billing')
  })

  it('does not invent tabs for overview, unknown, or compatibility-only routes', () => {
    expect(adminNavGroupForPath('/admin/overview')).toBeUndefined()
    expect(adminNavGroupForPath('/admin/not-a-route')).toBeUndefined()
    // App.tsx owns this legacy redirect; it is intentionally not a visible tab.
    expect(adminNavGroupForPath('/admin/settings')).toBeUndefined()
  })
})
