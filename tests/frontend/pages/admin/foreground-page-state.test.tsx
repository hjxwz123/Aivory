// @vitest-environment jsdom
import { act, createElement, type ReactElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { Link, MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/i18n'
import '@/i18n/admin-resources'
import { AuthGate } from '@/components/auth/auth-gate'
import { TooltipProvider } from '@/components/ui/tooltip'
import AdminUserConversation from '@/pages/admin/AdminUserConversation'
import AdminUserConversations from '@/pages/admin/AdminUserConversations'
import AdminUserLibrary from '@/pages/admin/AdminUserLibrary'
import AdminModelEdit from '@/pages/admin/AdminModelEdit'
import UserFiles from '@/pages/files/UserFiles'
import { useAuth } from '@/store/auth'
import type { ApiMessage, ApiUser } from '@/api/types'

const mocks = vi.hoisted(() => ({
  siteStatus: vi.fn(),
  me: vi.fn(), conversation: vi.fn(), conversationMessages: vi.fn(), users: vi.fn(),
  user: vi.fn(), userConversations: vi.fn(), models: vi.fn(), userProjects: vi.fn(),
  userKbs: vi.fn(), userImages: vi.fn(), kbDocuments: vi.fn(), channels: vi.fn(),
  modelTags: vi.fn(), skills: vi.fn(), builtinTools: vi.fn(), mcpServers: vi.fn(),
  settings: vi.fn(), modelChannels: vi.fn(), channelCapabilities: vi.fn(), userGroups: vi.fn(), modelQuotas: vi.fn(),
  myFiles: vi.fn(), myStorage: vi.fn(), error: vi.fn(),
  load: vi.fn().mockResolvedValue(undefined), getById: vi.fn(), sync: vi.fn(),
}))
vi.mock('@/api', () => ({
  adminApi: mocks, authApi: mocks, ApiError: class extends Error {},
  apiUrl: (path: string) => path, resetAuthFailureState: vi.fn(), setAccessToken: vi.fn(),
}))
vi.mock('@/api/client', () => ({
  api: mocks.siteStatus,
  invalidateAccountRequests: vi.fn(), isAuthRefreshSuppressed: () => false,
  setAuthLostHandler: vi.fn(), setBannedHandler: vi.fn(),
  setInitialPasswordRequiredHandler: vi.fn(), setRefreshHandler: vi.fn(),
}))
vi.mock('@/store/models', () => ({
  useModels: (select: (state: unknown) => unknown) => select({ loaded: true, load: mocks.load, getById: mocks.getById }),
}))
vi.mock('@/store/conversations', () => ({
  useConversations: (select: (state: unknown) => unknown) => select({ load: mocks.load }),
  toLocalMessage: (message: ApiMessage) => ({ ...message, parentId: message.parent_id, createdAt: message.created_at * 1000 }),
}))
vi.mock('@/store/projects', () => ({ useProjects: (select: (state: unknown) => unknown) => select({ load: mocks.load }) }))
vi.mock('@/store/workspaces', () => ({ useWorkspaces: { getState: () => ({ load: mocks.load }) } }))
vi.mock('@/store/settings', () => ({
  useSettings: Object.assign(
    (select: (state: unknown) => unknown) => select({ syncUserSettings: mocks.sync }),
    { getState: () => ({ codeBlockWrapPending: false, cancelCodeBlockWrapSave: vi.fn() }) },
  ),
}))
vi.mock('@/store/composer-prefs', () => ({ useComposerPrefs: { getState: () => ({ setDefaultToolMode: mocks.sync, resetForNewConversation: mocks.sync }) } }))
vi.mock('@/store/accent', () => ({ useAccent: { getState: () => ({ applyProfile: mocks.sync }) } }))
vi.mock('@/store/theme', () => ({ useTheme: Object.assign(
  (select: (state: unknown) => unknown) => select({ resolved: 'light' }),
  { getState: () => ({ applyPref: mocks.sync }) },
) }))
vi.mock('@/hooks/use-toast', () => ({ toast: { error: mocks.error, success: vi.fn() } }))
vi.mock('@/hooks/use-media-query', () => ({ useMediaQuery: () => true }))
vi.mock('@/components/files/document-preview', () => ({ DocumentPreview: () => null }))
vi.mock('@/components/layout/content-header', () => ({ ContentHeader: ({ title }: { title: string }) => <h1>{title}</h1> }))
vi.mock('@/components/chat/markdown', () => ({ Markdown: ({ content }: { content: string }) => createElement('p', null, content) }))

const administrator = {
  id: 'admin', name: 'Administrator', email: 'admin@example.test', role: 'admin',
  status: 'active', created_at: 1, has_password: true, settings: { language: 'zh' },
} as ApiUser
const target = { ...administrator, id: 'target', name: 'Target user', settings: {} }
const conversation = { id: 'conv', user_id: 'target', title: 'Conversation title', active_leaf_id: 'branch-a', model_label: 'Test model' }
const messages = [
  { id: 'root', role: 'user', content: 'Question', created_at: 1 },
  { id: 'branch-a', parent_id: 'root', role: 'assistant', content: 'Answer A', created_at: 2 },
  { id: 'branch-b', parent_id: 'root', role: 'assistant', content: 'Answer B', created_at: 3,
    reasoning: [{ id: 'thought', kind: 'thinking', text: 'Reasoning B' }] },
]
let root: Root
let container: HTMLDivElement
let visibility: DocumentVisibilityState
let now: number

beforeEach(async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.clearAllMocks()
  mocks.siteStatus.mockResolvedValue({ closed: false, message: '' })
  mocks.load.mockResolvedValue(undefined)
  now = 1_000_000
  visibility = 'visible'
  vi.spyOn(Date, 'now').mockImplementation(() => now)
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
  await i18n.changeLanguage('zh')
  useAuth.setState({ user: { ...administrator }, status: 'authenticated', setupProbed: true,
    authPolicyLoaded: true, hydrate: vi.fn().mockResolvedValue(undefined) })
  mocks.me.mockImplementation(async () => ({ ...administrator, settings: { ...administrator.settings } }))
  mocks.conversation.mockResolvedValue(conversation)
  mocks.conversationMessages.mockResolvedValue(messages)
  mocks.users.mockResolvedValue({ users: [target] })
  mocks.user.mockResolvedValue(target)
  mocks.userConversations.mockResolvedValue([conversation])
  mocks.models.mockResolvedValue([{ id: 'model-a', label: 'Original model', request_id: 'test-model', kind: 'chat', enabled: true }])
  for (const api of [mocks.userProjects, mocks.userKbs, mocks.userImages, mocks.kbDocuments,
    mocks.channels, mocks.modelTags, mocks.skills, mocks.builtinTools, mocks.mcpServers, mocks.channelCapabilities]) {
    api.mockResolvedValue([])
  }
  mocks.settings.mockResolvedValue({})
  mocks.modelChannels.mockResolvedValue({ regular: [], fallback: [] })
  mocks.userGroups.mockResolvedValue([{ id: 'group', name: 'Test group' }])
  mocks.modelQuotas.mockResolvedValue([{ group_id: 'group', period_seconds: 86400, limit_type: 'count', limit_value: 10 }])
  mocks.myFiles.mockResolvedValue({ files: [], total: 0 })
  mocks.myStorage.mockResolvedValue({ used_bytes: 0, quota_bytes: 1024 })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

async function mount(page: ReactElement, pattern: string, path: string, nextPath = '') {
  await act(async () => {
    root.render(<MemoryRouter initialEntries={[path]}><TooltipProvider>
      <Link data-next to={nextPath}>Next</Link>
      <AuthGate><Routes><Route path={pattern} element={page} /></Routes></AuthGate>
    </TooltipProvider></MemoryRouter>)
  })
}
async function foreground(language = 'zh') {
  mocks.me.mockResolvedValue({ ...administrator, settings: { language } })
  await act(async () => {
    visibility = 'hidden'
    document.dispatchEvent(new Event('visibilitychange'))
    now += 31_001
    visibility = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('focus'))
  })
}
async function click(selector: string) {
  const button = container.querySelector<HTMLElement>(selector)
  expect(button, selector).not.toBeNull()
  await act(async () => button!.click())
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

describe('administrator conversation inspection during app switching', () => {
  it('keeps the selected branch and expanded reasoning across repeated foreground sync and a new cloud language', async () => {
    await mount(<AdminUserConversation />, '/admin/users/:id/conversations/:cid', '/admin/users/target/conversations/conv')
    expect(mocks.conversation).toHaveBeenCalledTimes(1)
    expect(mocks.conversationMessages).toHaveBeenCalledWith('conv', 'tree')
    await click(`[data-message-id="branch-a"] button[aria-label="${i18n.t('chat:actions.nextBranch')}"]`)
    await click('[data-message-id="branch-b"] button[aria-expanded]')
    const node = container.querySelector('[data-message-id="branch-b"]')
    for (const language of ['zh', 'zh', 'en']) await foreground(language)
    expect(mocks.me).toHaveBeenCalledTimes(3)
    expect(i18n.language).toBe('en')
    expect(container.querySelector('[data-message-id="branch-b"]')).toBe(node)
    expect(node?.querySelector('button[aria-expanded]')?.getAttribute('aria-expanded')).toBe('true')
    expect(mocks.conversation).toHaveBeenCalledTimes(1)
    expect(mocks.conversationMessages).toHaveBeenCalledTimes(1)
    expect(mocks.users).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain(i18n.t('admin:users.backToConversations'))
    for (const action of ['edit', 'regenerate', 'delete', 'more']) {
      expect(container.querySelector(`button[aria-label="${i18n.t(`chat:actions.${action}`)}"]`)).toBeNull()
    }
  })

  it('loads a newly selected conversation and ignores the previous route response', async () => {
    const old = deferred<typeof conversation>()
    mocks.conversation.mockReturnValueOnce(old.promise).mockResolvedValue({ ...conversation, id: 'next', title: 'New conversation', active_leaf_id: 'branch-b' })
    await mount(<AdminUserConversation />, '/admin/users/:id/conversations/:cid', '/admin/users/target/conversations/conv', '/admin/users/target/conversations/next')
    await click('[data-next]')
    expect(mocks.conversation).toHaveBeenLastCalledWith('next')
    expect(container.textContent).toContain('New conversation')
    await act(async () => old.resolve(conversation))
    expect(container.textContent).toContain('New conversation')
    expect(container.querySelector('[data-message-id="branch-b"]')).not.toBeNull()
  })

  it('keeps list rows mounted when returning with a different language', async () => {
    await mount(<AdminUserConversations />, '/admin/users/:id/conversations', '/admin/users/target/conversations')
    const row = container.querySelector('tbody tr')
    await foreground('en')
    expect(container.querySelector('tbody tr')).toBe(row)
    expect(mocks.user).toHaveBeenCalledTimes(1)
    expect(mocks.userConversations).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain(i18n.t('admin:users.conversationsTitle', { name: target.name }))
  })

  it('loads a new user and ignores a late list response for the previous user', async () => {
    const old = deferred<typeof conversation[]>()
    mocks.userConversations.mockReturnValueOnce(old.promise).mockResolvedValue([{ ...conversation, id: 'other-conv', title: 'Other user conversation' }])
    mocks.user.mockImplementation(async (id: string) => ({ ...target, id }))
    await mount(<AdminUserConversations />, '/admin/users/:id/conversations', '/admin/users/target/conversations', '/admin/users/other/conversations')
    await click('[data-next]')
    await act(async () => old.resolve([conversation]))
    expect(mocks.userConversations).toHaveBeenLastCalledWith('other')
    expect(container.textContent).toContain('Other user conversation')
    expect(container.textContent).not.toContain('Conversation title')
  })

  it('retains embedded-list retry behavior without loading an unused user header', async () => {
    mocks.userConversations.mockRejectedValueOnce(new Error('temporarily offline'))
    await mount(<AdminUserConversations userId="target" embedded />, '/admin/users/:id/conversations', '/admin/users/target/conversations')
    expect(container.querySelector('[role="alert"]')).not.toBeNull()
    await click('[role="alert"] button')
    expect(container.textContent).toContain('Conversation title')
    expect(mocks.userConversations).toHaveBeenCalledTimes(2)
    expect(mocks.user).not.toHaveBeenCalled()
  })
})

describe('other page drafts and loaded data during foreground synchronization', () => {
  it('retains additional gallery images and an expanded knowledge base after a language update', async () => {
    mocks.userKbs.mockResolvedValue([{ id: 'kb', name: 'Expanded knowledge base' }])
    mocks.kbDocuments.mockResolvedValue([{ id: 'doc', filename: 'Knowledge document', status: 'ready' }])
    const images = Array.from({ length: 60 }, (_, index) => ({ id: `image-${index}`, url: `/images/${index}.png`, filename: `Image ${index}` }))
    mocks.userImages.mockResolvedValueOnce(images).mockResolvedValueOnce([{ id: 'more', url: '/images/more.png', filename: 'Additional image' }])
    await mount(<AdminUserLibrary />, '/admin/users/:id/library', '/admin/users/target/library')
    const kbButton = [...container.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent === 'Expanded knowledge base')!
    await act(async () => kbButton.click())
    const more = [...container.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent === i18n.t('admin:users.loadMore'))!
    await act(async () => more.click())
    expect(container.querySelectorAll('img')).toHaveLength(61)
    await foreground('en')
    expect(container.querySelectorAll('img')).toHaveLength(61)
    expect(kbButton.getAttribute('aria-expanded')).toBe('true')
    expect(container.textContent).toContain('Knowledge document')
    expect(mocks.userImages).toHaveBeenCalledTimes(2)
    expect(mocks.userImages).toHaveBeenLastCalledWith('target', 60, 60)
    expect(mocks.kbDocuments).toHaveBeenCalledTimes(1)
    expect(mocks.userProjects).toHaveBeenCalledTimes(1)
  })

  it('preserves an unsaved model label and still loads a different model on navigation', async () => {
    await mount(<AdminModelEdit />, '/admin/models/:id', '/admin/models/model-a', '/admin/models/model-b')
    const input = container.querySelector<HTMLInputElement>('#m-label')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'Unsaved model label')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const quota = container.querySelector<HTMLInputElement>('td[data-column="limit"] input')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(quota, '25')
      quota.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await foreground('en')
    expect(container.querySelector('#m-label')).toBe(input)
    expect(input.value).toBe('Unsaved model label')
    expect(quota.value).toBe('25')
    expect(mocks.modelQuotas).toHaveBeenCalledTimes(1)
    expect(mocks.models).toHaveBeenCalledTimes(1)
    expect(mocks.channels).toHaveBeenCalledTimes(1)
    mocks.models.mockResolvedValue([{ id: 'model-b', label: 'Different model', request_id: 'other-model', kind: 'chat' }])
    await click('[data-next]')
    expect(container.querySelector<HTMLInputElement>('#m-label')?.value).toBe('Different model')
    expect(mocks.modelChannels).toHaveBeenLastCalledWith('model-b')
  })

  it('keeps the file list loaded when the language changes and reloads for a search change', async () => {
    await mount(<UserFiles />, '/files', '/files')
    await foreground('en')
    expect(mocks.myFiles).toHaveBeenCalledTimes(1)
    expect(mocks.myStorage).toHaveBeenCalledTimes(1)
    const input = container.querySelector<HTMLInputElement>('input')!
    expect(input).not.toBeNull()
    vi.useFakeTimers()
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'report')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => vi.advanceTimersByTime(350))
    vi.useRealTimers()
    expect(mocks.myFiles).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'report' }))
    expect(mocks.myFiles).toHaveBeenCalledTimes(2)
  })

  it('preserves loaded files after a failed search and lets the user retry', async () => {
    const file = { id: 'file', source: 'file', filename: 'report.zip', mime_type: 'application/zip', size_bytes: 512, created_at: 1 }
    mocks.myFiles.mockResolvedValue({ files: [file], total: 1 })
    await mount(<UserFiles />, '/files', '/files')
    const list = container.querySelector('ul')
    expect(list?.textContent).toContain(file.filename)

    mocks.myFiles.mockRejectedValueOnce(new Error('temporarily offline'))
    vi.useFakeTimers()
    const input = container.querySelector<HTMLInputElement>('input')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'report')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => vi.advanceTimersByTime(350))
    vi.useRealTimers()

    expect(container.querySelector('ul')).toBe(list)
    expect(list?.textContent).toContain(file.filename)
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(i18n.t('files:preview.failed'))
    expect(container.textContent).not.toContain(i18n.t('files:empty.title'))

    const retry = deferred<{ files: typeof file[]; total: number }>()
    mocks.myFiles.mockReturnValueOnce(retry.promise)
    await click('[role="alert"] button')
    expect(container.querySelector('ul')).toBe(list)
    expect(list?.getAttribute('aria-busy')).toBe('true')
    await act(async () => retry.resolve({ files: [file], total: 1 }))
    expect(container.querySelector('[role="alert"]')).toBeNull()
    expect(container.querySelector('ul')).toBe(list)
    expect(mocks.myFiles).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'report' }))
  })
})
