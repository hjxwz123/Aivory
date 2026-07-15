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
import { useComposerPrefs } from '@/store/composer-prefs'
import { useAuth } from '@/store/auth'
import { MemoryManager } from '@/components/settings/memory-manager'
import { cn } from '@/lib/utils'
import { resolveDefaultToolMode, type ToolMode } from '@/lib/tool-mode'

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

const TOOL_MODES: ToolMode[] = ['auto', 'disabled', 'enabled']

export default function Personalization() {
  const { t } = useTranslation(['settings', 'memory', 'common'])
  const memoriesEnabled = useSettings((s) => s.privacy.memoriesEnabled)
  const setPrivacy = useSettings((s) => s.setPrivacy)
  // Global admin master switch: when memory is turned off platform-wide, hide the
  // per-user toggle entirely (no one can enable it; it's gated off server-side too).
  // Absent flag (older backend) ⇒ treat as available.
  const memoryAvailable = useAuth((s) => s.user?.memory_available !== false)
  const defaultToolMode = useComposerPrefs((s) => s.defaultToolMode)
  const setDefaultToolMode = useComposerPrefs((s) => s.setDefaultToolMode)
  const setToolMode = useComposerPrefs((s) => s.setToolMode)

  const [traits, setTraits] = useState<string[]>([])
  const [nickname, setNickname] = useState('')
  const [custom, setCustom] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)
  const [toolsSaving, setToolsSaving] = useState(false)

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
        setDefaultToolMode(resolveDefaultToolMode(s))
        setLoaded(true)
      })
      .catch(() => setLoaded(true))
    return () => {
      active = false
    }
  }, [setPrivacy, setDefaultToolMode])

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

  async function onSelectToolMode(next: ToolMode) {
    if (toolsSaving || next === defaultToolMode) return
    const previous = useComposerPrefs.getState()
    const previousDefault = previous.defaultToolMode
    const previousCurrent = previous.toolMode
    const previousMode = previous.mode
    const previousForceWebSearch = previous.forceWebSearch
    setDefaultToolMode(next)
    setToolMode(next)
    setToolsSaving(true)
    try {
      await authApi.updateSettings({ tool_mode_default: next })
    } catch (e) {
      setDefaultToolMode(previousDefault)
      setToolMode(previousCurrent)
      // setToolMode intentionally clears dependent flags. Restore the complete
      // pre-request snapshot so a failed settings PATCH cannot silently turn off
      // Deep Research, Canvas, or forced non-tool web search.
      useComposerPrefs.getState().setMode(previousMode)
      if (previousForceWebSearch) useComposerPrefs.getState().setForceWebSearch(true)
      toast.error(t('common:actions.failed', { defaultValue: 'Failed to save' }), e instanceof Error ? e.message : undefined)
    } finally {
      setToolsSaving(false)
    }
  }

  return (
    <div className="mx-auto max-w-[60rem]">
      <header className="mb-8">
        <h1 className="tracking-tight text-3xl text-[var(--color-fg)]">
          {t('settings:personalization.title')}
        </h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">{t('settings:personalization.subtitle')}</p>
      </header>

      {/* Response style */}
      <section className="mb-12">
        <div className="mb-5">
          <h2 className="tracking-tight text-xl text-[var(--color-fg)]">
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

      {/* Default tool mode. */}
      <section className="mb-12">
        <div className="mb-5">
          <h2 className="tracking-tight text-xl text-[var(--color-fg)]">
            {t('settings:personalization.toolsTitle', { defaultValue: 'Tools' })}
          </h2>
          <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">
            {t('settings:personalization.toolsSubtitle', {
              defaultValue: 'Control whether the model may call tools (web search, Python, image generation, knowledge base).',
            })}
          </p>
        </div>
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 sm:p-6">
          <div id="tool-mode-label" className="text-sm font-medium text-[var(--color-fg)]">
            {t('settings:personalization.toolsDefaultLabel')}
          </div>
          <p id="tool-mode-description" className="mt-1 text-xs leading-relaxed text-[var(--color-fg-muted)]">
            {t('settings:personalization.toolsDefaultBody')}
          </p>
          <div
            role="radiogroup"
            aria-labelledby="tool-mode-label"
            aria-describedby="tool-mode-description tool-mode-selected-description"
            className="mt-4 grid grid-cols-3 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-1"
          >
            {TOOL_MODES.map((mode) => {
              const selected = defaultToolMode === mode
              return (
                <label
                  key={mode}
                  className={cn(
                    'flex min-h-11 min-w-0 items-center justify-center rounded-md px-2 py-2 text-center interactive',
                    'has-[:focus-visible]:outline-none has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-[var(--color-ring)]',
                    !loaded || toolsSaving ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
                    selected
                      ? 'bg-[var(--color-surface)] text-[var(--color-accent)] shadow-[var(--shadow-xs)]'
                      : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)]',
                  )}
                >
                  <input
                    type="radio"
                    name="default-tool-mode"
                    value={mode}
                    checked={selected}
                    disabled={!loaded || toolsSaving}
                    onChange={() => void onSelectToolMode(mode)}
                    className="sr-only"
                  />
                  <span className="min-w-0 break-words text-xs font-medium leading-tight sm:text-sm">
                    {t(`settings:personalization.toolModes.${mode}.label`)}
                  </span>
                </label>
              )
            })}
          </div>
          <p
            id="tool-mode-selected-description"
            aria-live="polite"
            className="mt-3 min-h-10 text-xs leading-relaxed text-[var(--color-fg-muted)]"
          >
            {t(`settings:personalization.toolModes.${defaultToolMode}.body`)}
          </p>
        </div>
      </section>

      {/* Memory — only shown when the global admin master switch allows it. */}
      {memoryAvailable && (
        <section className="mb-12">
          <div className="mb-5">
            <h2 className="tracking-tight text-xl text-[var(--color-fg)]">
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
      )}
    </div>
  )
}
