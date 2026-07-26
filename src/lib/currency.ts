export const DEFAULT_SETTLEMENT_CURRENCY = 'USD'

export function isSettlementCurrencyCode(value: string): boolean {
  return /^[A-Z]{3}$/.test(value)
}

export function normalizeSettlementCurrency(value: unknown): string {
  if (typeof value !== 'string') return DEFAULT_SETTLEMENT_CURRENCY
  const code = value.trim().toUpperCase()
  return isSettlementCurrencyCode(code) ? code : DEFAULT_SETTLEMENT_CURRENCY
}

export function currencyFractionDigits(currency: string, locale = 'en'): number {
  const code = normalizeSettlementCurrency(currency)
  try {
    return (
      new Intl.NumberFormat(locale, { style: 'currency', currency: code }).resolvedOptions()
        .maximumFractionDigits ?? 2
    )
  } catch {
    return 2
  }
}

export function formatCurrencyMinor(amountMinor: number, currency: string, locale?: string): string {
  const code = normalizeSettlementCurrency(currency)
  const digits = currencyFractionDigits(code, locale)
  const amount = Number.isFinite(amountMinor) ? Math.round(amountMinor) / 10 ** digits : 0
  try {
    return new Intl.NumberFormat(locale, {
      style: 'currency',
      currency: code,
      currencyDisplay: 'symbol',
    }).format(amount)
  } catch {
    return `${code} ${amount.toFixed(digits)}`
  }
}

export function minorAmountToInput(amountMinor: number, currency: string, locale = 'en'): string {
  const digits = currencyFractionDigits(currency, locale)
  const safeMinor = Number.isSafeInteger(amountMinor) ? amountMinor : Math.round(Number(amountMinor) || 0)
  const sign = safeMinor < 0 ? '-' : ''
  const absolute = Math.abs(safeMinor)
  if (digits === 0) return `${sign}${absolute}`
  const scale = 10 ** digits
  const whole = Math.floor(absolute / scale)
  const fraction = String(absolute % scale).padStart(digits, '0')
  return `${sign}${whole}.${fraction}`
}

export function inputAmountToMinor(value: string, currency: string, locale = 'en'): number | null {
  const input = value.trim()
  if (!/^\d+(?:\.\d*)?$/.test(input)) return null

  const digits = currencyFractionDigits(currency, locale)
  const [wholeText, fractionText = ''] = input.split('.')
  if (fractionText.length > digits && /[1-9]/.test(fractionText.slice(digits))) return null

  const whole = Number(wholeText)
  const fraction = digits === 0 ? 0 : Number(fractionText.slice(0, digits).padEnd(digits, '0'))
  const minor = whole * 10 ** digits + fraction
  return Number.isSafeInteger(minor) && minor >= 0 ? minor : null
}

export function currencyInputStep(currency: string, locale = 'en'): string {
  const digits = currencyFractionDigits(currency, locale)
  return digits === 0 ? '1' : `0.${'0'.repeat(digits - 1)}1`
}
