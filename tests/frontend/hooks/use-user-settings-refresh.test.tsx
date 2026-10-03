// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useUserSettingsRefresh } from '@/hooks/use-user-settings-refresh'

const mocks = vi.hoisted(() => ({
  auth: { status: 'authenticated', user: { id: 'account-a' } },
  pending: false,
  refresh: vi.fn(),
}))
vi.mock('@/store/auth', () => ({ useAuth: Object.assign(
  (selector: (state: typeof mocks.auth) => unknown) => selector(mocks.auth),
  { getState: () => ({ refreshProfile: mocks.refresh }) },
) }))
vi.mock('@/store/settings', () => ({ useSettings: { getState: () => ({ codeBlockWrapPending: mocks.pending }) } }))

function Probe() {
  useUserSettingsRefresh()
  return null
}

describe('foreground account preference refresh', () => {
  beforeEach(() => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    mocks.auth.status = 'authenticated'
    mocks.pending = false
    mocks.refresh.mockReset()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('refreshes after returning to the foreground and coalesces focus/visibility events', () => {
    let currentTime = 1000
    vi.spyOn(Date, 'now').mockImplementation(() => currentTime)
    const root = createRoot(document.createElement('div'))
    try {
      act(() => root.render(createElement(Probe)))
      currentTime = 31_001
      window.dispatchEvent(new Event('focus'))
      document.dispatchEvent(new Event('visibilitychange'))
      expect(mocks.refresh).toHaveBeenCalledTimes(1)
      currentTime = 62_001
      document.dispatchEvent(new Event('visibilitychange'))
      expect(mocks.refresh).toHaveBeenCalledTimes(2)
    } finally { act(() => root.unmount()) }
  })

  it('skips refreshes while saving or while the page is hidden', () => {
    let currentTime = 1000
    vi.spyOn(Date, 'now').mockImplementation(() => currentTime)
    const root = createRoot(document.createElement('div'))
    try {
      act(() => root.render(createElement(Probe)))
      currentTime = 31_001
      mocks.pending = true
      window.dispatchEvent(new Event('focus'))
      mocks.pending = false
      vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
      document.dispatchEvent(new Event('visibilitychange'))
      expect(mocks.refresh).not.toHaveBeenCalled()
    } finally { act(() => root.unmount()) }
  })

  it('removes foreground listeners after signing out', () => {
    let currentTime = 1000
    vi.spyOn(Date, 'now').mockImplementation(() => currentTime)
    const root = createRoot(document.createElement('div'))
    try {
      act(() => root.render(createElement(Probe)))
      mocks.auth.status = 'unauthenticated'
      act(() => root.render(createElement(Probe)))
      currentTime = 31_001
      window.dispatchEvent(new Event('focus'))
      expect(mocks.refresh).not.toHaveBeenCalled()
    } finally { act(() => root.unmount()) }
  })
})
