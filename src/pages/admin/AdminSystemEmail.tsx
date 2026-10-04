import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { SettingsActions, SettingsBlock, SettingsRow, SettingsSection } from '@/components/settings/settings-section'

type Settings = Record<string, unknown>

const OWNED_KEYS = ['smtp_host', 'smtp_port', 'smtp_user', 'smtp_password', 'smtp_from', 'smtp_tls'] as const

export default function AdminSystemEmail() {
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

  function readString(key: string, fallback = ''): string {
    return typeof draft[key] === 'string' ? draft[key] : fallback
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

  const directTls = readBool('smtp_tls')

  return (
    <div>
      <AdminPageHeader
        title={t('admin:menu.emailService', { defaultValue: 'Email service' })}
        description={t('admin:settings.fields.smtpLead')}
      />

      {loading ? (
        <PanelFallback />
      ) : (
        <div className="mt-8">
          <SettingsSection
            title={t('admin:settings.fields.smtpSection', { defaultValue: 'Outgoing mail server' })}
            bodyClassName="divide-y-0"
          >
            <SettingsBlock className="flex flex-col gap-5">
              <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
                <Field
                  label={t('admin:settings.fields.smtpHost')}
                  htmlFor="smtp-host"
                  className="md:col-span-2"
                  hint={t('admin:settings.fields.smtpHostHint')}
                >
                  <Input
                    id="smtp-host"
                    data-admin-tour="email-smtp-host"
                    value={readString('smtp_host')}
                    placeholder="smtp.example.com"
                    onChange={(event) => setDraft((current) => ({ ...current, smtp_host: event.target.value }))}
                  />
                </Field>
                <Field
                  label={t('admin:settings.fields.smtpPort')}
                  htmlFor="smtp-port"
                  hint={t('admin:settings.fields.smtpPortHint')}
                >
                  <Input
                    id="smtp-port"
                    inputMode="numeric"
                    value={readString('smtp_port', '587')}
                    placeholder="587"
                    onChange={(event) => setDraft((current) => ({ ...current, smtp_port: event.target.value }))}
                  />
                </Field>
              </div>

              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                <Field label={t('admin:settings.fields.smtpUser')} htmlFor="smtp-user">
                  <Input
                    id="smtp-user"
                    autoComplete="off"
                    value={readString('smtp_user')}
                    onChange={(event) => setDraft((current) => ({ ...current, smtp_user: event.target.value }))}
                  />
                </Field>
                <Field
                  label={t('admin:settings.fields.smtpPassword')}
                  htmlFor="smtp-password"
                  hint={t('admin:settings.fields.smtpPasswordHint')}
                >
                  <Input
                    id="smtp-password"
                    type="password"
                    autoComplete="new-password"
                    value={readString('smtp_password')}
                    onChange={(event) => setDraft((current) => ({ ...current, smtp_password: event.target.value }))}
                  />
                </Field>
              </div>

              <Field
                label={t('admin:settings.fields.smtpFrom')}
                htmlFor="smtp-from"
                hint={t('admin:settings.fields.smtpFromHint')}
              >
                <Input
                  id="smtp-from"
                  value={readString('smtp_from')}
                  placeholder="noreply@example.com"
                  onChange={(event) => setDraft((current) => ({ ...current, smtp_from: event.target.value }))}
                />
              </Field>
            </SettingsBlock>
            <div className="border-t border-[var(--color-divider)]">
              <SettingsRow
                label={t('admin:settings.fields.smtpTls')}
                description={directTls ? t('admin:settings.fields.smtpTlsHint') : undefined}
                htmlFor="smtp-tls"
              >
                <Switch
                  id="smtp-tls"
                  checked={directTls}
                  onCheckedChange={(value) => setDraft((current) => ({ ...current, smtp_tls: value }))}
                />
              </SettingsRow>
            </div>
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
