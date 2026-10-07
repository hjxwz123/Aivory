// @vitest-environment jsdom
import { act, createElement, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminOverview from '@/pages/admin/AdminOverview'
import type { ApiAdminOverview, ApiUsageTotals } from '@/api/types'

const { overview } = vi.hoisted(() => ({ overview: vi.fn() }))
vi.mock('@/api', () => ({ adminApi: { overview }, ApiError: class extends Error {} }))
vi.mock('@/hooks/use-toast', () => ({ toast: { error: vi.fn() } }))
vi.mock('@/components/ui/tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))
vi.mock('@/store/language', () => ({ useLanguage: (select: (state: { lang: string }) => unknown) => select({ lang: 'en' }) }))
vi.mock('react-i18next', () => {
  const t = (key: string, options?: { count?: number }) => key === 'admin:overview.trends.days' ? `${options?.count} days` : key
  return { useTranslation: () => ({ t }) }
})

function fixture(days = 30, ready = true): ApiAdminOverview {
  const totals: ApiUsageTotals = {
    calls: 3, turns: 2, credit_charged_turns: 2, input_tokens: 300, output_tokens: 50,
    cache_read_tokens: 0, cache_write_tokens: 0, images_count: 0, cost: 0.25,
    credits: 1.25, turn_cost: 0.25, credit_charged_cost: 0.25, users: 1,
    credit_charged_users: 1, conversations: 1, workspaces: 0,
  }
  return {
    channel_count: 1, enabled_channel_count: 1, model_count: 1, group_count: 1,
    payment_channel_count: 0, payment_method_count: 0, user_count: 2,
    health: { channel_ready: ready, default_model_ready: ready, task_model_ready: true,
      task_model_inherited: true, email_verification: false, smtp_ready: false,
      email_ready: true, storage_provider: 'local', storage_ready: true,
      payments_ready: true, all_ready: ready },
    today: ready ? totals : null,
    trends: ready ? {
      days, period_start: 1775001600, period_end: 1775001600 + days * 86400,
      totals, registrations: 2,
      points: Array.from({ length: days }, (_, index) => ({
        bucket_start: 1775001600 + index * 86400,
        input_tokens: index === 0 ? 300 : 0, output_tokens: index === 0 ? 50 : 0,
        cache_read_tokens: 0, cache_write_tokens: 0, calls: index === 0 ? 3 : 0,
        turns: index === 0 ? 2 : 0, users: index === 0 ? 1 : 0,
        cost: index === 0 ? 0.25 : 0, credits: index === 0 ? 1.25 : 0,
        registrations: index === 0 ? 2 : 0, images_count: 0,
      })),
    } : null,
  }
}

let root: Root
let container: HTMLDivElement
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  overview.mockReset()
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
  await act(async () => root.render(createElement(MemoryRouter, null, createElement(AdminOverview))))
}
async function clickDays(days: number) {
  const target = [...container.querySelectorAll<HTMLButtonElement>('button')].find((button) => button.textContent === `${days} days`)!
  await act(async () => target.click())
}

describe('admin overview trends', () => {
  it('keeps configuration checks until the deployment is ready', async () => {
    overview.mockResolvedValue(fixture(30, false))
    await mount()
    expect(container.textContent).toContain('admin:overview.healthTitle')
    expect(container.querySelector('figure')).toBeNull()
  })

  it('renders all four charts and supports keyboard inspection and metric switching', async () => {
    overview.mockResolvedValue(fixture())
    await mount()
    const charts = container.querySelectorAll('figure')
    expect(charts).toHaveLength(4)
    const plot = charts[0].querySelector('[role="slider"]')!
    act(() => plot.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true })))
    expect(plot.getAttribute('aria-valuenow')).toBe('0')
    expect(plot.getAttribute('aria-valuetext')).toContain('300')
    act(() => charts[1].querySelectorAll<HTMLButtonElement>('button')[1].click())
    expect(container.querySelectorAll('figure')[1].textContent).toContain('1.25')
    act(() => container.querySelectorAll('figure')[3].querySelectorAll<HTMLButtonElement>('button')[1].click())
    expect(container.querySelectorAll('figure')[3].querySelector('[role="slider"]')?.getAttribute('aria-valuetext')).toContain('analytics.metric.turns')
  })

  it('ignores an older response after switching the range again', async () => {
    overview.mockResolvedValueOnce(fixture())
    await mount()
    let resolve7!: (value: ApiAdminOverview) => void
    let resolve90!: (value: ApiAdminOverview) => void
    overview.mockImplementationOnce(() => new Promise((resolve) => { resolve7 = resolve }))
    overview.mockImplementationOnce(() => new Promise((resolve) => { resolve90 = resolve }))
    await clickDays(7)
    await clickDays(90)
    await act(async () => resolve90(fixture(90)))
    await act(async () => resolve7(fixture(7)))
    expect(container.querySelector('[role="slider"]')?.getAttribute('aria-valuemax')).toBe('89')
    expect(overview.mock.calls.map(([days]) => days)).toEqual([30, 7, 90])
  })

  it('shows a retry state when trend statistics are unavailable', async () => {
    overview.mockResolvedValue({ ...fixture(), trends: null })
    await mount()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('admin:overview.trends.loadFailed')
    overview.mockResolvedValue(fixture())
    await act(async () => container.querySelector<HTMLButtonElement>('[role="alert"] button')!.click())
    expect(container.querySelectorAll('figure')).toHaveLength(4)
  })
})
