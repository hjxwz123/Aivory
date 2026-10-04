import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Switch } from '@/components/ui/switch'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { SettingsActions, SettingsRow, SettingsSection } from '@/components/settings/settings-section'

type LogScope = 'errors' | 'all'

function readBool(settings: Record<string, unknown>, key: string, fallback: boolean): boolean {
  return typeof settings[key] === 'boolean' ? settings[key] : fallback
}

export default function AdminSystemLogging() {
  const { t } = useTranslation(['admin', 'common'])
  const [scope, setScope] = useState<LogScope>('errors')
  const [requestBodies, setRequestBodies] = useState(true)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    adminApi.settings()
      .then((settings) => {
        const logsAllRequests =
          readBool(settings, 'log_full_requests', false) && !readBool(settings, 'log_errors_only', true)
        setScope(logsAllRequests ? 'all' : 'errors')
        setRequestBodies(readBool(settings, 'log_request_bodies', true))
      })
      .catch((error) => toast.error(error instanceof ApiError ? error.message : t('admin:common.failed')))
      .finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function save() {
    setSaving(true)
    try {
      await adminApi.updateSettings({
        log_full_requests: scope === 'all',
        log_errors_only: scope === 'errors',
        log_request_bodies: requestBodies,
      })
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div>
      <AdminPageHeader
        title={t('admin:menu.loggingPrivacy', { defaultValue: 'Logging and privacy' })}
        description={t('admin:settings.fields.logFullRequestsLead')}
      />

      {loading ? (
        <PanelFallback />
      ) : (
        <div className="mt-8">
          <SettingsSection>
            <SettingsRow
              label={t('admin:settings.fields.logFullRequests')}
              description={scope === 'all' ? t('admin:settings.fields.logErrorsOnlyHint') : undefined}
              htmlFor="request-log-scope"
            >
              <Select value={scope} onValueChange={(value) => setScope(value as LogScope)}>
                <SelectTrigger id="request-log-scope" className="w-full sm:w-56">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="errors">{t('admin:usage.status.errorsOnly')}</SelectItem>
                  <SelectItem value="all">{t('admin:usage.status.all')}</SelectItem>
                </SelectContent>
              </Select>
            </SettingsRow>
            <SettingsRow
              label={t('admin:settings.fields.logRequestBodies')}
              description={t('admin:settings.fields.logRequestBodiesHint')}
              htmlFor="request-body-logging"
            >
              <Switch
                id="request-body-logging"
                checked={requestBodies}
                onCheckedChange={setRequestBodies}
              />
            </SettingsRow>
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
