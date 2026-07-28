import { describe, expect, it } from 'vitest'

import { updatePaymentProviderCurrencyConfig } from '@/lib/payment-channel-config'

describe('updatePaymentProviderCurrencyConfig', () => {
  it('clears an old rate when the provider currency changes', () => {
    expect(updatePaymentProviderCurrencyConfig({
      currency: 'CNY',
      conversion_rate: '7',
      conversion_rate_base_currency: 'USD',
    }, 'jpy', 'USD')).toEqual({
      currency: 'JPY',
      conversion_rate: '',
      conversion_rate_base_currency: 'USD',
    })
  })

  it('preserves the rate when only currency casing changes', () => {
    expect(updatePaymentProviderCurrencyConfig({
      currency: 'cny',
      conversion_rate: '7',
      conversion_rate_base_currency: 'USD',
    }, 'CNY', 'USD')).toEqual({
      currency: 'CNY',
      conversion_rate: '7',
      conversion_rate_base_currency: 'USD',
    })
  })

  it('removes conversion fields for a same-currency channel', () => {
    expect(updatePaymentProviderCurrencyConfig({
      currency: 'CNY',
      conversion_rate: '7',
      conversion_rate_base_currency: 'USD',
    }, 'USD', 'USD')).toEqual({ currency: 'USD' })
  })

  it('preserves Waffo product fields while resetting a stale conversion rate', () => {
    expect(updatePaymentProviderCurrencyConfig({
      product_id: 'PROD_0123456789abcdefghijkl',
      currency: 'USD',
      conversion_rate: '1',
      conversion_rate_base_currency: 'CNY',
    }, 'eur', 'CNY')).toEqual({
      product_id: 'PROD_0123456789abcdefghijkl',
      currency: 'EUR',
      conversion_rate: '',
      conversion_rate_base_currency: 'CNY',
    })
  })
})
