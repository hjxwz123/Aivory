import { describe, expect, it } from 'vitest'
import { formatShortcut, formatTimeAgo } from '@/lib/utils'

describe('formatShortcut', () => {
  it('spells macOS shortcuts as one glyph run', () => {
    expect(formatShortcut('O', { shift: true, mac: true })).toBe('⇧⌘O')
    expect(formatShortcut('K', { mac: true })).toBe('⌘K')
  })

  it('spells other platforms with Ctrl and plus signs', () => {
    expect(formatShortcut('O', { shift: true, mac: false })).toBe('Ctrl+Shift+O')
    expect(formatShortcut(',', { mac: false })).toBe('Ctrl+,')
  })
})

describe('formatTimeAgo', () => {
  const now = Date.UTC(2026, 9, 2, 12, 0, 0)

  it('uses relative units for the past week', () => {
    expect(formatTimeAgo(now - 20_000, 'en', now)).toBe('now')
    expect(formatTimeAgo(now - 5 * 60_000, 'en', now)).toBe('5 minutes ago')
    expect(formatTimeAgo(now - 2 * 3_600_000, 'zh', now)).toBe('2小时前')
    expect(formatTimeAgo(now - 86_400_000, 'zh', now)).toBe('昨天')
  })

  it('falls back to a calendar date after a week', () => {
    expect(formatTimeAgo(now - 10 * 86_400_000, 'en', now)).not.toMatch(/ago/)
  })
})
