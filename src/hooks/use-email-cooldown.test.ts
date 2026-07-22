import { describe, expect, it } from 'vitest'
import {
  emailRetryAfterFromBody,
  normalizeEmailRetryAfter,
  remainingEmailCooldown,
} from './use-email-cooldown'

describe('email cooldown helpers', () => {
  it('normalizes server retry-after values to positive whole seconds', () => {
    expect(normalizeEmailRetryAfter(119.1)).toBe(120)
    expect(normalizeEmailRetryAfter('87')).toBe(87)
    expect(normalizeEmailRetryAfter(undefined, 120)).toBe(120)
    expect(normalizeEmailRetryAfter(-1)).toBe(0)
  })

  it('reads retry_after from an API error or success body', () => {
    expect(emailRetryAfterFromBody({ error: 'email_cooldown', retry_after: 73 })).toBe(73)
    expect(emailRetryAfterFromBody({ error: 'rate_limited' })).toBe(0)
    expect(emailRetryAfterFromBody(null, 120)).toBe(120)
  })

  it('rounds a deadline up and reaches zero once expired', () => {
    expect(remainingEmailCooldown(120_001, 1)).toBe(120)
    expect(remainingEmailCooldown(1_001, 1)).toBe(1)
    expect(remainingEmailCooldown(1_000, 1_000)).toBe(0)
  })
})
