import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

type LogScope = 'errors' | 'all'

function readBool(settings: Record<string, unknown>, key: string, fallback: boolean): boolean {
  return typeof settings[key] === 'boolean' ? settings[key] : fallback
}

export default function AdminSystemLogging() {
  const { t } = useTranslation(['admin', 'common'])
  const [scope, setScope] = useState<LogScope>('errors')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    adminApi.settings()
      .then((settings) => {
        const logsAllRequests =
          readBool(settings, 'log_full_requests', false) && !readBool(settings, 'log_errors_only', true)
        setScope(logsAllRequests ? 'all' : 'errors')
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
      })
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">
          {t('admin:menu.loggingPrivacy', { defaultValue: 'Logging and privacy' })}
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:settings.fields.logFullRequestsLead')}
        </p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          <Field
            label={t('admin:settings.fields.logFullRequests')}
            htmlFor="request-log-scope"
          >
            <Select value={scope} onValueChange={(value) => setScope(value as LogScope)}>
              <SelectTrigger id="request-log-scope">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="errors">{t('admin:usage.status.errorsOnly')}</SelectItem>
                <SelectItem value="all">{t('admin:usage.status.all')}</SelectItem>
              </SelectContent>
            </Select>
          </Field>

          {scope === 'all' && (
            <p className="text-xs leading-5 text-[var(--color-fg-subtle)]">
              {t('admin:settings.fields.logErrorsOnlyHint')}
            </p>
          )}

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
