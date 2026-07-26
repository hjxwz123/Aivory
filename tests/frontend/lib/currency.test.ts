import { describe, expect, it } from 'vitest'

import {
  currencyFractionDigits,
  formatCurrencyMinor,
  inputAmountToMinor,
  minorAmountToInput,
  normalizeSettlementCurrency,
} from '@/lib/currency'

function normalizeSpaces(value: string): string {
  return value.replace(/[\s\u00a0\u202f]+/g, ' ')
}

describe('settlement currency formatting', () => {
  it('formats USD, EUR, and JPY from integer minor units', () => {
    expect(formatCurrencyMinor(999, 'USD', 'en-US')).toBe('$9.99')
    expect(normalizeSpaces(formatCurrencyMinor(899, 'EUR', 'fr-FR'))).toBe('8,99 €')
    expect(formatCurrencyMinor(1200, 'JPY', 'ja-JP')).toBe('￥1,200')
  })

  it('uses the currency-defined three decimal places for KWD', () => {
    expect(currencyFractionDigits('KWD', 'en-US')).toBe(3)
    expect(normalizeSpaces(formatCurrencyMinor(1234, 'KWD', 'en-US'))).toBe('KWD 1.234')
    expect(minorAmountToInput(1234, 'KWD', 'en-US')).toBe('1.234')
  })

  it('falls back to USD for malformed currency codes', () => {
    expect(normalizeSettlementCurrency('not-a-currency')).toBe('USD')
    expect(currencyFractionDigits('not-a-currency', 'en-US')).toBe(2)
    expect(formatCurrencyMinor(999, 'not-a-currency', 'en-US')).toBe('$9.99')
  })
})

describe('settlement price input conversion', () => {
  it('converts decimal major-unit input into integer minor units', () => {
    expect(inputAmountToMinor('9.99', 'USD', 'en-US')).toBe(999)
    expect(inputAmountToMinor('8.9', 'EUR', 'fr-FR')).toBe(890)
    expect(inputAmountToMinor('1.234', 'KWD', 'en-US')).toBe(1234)
  })

  it('rejects non-zero precision beyond the currency limit', () => {
    expect(inputAmountToMinor('9.991', 'USD', 'en-US')).toBeNull()
    expect(inputAmountToMinor('1.2345', 'KWD', 'en-US')).toBeNull()
    expect(inputAmountToMinor('9.990', 'USD', 'en-US')).toBe(999)
  })

  it('rejects malformed and negative values', () => {
    for (const value of ['', 'abc', '-1', '1,25', '.', '1.2.3']) {
      expect(inputAmountToMinor(value, 'USD', 'en-US')).toBeNull()
    }
  })

  it('enforces zero decimal places for JPY', () => {
    expect(inputAmountToMinor('1200', 'JPY', 'ja-JP')).toBe(1200)
    expect(inputAmountToMinor('1200.0', 'JPY', 'ja-JP')).toBe(1200)
    expect(inputAmountToMinor('1200.1', 'JPY', 'ja-JP')).toBeNull()
  })
})
