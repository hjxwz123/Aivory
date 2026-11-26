// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminDocuments from '@/pages/admin/AdminDocuments'

const { settings, updateSettings, models, errorToast, successToast } = vi.hoisted(() => ({
  settings: vi.fn(),
  updateSettings: vi.fn(),
  models: vi.fn(),
  errorToast: vi.fn(),
  successToast: vi.fn(),
}))

vi.mock('@/api', () => ({
  adminApi: { settings, updateSettings, models },
  ApiError: class extends Error {},
}))
vi.mock('@/hooks/use-toast', () => ({
  toast: { error: errorToast, success: successToast },
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, opts?: { defaultValue?: string }) => opts?.defaultValue ?? key }),
}))

let root: Root
let container: HTMLDivElement

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
  vi.clearAllMocks()
  settings.mockResolvedValue({
    document_retrieval_mode: 'rag',
    embedding_model_id: '',
    rag_full_text_threshold: 8000,
    grep_full_text_threshold: 500,
    grep_code_full_text_max_lines: 200,
  })
  updateSettings.mockImplementation(async (patch: Record<string, unknown>) => patch)
  models.mockResolvedValue([])
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
  await act(async () => {
    root.render(createElement(MemoryRouter, null, createElement(AdminDocuments)))
  })
}

describe('AdminDocuments RAG vs Grep mode toggle', () => {
  it('shows RAG settings by default and switches cleanly to Grep settings while keeping MinerU visible', async () => {
    await mount()

    expect(container.querySelector('#embed-model')).not.toBeNull()
    expect(container.querySelector('#rag-threshold')).not.toBeNull()
    expect(container.querySelector('#grep-threshold')).toBeNull()
    expect(container.querySelector('#mineru-url')).not.toBeNull()

    const radios = [...container.querySelectorAll<HTMLButtonElement>('[role="radio"]')]
    expect(radios).toHaveLength(2)
    await act(async () => {
      radios[1].click()
    })

    expect(container.querySelector('#embed-model')).toBeNull()
    expect(container.querySelector('#rag-threshold')).toBeNull()
    expect(container.querySelector('#grep-threshold')).not.toBeNull()
    expect(container.querySelector<HTMLInputElement>('#grep-threshold')?.value).toBe('500')
    expect(container.querySelector<HTMLInputElement>('#grep-code-lines')?.value).toBe('200')
    expect(container.querySelector('#mineru-url')).not.toBeNull()

    const saveBtn = [...container.querySelectorAll('button')].find((btn) => btn.textContent === 'common:actions.save')!
    await act(async () => {
      saveBtn.click()
    })
    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({ document_retrieval_mode: 'grep' }))
    expect(errorToast).not.toHaveBeenCalled()
  })
})
