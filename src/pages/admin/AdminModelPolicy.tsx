import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'default_model_id',
  'task_model_id',
  'verify_model_id',
  'fallback_model_id',
  'fallback_ttft_sec',
] as const

export default function AdminModelPolicy() {
  const { t } = useTranslation(['admin', 'common'])
  const [models, setModels] = useState<ApiModel[]>([])
  const [draft, setDraft] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    Promise.all([adminApi.settings(), adminApi.models('chat')])
      .then(([settings, chatModels]) => {
        setDraft(settings)
        setModels(chatModels)
      })
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

  const fallbackModelId = readString('fallback_model_id')

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:menu.modelPolicy', { defaultValue: 'Model policy' })}
        </h1>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          <Field label={t('admin:settings.fields.defaultModel')} htmlFor="default-model">
            <Select
              value={readString('default_model_id')}
              onValueChange={(value) => setDraft((current) => ({ ...current, default_model_id: value }))}
            >
              <SelectTrigger id="default-model">
                <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
              </SelectTrigger>
              <SelectContent>
                {models.map((model) => (
                  <SelectItem key={model.id} value={model.id}>
                    {model.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.taskModel')}
            htmlFor="task-model"
            hint={t('admin:settings.fields.taskModelHint')}
          >
            <Select
              value={readString('task_model_id')}
              onValueChange={(value) => setDraft((current) => ({ ...current, task_model_id: value }))}
            >
              <SelectTrigger id="task-model">
                <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
              </SelectTrigger>
              <SelectContent>
                {models.map((model) => (
                  <SelectItem key={model.id} value={model.id}>
                    {model.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.verifyModel')}
            htmlFor="verify-model"
            hint={t('admin:settings.fields.verifyModelHint')}
          >
            <Select
              value={readString('verify_model_id') || 'none'}
              onValueChange={(value) =>
                setDraft((current) => ({ ...current, verify_model_id: value === 'none' ? '' : value }))
              }
            >
              <SelectTrigger id="verify-model">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">{t('admin:settings.fields.fallbackNone')}</SelectItem>
                {models.map((model) => (
                  <SelectItem key={model.id} value={model.id}>
                    {model.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Field
              label={t('admin:settings.fields.fallbackModel')}
              htmlFor="fallback-model"
              hint={t('admin:settings.fields.fallbackModelHint')}
            >
              <Select
                value={fallbackModelId || 'none'}
                onValueChange={(value) =>
                  setDraft((current) => ({ ...current, fallback_model_id: value === 'none' ? '' : value }))
                }
              >
                <SelectTrigger id="fallback-model">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t('admin:settings.fields.fallbackNone')}</SelectItem>
                  {models.map((model) => (
                    <SelectItem key={model.id} value={model.id}>
                      {model.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            {fallbackModelId && (
              <Field
                label={t('admin:settings.fields.fallbackTtft')}
                htmlFor="fallback-ttft"
                hint={t('admin:settings.fields.fallbackTtftHint')}
              >
                <Input
                  id="fallback-ttft"
                  type="number"
                  min={0}
                  value={String(readNumber('fallback_ttft_sec'))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      fallback_ttft_sec: Math.max(0, Number(event.target.value) || 0),
                    }))
                  }
                />
              </Field>
            )}
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
