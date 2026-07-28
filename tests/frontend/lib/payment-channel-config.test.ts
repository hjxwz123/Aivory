import { describe, expect, it } from 'vitest'

import { updateEPayCurrencyConfig } from '@/lib/payment-channel-config'

describe('updateEPayCurrencyConfig', () => {
  it('clears an old rate when the provider currency changes', () => {
    expect(updateEPayCurrencyConfig({
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
    expect(updateEPayCurrencyConfig({
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
    expect(updateEPayCurrencyConfig({
      currency: 'CNY',
      conversion_rate: '7',
      conversion_rate_base_currency: 'USD',
    }, 'USD', 'USD')).toEqual({ currency: 'USD' })
  })
})
