import { describe, expect, it } from 'vitest'
import enAdmin from '@/i18n/locales/en/admin.json'
import enAuth from '@/i18n/locales/en/auth.json'
import frAdmin from '@/i18n/locales/fr/admin.json'
import frAuth from '@/i18n/locales/fr/auth.json'
import jaAdmin from '@/i18n/locales/ja/admin.json'
import jaAuth from '@/i18n/locales/ja/auth.json'
import zhHantAdmin from '@/i18n/locales/zh-Hant/admin.json'
import zhHantAuth from '@/i18n/locales/zh-Hant/auth.json'
import zhAdmin from '@/i18n/locales/zh/admin.json'
import zhAuth from '@/i18n/locales/zh/auth.json'
import { changedAdminSettings } from '@/lib/admin-settings-patch'

const locales = {
  en: { admin: enAdmin, auth: enAuth },
  fr: { admin: frAdmin, auth: frAuth },
  ja: { admin: jaAdmin, auth: jaAuth },
  'zh-Hant': { admin: zhHantAdmin, auth: zhHantAuth },
  zh: { admin: zhAdmin, auth: zhAuth },
} as const

describe('changedAdminSettings', () => {
  it('does not resend unrelated authentication policy fields', () => {
    const saved = {
      signup_open: true,
      password_login_enabled: true,
      auth_entry_mode: 'login_page',
      oauth_auto_provision_enabled: true,
    }
    const draft = { ...saved, signup_open: false }

    expect(changedAdminSettings(draft, saved, Object.keys(saved))).toEqual({
      signup_open: false,
    })
  })

  it('includes each authentication policy field that actually changed', () => {
    const saved = {
      signup_open: true,
      password_login_enabled: true,
      auth_entry_mode: 'login_page',
    }
    const draft = {
      ...saved,
      password_login_enabled: false,
      auth_entry_mode: 'provider_picker',
    }

    expect(changedAdminSettings(draft, saved, Object.keys(saved))).toEqual({
      password_login_enabled: false,
      auth_entry_mode: 'provider_picker',
    })
  })

  it('explains provider-only signup in every supported language', () => {
    for (const [locale, messages] of Object.entries(locales)) {
      expect(messages.admin.settings.authPolicy.autoProvisionHint, `${locale} OAuth hint`).toBeTruthy()
      expect(messages.admin.settings.fields.signupOpenHint, `${locale} direct signup hint`).toBeTruthy()
      expect(messages.auth.register.passwordSignupClosed, `${locale} closed signup message`).toBeTruthy()
    }
  })
})
