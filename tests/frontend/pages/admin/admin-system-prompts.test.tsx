// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminSystemPrompts from '@/pages/admin/AdminSystemPrompts'
import type { ApiSystemPromptConfig } from '@/api/types'

const { getPrompts, updatePrompts, errorToast, t } = vi.hoisted(() => ({
  getPrompts: vi.fn(), updatePrompts: vi.fn(), errorToast: vi.fn(), t: (key: string) => key,
}))
vi.mock('@/api', () => ({
  adminApi: { systemPrompts: getPrompts, updateSystemPrompts: updatePrompts },
  ApiError: class extends Error {},
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t, i18n: { language: 'zh' } }) }))
vi.mock('@/hooks/use-toast', () => ({ toast: { success: vi.fn(), error: errorToast } }))

let container: HTMLDivElement
let root: Root
let config: ApiSystemPromptConfig

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
  vi.clearAllMocks()
  config = {
    max_prompt_bytes: 65536,
    overrides: { 'decision.tool_scope.instructions': 'Saved scope prompt' },
    prompts: [
      { id: 'decision.tool_scope.instructions', name: '决策路由', group: 'routing', engine: 'decision', default: 'Default decision instructions', format: 'Choice: search_only | full_tools', notes: 'Live tools are appended.' },
      { id: 'task.title', name: '标题生成', group: 'conversation', engine: 'chat', default: 'Return title only', format: 'Plain title; no JSON', notes: 'Locale appended.' },
      { id: 'conversation.identity.zh', name: '助手身份', group: 'conversation', engine: 'shared', locale: 'zh', default: '你是 {{model}}', variables: ['{{model}}'], format: 'Text', notes: 'Model name is inserted.' },
    ],
  }
  getPrompts.mockResolvedValue(config)
  updatePrompts.mockImplementation(async (patch: Record<string, string>) => {
    const overrides = { ...config.overrides }
    for (const [key, value] of Object.entries(patch)) {
      if (value.trim()) overrides[key] = value
      else delete overrides[key]
    }
    return { ...config, overrides }
  })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

const button = (label: string) => Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes(label))!
async function mount() { await act(async () => root.render(createElement(AdminSystemPrompts))) }
async function click(label: string) { await act(async () => button(label).click()) }
async function edit(value: string) {
  const field = container.querySelector<HTMLTextAreaElement>('#system-prompt-override')!
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(field, value)
    field.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('system prompt editor', () => {
  it('shows contracts and defaults, saves only changed IDs, and restores the default with an empty value', async () => {
    await mount()
    expect(container.textContent).toContain('Choice: search_only | full_tools')
    expect(container.textContent).toContain('Default decision instructions')
    expect(button('admin:systemPrompts.save').disabled).toBe(true)
    await edit('Custom scope instructions')
    await click('admin:systemPrompts.save')
    expect(updatePrompts).toHaveBeenLastCalledWith({ 'decision.tool_scope.instructions': 'Custom scope instructions' })
    await click('admin:systemPrompts.reset')
    await click('admin:systemPrompts.save')
    expect(updatePrompts).toHaveBeenLastCalledWith({ 'decision.tool_scope.instructions': '' })
    expect(container.querySelector<HTMLTextAreaElement>('textarea')?.value).toBe('')
    expect(container.textContent).toContain('admin:systemPrompts.defaultStatus')
  })

  it('retains drafts when switching prompts and prevents saving a missing placeholder', async () => {
    await mount()
    await edit('Draft decision prompt')
    await click('助手身份')
    await edit('缺少模型变量')
    expect(button('admin:systemPrompts.save').disabled).toBe(true)
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('admin:systemPrompts.missingVariables')
    await click('决策路由')
    expect(container.querySelector<HTMLTextAreaElement>('textarea')?.value).toBe('Draft decision prompt')
    expect(button('admin:systemPrompts.save').disabled).toBe(true)
    await click('助手身份')
    await edit('你是 {{model}}')
    expect(button('admin:systemPrompts.save').disabled).toBe(false)
  })

  it('provides retry after loading fails and never allows saving an empty failed load', async () => {
    getPrompts.mockRejectedValueOnce(new Error('offline'))
    await mount()
    expect(container.querySelector('[role="alert"]')).not.toBeNull()
    expect(button('admin:systemPrompts.save').disabled).toBe(true)
    await click('admin:systemPrompts.retry')
    expect(getPrompts).toHaveBeenCalledTimes(2)
    expect(container.querySelector('textarea')).not.toBeNull()
  })
})
