import { describe, expect, it } from 'vitest'
import { FONT_PRESETS, normalizeFontPref } from '@/types/settings'

describe('font preferences', () => {
  it('keeps the four distinct current presets', () => {
    expect(FONT_PRESETS).toEqual(['default', 'humanist', 'rounded', 'serif'])
    for (const preset of FONT_PRESETS) expect(normalizeFontPref(preset)).toBe(preset)
  })

  it('migrates the two legacy sans presets', () => {
    expect(normalizeFontPref('inter')).toBe('humanist')
    expect(normalizeFontPref('system')).toBe('rounded')
  })

  it('rejects unknown persisted values', () => {
    expect(normalizeFontPref('comic-sans')).toBeUndefined()
    expect(normalizeFontPref(null)).toBeUndefined()
  })
})
