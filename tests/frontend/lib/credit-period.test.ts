import { describe, expect, it } from 'vitest'
import { CREDIT_PERIOD_SECONDS, splitCreditPeriod } from '@/lib/credit-period'

describe('credit refresh periods', () => {
  it('defines a month as exactly 30 days', () => {
    expect(CREDIT_PERIOD_SECONDS.month).toBe(30 * 24 * 60 * 60)
  })

  it('displays whole 30-day periods as months', () => {
    expect(splitCreditPeriod(60 * 24 * 60 * 60)).toEqual({ value: 2, unit: 'month' })
  })

  it('preserves a four-week period as weeks', () => {
    expect(splitCreditPeriod(4 * 7 * 24 * 60 * 60)).toEqual({ value: 4, unit: 'week' })
  })
})
