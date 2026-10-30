// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import AdminSkills from '@/pages/admin/AdminSkills'
import type { ApiSkillImportPreview } from '@/api/types'

const mocks = vi.hoisted(() => ({
  skills: vi.fn(), importRemote: vi.fn(), importArchive: vi.fn(), createSkill: vi.fn(),
  error: vi.fn(), success: vi.fn(), warning: vi.fn(),
}))
vi.mock('@/api', () => ({
  adminApi: {
    skills: mocks.skills, importSkillRemote: mocks.importRemote,
    importSkillArchive: mocks.importArchive, createSkill: mocks.createSkill,
  },
  ApiError: class extends Error {},
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key, i18n: { language: 'zh' } }),
}))
vi.mock('@/hooks/use-toast', () => ({ toast: { error: mocks.error, success: mocks.success, warning: mocks.warning } }))
vi.mock('@/components/admin/icon-picker', () => ({ IconPicker: () => null }))

let root: Root
let container: HTMLDivElement
const preview: ApiSkillImportPreview = { name: 'imported-skill', description: 'Imported description', instructions: 'Imported instructions', assets: [], warnings: [], name_exists: false }

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

beforeEach(() => {
  vi.resetAllMocks()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('prefers-reduced-motion'), addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn() }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  mocks.skills.mockResolvedValue([])
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

function button(text: string) {
  const button = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.startsWith(text))
  expect(button, text).toBeTruthy()
  return button!
}
async function click(text: string) {
  const target = button(text)
  await act(async () => target.click())
}
async function edit(id: string, value: string) {
  const input = document.querySelector<HTMLInputElement>(id)!
  expect(input, id).toBeTruthy()
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
async function selectTab(text: string) {
  const tab = Array.from(document.querySelectorAll('[role="tab"]')).find((item) => item.textContent === text)!
  await act(async () => {
    tab.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, ctrlKey: false }))
  })
}
async function importUrl() {
  await selectTab('admin:skills.importTab.url')
  await edit('#admin-skill-import-url', 'https://example.test/SKILL.md')
  await click('admin:skills.importGo')
}
async function startImport() {
  await act(async () => root.render(createElement(AdminSkills)))
  await click('admin:skills.new')
  await importUrl()
}

it('does not report an error for a successful backend response with null warnings', async () => {
  mocks.importRemote.mockResolvedValue({ ...preview, warnings: null })
  await startImport()
  expect(mocks.importRemote).toHaveBeenCalledOnce()
  expect(document.querySelector<HTMLInputElement>('#s-name')?.value).toBe(preview.name)
  expect(mocks.error).not.toHaveBeenCalled()
})

it('does not overwrite a new editor with the response from a cancelled import', async () => {
  const request = deferred<ApiSkillImportPreview>()
  mocks.importRemote.mockReturnValue(request.promise)
  await startImport()
  const signal = mocks.importRemote.mock.calls[0][1] as AbortSignal
  await click('common:actions.cancel')
  expect(signal.aborted).toBe(true)
  await click('admin:skills.new')
  await edit('#s-name', 'another-skill')
  await act(async () => request.resolve(preview))
  expect(document.querySelector<HTMLInputElement>('#s-name')?.value).toBe('another-skill')
  expect(mocks.success).not.toHaveBeenCalled()
  expect(mocks.error).not.toHaveBeenCalled()
})

it('keeps a second import pending when the cancelled request finishes', async () => {
  const first = deferred<ApiSkillImportPreview>()
  const second = deferred<ApiSkillImportPreview>()
  mocks.importRemote.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
  await startImport()
  expect(button('common:actions.save').disabled).toBe(true)
  await click('common:actions.cancel')
  await click('admin:skills.new')
  await importUrl()
  expect(mocks.importRemote).toHaveBeenCalledTimes(2)
  await act(async () => first.resolve(preview))
  expect(document.querySelector<HTMLInputElement>('#s-name')?.value).toBe('')
  expect(button('common:actions.save').disabled).toBe(true)
  await act(async () => second.resolve({ ...preview, name: 'second-skill' }))
  expect(document.querySelector<HTMLInputElement>('#s-name')?.value).toBe('second-skill')
  expect(button('common:actions.save').disabled).toBe(false)
  expect(mocks.success).toHaveBeenCalledOnce()
})

it('does not show an error from an import cancelled by closing the drawer', async () => {
  const request = deferred<ApiSkillImportPreview>()
  mocks.importRemote.mockReturnValue(request.promise)
  await startImport()
  await act(async () => {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  })
  expect((mocks.importRemote.mock.calls[0][1] as AbortSignal).aborted).toBe(true)
  await act(async () => request.reject(new Error('late network error')))
  expect(mocks.error).not.toHaveBeenCalled()
})

it('ignores a cancelled ZIP import after opening a new editor', async () => {
  const request = deferred<ApiSkillImportPreview>()
  mocks.importArchive.mockReturnValue(request.promise)
  await act(async () => root.render(createElement(AdminSkills)))
  await click('admin:skills.new')
  await selectTab('admin:skills.importTab.zip')
  const input = document.querySelector<HTMLInputElement>('input[accept=".zip,application/zip"]')!
  Object.defineProperty(input, 'files', { value: [new File(['zip'], 'skill.zip')] })
  await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
  expect(mocks.importArchive).toHaveBeenCalledOnce()
  await click('common:actions.cancel')
  expect((mocks.importArchive.mock.calls[0][1] as AbortSignal).aborted).toBe(true)
  await click('admin:skills.new')
  await edit('#s-name', 'new-skill')
  await act(async () => request.resolve(preview))
  expect(document.querySelector<HTMLInputElement>('#s-name')?.value).toBe('new-skill')
  expect(mocks.success).not.toHaveBeenCalled()
})

it('shows real import failures and allows retrying', async () => {
  mocks.importRemote.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(preview)
  await startImport()
  expect(mocks.error).toHaveBeenCalledOnce()
  await click('admin:skills.importGo')
  expect(mocks.importRemote).toHaveBeenCalledTimes(2)
  expect(document.querySelector<HTMLInputElement>('#s-name')?.value).toBe(preview.name)
  expect(mocks.success).toHaveBeenCalledOnce()
})
