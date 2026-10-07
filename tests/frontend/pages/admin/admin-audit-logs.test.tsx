// @vitest-environment jsdom
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminAuditLogs from '@/pages/admin/AdminAuditLogs'
import type { ApiAdminAuditLog } from '@/api/types'
import { TooltipProvider } from '@/components/ui/tooltip'

const { auditLogs, translate } = vi.hoisted(() => ({
  auditLogs: vi.fn(),
  translate: (key: string, options?: { count?: number }) => options?.count === undefined ? key : `${options.count} records`,
}))
vi.mock('@/api', () => ({ adminApi: { auditLogs }, ApiError: class extends Error {} }))
vi.mock('@/hooks/use-toast', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: translate,
    i18n: { language: 'en' },
  }),
}))

const entry: ApiAdminAuditLog = {
  id: 'aud-test-42',
  workspace_id: 'ws-test-7',
  workspace_name: 'Operations workspace',
  actor_user_id: 'user-test-9',
  actor_name: 'Ada Operator',
  action: 'member.role_updated',
  target_type: 'member',
  target_id: 'member-test-3',
  metadata: { previous_role: 'member', role: 'admin' },
  created_at: 1_780_000_000,
  result: 'success',
  request_id: 'request-42',
  changes: { role: { before: 'member', after: 'admin' }, api_key: { redacted: true } },
}

let root: Root
let container: HTMLDivElement

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  auditLogs.mockReset()
  auditLogs.mockResolvedValue({ logs: [entry], total: 1, page: 1, page_size: 50 })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllGlobals()
})

describe('admin audit logs', () => {
  it('keeps metadata out of the list and opens it in the detail drawer', async () => {
    await act(async () => {
      root.render(createElement(TooltipProvider, null, createElement(MemoryRouter, null, createElement(AdminAuditLogs))))
    })

    const row = container.querySelector<HTMLTableRowElement>('tbody tr')
    expect(row?.textContent).toContain(entry.id)
    expect(row?.textContent).toContain(entry.action)
    expect(row?.textContent).not.toContain('previous_role')

    await act(async () => {
      row?.click()
    })

    expect(document.body.textContent).toContain('logs.detailTitle')
    expect(document.body.textContent).toContain('previous_role')
    expect(document.body.textContent).toContain('member-test-3')
    expect(document.body.textContent).toContain('request-42')
    expect(document.body.textContent).toContain('logs.redacted')
    expect(document.body.textContent).toContain('logs.before')
  })

  it('ignores an older response after the search filters change', async () => {
    let resolveOld!: (value: unknown) => void
    auditLogs.mockReturnValueOnce(new Promise((resolve) => { resolveOld = resolve }))
    auditLogs.mockResolvedValueOnce({ logs: [{ ...entry, id: 'filtered-record' }], total: 1 })
    await act(async () => { root.render(createElement(TooltipProvider, null, createElement(AdminAuditLogs))) })
    const input = container.querySelector('input')!
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, 'filtered-record')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 350)) })
    expect(auditLogs).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'filtered-record', page: 1 }))
    await act(async () => { resolveOld({ logs: [entry], total: 1 }) })
    expect(container.textContent).toContain('filtered-record')
    expect(container.textContent).not.toContain(entry.id)
  })
})
