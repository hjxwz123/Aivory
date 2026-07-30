export type CreditPeriodUnit = 'hour' | 'day' | 'week' | 'month'

export const CREDIT_PERIOD_SECONDS: Record<CreditPeriodUnit, number> = {
  hour: 60 * 60,
  day: 24 * 60 * 60,
  week: 7 * 24 * 60 * 60,
  month: 30 * 24 * 60 * 60,
}

// Months are fixed 30-day periods, not calendar months.
export function splitCreditPeriod(seconds: number): { value: number; unit: CreditPeriodUnit } {
  if (!seconds || seconds <= 0) return { value: 0, unit: 'day' }
  for (const unit of ['month', 'week', 'day', 'hour'] as const) {
    if (seconds % CREDIT_PERIOD_SECONDS[unit] === 0) {
      return { value: seconds / CREDIT_PERIOD_SECONDS[unit], unit }
    }
  }
  return { value: Math.round(seconds / CREDIT_PERIOD_SECONDS.hour), unit: 'hour' }
}
