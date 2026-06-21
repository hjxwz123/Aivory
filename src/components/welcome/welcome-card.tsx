import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Pencil, BookOpen, ShieldCheck, Check } from 'lucide-react'
import { authApi } from '@/api'
import { useAuth } from '@/store/auth'
import { useLanguage } from '@/store/language'
import { useAccent } from '@/store/accent'
import { useTheme } from '@/store/theme'
import { useSettings } from '@/store/settings'
import { SUPPORTED_LANGUAGES, type LanguageCode } from '@/i18n'
import { ACCENT_PRESETS, type AccentPref, type ChatWidthPref, type ThemePref } from '@/types/settings'
import { Logo } from '@/components/brand/logo'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { cn } from '@/lib/utils'

type ReplyStyle = 'concise' | 'balanced' | 'detailed'
type StepKey = 'language' | 'theme' | 'accent' | 'layout' | 'style' | 'memory'
const STEPS: StepKey[] = ['language', 'theme', 'accent', 'layout', 'style', 'memory']
const CHAT_WIDTH_OPTS: ChatWidthPref[] = ['comfortable', 'full']

// Static hue per accent preset, mirrored from the Appearance picker so the
// swatches read as the same colors regardless of the document's data-accent.
const ACCENT_PREVIEW: Record<AccentPref, string> = {
  violet: 'oklch(58% 0.225 290)',
  lagoon: 'oklch(58% 0.130 200)',
  ember: 'oklch(62% 0.170 40)',
  moss: 'oklch(54% 0.125 145)',
  indigo: 'oklch(54% 0.180 260)',
  rose: 'oklch(60% 0.180 5)',
  mono: 'linear-gradient(135deg, oklch(26% 0 0) 0 50%, oklch(96% 0 0) 50% 100%)',
}
const THEME_OPTS: ThemePref[] = ['light', 'dark', 'system']
const STYLE_OPTS: ReplyStyle[] = ['concise', 'balanced', 'detailed']

/**
 * First-login welcome — a small wizard (one choice per page). Shows once per
 * account (gated on a server-side `onboarded` flag in user settings). The left
 * panel is an editorial intro with a slow accent-tinted aurora that re-tints
 * live as the user picks an accent; the right steps through language → theme →
 * accent → reply style → memory. Choices apply live for instant preview; "Skip"
 * restores whatever was set before so it's truly non-committal, while the final
 * "Get started" persists. Either path marks the account onboarded.
 */
