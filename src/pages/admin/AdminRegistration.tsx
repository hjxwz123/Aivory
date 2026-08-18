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
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useAuth } from '@/store/auth'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'signup_open',
  'register_ip_daily_limit',
  'register_captcha_required',
  'login_captcha_required',
  'email_verification_required',
  'email_domain_whitelist',
  'password_login_enabled',
  'auth_entry_mode',
  'auth_default_provider_id',
  'oauth_initial_password_policy',
  'oauth_auto_provision_enabled',
] as const

export default function AdminRegistration() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
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
    const passwordLoginEnabled = readBool('password_login_enabled', true)
    const entryMode = (readString('auth_entry_mode') || 'login_page') as AuthEntryMode
    const readyProviders = providers.filter((provider) => provider.enabled)
    if ((!passwordLoginEnabled || entryMode !== 'login_page') && readyProviders.length === 0) {
      toast.error(t('admin:settings.authPolicy.providerRequired'))
      return
    }
    if (entryMode === 'auto_redirect' && !readString('auth_default_provider_id')) {
      toast.error(t('admin:settings.authPolicy.defaultProviderRequired'))
      return
    }
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const key of OWNED_KEYS) {
        if (key in draft) patch[key] = draft[key]
      }
      await adminApi.updateSettings(patch)
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
  const authEntryMode = (readString('auth_entry_mode') || 'login_page') as AuthEntryMode
  const oauthPasswordPolicy = (readString('oauth_initial_password_policy') || 'required') as OAuthInitialPasswordPolicy
  const enabledProviders = providers.filter((provider) => provider.enabled)

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:menu.registrationPolicy', { defaultValue: 'Registration policy' })}
        </h1>
      </header>

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
        <section className="mt-8 flex flex-col gap-5">
          <div className="border-b border-[var(--color-divider)] pb-7">
            <div className="mb-5 max-w-2xl">
              <h2 className="text-base font-semibold text-[var(--color-fg)]">
                {t('admin:settings.authPolicy.title')}
              </h2>
              <p className="mt-1.5 text-sm leading-relaxed text-[var(--color-fg-muted)]">
                {t('admin:settings.authPolicy.description')}
              </p>
            </div>

            <div className="flex flex-col gap-5">
              <ToggleRow
                label={t('admin:settings.authPolicy.passwordLogin')}
                checked={passwordLoginEnabled}
                onChange={(value) =>
                  setDraft((current) => ({
                    ...current,
                    password_login_enabled: value,
                  }))
                }
              />

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

              <ToggleRow
                label={t('admin:settings.authPolicy.autoProvision')}
                checked={readBool('oauth_auto_provision_enabled', true)}
                onChange={(value) => setDraft((current) => ({ ...current, oauth_auto_provision_enabled: value }))}
              />

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
            </div>
          </div>

          <div className="flex flex-col gap-5 pt-1">
            <h2 className="text-base font-semibold text-[var(--color-fg)]">
              {t('admin:settings.authPolicy.registrationTitle')}
            </h2>
            <ToggleRow
              label={t('admin:settings.fields.signupOpen')}
              checked={readBool('signup_open', true)}
              onChange={(value) => setDraft((current) => ({ ...current, signup_open: value }))}
            />

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

            <div>
              <ToggleRow
                label={t('admin:settings.fields.registerCaptcha')}
                checked={registrationCaptchaRequired}
                onChange={(value) => setDraft((current) => ({ ...current, register_captcha_required: value }))}
              />
              {registrationCaptchaRequired && (
                <p className="mt-2 pl-1 text-xs text-[var(--color-fg-subtle)]">
                  {t('admin:settings.fields.registerCaptchaHint')}
                </p>
              )}
            </div>

            <div>
              <ToggleRow
                label={t('admin:settings.fields.loginCaptcha')}
                checked={loginCaptchaRequired}
                onChange={(value) => setDraft((current) => ({ ...current, login_captcha_required: value }))}
              />
              {loginCaptchaRequired && (
                <p className="mt-2 pl-1 text-xs text-[var(--color-fg-subtle)]">
                  {t('admin:settings.fields.loginCaptchaHint')}
                </p>
              )}
            </div>

            <div>
              <ToggleRow
                label={t('admin:settings.fields.emailVerificationRequired')}
                checked={emailVerificationRequired}
                onChange={(value) => setDraft((current) => ({ ...current, email_verification_required: value }))}
              />
              {emailVerificationRequired && (
                <p className="mt-2 pl-1 text-xs text-[var(--color-fg-subtle)]">
                  {t('admin:settings.fields.emailVerificationHint')}
                </p>
              )}
            </div>

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
          </div>

          <div className="flex justify-end">
            <Button loading={saving} onClick={() => void save()}>
              {t('common:actions.save')}
            </Button>
          </div>
        </section>
      )}
    </div>
  )
}

function ToggleRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return (
    <label className="flex items-center justify-between rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
      <span className="text-sm">{label}</span>
      <Switch checked={checked} onCheckedChange={onChange} />
    </label>
  )
}
