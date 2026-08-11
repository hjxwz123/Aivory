import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/admin.json'
import fr from '@/i18n/locales/fr/admin.json'
import ja from '@/i18n/locales/ja/admin.json'
import zhHant from '@/i18n/locales/zh-Hant/admin.json'
import zh from '@/i18n/locales/zh/admin.json'

const locales = { en, fr, ja, 'zh-Hant': zhHant, zh } as const

describe('administrator activity translations', () => {
  it('provides every activity state in every supported language', () => {
    for (const [locale, messages] of Object.entries(locales)) {
      expect(Object.keys(messages.activity).sort(), locale).toEqual([
        'loading',
        'navigating',
        'stillWaiting',
      ])
      for (const [key, value] of Object.entries(messages.activity)) {
        expect(value.trim(), `${locale}: activity.${key}`).toBeTruthy()
      }
    }
  })
})
