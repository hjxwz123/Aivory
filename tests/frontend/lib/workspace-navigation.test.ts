import { describe, expect, it } from 'vitest'
import { workspaceSwitchDestination } from '@/lib/workspace-navigation'

describe('workspace switch navigation', () => {
  it('returns home whenever the active scope changes', () => {
    expect(workspaceSwitchDestination(null, 'workspace-a')).toBe('/')
    expect(workspaceSwitchDestination('workspace-a', 'workspace-b')).toBe('/')
    expect(workspaceSwitchDestination('workspace-a', null)).toBe('/')
  })

  it('keeps the current route for repeated workspace state', () => {
    expect(workspaceSwitchDestination(null, null)).toBeNull()
    expect(workspaceSwitchDestination('workspace-a', 'workspace-a')).toBeNull()
  })
})
