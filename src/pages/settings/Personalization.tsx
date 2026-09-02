/**
 * Personalization — response style (GPT-like tone traits + custom instructions
 * + nickname) and memory management. The style is persisted to per-user
 * settings (persona_*) and injected into the system prompt by the orchestrator;
 * the memory toggle gates both injection and extraction server-side.
 */
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Switch } from '@/components/ui/switch'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/components/ui/label'
import { toast } from '@/hooks/use-toast'
import { useSettings } from '@/store/settings'
import { useAuth } from '@/store/auth'
import { MemoryManager } from '@/components/settings/memory-manager'
import { cn } from '@/lib/utils'
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

  return (
    <div className="mx-auto max-w-[60rem]">
      <header className="mb-6">
        <h1 className="text-xl font-semibold tracking-normal text-[var(--color-fg)]">
          {t('settings:personalization.title')}
        </h1>
        <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('settings:personalization.subtitle')}</p>
      </header>

      {/* Response style */}
      <section className="mb-8 last:mb-0">
        <div className="mb-3">
          <h2 className="text-lg font-medium tracking-normal text-[var(--color-fg)]">
            {t('settings:personalization.styleTitle')}
          </h2>
          <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('settings:personalization.styleSubtitle')}</p>
        </div>
        <div className="space-y-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
          <div>
            <span className="text-sm font-medium text-[var(--color-fg)]">
              {t('settings:personalization.traitsLabel')}
            </span>
            <div className="mt-2 flex flex-wrap gap-2">
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

      {/* Memory — only shown when the global admin master switch allows it. */}
      {memoryAvailable && (
        <section className="mb-8 last:mb-0">
          <div className="mb-3">
            <h2 className="text-lg font-medium tracking-normal text-[var(--color-fg)]">
              {t('settings:personalization.memoryTitle')}
            </h2>
            <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{t('settings:personalization.memorySubtitle')}</p>
          </div>
          <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            <div className="flex items-center justify-between gap-4 border-b border-[var(--color-divider)] px-4 py-3">
              <div className="min-w-0">
                <div className="text-sm font-medium text-[var(--color-fg)]">
                  {t('settings:personalization.memoryToggle')}
                </div>
                <p className="mt-0.5 max-w-md text-xs leading-normal text-[var(--color-fg-muted)]">
                  {t('settings:personalization.memoryToggleBody')}
                </p>
              </div>
              <Switch checked={memoriesEnabled} onCheckedChange={(v) => void onToggleMemory(Boolean(v))} />
            </div>
            <div className="p-4">
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
