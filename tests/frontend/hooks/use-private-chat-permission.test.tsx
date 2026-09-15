import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { usePrivateChatPermission } from '@/hooks/use-private-chat-permission'

const state = vi.hoisted(() => ({
  user: { role: 'user', permissions: { allow_private_chat: true } },
  workspace: {
    activeId: 'ws', lockedWorkspaceId: null as string | null, loaded: true, switching: false,
    workspaces: [{ id: 'ws', role: 'member' }],
    policies: { ws: { AllowPrivateChat: true } } as Record<string, { AllowPrivateChat: boolean }>,
    policyLoading: {} as Record<string, boolean>, policyErrors: {} as Record<string, string>,
  },
}))
vi.mock('@/store/auth', () => ({ useAuth: (selector: (s: { user: typeof state.user }) => unknown) => selector({ user: state.user }) }))
vi.mock('@/store/workspaces', () => ({ useWorkspaces: (selector: (s: typeof state.workspace) => unknown) => selector(state.workspace) }))

function Probe() {
  const result = usePrivateChatPermission()
  return createElement('span', null, result.allowed ? 'allowed' : 'denied')
}

describe('private chat permission intersection', () => {
  it.each([
    ['member', 'user', true, true, 'allowed'],
    ['admin', 'user', false, true, 'denied'],
    ['admin', 'user', true, false, 'denied'],
    ['admin', 'admin', false, true, 'allowed'],
    ['admin', 'admin', true, false, 'denied'],
    ['guest', 'user', true, true, 'denied'],
  ])('%s / %s / group=%s / workspace=%s', (memberRole, role, group, workspace, expected) => {
    state.user = { role: role as string, permissions: { allow_private_chat: group as boolean } }
    state.workspace.workspaces = [{ id: 'ws', role: memberRole as string }]
    state.workspace.policies = { ws: { AllowPrivateChat: workspace as boolean } }
    expect(renderToStaticMarkup(createElement(Probe))).toBe(`<span>${expected}</span>`)
  })

  it('does not expose private chat while the workspace policy is unknown', () => {
    state.user = { role: 'user', permissions: { allow_private_chat: true } }
    state.workspace.workspaces = [{ id: 'ws', role: 'member' }]
    state.workspace.policies = {}
    expect(renderToStaticMarkup(createElement(Probe))).toBe('<span>denied</span>')
  })

  it('preserves an existing transcript while refreshing policy and blocks sends', () => {
    state.user = { role: 'user', permissions: { allow_private_chat: true } }
    state.workspace.workspaces = [{ id: 'ws', role: 'member' }]
    state.workspace.policies = { ws: { AllowPrivateChat: true } }
    state.workspace.policyLoading = { ws: true }
    function RefreshProbe() {
      const { allowed, canRender, resolved } = usePrivateChatPermission()
      return createElement('span', null, JSON.stringify({ allowed, canRender, resolved }))
    }
    expect(renderToStaticMarkup(createElement(RefreshProbe))).toContain('{&quot;allowed&quot;:false,&quot;canRender&quot;:true,&quot;resolved&quot;:false}')
    state.workspace.policyErrors = { ws: 'failed' }
    expect(renderToStaticMarkup(createElement(RefreshProbe))).toContain('{&quot;allowed&quot;:false,&quot;canRender&quot;:false,&quot;resolved&quot;:true}')
    state.workspace.policyLoading = {}
    state.workspace.policyErrors = {}
  })
})
