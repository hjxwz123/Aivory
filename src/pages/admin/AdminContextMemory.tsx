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
  'keep_recent_rounds',
  'compaction_token_trigger',
  'summary_max_tokens',
  'compaction_enabled',
  'memory_enabled',
] as const

export default function AdminContextMemory() {
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

  const compactionEnabled = readBool('compaction_enabled', true)

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">
          {t('admin:menu.contextMemory', { defaultValue: 'Context and memory' })}
        </h1>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          <ToggleRow
            label={t('admin:settings.fields.compactionEnabled')}
            checked={compactionEnabled}
            onChange={(value) => setDraft((current) => ({ ...current, compaction_enabled: value }))}
          />

          {compactionEnabled && (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <Field label={t('admin:settings.fields.keep')} htmlFor="keep-recent-rounds">
                <Input
                  id="keep-recent-rounds"
                  type="number"
                  min={0}
                  value={String(readNumber('keep_recent_rounds', 6))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      keep_recent_rounds: Math.max(0, Number(event.target.value) || 0),
                    }))
                  }
                />
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
                  value={String(readNumber('compaction_token_trigger', 32000))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      compaction_token_trigger: Math.max(0, Number(event.target.value) || 0),
                    }))
                  }
                />
              </Field>
              <Field label={t('admin:settings.fields.sumTokens')} htmlFor="summary-max-tokens">
                <Input
                  id="summary-max-tokens"
                  type="number"
                  min={0}
                  value={String(readNumber('summary_max_tokens', 8192))}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      summary_max_tokens: Math.max(0, Number(event.target.value) || 0),
                    }))
                  }
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
