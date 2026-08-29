import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LoaderCircle, RefreshCw } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'keep_recent_rounds',
  'compaction_token_trigger',
  'compaction_token_cap',
  'compaction_token_target_percentage',
  'compaction_retention_percentage',
  'summary_max_tokens',
  'summary_target_percent',
  'summary_merge_max_tokens',
  'compaction_request_max_tokens',
  'context_compaction_prompt',
  'compaction_enabled',
  'context_compaction_model_id',
  'memory_enabled',
] as const

export default function AdminContextMemory() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [models, setModels] = useState<ApiModel[]>([])
  const [settingsLoading, setSettingsLoading] = useState(true)
  const [settingsError, setSettingsError] = useState<string | null>(null)
  const [modelsLoading, setModelsLoading] = useState(true)
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const loadSettings = useCallback(async () => {
    setSettingsLoading(true)
    setSettingsError(null)
    try {
      setDraft(await adminApi.settings())
    } catch (error) {
      const message = error instanceof ApiError ? error.message : t('admin:common.failed')
      setSettingsError(message)
      toast.error(message)
    } finally {
      setSettingsLoading(false)
    }
  }, [t])

  const loadModels = useCallback(async () => {
    setModelsLoading(true)
    setModelsError(null)
    try {
      setModels(await adminApi.models('chat'))
    } catch (error) {
      setModelsError(
        error instanceof ApiError
          ? error.message
          : t('admin:settings.fields.compactionModelsLoadFailed'),
      )
    } finally {
      setModelsLoading(false)
    }
  }, [t])

  useEffect(() => {
    void loadSettings()
    void loadModels()
  }, [loadModels, loadSettings])

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
    const retention = readNumber('compaction_retention_percentage', 40)
    const tokenTarget = readNumber('compaction_token_target_percentage', 60)
    const target = readNumber('summary_target_percent', 30)
    const keepRounds = readNumber('keep_recent_rounds', 6)
    const summaryTokens = readNumber('summary_max_tokens', 8192)
    const mergedSummaryTokens = readNumber('summary_merge_max_tokens', 8192)
    const requestTokens = readNumber('compaction_request_max_tokens', 32768)
    if (
      retention < 10 ||
      retention > 50 ||
      tokenTarget < 25 ||
      tokenTarget > 80 ||
      target < 5 ||
      target > 80 ||
      keepRounds < 1 ||
      summaryTokens < 256 ||
      mergedSummaryTokens < 256 ||
      requestTokens < 8192
    ) {
      toast.error(t('admin:settings.fields.compactionRangeError'))
      return
    }
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const key of OWNED_KEYS) {
        if (key in draft) patch[key] = draft[key]
      }
      const saved = await adminApi.updateSettings(patch)
      setDraft(saved)
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  const compactionEnabled = readBool('compaction_enabled', true)
  const compactionModelId = readString('context_compaction_model_id')
  const selectedCompactionModelExists = models.some((model) => model.id === compactionModelId)

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:menu.contextMemory', { defaultValue: 'Context and memory' })}
        </h1>
      </header>

      {settingsLoading ? (
        <PanelFallback />
      ) : settingsError ? (
        <section
          className="mt-8 flex flex-col items-start gap-3 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-4 py-4"
          role="alert"
        >
          <p className="text-sm text-[var(--color-danger)]">{settingsError}</p>
          <Button
            variant="secondary"
            size="sm"
            leadingIcon={<RefreshCw size={14} aria-hidden />}
            onClick={() => void loadSettings()}
          >
            {t('common:actions.tryAgain')}
          </Button>
        </section>
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          <ToggleRow
            label={t('admin:settings.fields.compactionEnabled')}
            checked={compactionEnabled}
            onChange={(value) => setDraft((current) => ({ ...current, compaction_enabled: value }))}
          />

          {compactionEnabled && (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <Field
                label={t('admin:settings.fields.compactionModel')}
                htmlFor="context-compaction-model"
                hint={modelsError ? undefined : t('admin:settings.fields.compactionModelHint')}
                className="md:col-span-2"
              >
                <Select
                  value={compactionModelId || 'inherit'}
                  disabled={modelsLoading || Boolean(modelsError)}
                  onValueChange={(value) =>
                    setDraft((current) => ({
                      ...current,
                      context_compaction_model_id: value === 'inherit' ? '' : value,
                    }))
                  }
                >
                  <SelectTrigger
                    id="context-compaction-model"
                    aria-busy={modelsLoading || undefined}
                  >
                    <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="inherit">
                      {t('admin:settings.fields.currentConversationModel')}
                    </SelectItem>
                    {compactionModelId && !selectedCompactionModelExists ? (
                      <SelectItem value={compactionModelId}>
                        {t('admin:settings.fields.compactionModelUnavailable', { id: compactionModelId })}
                      </SelectItem>
                    ) : null}
                    {models.map((model) => (
                      <SelectItem key={model.id} value={model.id}>
                        {model.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {modelsLoading ? (
                  <p
                    className="inline-flex items-center gap-2 text-xs text-[var(--color-fg-subtle)]"
                    role="status"
                  >
                    <LoaderCircle size={13} className="animate-spin" aria-hidden />
                    {t('admin:settings.fields.compactionModelsLoading')}
                  </p>
                ) : modelsError ? (
                  <div className="flex flex-col items-start gap-2 sm:flex-row sm:items-center sm:justify-between" role="alert">
                    <p className="text-xs text-[var(--color-danger)]">{modelsError}</p>
                    <Button
                      variant="secondary"
                      size="xs"
                      leadingIcon={<RefreshCw size={13} aria-hidden />}
                      onClick={() => void loadModels()}
                    >
                      {t('common:actions.tryAgain')}
                    </Button>
                  </div>
                ) : models.length === 0 ? (
                  <p className="text-xs text-[var(--color-fg-subtle)]">
                    {t('admin:settings.fields.compactionModelsEmpty')}
                  </p>
                ) : null}
              </Field>

              <Field
                label={t('admin:settings.fields.tokenTrigger')}
                htmlFor="compaction-token-trigger"
                hint={t('admin:settings.fields.tokenTriggerHint')}
              >
                <Input
                  id="compaction-token-trigger"
                  type="number"
                  min={0}
                  step={1}
                  value={String(readNumber('compaction_token_trigger', 32000))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      compaction_token_trigger: Math.max(0, Math.floor(Number(event.target.value) || 0)),
                    }))
                  }
                />
              </Field>
              <Field
                label={t('admin:settings.fields.tokenTargetPercentage')}
                htmlFor="compaction-token-target-percentage"
                hint={t('admin:settings.fields.tokenTargetPercentageHint')}
              >
                <Input
                  id="compaction-token-target-percentage"
                  type="number"
                  min={25}
                  max={80}
                  step={1}
                  value={String(readNumber('compaction_token_target_percentage', 60))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      compaction_token_target_percentage: Math.floor(Number(event.target.value) || 0),
                    }))
                  }
                />
              </Field>
              <Field
                label={t('admin:settings.fields.tokenCap')}
                htmlFor="compaction-token-cap"
                hint={t('admin:settings.fields.tokenCapHint')}
              >
                <Input
                  id="compaction-token-cap"
                  type="number"
                  min={0}
                  step={1}
                  value={String(readNumber('compaction_token_cap', 80000))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      compaction_token_cap: Math.max(0, Math.floor(Number(event.target.value) || 0)),
                    }))
                  }
                />
              </Field>

              <Field
                label={t('admin:settings.fields.keep')}
                htmlFor="keep-recent-rounds"
                hint={t('admin:settings.fields.keepHint')}
              >
                <Input
                  id="keep-recent-rounds"
                  type="number"
                  min={1}
                  step={1}
                  value={String(readNumber('keep_recent_rounds', 6))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      keep_recent_rounds: Math.max(1, Math.floor(Number(event.target.value) || 1)),
                    }))
                  }
                />
              </Field>
              <Field
                label={t('admin:settings.fields.retentionPercentage')}
                htmlFor="compaction-retention-percentage"
                hint={t('admin:settings.fields.retentionPercentageHint')}
              >
                <Input
                  id="compaction-retention-percentage"
                  type="number"
                  min={10}
                  max={50}
                  step={1}
                  value={String(readNumber('compaction_retention_percentage', 40))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      compaction_retention_percentage: Math.floor(Number(event.target.value) || 0),
                    }))
                  }
                />
              </Field>

              <Field
                label={t('admin:settings.fields.sumTokens')}
                htmlFor="summary-max-tokens"
                hint={t('admin:settings.fields.sumTokensHint')}
              >
                <Input
                  id="summary-max-tokens"
                  type="number"
                  min={256}
                  step={1}
                  value={String(readNumber('summary_max_tokens', 8192))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      summary_max_tokens: Math.max(256, Math.floor(Number(event.target.value) || 256)),
                    }))
                  }
                />
              </Field>
              <Field
                label={t('admin:settings.fields.summaryTargetPercent')}
                htmlFor="summary-target-percent"
                hint={t('admin:settings.fields.summaryTargetPercentHint')}
              >
                <Input
                  id="summary-target-percent"
                  type="number"
                  min={5}
                  max={80}
                  step={1}
                  value={String(readNumber('summary_target_percent', 30))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      summary_target_percent: Math.floor(Number(event.target.value) || 0),
                    }))
                  }
                />
              </Field>
              <Field
                label={t('admin:settings.fields.summaryMergeTokens')}
                htmlFor="summary-merge-max-tokens"
                hint={t('admin:settings.fields.summaryMergeTokensHint')}
              >
                <Input
                  id="summary-merge-max-tokens"
                  type="number"
                  min={256}
                  step={1}
                  value={String(readNumber('summary_merge_max_tokens', 8192))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      summary_merge_max_tokens: Math.max(256, Math.floor(Number(event.target.value) || 256)),
                    }))
                  }
                />
              </Field>

              <Field
                label={t('admin:settings.fields.compactionRequestTokens')}
                htmlFor="compaction-request-max-tokens"
                hint={t('admin:settings.fields.compactionRequestTokensHint')}
              >
                <Input
                  id="compaction-request-max-tokens"
                  type="number"
                  min={8192}
                  step={1024}
                  value={String(readNumber('compaction_request_max_tokens', 32768))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      compaction_request_max_tokens: Math.max(8192, Math.floor(Number(event.target.value) || 8192)),
                    }))
                  }
                />
              </Field>

              <Field
                label={t('admin:settings.fields.compactionPrompt')}
                htmlFor="context-compaction-prompt"
                hint={t('admin:settings.fields.compactionPromptHint')}
                className="md:col-span-2"
              >
                <Textarea
                  id="context-compaction-prompt"
                  rows={7}
                  maxLength={16384}
                  value={readString('context_compaction_prompt')}
                  onChange={(event) =>
                    setDraft((current) => ({ ...current, context_compaction_prompt: event.target.value }))
                  }
                  placeholder={t('admin:settings.fields.compactionPromptPlaceholder')}
                  className="min-h-[10rem] font-mono text-[12px] leading-relaxed"
                />
              </Field>
            </div>
          )}

          <ToggleRow
            label={t('admin:settings.fields.memoryEnabled')}
            checked={readBool('memory_enabled', true)}
            onChange={(value) => setDraft((current) => ({ ...current, memory_enabled: value }))}
          />

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
