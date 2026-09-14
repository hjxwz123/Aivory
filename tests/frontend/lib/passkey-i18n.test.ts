import { describe, expect, it } from 'vitest'
import enAuth from '@/i18n/locales/en/auth.json'
import frAuth from '@/i18n/locales/fr/auth.json'
import jaAuth from '@/i18n/locales/ja/auth.json'
import zhHantAuth from '@/i18n/locales/zh-Hant/auth.json'
import zhAuth from '@/i18n/locales/zh/auth.json'
import enSettings from '@/i18n/locales/en/settings.json'
import frSettings from '@/i18n/locales/fr/settings.json'
import jaSettings from '@/i18n/locales/ja/settings.json'
import zhHantSettings from '@/i18n/locales/zh-Hant/settings.json'
import zhSettings from '@/i18n/locales/zh/settings.json'
import enAdmin from '@/i18n/locales/en/admin.json'
import frAdmin from '@/i18n/locales/fr/admin.json'
import jaAdmin from '@/i18n/locales/ja/admin.json'
import zhHantAdmin from '@/i18n/locales/zh-Hant/admin.json'
import zhAdmin from '@/i18n/locales/zh/admin.json'

const authLocales = { en: enAuth, fr: frAuth, ja: jaAuth, 'zh-Hant': zhHantAuth, zh: zhAuth } as const
const settingsLocales = { en: enSettings, fr: frSettings, ja: jaSettings, 'zh-Hant': zhHantSettings, zh: zhSettings } as const
const adminLocales = { en: enAdmin, fr: frAdmin, ja: jaAdmin, 'zh-Hant': zhHantAdmin, zh: zhAdmin } as const

describe('passkey translations', () => {
  it('defines login strings and every server error code in each language', () => {
    for (const [locale, messages] of Object.entries(authLocales)) {
      expect(messages.login.passkey, `${locale}: login.passkey`).toBeTruthy()
      expect(messages.login.passkeyFailed, `${locale}: login.passkeyFailed`).toBeTruthy()
      for (const code of [
        'passkey_login_failed',
        'passkey_cancelled',
        'passkey_unavailable',
        'passkey_registration_failed',
        'passkey_login_disabled',
        'passkey_limit',
        'passkey_setup_expired',
        'passkey_not_found',
        'passkey_insecure_origin',
      ] as const) {
        expect(messages.errorCodes[code], `${locale}: errorCodes.${code}`).toBeTruthy()
      }
    }
  })

  it('defines the settings management strings and registration error codes in each language', () => {
    for (const [locale, messages] of Object.entries(settingsLocales)) {
      const passkey = messages.account.passkey
      for (const key of [
        'label',
        'body',
        'unsupported',
        'add',
        'addTitle',
        'addLead',
        'nameLabel',
        'added',
        'deleted',
        'delete',
        'deleteTitle',
        'deleteLead',
        'failed',
        'unnamed',
        'lastUsed',
        'neverUsed',
        'empty',
        'addHint',
      ] as const) {
        expect(passkey[key], `${locale}: account.passkey.${key}`).toBeTruthy()
      }
      for (const code of [
        'passkey_login_disabled',
        'passkey_limit',
        'passkey_setup_expired',
        'passkey_registration_failed',
        'passkey_unavailable',
        'passkey_insecure_origin',
      ] as const) {
        expect(passkey.errors[code], `${locale}: account.passkey.errors.${code}`).toBeTruthy()
      }
    }
  })

  it('labels the admin policy toggle in each language', () => {
    for (const [locale, messages] of Object.entries(adminLocales)) {
      expect(messages.settings.authPolicy.passkeyLogin, `${locale}: settings.authPolicy.passkeyLogin`).toBeTruthy()
    }
  })
})
