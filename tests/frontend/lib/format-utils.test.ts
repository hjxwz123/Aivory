// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { formatBytes, formatRelativeDate } from '@/lib/utils'

describe('formatRelativeDate', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    // Midday so "yesterday" never straddles a DST boundary in the test zone.
    vi.setSystemTime(new Date(2026, 9, 5, 12, 0, 0))
  })

  afterEach(() => {
    vi.useRealTimers()
    document.documentElement.lang = ''
  })

  it('follows the UI language instead of hard-coded English', () => {
    document.documentElement.lang = 'zh'
    expect(formatRelativeDate(new Date(2026, 9, 5, 8, 0))).toBe('今天')
    expect(formatRelativeDate(new Date(2026, 9, 4, 23, 0))).toBe('昨天')

    document.documentElement.lang = 'en'
    expect(formatRelativeDate(new Date(2026, 9, 5, 8, 0))).toBe('today')
    expect(formatRelativeDate(new Date(2026, 9, 4, 23, 0))).toBe('yesterday')
  })

  it('compares calendar days, not 24-hour windows', () => {
    document.documentElement.lang = 'en'
    // 13 hours ago but on the previous calendar day.
    expect(formatRelativeDate(new Date(2026, 9, 4, 23, 30))).toBe('yesterday')
  })

  it('drops the year only for dates in the current year', () => {
    document.documentElement.lang = 'en'
    expect(formatRelativeDate(new Date(2026, 1, 3))).not.toMatch(/2026/)
    expect(formatRelativeDate(new Date(2025, 11, 20))).toMatch(/2025/)
  })
})

describe('formatBytes', () => {
  it('scales through B, KB, MB and GB', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatBytes(250 * 1024 * 1024)).toBe('250 MB')
    expect(formatBytes(3 * 1024 ** 3)).toBe('3.0 GB')
  })

  it('treats invalid input as zero', () => {
    expect(formatBytes(Number.NaN)).toBe('0 B')
    expect(formatBytes(-10)).toBe('0 B')
  })
})