export function WelcomeCard() {
  const { t } = useTranslation(['welcome', 'settings', 'common'])
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const setUser = useAuth((s) => s.setUser)

  const lang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const accent = useAccent((s) => s.accent)
  const setAccent = useAccent((s) => s.setAccent)
  const themePref = useTheme((s) => s.pref)
  const setPref = useTheme((s) => s.setPref)
  const replyStyle = useSettings((s) => s.models.responseLength)
  const setModels = useSettings((s) => s.setModels)
  const memory = useSettings((s) => s.privacy.memoriesEnabled)
  const setPrivacy = useSettings((s) => s.setPrivacy)
  const chatWidth = useSettings((s) => s.appearance.chatWidth)
  const setAppearance = useSettings((s) => s.setAppearance)

  const onboarded = Boolean((user?.settings as Record<string, unknown> | undefined)?.onboarded)
  // OAuth accounts must set a password first (SetPasswordGate); hold the wizard
  // back until they have, so the two mandatory dialogs don't stack.
  const needsPassword = user?.has_password === false
  const eligible = status === 'authenticated' && Boolean(user) && !onboarded && !needsPassword

  const [mounted, setMounted] = useState(false)
  const [open, setOpen] = useState(false)
  // After "Get started" the wizard hands off to a short celebratory welcome
  // dialog before the user lands in the app.
  const [welcomeOpen, setWelcomeOpen] = useState(false)
  const [step, setStep] = useState(0)
  const [saving, setSaving] = useState(false)
  const initial = useRef<{
    lang: LanguageCode
    accent: AccentPref
    theme: ThemePref
    chatWidth: ChatWidthPref
    replyStyle: ReplyStyle
    memory: boolean
  } | null>(null)

  // Open once when first eligible, snapshotting the current prefs so Skip can
  // undo any live previews the user made. `mounted` keeps the dialog in the tree
  // through its exit animation even after `open` flips false.
  useEffect(() => {
    if (eligible && !mounted && initial.current === null) {
      initial.current = { lang, accent, theme: themePref, chatWidth, replyStyle, memory }
      setMounted(true)
      setOpen(true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [eligible])

  if (!mounted) return null

  const last = STEPS.length - 1
  const current = STEPS[step]

  function markOnboarded(extra: Record<string, unknown>) {
    const patch = { onboarded: true, ...extra }
    if (user) setUser({ ...user, settings: { ...(user.settings ?? {}), ...patch } })
    void authApi.updateSettings(patch).catch(() => {
      /* best-effort — if it fails the card simply reappears next session */
    })
  }

  // Close with the exit animation: flip `open` (Radix plays the zoom-into-app
  // close), then unmount after it finishes.
  function close() {
    setOpen(false)
    window.setTimeout(() => setMounted(false), 360)
  }

  function handleStart() {
    if (!open) return
    setSaving(true)
    // Memory is the only choice with a server-side mirror; the rest are
    // localStorage-backed and already applied live.
    markOnboarded({ memory_enabled: memory })
    setSaving(false)
    // Close the wizard (plays the zoom-out), then hand off to the welcome
    // dialog once the exit animation finishes.
    setOpen(false)
    window.setTimeout(() => setWelcomeOpen(true), 360)
  }

  // Dismiss the welcome dialog and unmount the whole flow.
  function finishWelcome() {
    setWelcomeOpen(false)
    window.setTimeout(() => setMounted(false), 200)
  }

  function handleSkip() {
    if (!open) return
    const init = initial.current
    if (init) {
      if (lang !== init.lang) setLang(init.lang)
      if (accent !== init.accent) setAccent(init.accent)
      if (themePref !== init.theme) setPref(init.theme)
      if (chatWidth !== init.chatWidth) setAppearance({ chatWidth: init.chatWidth })
      if (replyStyle !== init.replyStyle) setModels({ responseLength: init.replyStyle })
      if (memory !== init.memory) setPrivacy({ memoriesEnabled: init.memory })
    }
    markOnboarded({})
    close()
  }

  function renderControl(key: StepKey) {
    switch (key) {
      case 'language':
        return (
          <Select value={lang} onValueChange={(v) => setLang(v as LanguageCode)}>
            <SelectTrigger className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SUPPORTED_LANGUAGES.map((l) => (
                <SelectItem key={l.code} value={l.code}>
                  {l.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )
      case 'theme':
        return (
          <div className="flex items-center gap-2">
            {THEME_OPTS.map((opt) => (
              <Seg key={opt} active={themePref === opt} onClick={() => setPref(opt)}>
                {t(`welcome:theme.${opt}`)}
              </Seg>
            ))}
          </div>
        )
      case 'accent':
        return (
          <div className="flex items-center gap-3 flex-wrap">
            {ACCENT_PRESETS.map((p) => (
              <button
                key={p}
                type="button"
                aria-label={t(`settings:appearance.accent.${p}`)}
                aria-pressed={accent === p}
                onClick={() => setAccent(p)}
                className={cn(
                  'relative size-9 rounded-full transition-transform interactive',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  accent === p
                    ? 'ring-2 ring-offset-2 ring-offset-[var(--color-surface)] ring-[var(--color-fg)] scale-105'
                    : 'hover:scale-105',
                )}
                style={{ background: ACCENT_PREVIEW[p] }}
              >
                {accent === p ? (
                  <Check size={16} aria-hidden className="absolute inset-0 m-auto text-white" />
                ) : null}
              </button>
            ))}
          </div>
        )
      case 'layout':
        return (
          <div className="flex items-center gap-2">
            {CHAT_WIDTH_OPTS.map((opt) => (
              <Seg key={opt} active={chatWidth === opt} onClick={() => setAppearance({ chatWidth: opt })}>
                {t(`settings:appearance.chatWidth.${opt}`)}
              </Seg>
            ))}
          </div>
        )
      case 'style':
        return (
          <div className="flex items-center gap-2">
            {STYLE_OPTS.map((opt) => (
              <Seg key={opt} active={replyStyle === opt} onClick={() => setModels({ responseLength: opt })}>
                {t(`welcome:chatStyle.${opt}`)}
              </Seg>
            ))}
          </div>
        )
      case 'memory':
        return (
          <label className="flex items-center justify-between gap-4 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-4 py-3.5">
            <span className="text-sm text-[var(--color-fg)]">{t('welcome:fields.memory')}</span>
            <Switch checked={memory} onCheckedChange={(v) => setPrivacy({ memoriesEnabled: v })} />
          </label>
        )
    }
  }

  return (
    <>
    <Dialog open={open} onOpenChange={(o) => { if (!o) handleSkip() }}>
      <DialogContent
        size="xl"
        showClose={false}
        className="p-0 overflow-hidden data-[state=closed]:animate-[welcome-exit_340ms_var(--ease-in)]"
      >
        {/* a11y: one stable title/description for the dialog as a whole. */}
        <DialogTitle className="sr-only">{t('welcome:config.title')}</DialogTitle>
        <DialogDescription className="sr-only">{t('welcome:config.subtitle')}</DialogDescription>

        <div className="flex flex-col md:flex-row min-h-0 flex-1">
          {/* Editorial intro with a live, accent-tinted aurora. */}
          <aside className="relative hidden md:flex md:w-[42%] flex-col justify-between gap-10 overflow-hidden p-8 bg-[var(--color-bg-muted)] border-r border-[var(--color-divider)]">
            <div aria-hidden className="pointer-events-none absolute inset-0">
              <div
                className="absolute -inset-[25%] blur-2xl opacity-[0.16] animate-[welcome-aurora_20s_var(--ease-out)_infinite]"
                style={{
                  background:
                    'radial-gradient(38% 38% at 28% 30%, var(--color-accent), transparent 72%), radial-gradient(42% 42% at 72% 70%, var(--color-accent), transparent 72%)',
                }}
              />
            </div>

            <div className="relative z-10">
              <Logo size="lg" />
            </div>
            <div className="relative z-10">
              <div className="text-[12px] uppercase tracking-[0.1em] text-[var(--color-fg-subtle)]">
                {t('welcome:intro.eyebrow')}
              </div>
              <h2 className="mt-3 font-serif text-[2rem] leading-[1.1] tracking-[-0.01em] text-[var(--color-fg)]">
                {t('welcome:intro.title')}
              </h2>
              <p className="mt-3 text-sm leading-relaxed text-[var(--color-fg-muted)]">
                {t('welcome:intro.subtitle')}
              </p>
            </div>
            <ul className="relative z-10 flex flex-col gap-3.5">
              {[
                { icon: Pencil, key: 'write' },
                { icon: BookOpen, key: 'research' },
                { icon: ShieldCheck, key: 'own' },
              ].map(({ icon: Icon, key }) => (
                <li key={key} className="flex items-center gap-3 text-sm text-[var(--color-fg)]">
                  <span className="shrink-0 inline-flex items-center justify-center size-8 rounded-[9px] bg-[var(--color-surface)] border border-[var(--color-border)] text-[var(--color-secondary)]">
                    <Icon size={15} aria-hidden />
                  </span>
                  {t(`welcome:intro.points.${key}`)}
                </li>
              ))}
            </ul>
          </aside>

          {/* Step panel */}
          <div className="flex-1 min-w-0 flex flex-col min-h-0">
            <div className="flex-1 overflow-y-auto px-6 sm:px-8 pt-7 pb-2">
              {/* Progress */}
              <div className="flex items-center gap-1.5">
                {STEPS.map((_, i) => (
                  <span
                    key={i}
                    className={cn(
                      'h-1.5 rounded-full transition-all duration-300',
                      i === step
                        ? 'w-6 bg-[var(--color-accent)]'
                        : i < step
                          ? 'w-1.5 bg-[var(--color-fg-muted)]'
                          : 'w-1.5 bg-[var(--color-border)]',
                    )}
                  />
                ))}
                <span className="ml-auto text-[12px] tabular-nums text-[var(--color-fg-subtle)]">
                  {step + 1} / {STEPS.length}
                </span>
              </div>

              {/* Step content — re-keyed so it animates in on each change. */}
              <div key={step} className="mt-7 animate-[welcome-step_300ms_var(--ease-out)]">
                <h3 className="font-serif text-2xl tracking-tight text-[var(--color-fg)]">
                  {t(`welcome:fields.${current === 'style' ? 'chatStyle' : current}`)}
                </h3>
                <p className="mt-1.5 text-sm text-[var(--color-fg-muted)] leading-relaxed">
                  {t(`welcome:stepHints.${current}`)}
                </p>
                <div className="mt-6">{renderControl(current)}</div>
              </div>
            </div>

            {/* Footer nav */}
            <div className="shrink-0 border-t border-[var(--color-divider)] px-6 sm:px-8 py-4 flex items-center justify-between gap-3">
              <Button variant="ghost" onClick={handleSkip}>
                {t('welcome:actions.skip')}
              </Button>
              <div className="flex items-center gap-2">
                {step > 0 ? (
                  <Button variant="ghost" onClick={() => setStep((s) => s - 1)}>
                    {t('welcome:actions.back')}
                  </Button>
                ) : null}
                {step < last ? (
                  <Button onClick={() => setStep((s) => s + 1)}>{t('welcome:actions.next')}</Button>
                ) : (
                  <Button onClick={handleStart} loading={saving}>
                    {t('welcome:actions.start')}
                  </Button>
                )}
              </div>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>

    {/* Celebratory welcome shown right after the wizard completes. */}
    <Dialog open={welcomeOpen} onOpenChange={(o) => { if (!o) finishWelcome() }}>
      <DialogContent size="sm" showClose={false} className="overflow-hidden text-center">
        <div aria-hidden className="pointer-events-none absolute inset-x-0 top-0 h-28 opacity-[0.18] blur-2xl"
          style={{ background: 'radial-gradient(60% 80% at 50% 0%, var(--color-accent), transparent 70%)' }}
        />
        <div className="relative flex flex-col items-center gap-4 pt-3 pb-1">
          <Logo size="lg" />
          <div>
            <DialogTitle className="font-serif text-2xl tracking-tight text-[var(--color-fg)]">
              {t('welcome:ready.title')}
            </DialogTitle>
            <DialogDescription className="mt-2 text-sm leading-relaxed text-[var(--color-fg-muted)] max-w-[34ch]">
              {t('welcome:ready.body')}
            </DialogDescription>
          </div>
          <Button className="mt-2 w-full" onClick={finishWelcome}>
            {t('welcome:ready.cta')}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
    </>
  )
}

function Seg({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'px-3.5 h-9 rounded-[9px] text-sm font-medium transition-colors interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        active
          ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]'
          : 'border border-[var(--color-border)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
      )}
    >
      {children}
    </button>
  )
}
