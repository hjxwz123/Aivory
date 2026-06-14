/**
 * Personalization — response style (GPT-like tone traits + custom instructions
 * + nickname) and memory management. The style is persisted to per-user
 * settings (persona_*) and injected into the system prompt by the orchestrator;
 * the memory toggle gates both injection and extraction server-side.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/components/ui/label'
import { toast } from '@/hooks/use-toast'
import { authApi } from '@/api'
import { useSettings } from '@/store/settings'
import { FONT_PRESETS, type FontPref } from '@/types/settings'
import { MemoryManager } from '@/components/settings/memory-manager'
import { cn } from '@/lib/utils'

// CSS family per preset so each card previews in its own typeface, regardless of
// the font currently applied to the document.
const FONT_PREVIEW: Record<FontPref, string> = {
  default: "'Geist Variable', 'Geist', ui-sans-serif, sans-serif",
  inter: "'Inter Variable', 'Inter', ui-sans-serif, sans-serif",
  system: 'ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, sans-serif',
  serif: "'Fraunces Variable', 'Fraunces', ui-serif, Georgia, serif",
}

// Trait keys MUST match personaTraitPhrases on the backend (orchestrator.go).
const TRAITS = [
  'concise',
  'detailed',
  'friendly',
  'professional',
  'encouraging',
  'direct',
  'witty',
  'socratic',
  'genz',
  'formal',
] as const

export default function Personalization() {
  const { t } = useTranslation(['settings', 'memory', 'common'])
  const memoriesEnabled = useSettings((s) => s.privacy.memoriesEnabled)
  const setPrivacy = useSettings((s) => s.setPrivacy)
  const font = useSettings((s) => s.appearance.font)
  const setAppearance = useSettings((s) => s.setAppearance)

  // Eager-load Inter so its preview card renders in the real face even before
  // it's selected (it's otherwise lazy-loaded on selection).
  useEffect(() => {
    void import('@fontsource-variable/inter')
  }, [])

  const [traits, setTraits] = useState<string[]>([])
  const [nickname, setNickname] = useState('')
  const [custom, setCustom] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)

  // Load the server-side persona + memory flag (the source of truth).
  useEffect(() => {
    let active = true
    authApi
      .getSettings()
      .then((s) => {
        if (!active) return
        setTraits(Array.isArray(s.persona_traits) ? (s.persona_traits as string[]) : [])
        setNickname(typeof s.persona_nickname === 'string' ? s.persona_nickname : '')
        setCustom(typeof s.persona_custom === 'string' ? s.persona_custom : '')
        if (typeof s.memory_enabled === 'boolean') setPrivacy({ memoriesEnabled: s.memory_enabled })
        setLoaded(true)
      })
      .catch(() => setLoaded(true))
    return () => {
      active = false
    }
  }, [setPrivacy])

  function toggleTrait(key: string) {
    setTraits((prev) => (prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]))
  }

  async function savePersona() {
    setSaving(true)
    try {
      await authApi.updateSettings({
        persona_traits: traits,
        persona_nickname: nickname.trim(),
        persona_custom: custom.trim(),
      })
      toast.success(t('settings:personalization.saved'))
    } catch (e) {
      toast.error(t('common:actions.failed', { defaultValue: 'Failed to save' }), e instanceof Error ? e.message : undefined)
    } finally {
      setSaving(false)
    }
  }

  async function onToggleMemory(v: boolean) {
    setPrivacy({ memoriesEnabled: v })
    try {
      await authApi.updateSettings({ memory_enabled: v })
    } catch (e) {
      setPrivacy({ memoriesEnabled: !v })
      toast.error(t('common:actions.failed', { defaultValue: 'Failed to save' }), e instanceof Error ? e.message : undefined)
    }
  }

  return (
    <div className="max-w-[44rem]">
      <header className="mb-8">
        <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)]">
          {t('settings:personalization.title')}
        </h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">{t('settings:personalization.subtitle')}</p>
      </header>

      {/* Response style */}
      <section className="mb-12">
        <div className="mb-5">
          <h2 className="font-serif tracking-tight text-xl text-[var(--color-fg)]">
            {t('settings:personalization.styleTitle')}
          </h2>
          <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('settings:personalization.styleSubtitle')}</p>
        </div>
        <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5 sm:p-6 space-y-6">
          <div>
            <span className="text-sm font-medium text-[var(--color-fg)]">
              {t('settings:personalization.traitsLabel')}
            </span>
            <div className="mt-3 flex flex-wrap gap-2">
              {TRAITS.map((key) => {
                const on = traits.includes(key)
                return (
                  <button
                    key={key}
                    type="button"
                    aria-pressed={on}
                    onClick={() => toggleTrait(key)}
                    className={cn(
                      'rounded-full border px-3 py-1.5 text-[13px] interactive',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      on
                        ? 'border-[var(--color-accent)] bg-[var(--color-accent-soft)] text-[var(--color-accent)]'
                        : 'border-[var(--color-border)] text-[var(--color-fg-muted)] hover:border-[var(--color-border-strong)] hover:text-[var(--color-fg)]',
                    )}
                  >
                    {t(`settings:personalization.traits.${key}`)}
                  </button>
                )
              })}
            </div>
          </div>

          <Field label={t('settings:personalization.nicknameLabel')} htmlFor="p-nick">
            <Input
              id="p-nick"
              value={nickname}
              onChange={(e) => setNickname(e.target.value)}
              placeholder={t('settings:personalization.nicknamePlaceholder')}
              maxLength={48}
            />
          </Field>

          <Field label={t('settings:personalization.customLabel')} htmlFor="p-custom">
            <Textarea
              id="p-custom"
              rows={4}
              value={custom}
              onChange={(e) => setCustom(e.target.value)}
              placeholder={t('settings:personalization.customPlaceholder')}
              maxLength={1500}
            />
          </Field>

          <div className="flex justify-end">
            <Button loading={saving} disabled={!loaded} onClick={() => void savePersona()}>
              {t('common:actions.save')}
            </Button>
          </div>
        </div>
      </section>

      {/* Typeface */}
      <section className="mb-12">
        <div className="mb-5">
          <h2 className="font-serif tracking-tight text-xl text-[var(--color-fg)]">
            {t('settings:personalization.font.title')}
          </h2>
          <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">
            {t('settings:personalization.font.subtitle')}
          </p>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          {FONT_PRESETS.map((opt) => {
            const selected = font === opt
            return (
              <button
                key={opt}
                type="button"
                aria-pressed={selected}
                onClick={() => setAppearance({ font: opt })}
                style={{ fontFamily: FONT_PREVIEW[opt] }}
                className={cn(
                  'flex flex-col items-start gap-1 rounded-[14px] border px-4 py-3.5 text-left interactive',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  selected
                    ? 'border-[var(--color-accent)] bg-[var(--color-accent-soft)]'
                    : 'border-[var(--color-border)] bg-[var(--color-surface)] hover:border-[var(--color-border-strong)]',
                )}
              >
                <span className="text-[15px] font-medium text-[var(--color-fg)]">
                  {t(`settings:personalization.font.options.${opt}`)}
                </span>
                <span className="text-[22px] leading-tight text-[var(--color-fg-muted)]">Ag 字 0123</span>
              </button>
            )
          })}
        </div>
      </section>

      {/* Memory */}
      <section className="mb-12">
        <div className="mb-5">
          <h2 className="font-serif tracking-tight text-xl text-[var(--color-fg)]">
            {t('settings:personalization.memoryTitle')}
          </h2>
          <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('settings:personalization.memorySubtitle')}</p>
        </div>
        <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)]">
          <div className="px-5 sm:px-6 py-4 sm:py-5 flex items-center justify-between gap-6 border-b border-[var(--color-divider)]">
            <div className="min-w-0">
              <div className="text-sm font-medium text-[var(--color-fg)]">
                {t('settings:personalization.memoryToggle')}
              </div>
              <p className="mt-1 text-xs text-[var(--color-fg-muted)] leading-relaxed max-w-md">
                {t('settings:personalization.memoryToggleBody')}
              </p>
            </div>
            <Switch checked={memoriesEnabled} onCheckedChange={(v) => void onToggleMemory(Boolean(v))} />
          </div>
          <div className="px-5 sm:px-6 py-5">
            {memoriesEnabled ? (
              <MemoryManager />
            ) : (
              <p className="text-sm text-[var(--color-fg-subtle)]">{t('settings:personalization.memoryDisabled')}</p>
            )}
          </div>
        </div>
      </section>
    </div>
  )
}
