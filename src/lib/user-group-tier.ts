import type { ApiUserGroup } from '@/api/types'

export type BillingCycle = 'monthly' | 'yearly'

export function groupPriceAmount(group: ApiUserGroup, cycle: BillingCycle): number {
  return cycle === 'monthly' ? group.monthly_price_amount_minor : group.yearly_price_amount_minor
}
