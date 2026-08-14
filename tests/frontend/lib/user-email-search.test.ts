import { describe, expect, it } from 'vitest'
import { normalizeExactUserEmailQuery } from '@/lib/user-email-search'

describe('normalizeExactUserEmailQuery', () => {
  it('normalizes a complete email for an exact lookup', () => {
    expect(normalizeExactUserEmailQuery('  Person@Example.COM  ')).toBe('person@example.com')
    expect(normalizeExactUserEmailQuery('person@localhost')).toBe('person@localhost')
  })

  it('rejects blank, partial, and malformed discovery queries', () => {
    expect(normalizeExactUserEmailQuery('')).toBeNull()
    expect(normalizeExactUserEmailQuery('Person')).toBeNull()
    expect(normalizeExactUserEmailQuery('person@')).toBeNull()
    expect(normalizeExactUserEmailQuery('@example.com')).toBeNull()
    expect(normalizeExactUserEmailQuery('person @example.com')).toBeNull()
    expect(normalizeExactUserEmailQuery('person@example.com extra')).toBeNull()
  })
})
