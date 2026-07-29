import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { ApiUserGroup } from '@/api/types'
import { UserGroupTierCard } from '@/components/subscription/user-group-tier-card'
import { groupPriceAmount } from '@/lib/user-group-tier'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

function userGroup(patch: Partial<ApiUserGroup> = {}): ApiUserGroup {
  return {
    id: 'group_test',
    name: 'Test group',
    description: 'Test description',
    features: ['Priority models', 'research'],
    monthly_price_amount_minor: 1200,
    yearly_price_amount_minor: 12000,
    settlement_currency: 'USD',
    is_default: false,
    sort_order: 0,
    max_projects: 0,
    max_kbs: 0,
    credit_allowance: 0,
    credit_period_seconds: 0,
    created_at: 1,
    updated_at: 1,
    is_purchasable: true,
    ...patch,
  }
}

function renderCard(group: ApiUserGroup, billingCycle: 'monthly' | 'yearly'): string {
  return renderToStaticMarkup(
    createElement(
      MemoryRouter,
      null,
      createElement(UserGroupTierCard, {
        group,
        billingCycle,
        publicAction: { href: '/register', label: 'Get started' },
        locale: 'en',
      }),
    ),
  )
}

describe('UserGroupTierCard', () => {
  it('uses the selected billing cycle and hides internal feature markers', () => {
    const group = userGroup()
    const html = renderCard(group, 'yearly')

    expect(groupPriceAmount(group, 'monthly')).toBe(1200)
    expect(groupPriceAmount(group, 'yearly')).toBe(12000)
    expect(html).toContain('$120.00')
    expect(html).toContain('billing.perYear')
    expect(html).toContain('Priority models')
    expect(html).not.toContain('research')
    expect(html).toContain('href="/register"')
    expect(html).toContain('rounded-[10px]')
  })

  it('does not expose a registration link when the selected cycle is unavailable', () => {
    const html = renderCard(userGroup({ monthly_price_amount_minor: 0 }), 'monthly')

    expect(html).toContain('billing.unavailable')
    expect(html).not.toContain('href="/register"')
    expect(html).not.toContain('priceFree')
  })

  it('keeps the default group free and available', () => {
    const html = renderCard(
      userGroup({ is_default: true, monthly_price_amount_minor: 0, yearly_price_amount_minor: 0 }),
      'yearly',
    )

    expect(html).toContain('priceFree')
    expect(html).toContain('href="/register"')
  })

  it('shows a disabled purchase-unavailable action while keeping the paid group visible', () => {
    const html = renderCard(userGroup({ is_purchasable: false }), 'monthly')

    expect(html).toContain('purchaseUnavailable')
    expect(html).toContain('disabled=""')
    expect(html).toContain('$12.00')
    expect(html).not.toContain('href="/register"')
  })

  it('does not apply the purchase flag to switching into the default group', () => {
    const html = renderCard(
      userGroup({
        is_default: true,
        is_purchasable: false,
        monthly_price_amount_minor: 0,
        yearly_price_amount_minor: 0,
      }),
      'monthly',
    )

    expect(html).toContain('priceFree')
    expect(html).toContain('href="/register"')
    expect(html).not.toContain('purchaseUnavailable')
  })
})
