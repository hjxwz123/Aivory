import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiOAuthProvider, AuthEntryMode, OAuthInitialPasswordPolicy } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { changedAdminSettings } from '@/lib/admin-settings-patch'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useAuth } from '@/store/auth'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { SettingsActions, SettingsBlock, SettingsRow, SettingsSection } from '@/components/settings/settings-section'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'signup_open',
  'register_ip_daily_limit',
  'register_captcha_required',
  'login_captcha_required',
  'email_verification_required',
  'email_domain_whitelist',
  'password_login_enabled',
  'passkey_login_enabled',
  'auth_entry_mode',
  'auth_default_provider_id',
  'oauth_initial_password_policy',
  'oauth_auto_provision_enabled',
] as const

const AUTH_POLICY_KEYS = [
  'password_login_enabled',
  'auth_entry_mode',
  'auth_default_provider_id',
  'oauth_initial_password_policy',
  'oauth_auto_provision_enabled',
] as const

export default function AdminRegistration() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [savedSettings, setSavedSettings] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [providers, setProviders] = useState<ApiOAuthProvider[]>([])
  const [loadError, setLoadError] = useState('')

  async function load() {
    setLoading(true)
    setLoadError('')
    try {
      const [settings, nextProviders] = await Promise.all([adminApi.settings(), adminApi.oauthProviders()])
      setDraft(settings)
      setSavedSettings(settings)
      setProviders(nextProviders)
    } catch (error) {
      const message = error instanceof ApiError ? error.message : t('admin:common.failed')
      setLoadError(message)
      toast.error(message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function readString(key: string): string {
    return typeof draft[key] === 'string' ? draft[key] : ''
  }

  function readNumber(key: string, fallback = 0): number {
    return typeof draft[key] === 'number' ? draft[key] : fallback
  }

  function readBool(key: string, fallback = false): boolean {
    return typeof draft[key] === 'boolean' ? draft[key] : fallback
  }

  async function save() {
    const patch = changedAdminSettings(draft, savedSettings, OWNED_KEYS)
    const authPolicyChanged = AUTH_POLICY_KEYS.some((key) => key in patch)
    const passwordLoginEnabled = readBool('password_login_enabled', true)
    const entryMode = (readString('auth_entry_mode') || 'login_page') as AuthEntryMode
    const readyProviders = providers.filter((provider) => provider.enabled)
    if (authPolicyChanged && (!passwordLoginEnabled || entryMode !== 'login_page') && readyProviders.length === 0) {
      toast.error(t('admin:settings.authPolicy.providerRequired'))
      return
    }
    if (authPolicyChanged && entryMode === 'auto_redirect' && !readString('auth_default_provider_id')) {
      toast.error(t('admin:settings.authPolicy.defaultProviderRequired'))
      return
    }
    if (Object.keys(patch).length === 0) {
      toast.success(t('admin:settings.saved'))
      return
    }
    setSaving(true)
    try {
      const updated = await adminApi.updateSettings(patch)
      setDraft(updated)
      setSavedSettings(updated)
      await useAuth.getState().refreshAuthPolicy()
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      const code = error instanceof ApiError ? error.message : ''
      const message =
        code === 'auth_policy_conflict'
          ? t('admin:settings.authPolicy.conflict')
          : code === 'auth_policy_provider_required'
            ? t('admin:settings.authPolicy.providerRequired')
            : code === 'auth_policy_admin_identity_required'
              ? t('admin:settings.authPolicy.adminIdentityRequired')
              : code || t('admin:common.failed')
      toast.error(message)
    } finally {
      setSaving(false)
    }
  }

  const registrationCaptchaRequired = readBool('register_captcha_required')
  const loginCaptchaRequired = readBool('login_captcha_required')
  const emailVerificationRequired = readBool('email_verification_required')
  const passwordLoginEnabled = readBool('password_login_enabled', true)
  const passkeyLoginEnabled = readBool('passkey_login_enabled', true)
  const authEntryMode = (readString('auth_entry_mode') || 'login_page') as AuthEntryMode
  const oauthPasswordPolicy = (readString('oauth_initial_password_policy') || 'required') as OAuthInitialPasswordPolicy
  const enabledProviders = providers.filter((provider) => provider.enabled)

  return (
    <div>
      <AdminPageHeader
        title={t('admin:menu.registrationPolicy', { defaultValue: 'Registration policy' })}
      />

      {loading ? (
        <PanelFallback />
      ) : loadError ? (
        <div className="mt-8 flex min-h-64 flex-col items-center justify-center gap-4 text-center" role="alert">
          <p className="max-w-md text-sm text-[var(--color-fg-muted)]">{loadError}</p>
          <Button variant="secondary" onClick={() => void load()}>
            {t('common:actions.retry', { defaultValue: 'Retry' })}
          </Button>
        </div>
      ) : (
        <div className="mt-8">
          <SettingsSection
            title={t('admin:settings.authPolicy.title')}
            description={t('admin:settings.authPolicy.description')}
          >
            <SettingsRow label={t('admin:settings.authPolicy.passwordLogin')} htmlFor="password-login">
              <Switch
                id="password-login"
                checked={passwordLoginEnabled}
                onCheckedChange={(value) =>
                  setDraft((current) => ({
                    ...current,
                    password_login_enabled: value,
                  }))
                }
              />
            </SettingsRow>
            <SettingsRow
              label={t('admin:settings.authPolicy.passkeyLogin', { defaultValue: 'Passkey login' })}
              htmlFor="passkey-login"
            >
              <Switch
                id="passkey-login"
                checked={passkeyLoginEnabled}
                onCheckedChange={(value) =>
                  setDraft((current) => ({
                    ...current,
                    passkey_login_enabled: value,
                  }))
                }
              />
            </SettingsRow>
            <SettingsBlock className="flex flex-col gap-5">
              <Field
                label={t('admin:settings.authPolicy.entryMode')}
                htmlFor="auth-entry-mode"
                hint={t('admin:settings.authPolicy.entryModeHint')}
              >
                <Select
                  value={authEntryMode}
                  onValueChange={(value: AuthEntryMode) =>
                    setDraft((current) => ({ ...current, auth_entry_mode: value }))
                  }
                >
                  <SelectTrigger id="auth-entry-mode">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="login_page">{t('admin:settings.authPolicy.entryLoginPage')}</SelectItem>
                    <SelectItem value="provider_picker">{t('admin:settings.authPolicy.entryProviderPicker')}</SelectItem>
                    <SelectItem value="auto_redirect">{t('admin:settings.authPolicy.entryAutoRedirect')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>

              {authEntryMode === 'auto_redirect' ? (
                <Field
                  label={t('admin:settings.authPolicy.defaultProvider')}
                  htmlFor="auth-default-provider"
                  hint={t('admin:settings.authPolicy.defaultProviderHint')}
                >
                  <Select
                    value={readString('auth_default_provider_id')}
                    onValueChange={(value) =>
                      setDraft((current) => ({ ...current, auth_default_provider_id: value }))
                    }
                    disabled={enabledProviders.length === 0}
                  >
                    <SelectTrigger id="auth-default-provider">
                      <SelectValue placeholder={t('admin:settings.authPolicy.selectProvider')} />
                    </SelectTrigger>
                    <SelectContent>
                      {enabledProviders.map((provider) => (
                        <SelectItem key={provider.id} value={provider.id}>{provider.name}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              ) : null}

              <Field
                label={t('admin:settings.authPolicy.initialPassword')}
                htmlFor="oauth-password-policy"
                hint={t('admin:settings.authPolicy.initialPasswordHint')}
              >
                <Select
                  value={oauthPasswordPolicy}
                  onValueChange={(value: OAuthInitialPasswordPolicy) =>
                    setDraft((current) => ({ ...current, oauth_initial_password_policy: value }))
                  }
                >
                  <SelectTrigger id="oauth-password-policy">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="required">
                      {t('admin:settings.authPolicy.passwordRequired')}
                    </SelectItem>
                    <SelectItem value="optional">{t('admin:settings.authPolicy.passwordOptional')}</SelectItem>
                    <SelectItem value="disabled">{t('admin:settings.authPolicy.passwordDisabled')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </SettingsBlock>
            <SettingsRow
              label={t('admin:settings.authPolicy.autoProvision')}
              description={t('admin:settings.authPolicy.autoProvisionHint')}
              htmlFor="oauth-auto-provision"
            >
              <Switch
                id="oauth-auto-provision"
                checked={readBool('oauth_auto_provision_enabled', true)}
                onCheckedChange={(value) => setDraft((current) => ({ ...current, oauth_auto_provision_enabled: value }))}
              />
            </SettingsRow>
            <SettingsBlock className="py-3">
              {enabledProviders.length === 0 ? (
                <div className="flex flex-wrap items-center justify-between gap-3 rounded-[8px] bg-[var(--color-bg-muted)] px-3.5 py-3 text-sm text-[var(--color-fg-muted)]">
                  <span>{t('admin:settings.authPolicy.noProviders')}</span>
                  <Link to="/admin/oauth" className="font-medium text-[var(--color-accent)] hover:text-[var(--color-accent-hover)]">
                    {t('admin:settings.authPolicy.configureProviders')}
                  </Link>
                </div>
              ) : (
                <p className="text-xs leading-relaxed text-[var(--color-fg-subtle)]">
                  {t('admin:settings.authPolicy.lockoutNotice')}
                </p>
              )}
            </SettingsBlock>
          </SettingsSection>

          <SettingsSection title={t('admin:settings.authPolicy.registrationTitle')}>
            <SettingsRow
              label={t('admin:settings.fields.signupOpen')}
              description={t('admin:settings.fields.signupOpenHint')}
              htmlFor="signup-open"
            >
              <Switch
                id="signup-open"
                checked={readBool('signup_open', true)}
                onCheckedChange={(value) => setDraft((current) => ({ ...current, signup_open: value }))}
              />
            </SettingsRow>
            <SettingsBlock>
              <Field
                label={t('admin:settings.fields.registerIpDailyLimit')}
                htmlFor="register-ip-daily-limit"
                hint={t('admin:settings.fields.registerIpDailyLimitHint')}
              >
                <Input
                  id="register-ip-daily-limit"
                  type="number"
                  min={0}
                  value={String(readNumber('register_ip_daily_limit'))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      register_ip_daily_limit: Math.max(0, Number(event.target.value) || 0),
                    }))
                  }
                />
              </Field>
            </SettingsBlock>
            <SettingsRow
              label={t('admin:settings.fields.registerCaptcha')}
              description={registrationCaptchaRequired ? t('admin:settings.fields.registerCaptchaHint') : undefined}
              htmlFor="register-captcha"
            >
              <Switch
                id="register-captcha"
                checked={registrationCaptchaRequired}
                onCheckedChange={(value) => setDraft((current) => ({ ...current, register_captcha_required: value }))}
              />
            </SettingsRow>
            <SettingsRow
              label={t('admin:settings.fields.loginCaptcha')}
              description={loginCaptchaRequired ? t('admin:settings.fields.loginCaptchaHint') : undefined}
              htmlFor="login-captcha"
            >
              <Switch
                id="login-captcha"
                checked={loginCaptchaRequired}
                onCheckedChange={(value) => setDraft((current) => ({ ...current, login_captcha_required: value }))}
              />
            </SettingsRow>
            <SettingsRow
              label={t('admin:settings.fields.emailVerificationRequired')}
              description={emailVerificationRequired ? t('admin:settings.fields.emailVerificationHint') : undefined}
              htmlFor="email-verification"
            >
              <Switch
                id="email-verification"
                checked={emailVerificationRequired}
                onCheckedChange={(value) => setDraft((current) => ({ ...current, email_verification_required: value }))}
              />
            </SettingsRow>
            <SettingsBlock>
              <Field
                label={t('admin:settings.fields.domainWhitelist')}
                htmlFor="email-domain-whitelist"
                hint={t('admin:settings.fields.domainWhitelistHint')}
              >
                <Input
                  id="email-domain-whitelist"
                  value={readString('email_domain_whitelist')}
                  placeholder="example.com, company.io"
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, email_domain_whitelist: event.target.value }))
                  }
                />
              </Field>
            </SettingsBlock>
          </SettingsSection>

          <SettingsActions>
            <Button loading={saving} onClick={() => void save()}>
              {t('common:actions.save')}
            </Button>
          </SettingsActions>
        </div>
      )}
    </div>
  )
}
