function configText(config: Record<string, unknown>, key: string): string {
  const value = config[key]
  if (typeof value === 'string') return value
  return typeof value === 'number' && Number.isFinite(value) ? String(value) : ''
}

export function updateEPayCurrencyConfig(
  config: Record<string, unknown>,
  value: string,
  settlementCurrency: string,
): Record<string, unknown> {
  const currentCurrency = configText(config, 'currency').trim().toUpperCase()
  const nextCurrency = value.toUpperCase()
  const next: Record<string, unknown> = { ...config, currency: nextCurrency }
  if (nextCurrency === currentCurrency) return next
  if (nextCurrency === settlementCurrency) {
    delete next.conversion_rate
    delete next.conversion_rate_base_currency
  } else {
    next.conversion_rate = ''
    next.conversion_rate_base_currency = settlementCurrency
  }
  return next
}
