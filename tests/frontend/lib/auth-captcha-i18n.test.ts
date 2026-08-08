import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/auth.json'
import fr from '@/i18n/locales/fr/auth.json'
import ja from '@/i18n/locales/ja/auth.json'
import zhHant from '@/i18n/locales/zh-Hant/auth.json'
import zh from '@/i18n/locales/zh/auth.json'

const locales = { en, fr, ja, 'zh-Hant': zhHant, zh } as const

describe('puzzle captcha translations', () => {
  it('defines every visible captcha message in each supported language', () => {
    const requiredKeys = [
      'captchaTitle',
      'captchaSlide',
      'captchaLoading',
      'captchaRefresh',
      'captchaWrong',
      'captchaSuccess',
    ] as const

    for (const [locale, messages] of Object.entries(locales)) {
      for (const key of requiredKeys) {
        expect(messages.register[key], `${locale}: register.${key}`).toBeTruthy()
      }
    }
  })
})
