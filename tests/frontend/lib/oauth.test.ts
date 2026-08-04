import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en/admin.json'
import fr from '@/i18n/locales/fr/admin.json'
import ja from '@/i18n/locales/ja/admin.json'
import zhHant from '@/i18n/locales/zh-Hant/admin.json'
import zh from '@/i18n/locales/zh/admin.json'
import {
  getOAuthProviderFormCapabilities,
  oauthProviderErrorTranslationKey,
  oauthStartPath,
  OAUTH_PROVIDER_KINDS,
} from '@/lib/oauth'

const adminLocales = { en, fr, ja, 'zh-Hant': zhHant, zh } as const

describe('oauthStartPath', () => {
  it('binds the one-time captcha pass to the OAuth start request', () => {
    expect(oauthStartPath('oa/provider', 'pass+token/with=symbols')).toBe(
      '/auth/oauth/oa%2Fprovider/start?captcha_token=pass%2Btoken%2Fwith%3Dsymbols',
    )
  })

  it('does not add an empty captcha credential', () => {
    expect(oauthStartPath('oa_google')).toBe('/auth/oauth/oa_google/start')
  })
})

describe('OAuth provider form capabilities', () => {
  it('offers generic OAuth 2.0 as a distinct provider type', () => {
    expect(OAUTH_PROVIDER_KINDS).toEqual(['google', 'github', 'apple', 'oauth2', 'oidc'])
  })

  it('maps stable admin API errors to localized messages', () => {
    expect(oauthProviderErrorTranslationKey('oauth_provider_changed')).toBe(
      'admin:oauth.errors.providerChanged',
    )
    expect(oauthProviderErrorTranslationKey('oauth_client_secret_reentry_required')).toBe(
      'admin:oauth.errors.secretReentryRequired',
    )
    expect(oauthProviderErrorTranslationKey('oauth_provider_id_exists')).toBe('admin:oauth.errors.idConflict')
    expect(oauthProviderErrorTranslationKey('unknown_error')).toBeNull()
  })

  it('shows UserInfo endpoints but not OIDC verification metadata for OAuth 2.0', () => {
    expect(getOAuthProviderFormCapabilities('oauth2')).toEqual({
      usesAppleCredentials: false,
      usesCustomIcon: true,
      showsCustomEndpoints: true,
      showsOidcMetadata: false,
      showsUserInfoEndpoint: true,
    })
  })

  it('keeps issuer and JWKS fields exclusive to generic OIDC', () => {
    expect(getOAuthProviderFormCapabilities('oidc')).toEqual({
      usesAppleCredentials: false,
      usesCustomIcon: true,
      showsCustomEndpoints: true,
      showsOidcMetadata: true,
      showsUserInfoEndpoint: false,
    })

    expect(getOAuthProviderFormCapabilities('github').showsCustomEndpoints).toBe(false)
  })

  it('labels and explains both generic protocols in every admin locale', () => {
    for (const [locale, messages] of Object.entries(adminLocales)) {
      expect(messages.oauth.kinds.oauth2, `${locale} OAuth 2.0 label`).toBeTruthy()
      expect(messages.oauth.kinds.oidc, `${locale} OIDC label`).toBeTruthy()
      expect(messages.oauth.hints.oauth2, `${locale} OAuth 2.0 hint`).toBeTruthy()
      expect(messages.oauth.hints.oidc, `${locale} OIDC hint`).toBeTruthy()
      expect(messages.oauth.fields.scopesHintOauth2, `${locale} OAuth 2.0 scopes hint`).toBeTruthy()
      expect(messages.oauth.fields.scopesHintOidc, `${locale} OIDC scopes hint`).toContain('openid email profile')
      expect(messages.oauth.fields.scopesHintOauth2).not.toBe(messages.oauth.fields.scopesHintOidc)
      expect(messages.oauth.errors.providerChanged, `${locale} concurrent update error`).toBeTruthy()
      expect(messages.oauth.errors.secretReentryRequired, `${locale} secret re-entry error`).toContain('.p8')
    }
  })
})
