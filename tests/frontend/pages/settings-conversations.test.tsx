// @vitest-environment jsdom
import { act, createElement, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Conversations from '@/pages/settings/Conversations'

const mocks = vi.hoisted(() => ({
  archived: vi.fn(), update: vi.fn(), shared: vi.fn(), html: vi.fn(), revokeShared: vi.fn(), revokeHTML: vi.fn(),
  load: vi.fn(), remove: vi.fn(), copy: vi.fn(), success: vi.fn(), error: vi.fn(), close: vi.fn(),
  auth: { user: { id: 'owner', role: 'admin' } },
}))
vi.mock('@/api', () => ({ conversationsApi: { listArchived: mocks.archived, update: mocks.update }, apiUrl: (path: string) => `/api${path}` }))
vi.mock('@/api/user-links', () => ({ userLinksApi: { conversations: mocks.shared, htmlPreviews: mocks.html, revokeConversation: mocks.revokeShared, revokeHTMLPreview: mocks.revokeHTML } }))
vi.mock('@/store/conversations', () => ({ useConversations: { getState: () => ({ load: mocks.load, deleteConversation: mocks.remove }) } }))
vi.mock('@/store/auth', () => {
  const useAuth = Object.assign((selector: (state: typeof mocks.auth) => unknown) => selector(mocks.auth), { getState: () => mocks.auth })
  return { useAuth }
})
vi.mock('@/store/settings-modal', () => ({ useSettingsModal: Object.assign((selector: (state: { open: boolean; tab: string }) => unknown) => selector({ open: true, tab: 'conversations' }), { getState: () => ({ close: mocks.close }) }) }))
vi.mock('@/store/language', () => ({ useLanguage: (selector: (state: { lang: string }) => unknown) => selector({ lang: 'en' }) }))
vi.mock('@/lib/utils', async (original) => ({ ...await original<typeof import('@/lib/utils')>(), copyText: mocks.copy }))
vi.mock('@/hooks/use-toast', () => ({ toast: { success: mocks.success, error: mocks.error } }))
vi.mock('@/components/ui/tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))
vi.mock('react-i18next', () => {
  const t = (key: string) => key
  return { useTranslation: () => ({ t }) }
})

let root: Root
let container: HTMLDivElement
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.clearAllMocks()
  mocks.auth.user.id = 'owner'
  mocks.archived.mockReset().mockResolvedValue({ conversations: [{ id: 'archived-1', title: 'Archived conversation', updated_at: 1775001600 }], has_more: false })
  mocks.shared.mockReset().mockResolvedValue({ items: [{ id: 'sh_own', title: 'Shared conversation', created_at: 1775001600 }], has_more: false })
  mocks.html.mockReset().mockResolvedValue({ items: [{ id: 'hp_own', created_at: 1775001600 }], has_more: false })
  mocks.update.mockReset().mockResolvedValue({ archived: false })
  mocks.revokeHTML.mockReset().mockResolvedValue({ ok: true })
  mocks.revokeShared.mockReset().mockResolvedValue({ ok: true })
  mocks.remove.mockReset().mockResolvedValue(true)
  mocks.copy.mockReset().mockResolvedValue(true)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})
async function render() {
  await act(async () => root.render(createElement(MemoryRouter, null, createElement(Conversations))))
}
function section(view: string) {
  return container.querySelector<HTMLElement>(`section[aria-labelledby="settings-conversations-${view}"]`)!
}
async function click(label: string, view = 'archived') {
  const button = section(view).querySelector<HTMLButtonElement>(`[aria-label="${label}"]`)!
  await act(async () => button.click())
}

describe('conversation and link settings', () => {
  it('restores archived conversations only after the server confirms success', async () => {
    await render()
    mocks.archived.mockResolvedValue({ conversations: [], has_more: false })
    await click('settings:conversations.restore')
    expect(mocks.update).toHaveBeenCalledWith('archived-1', { archived: false })
    expect(mocks.load).toHaveBeenCalled()
    expect(container.textContent).not.toContain('Archived conversation')
    expect(mocks.success).toHaveBeenCalledWith('settings:conversations.restored')
  })

  it('keeps a row when restoring or deleting fails', async () => {
    await render()
    mocks.update.mockRejectedValue(new Error('offline'))
    await click('settings:conversations.restore')
    expect(container.textContent).toContain('Archived conversation')
    expect(mocks.success).not.toHaveBeenCalled()
    await click('common:actions.delete')
    expect(mocks.remove).not.toHaveBeenCalled()
    mocks.remove.mockResolvedValue(false)
    await act(async () => container.querySelector<HTMLButtonElement>('[role="alert"] button:last-child')!.click())
    expect(container.textContent).toContain('Archived conversation')
    expect(mocks.success).not.toHaveBeenCalled()
  })

  it('copies both kinds of public links and requires confirmation before revocation', async () => {
    await render()
    await click('settings:conversations.copyLink', 'shared')
    expect(mocks.copy).toHaveBeenLastCalledWith(new URL('/share/sh_own', window.location.origin).href)
    await click('settings:conversations.revoke', 'shared')
    expect(mocks.revokeShared).not.toHaveBeenCalled()
    mocks.shared.mockResolvedValue({ items: [], has_more: false })
    await act(async () => container.querySelector<HTMLButtonElement>('[role="alert"] button:last-child')!.click())
    expect(mocks.revokeShared).toHaveBeenCalledWith('sh_own')
    expect(container.textContent).not.toContain('Shared conversation')
    await click('settings:conversations.copyLink', 'html')
    expect(mocks.copy).toHaveBeenLastCalledWith(new URL('/api/public/html-previews/hp_own', window.location.origin).href)
  })

  it('loads each section independently when one list is delayed', async () => {
    let resolve!: (value: unknown) => void
    mocks.archived.mockImplementation(() => new Promise((done) => { resolve = done }))
    await render()
    expect(section('archived').querySelector('[role="status"]')).not.toBeNull()
    expect(section('shared').textContent).toContain('Shared conversation')
    expect(section('html').textContent).toContain('hp_own')
    await act(async () => resolve({ conversations: [{ id: 'old', title: 'Late private conversation', updated_at: 1775001600 }], has_more: false }))
    expect(section('archived').textContent).toContain('Late private conversation')
    expect(section('html').textContent).toContain('hp_own')
    expect(section('html').textContent).not.toContain('Late private conversation')
  })

  it('refreshes shared links after deleting a conversation that may have been shared', async () => {
    await render()
    await click('common:actions.delete')
    mocks.archived.mockResolvedValue({ conversations: [], has_more: false })
    mocks.shared.mockResolvedValue({ items: [], has_more: false })
    await act(async () => section('archived').querySelector<HTMLButtonElement>('[role="alert"] button:last-child')!.click())
    expect(mocks.remove).toHaveBeenCalledWith('archived-1')
    expect(section('archived').textContent).not.toContain('Archived conversation')
    expect(section('shared').textContent).not.toContain('Shared conversation')
    expect(section('html').textContent).toContain('hp_own')
  })

  it('removes the previous account list immediately when the account changes', async () => {
    await render()
    expect(container.textContent).toContain('Shared conversation')
    mocks.auth.user.id = 'new-owner'
    mocks.archived.mockImplementation(() => new Promise(() => {}))
    mocks.shared.mockImplementation(() => new Promise(() => {}))
    mocks.html.mockImplementation(() => new Promise(() => {}))
    await render()
    expect(container.textContent).not.toContain('Shared conversation')
  })
})
