/**
 * Personalization — response style (GPT-like tone traits + custom instructions
 * + nickname) and memory management. The style is persisted to per-user
 * settings (persona_*) and injected into the system prompt by the orchestrator;
 * the memory toggle gates both injection and extraction server-side.
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/components/ui/label'
import { toast } from '@/hooks/use-toast'
import { useSettings } from '@/store/settings'
import { useComposerPrefs } from '@/store/composer-prefs'
import { useAuth } from '@/store/auth'
import { MemoryManager } from '@/components/settings/memory-manager'
import { cn } from '@/lib/utils'
import {
  modelAllowsToolModeSelection,
  normalizeToolModeForCapabilities,
  resolveModelToolModeCapabilities,
  type ToolMode,
  type ToolModeCapabilities,
  visibleToolModes,
} from '@/lib/tool-mode'
import { modelHasBuiltinTools } from '@/lib/builtin-tools'
import { useModels } from '@/store/models'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Ban, Check, ChevronDown, Loader2, Sparkles, Wrench } from 'lucide-react'
import { persistUserSettings } from '@/lib/user-settings'
import { userCan } from '@/lib/user-permissions'

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
  const initialSettings = useRef(useAuth.getState().user?.settings ?? {}).current
  const memoriesEnabled = useSettings((s) => s.privacy.memoriesEnabled)
  const setPrivacy = useSettings((s) => s.setPrivacy)
  // Global admin master switch: when memory is turned off platform-wide, hide the
  // per-user toggle entirely (no one can enable it; it's gated off server-side too).
  // Absent flag (older backend) ⇒ treat as available.
  const user = useAuth((s) => s.user)
  const memoryAvailable = user?.memory_available !== false && userCan(user, 'allow_memory')
  const defaultToolMode = useComposerPrefs((s) => s.defaultToolMode)
  const setDefaultToolMode = useComposerPrefs((s) => s.setDefaultToolMode)
  const setToolMode = useComposerPrefs((s) => s.setToolMode)
  const defaultModelId = useModels((s) => s.defaultId)
  const defaultModel = useModels((s) => s.getById(defaultModelId))
  const loadModels = useModels((s) => s.load)
  const modelsLoaded = useModels((s) => s.loaded)
  const defaultToolModeCapabilities = useMemo<ToolModeCapabilities>(
    () =>
      resolveModelToolModeCapabilities(defaultModel?.tool_mode, {
        available:
          defaultModel?.tools_available ??
          (modelHasBuiltinTools(defaultModel) ||
            (Array.isArray(defaultModel?.official_tools) && defaultModel.official_tools.length > 0)),
      }),
    [defaultModel],
  )
  const defaultToolModeSelectionAllowed = Boolean(
    defaultModel &&
      defaultToolModeCapabilities.available &&
      modelAllowsToolModeSelection(defaultModel.tool_mode),
  )
  const visibleDefaultToolModes = useMemo(
    () => visibleToolModes(defaultToolModeCapabilities),
    [defaultToolModeCapabilities],
  )
  const availableDefaultToolMode =
    modelsLoaded && defaultModel
      ? normalizeToolModeForCapabilities(defaultToolMode, defaultToolModeCapabilities)
      : defaultToolMode

  const [traits, setTraits] = useState<string[]>(() =>
    Array.isArray(initialSettings.persona_traits) ? (initialSettings.persona_traits as string[]) : [],
  )
  const [nickname, setNickname] = useState(() =>
    typeof initialSettings.persona_nickname === 'string' ? initialSettings.persona_nickname : '',
  )
  const [custom, setCustom] = useState(() =>
    typeof initialSettings.persona_custom === 'string' ? initialSettings.persona_custom : '',
  )
  const loaded = true
  const [saving, setSaving] = useState(false)
  const [toolsSaving, setToolsSaving] = useState(false)
  const [toolMenuOpen, setToolMenuOpen] = useState(false)
  const toolSettingsQueueRef = useRef<Promise<unknown>>(Promise.resolve())

  function queueToolSettingsSave(patch: Record<string, unknown>) {
    const request = toolSettingsQueueRef.current
      .catch(() => undefined)
      .then(() => persistUserSettings(patch))
    toolSettingsQueueRef.current = request
    return request
  }

  // AuthGate already hydrated the account settings before this dialog can open;
  // only the model registry may still be outstanding.
  useEffect(() => {
    if (!modelsLoaded) void loadModels()
  }, [loadModels, modelsLoaded])

  // Account defaults can outlive an administrator changing the default model's
  // capabilities. Wait for the registry before falling back so initial loading
  // never overwrites a valid unified preference with automatic.
  useEffect(() => {
    if (!modelsLoaded || !defaultModel || availableDefaultToolMode === defaultToolMode) return
    setDefaultToolMode(availableDefaultToolMode)
    setToolMode(availableDefaultToolMode)
  }, [
    availableDefaultToolMode,
    defaultModel,
    defaultToolMode,
    modelsLoaded,
    setDefaultToolMode,
    setToolMode,
  ])


  function toggleTrait(key: string) {
    setTraits((prev) => (prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]))
  }

  async function savePersona() {
    setSaving(true)
    try {
      await persistUserSettings({
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
      await persistUserSettings({ memory_enabled: v })
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
      await queueToolSettingsSave({ tool_mode_default: next })
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
        <h1 className="text-xl font-semibold tracking-normal text-[var(--color-fg)]">
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

      {/* Default tool mode. Models configured with tool_mode=none expose no
          user-facing tool policy at all. */}
      {defaultToolModeSelectionAllowed ? (
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
            <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between sm:gap-6">
              <div className="min-w-0">
                <div id="tool-mode-label" className="text-sm font-medium text-[var(--color-fg)]">
                  {t('settings:personalization.toolsDefaultLabel')}
                </div>
                <p id="tool-mode-description" className="mt-1 max-w-xl text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('settings:personalization.toolsDefaultBody')}
                </p>
              </div>
              <DropdownMenu open={toolMenuOpen} onOpenChange={setToolMenuOpen}>
                <DropdownMenuTrigger
                  disabled={!loaded}
                  aria-labelledby="tool-mode-label"
                  aria-describedby="tool-mode-description"
                  className={cn(
                    'inline-flex h-10 w-full shrink-0 items-center gap-2 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3.5 text-sm text-[var(--color-fg)] sm:w-64',
                    'hover:border-[var(--color-border-strong)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    !loaded && 'cursor-not-allowed opacity-50',
                  )}
                >
                  {availableDefaultToolMode === 'enabled' ? (
                    <Wrench size={15} className="shrink-0 text-[var(--color-tool-selection-text)]" aria-hidden />
                  ) : availableDefaultToolMode === 'auto' ? (
                    <Sparkles size={15} className="shrink-0 text-[var(--color-tool-selection-text)]" aria-hidden />
                  ) : availableDefaultToolMode === 'disabled' ? (
                    <Ban size={15} className="shrink-0 text-[var(--color-tool-selection-text)]" aria-hidden />
                  ) : null}
                  <span className="min-w-0 flex-1 truncate text-left">
                    {t(`settings:personalization.toolModes.${availableDefaultToolMode}.label`)}
                  </span>
                  {toolsSaving ? (
                    <Loader2 size={14} className="shrink-0 animate-spin text-[var(--color-fg-muted)]" aria-hidden />
                  ) : (
                    <ChevronDown size={14} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden />
                  )}
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="w-[min(20rem,calc(100vw-2rem))]">
                  {visibleDefaultToolModes.map((mode) => {
                    const selected = availableDefaultToolMode === mode
                    const icon =
                      mode === 'enabled' ? (
                        <Wrench size={16} aria-hidden />
                      ) : mode === 'auto' ? (
                        <Sparkles size={16} aria-hidden />
                      ) : (
                        <Ban size={16} aria-hidden />
                      )
                    const label = t(`settings:personalization.toolModes.${mode}.label`)
                    const body = t(`settings:personalization.toolModes.${mode}.body`)

                    return (
                      <DropdownMenuItem
                        key={mode}
                        disabled={!loaded || toolsSaving}
                        onSelect={() => void onSelectToolMode(mode)}
                        className={cn(
                          'items-start py-2.5',
                          selected &&
                            'bg-[var(--color-tool-selection-soft)] data-[highlighted]:bg-[var(--color-tool-selection-soft)]',
                        )}
                      >
                        <span
                          className={cn(
                            'mt-0.5 shrink-0',
                            selected
                              ? 'text-[var(--color-tool-selection-text)]'
                              : 'text-[var(--color-fg-muted)]',
                          )}
                        >
                          {icon}
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block text-[13px] font-medium">{label}</span>
                          <span className="mt-0.5 block text-[11.5px] leading-snug text-[var(--color-fg-subtle)]">
                            {body}
                          </span>
                        </span>
                        {selected ? (
                          <Check size={14} className="mt-1 shrink-0 text-[var(--color-tool-selection-text)]" aria-hidden />
                        ) : null}
                      </DropdownMenuItem>
                    )
                  })}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>
        </section>
      ) : null}

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
