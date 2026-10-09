// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminTools from '@/pages/admin/AdminTools'

const { settings, updateSettings, builtinTools, mcpServers, errorToast } = vi.hoisted(() => ({
  settings: vi.fn(), updateSettings: vi.fn(), builtinTools: vi.fn(), mcpServers: vi.fn(), errorToast: vi.fn(),
}))
vi.mock('@/api', () => ({
  adminApi: { settings, updateSettings, builtinTools, mcpServers }, ApiError: class extends Error {},
}))
vi.mock('@/hooks/use-toast', () => ({ toast: { error: errorToast, success: vi.fn() } }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

let root: Root
let container: HTMLDivElement

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.clearAllMocks()
  settings.mockResolvedValue({ search_provider: 'searxng', search_base_url: 'https://search.example.test', search_result_count: 20 })
  updateSettings.mockImplementation(async (patch: Record<string, unknown>) => patch)
  builtinTools.mockResolvedValue([])
  mcpServers.mockResolvedValue([])
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

async function mount() {
  await act(async () => { root.render(createElement(MemoryRouter, null, createElement(AdminTools))) })
}

async function setCount(value: string) {
  const input = container.querySelector<HTMLInputElement>('#search-result-count')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function save() {
  const button = [...container.querySelectorAll('button')].find((item) => item.textContent === 'common:actions.save')!
  await act(async () => { button.click() })
}

describe('SearXNG administrator result count', () => {
  it('loads the stored count and saves edits as an integer', async () => {
    await mount()
    expect(container.querySelector<HTMLInputElement>('#search-result-count')?.value).toBe('20')
    expect(container.querySelector('label[for="search-result-count"]')?.textContent).toBe('admin:settings.fields.searchResultCount')
    await setCount('30')
    await save()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({ search_result_count: 30 }))
    expect(errorToast).not.toHaveBeenCalled()
  })

  it('defaults old settings to five and persists that value on save', async () => {
    settings.mockResolvedValue({ search_provider: 'searxng', search_base_url: 'https://search.example.test' })
    await mount()
    expect(container.querySelector<HTMLInputElement>('#search-result-count')?.value).toBe('5')
    await save()
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({ search_result_count: 5 }))
  })

  it('rejects empty, fractional, and out-of-range values before saving', async () => {
    await mount()
    for (const value of ['', '1.5', '0', '51']) {
      await setCount(value)
      await save()
      expect(updateSettings).not.toHaveBeenCalled()
      expect(errorToast).toHaveBeenLastCalledWith('admin:settings.fields.searchResultCountInvalid')
    }
  })

  it('keeps SearXNG configuration out of other provider forms and save patches', async () => {
    settings.mockResolvedValue({ search_provider: 'brave', search_result_count: 20 })
    await mount()
    expect(container.querySelector('#search-result-count')).toBeNull()
    await save()
    expect(updateSettings.mock.calls[0][0]).not.toHaveProperty('search_result_count')
  })
})
