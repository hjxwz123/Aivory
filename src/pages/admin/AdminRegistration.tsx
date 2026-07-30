import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'signup_open',
  'register_ip_daily_limit',
  'register_captcha_required',
  'login_captcha_required',
  'email_verification_required',
  'email_domain_whitelist',
] as const

export default function AdminRegistration() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    adminApi.settings()
      .then(setDraft)
      .catch((error) => toast.error(error instanceof ApiError ? error.message : t('admin:common.failed')))
      .finally(() => setLoading(false))
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
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const key of OWNED_KEYS) {
        if (key in draft) patch[key] = draft[key]
      }
      await adminApi.updateSettings(patch)
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  const registrationCaptchaRequired = readBool('register_captcha_required')
  const loginCaptchaRequired = readBool('login_captcha_required')
  const emailVerificationRequired = readBool('email_verification_required')

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:menu.registrationPolicy', { defaultValue: 'Registration policy' })}
        </h1>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
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
