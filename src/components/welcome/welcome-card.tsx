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
import { ACCENT_PRESETS, type AccentPref, type ThemePref } from '@/types/settings'
import { Logo } from '@/components/brand/logo'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { cn } from '@/lib/utils'

type ReplyStyle = 'concise' | 'balanced' | 'detailed'

// Static hue per accent preset, mirrored from the Appearance picker so the
// swatches read as the same colors regardless of the document's data-accent.
const ACCENT_PREVIEW: Record<AccentPref, string> = {
  violet: 'oklch(58% 0.225 290)',
  lagoon: 'oklch(58% 0.130 200)',
  ember: 'oklch(62% 0.170 40)',
  moss: 'oklch(54% 0.125 145)',
  indigo: 'oklch(54% 0.180 260)',
  rose: 'oklch(60% 0.180 5)',
}
const THEME_OPTS: ThemePref[] = ['light', 'dark', 'system']
const STYLE_OPTS: ReplyStyle[] = ['concise', 'balanced', 'detailed']

/**
 * First-login welcome card. Shows once per account (gated on a server-side
 * `onboarded` flag stored in user settings): an editorial intro on the left and
 * a quick-config panel on the right. Choices apply live for instant preview;
 * "Skip" restores whatever was set before the card opened so it's truly
 * non-committal, while "Get started" persists and dismisses. Either path marks
 * the account onboarded so the card never returns.
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

  const onboarded = Boolean((user?.settings as Record<string, unknown> | undefined)?.onboarded)
  const eligible = status === 'authenticated' && Boolean(user) && !onboarded

  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const initial = useRef<{
    lang: LanguageCode
    accent: AccentPref
    theme: ThemePref
    replyStyle: ReplyStyle
    memory: boolean
  } | null>(null)

  // Open once when first eligible, snapshotting the current prefs so Skip can
  // undo any live previews the user made.
  useEffect(() => {
    if (eligible && !open && initial.current === null) {
      initial.current = { lang, accent, theme: themePref, replyStyle, memory }
      setOpen(true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [eligible])

  if (!open) return null

  function markOnboarded(extra: Record<string, unknown>) {
    const patch = { onboarded: true, ...extra }
    if (user) setUser({ ...user, settings: { ...(user.settings ?? {}), ...patch } })
    void authApi.updateSettings(patch).catch(() => {
      /* best-effort — if it fails the card simply reappears next session */
    })
  }

  function handleStart() {
    setSaving(true)
    // Memory is the only choice with a server-side mirror; the rest are
    // localStorage-backed and already applied live.
    markOnboarded({ memory_enabled: memory })
    setOpen(false)
    setSaving(false)
  }

  function handleSkip() {
    const init = initial.current
    if (init) {
      if (lang !== init.lang) setLang(init.lang)
      if (accent !== init.accent) setAccent(init.accent)
      if (themePref !== init.theme) setPref(init.theme)
      if (replyStyle !== init.replyStyle) setModels({ responseLength: init.replyStyle })
      if (memory !== init.memory) setPrivacy({ memoriesEnabled: init.memory })
    }
    markOnboarded({})
    setOpen(false)
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) handleSkip() }}>
      <DialogContent size="xl" showClose={false} className="p-0 overflow-hidden">
        <div className="flex flex-col md:flex-row min-h-0 flex-1">
          {/* Editorial intro */}
          <aside className="hidden md:flex md:w-[44%] flex-col justify-between gap-10 p-8 bg-[var(--color-bg-muted)] border-r border-[var(--color-divider)]">
            <Logo size="lg" />
            <div>
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
            <ul className="flex flex-col gap-3.5">
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

          {/* Quick config */}
          <div className="flex-1 min-w-0 flex flex-col min-h-0">
            <div className="flex-1 overflow-y-auto px-6 sm:px-7 pt-6 sm:pt-7 pb-2">
              <DialogTitle>{t('welcome:config.title')}</DialogTitle>
              <DialogDescription>{t('welcome:config.subtitle')}</DialogDescription>

              <div className="mt-6 flex flex-col gap-6">
                {/* Language */}
                <Field label={t('welcome:fields.language')}>
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
                </Field>

                {/* Appearance / theme */}
                <Field label={t('welcome:fields.theme')}>
                  <div className="flex items-center gap-2">
                    {THEME_OPTS.map((opt) => (
                      <Seg key={opt} active={themePref === opt} onClick={() => setPref(opt)}>
                        {t(`welcome:theme.${opt}`)}
                      </Seg>
                    ))}
                  </div>
                </Field>

                {/* Accent color */}
                <Field label={t('welcome:fields.accent')}>
                  <div className="flex items-center gap-2.5">
                    {ACCENT_PRESETS.map((p) => (
                      <button
                        key={p}
                        type="button"
                        aria-label={t(`settings:appearance.accent.${p}`)}
                        aria-pressed={accent === p}
                        onClick={() => setAccent(p)}
                        className={cn(
                          'relative size-8 rounded-full transition-transform interactive',
                          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                          accent === p
                            ? 'ring-2 ring-offset-2 ring-offset-[var(--color-surface)] ring-[var(--color-fg)] scale-105'
                            : 'hover:scale-105',
                        )}
                        style={{ background: ACCENT_PREVIEW[p] }}
                      >
                        {accent === p ? (
                          <Check size={15} aria-hidden className="absolute inset-0 m-auto text-white" />
                        ) : null}
                      </button>
                    ))}
                  </div>
                </Field>

                {/* Reply style */}
                <Field label={t('welcome:fields.chatStyle')} body={t('welcome:fields.chatStyleBody')}>
                  <div className="flex items-center gap-2">
                    {STYLE_OPTS.map((opt) => (
                      <Seg key={opt} active={replyStyle === opt} onClick={() => setModels({ responseLength: opt })}>
                        {t(`welcome:chatStyle.${opt}`)}
                      </Seg>
                    ))}
                  </div>
                </Field>

                {/* Memory */}
                <label className="flex items-center justify-between gap-4 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-4 py-3">
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-[var(--color-fg)]">{t('welcome:fields.memory')}</div>
                    <p className="mt-0.5 text-[12px] text-[var(--color-fg-muted)] leading-relaxed">
                      {t('welcome:fields.memoryBody')}
                    </p>
                  </div>
                  <Switch checked={memory} onCheckedChange={(v) => setPrivacy({ memoriesEnabled: v })} />
                </label>
              </div>
            </div>

            <div className="shrink-0 border-t border-[var(--color-divider)] px-6 sm:px-7 py-4 flex items-center justify-between gap-3">
              <Button variant="ghost" onClick={handleSkip}>
                {t('welcome:actions.skip')}
              </Button>
              <Button onClick={handleStart} loading={saving}>
                {t('welcome:actions.start')}
              </Button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, body, children }: { label: string; body?: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="text-sm font-medium text-[var(--color-fg)]">{label}</div>
      {body ? <p className="mt-0.5 text-[12px] text-[var(--color-fg-muted)] leading-relaxed">{body}</p> : null}
      <div className="mt-2">{children}</div>
    </div>
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
