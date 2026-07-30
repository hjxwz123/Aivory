import { describe, expect, it } from 'vitest'
import { getRedeemCodeStatus } from '@/lib/redeem-code-status'

const NOW = 2_000

describe('redeem code status', () => {
  it.each([
    ['unused', { enabled: true, expires_at: 0, used_count: 0, max_uses: 1 }],
    ['partial', { enabled: true, expires_at: 0, used_count: 1, max_uses: 2 }],
    ['used', { enabled: true, expires_at: 0, used_count: 1, max_uses: 1 }],
    ['invalid', { enabled: false, expires_at: 0, used_count: 0, max_uses: 1 }],
    ['invalid', { enabled: true, expires_at: NOW - 1, used_count: 0, max_uses: 1 }],
  ] as const)('returns %s for the matching code state', (expected, code) => {
    expect(getRedeemCodeStatus(code, NOW)).toBe(expected)
  })

  it('treats invalid as higher priority than used', () => {
    expect(getRedeemCodeStatus({ enabled: false, expires_at: 0, used_count: 1, max_uses: 1 }, NOW)).toBe('invalid')
  })

  it('keeps a code valid through its expiry second', () => {
    expect(getRedeemCodeStatus({ enabled: true, expires_at: NOW, used_count: 0, max_uses: 1 }, NOW)).toBe('unused')
  })
})
