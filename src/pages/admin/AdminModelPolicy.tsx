import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { TriangleAlert } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiChannel, ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { changedAdminSettings } from '@/lib/admin-settings-patch'
import {
  availablePolicyModels,
  modelPolicyErrorText,
  unavailablePolicyModelIDs,
} from '@/lib/admin-model-policy'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'default_model_id',
  'task_model_id',
  'title_model_id',
  'file_route_model_id',
  'tool_route_model_id',
  'tool_mode_default',
  'verify_model_id',
  'fallback_model_id',
  'fallback_ttft_sec',
] as const

export default function AdminModelPolicy() {
  const { t } = useTranslation(['admin', 'common'])
  const [models, setModels] = useState<ApiModel[]>([])
  const [channels, setChannels] = useState<ApiChannel[]>([])
  const [draft, setDraft] = useState<Settings>({})
  const [savedSettings, setSavedSettings] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    Promise.all([adminApi.settings(), adminApi.models('chat'), adminApi.channels()])
      .then(([settings, chatModels, nextChannels]) => {
        setDraft(settings)
        setSavedSettings(settings)
        setModels(chatModels)
        setChannels(nextChannels)
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
    const patch = changedAdminSettings(draft, savedSettings, OWNED_KEYS)
    if (Object.keys(patch).length === 0) {
      toast.success(t('admin:settings.saved'))
      return
    }
    setSaving(true)
    try {
      const updated = await adminApi.updateSettings(patch)
      setDraft(updated)
      setSavedSettings(updated)
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(modelPolicyErrorText(t, error) || (error instanceof ApiError ? error.message : t('admin:common.failed')))
    } finally {
      setSaving(false)
    }
  }

  const fallbackModelId = readString('fallback_model_id')
  const taskModelId = readString('task_model_id')
  const titleModelId = readString('title_model_id')
  const fileRouteModelId = readString('file_route_model_id')
  const selectableModels = availablePolicyModels(models, channels)
  const unavailableModelIDs = unavailablePolicyModelIDs(draft, selectableModels)

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
          {unavailableModelIDs.length > 0 ? (
            <div
              role="alert"
              className="flex items-start gap-2.5 rounded-[8px] bg-[var(--color-warning-soft)] px-3.5 py-3 text-sm leading-relaxed text-[var(--color-warning)]"
            >
              <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
              <span>{t('admin:settings.modelPolicy.stale')}</span>
            </div>
          ) : null}
          {selectableModels.length === 0 ? (
            <div
              role="status"
              className="rounded-[8px] bg-[var(--color-bg-muted)] px-3.5 py-3 text-sm leading-relaxed text-[var(--color-fg-muted)]"
            >
              {t('admin:settings.modelPolicy.empty')}
            </div>
          ) : null}

          <Field label={t('admin:settings.fields.defaultModel')} htmlFor="default-model">
            <Select
              value={readString('default_model_id')}
              onValueChange={(value) => setDraft((current) => ({ ...current, default_model_id: value }))}
            >
              <SelectTrigger id="default-model" data-admin-tour="model-policy-default-model">
                <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
              </SelectTrigger>
              <SelectContent>
                <PolicyModelOptions
                  currentId={readString('default_model_id')}
                  models={models}
                  selectableModels={selectableModels}
                  unavailableLabel={t('admin:settings.modelPolicy.unavailableOption')}
                />
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.titleModel')}
            htmlFor="title-model"
            hint={t('admin:settings.fields.titleModelHint')}
          >
            <Select
              value={titleModelId || 'inherit'}
              onValueChange={(value) =>
                setDraft((current) => ({ ...current, title_model_id: value === 'inherit' ? '' : value }))
              }
            >
              <SelectTrigger id="title-model">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">{t('admin:settings.fields.inheritTaskModel')}</SelectItem>
                <PolicyModelOptions
                  currentId={titleModelId}
                  models={models}
                  selectableModels={selectableModels}
                  unavailableLabel={t('admin:settings.modelPolicy.unavailableOption')}
                />
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.queryRouteModel')}
            htmlFor="tool-route-model"
            hint={t('admin:settings.fields.queryRouteModelHint')}
          >
            <Select
              value={readString('tool_route_model_id') || 'inherit'}
              onValueChange={(value) =>
                setDraft((current) => ({ ...current, tool_route_model_id: value === 'inherit' ? '' : value }))
              }
            >
              <SelectTrigger id="tool-route-model" data-admin-tour="model-policy-tool-route-model">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">{t('admin:settings.fields.currentConversationModel')}</SelectItem>
                <PolicyModelOptions
                  currentId={readString('tool_route_model_id')}
                  models={models}
                  selectableModels={selectableModels}
                  unavailableLabel={t('admin:settings.modelPolicy.unavailableOption')}
                />
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.fileRouteModel')}
            htmlFor="file-route-model"
            hint={t('admin:settings.fields.fileRouteModelHint')}
          >
            <Select
              value={fileRouteModelId || 'inherit'}
              onValueChange={(value) =>
                setDraft((current) => ({ ...current, file_route_model_id: value === 'inherit' ? '' : value }))
              }
            >
              <SelectTrigger id="file-route-model">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">{t('admin:settings.fields.inheritTaskModel')}</SelectItem>
                <PolicyModelOptions
                  currentId={fileRouteModelId}
                  models={models}
                  selectableModels={selectableModels}
                  unavailableLabel={t('admin:settings.modelPolicy.unavailableOption')}
                />
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.taskModel')}
            htmlFor="task-model"
            hint={t('admin:settings.fields.taskModelHint')}
          >
            <Select
              value={taskModelId || 'inherit'}
              onValueChange={(value) =>
                setDraft((current) => ({ ...current, task_model_id: value === 'inherit' ? '' : value }))
              }
            >
              <SelectTrigger id="task-model" data-admin-tour="model-policy-task-model">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">{t('admin:settings.fields.currentConversationModel')}</SelectItem>
                <PolicyModelOptions
                  currentId={taskModelId}
                  models={models}
                  selectableModels={selectableModels}
                  unavailableLabel={t('admin:settings.modelPolicy.unavailableOption')}
                />
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('admin:settings.fields.defaultToolMode')}
            htmlFor="default-tool-mode"
            hint={t('admin:settings.fields.defaultToolModeHint')}
          >
            <Select
              value={readString('tool_mode_default') || 'auto'}
              onValueChange={(value) =>
                setDraft((current) => ({ ...current, tool_mode_default: value }))
              }
            >
              <SelectTrigger id="default-tool-mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">{t('admin:settings.fields.toolModeAuto')}</SelectItem>
                <SelectItem value="enabled">{t('admin:settings.fields.toolModeEnabled')}</SelectItem>
                <SelectItem value="disabled">{t('admin:settings.fields.toolModeDisabled')}</SelectItem>
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
                <PolicyModelOptions
                  currentId={readString('verify_model_id')}
                  models={models}
                  selectableModels={selectableModels}
                  unavailableLabel={t('admin:settings.modelPolicy.unavailableOption')}
                />
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
                  <PolicyModelOptions
                    currentId={fallbackModelId}
                    models={models}
                    selectableModels={selectableModels}
                    unavailableLabel={t('admin:settings.modelPolicy.unavailableOption')}
                  />
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

function PolicyModelOptions({
  currentId,
  models,
  selectableModels,
  unavailableLabel,
}: {
  currentId: string
  models: ApiModel[]
  selectableModels: ApiModel[]
  unavailableLabel: string
}) {
  const selectableIDs = new Set(selectableModels.map((model) => model.id))
  const unavailableCurrent = currentId && !selectableIDs.has(currentId)
    ? models.find((model) => model.id === currentId)
    : undefined

  return (
    <>
      {currentId && !selectableIDs.has(currentId) ? (
        <SelectItem value={currentId} disabled>
          {unavailableCurrent?.label ?? currentId} ({unavailableLabel})
        </SelectItem>
      ) : null}
      {selectableModels.map((model) => (
        <SelectItem key={model.id} value={model.id}>
          {model.label}
        </SelectItem>
      ))}
    </>
  )
}
