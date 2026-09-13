import { describe, expect, it } from 'vitest'
import { normalizeLanguage } from '@/i18n'

describe('initial language inherits the browser tag', () => {
  it('maps common navigator values onto the supported list', () => {
    const browserToSupported: Record<string, string> = {
      'zh-CN': 'zh',
      'zh-SG': 'zh',
      'zh-Hans-CN': 'zh',
      'zh-TW': 'zh-Hant',
      'zh-HK': 'zh-Hant',
      'zh-MO': 'zh-Hant',
      'en': 'en',
      'en-US': 'en',
      'en-GB': 'en',
      'ja-JP': 'ja',
      'fr-FR': 'fr',
      'fr-CA': 'fr',
    }
    for (const [tag, expected] of Object.entries(browserToSupported)) {
      expect(normalizeLanguage(tag), tag).toBe(expected)
    }
    // Fully unsupported languages must not hijack the fallback chain.
    expect(normalizeLanguage('de-DE')).toBeNull()
    expect(normalizeLanguage('')).toBeNull()
    expect(normalizeLanguage(undefined)).toBeNull()
  })

  it('keeps exact supported codes untouched', () => {
    expect(normalizeLanguage('zh-Hant')).toBe('zh-Hant')
    expect(normalizeLanguage('zh')).toBe('zh')
  })
})
