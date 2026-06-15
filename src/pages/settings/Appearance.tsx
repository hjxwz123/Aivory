import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { useSettings } from '@/store/settings'
import { useTheme } from '@/store/theme'
import { useAccent } from '@/store/accent'
import { useLanguage } from '@/store/language'
import { SUPPORTED_LANGUAGES } from '@/i18n'
import { type AccentPref, ACCENT_PRESETS, type FontPref, FONT_PRESETS } from '@/types/settings'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Sun, Moon, Monitor, Check } from 'lucide-react'
import { cn } from '@/lib/utils'

/**
 * Static preview color per accent preset. Chosen at L≈58 / C≈0.18 so the
 * swatches read as the same hue family the user will actually see, without
 * inheriting whatever data-accent the document currently has. Keeps the
 * picker consistent under any light/dark + accent combination.
 */
const ACCENT_PREVIEW: Record<AccentPref, string> = {
  violet: 'oklch(58% 0.225 290)',
  lagoon: 'oklch(58% 0.130 200)',
  ember:  'oklch(62% 0.170 40)',
  moss:   'oklch(54% 0.125 145)',
  indigo: 'oklch(54% 0.180 260)',
  rose:   'oklch(60% 0.180 5)',
}

// CSS family per typeface preset so each card previews in its own font,
// regardless of which font is currently applied to the document.
const FONT_PREVIEW: Record<FontPref, string> = {
  default: "'Geist Variable', 'Geist', ui-sans-serif, sans-serif",
  inter: "'Inter Variable', 'Inter', ui-sans-serif, sans-serif",
  system: 'ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, sans-serif',
  serif: "'Fraunces Variable', 'Fraunces', ui-serif, Georgia, serif",
}

export default function Appearance() {
  const pref = useTheme((s) => s.pref)
  const setPref = useTheme((s) => s.setPref)
  const syncSystem = useTheme((s) => s.syncSystem)
  const accent = useAccent((s) => s.accent)
  const setAccent = useAccent((s) => s.setAccent)
  const appearance = useSettings((s) => s.appearance)
  const setAppearance = useSettings((s) => s.setAppearance)
  const lang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const { t } = useTranslation('settings')

  useEffect(() => syncSystem(), [syncSystem])
  // Eager-load Inter so its preview renders in the real face before selection
  // (it's otherwise lazy-loaded only when chosen).
  useEffect(() => {
    void import('@fontsource-variable/inter')
  }, [])

  return (
    <div className="max-w-[44rem]">
      <header className="mb-8">
        <h1 className="font-serif tracking-tight text-3xl text-[var(--color-fg)]">{t('appearance.title')}</h1>
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)]">{t('appearance.subtitle')}</p>
      </header>

      <SettingsSection title={t('appearance.theme')} description={t('appearance.themeBody')}>
        <SettingsRow label={t('appearance.colorTheme')} description={t('appearance.colorThemeBody')}>
          <div className="flex items-center gap-2">
            <ThemeChip current={pref} value="light" onClick={() => setPref('light')} icon={<Sun size={14} aria-hidden />} label={t('appearance.light')} />
            <ThemeChip current={pref} value="dark" onClick={() => setPref('dark')} icon={<Moon size={14} aria-hidden />} label={t('appearance.dark')} />
            <ThemeChip current={pref} value="system" onClick={() => setPref('system')} icon={<Monitor size={14} aria-hidden />} label={t('appearance.system')} />
          </div>
        </SettingsRow>
        <SettingsRow label={t('appearance.accentColor')} description={t('appearance.accentColorBody')}>
          <div className="flex items-center gap-2 flex-wrap">
            {ACCENT_PRESETS.map((preset) => (
              <AccentSwatch
                key={preset}
                preset={preset}
                active={accent === preset}
                onClick={() => setAccent(preset)}
                label={t(`appearance.accent.${preset}`)}
                color={ACCENT_PREVIEW[preset]}
              />
            ))}
          </div>
        </SettingsRow>
        <SettingsRow label={t('appearance.language')} description={t('appearance.languageBody')}>
          <Select value={lang} onValueChange={(v) => setLang(v as typeof lang)}>
            <SelectTrigger className="w-48" aria-label={t('appearance.language')}>
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
        </SettingsRow>
      </SettingsSection>

      <SettingsSection title={t('appearance.density')} description={t('appearance.densityBody')}>
        <SettingsRow label={t('appearance.spacing')} description={t('appearance.spacingBody')}>
          <div className="inline-flex items-center gap-1 p-0.5 rounded-[10px] bg-[var(--color-bg-muted)] border border-[var(--color-border-subtle)]">
            <Segment current={appearance.density} value="cozy" onClick={() => setAppearance({ density: 'cozy' })}>
              {t('appearance.cozy')}
            </Segment>
            <Segment
              current={appearance.density}
              value="comfortable"
              onClick={() => setAppearance({ density: 'comfortable' })}
            >
              {t('appearance.comfortable')}
            </Segment>
          </div>
        </SettingsRow>
        <SettingsRow label={t('appearance.chatWidth.label')} description={t('appearance.chatWidth.body')}>
          <div className="inline-flex items-center gap-1 p-0.5 rounded-[10px] bg-[var(--color-bg-muted)] border border-[var(--color-border-subtle)]">
            <Segment
              current={appearance.chatWidth}
              value="comfortable"
              onClick={() => setAppearance({ chatWidth: 'comfortable' })}
            >
              {t('appearance.chatWidth.comfortable')}
            </Segment>
            <Segment
              current={appearance.chatWidth}
              value="full"
              onClick={() => setAppearance({ chatWidth: 'full' })}
            >
              {t('appearance.chatWidth.full')}
            </Segment>
          </div>
        </SettingsRow>
        <SettingsRow label={t('appearance.fontSize')} description={t('appearance.fontSizeBody')}>
          <div className="inline-flex items-center gap-1 p-0.5 rounded-[10px] bg-[var(--color-bg-muted)] border border-[var(--color-border-subtle)]">
            <Segment current={appearance.fontSize} value="sm" onClick={() => setAppearance({ fontSize: 'sm' })}>
              S
            </Segment>
            <Segment current={appearance.fontSize} value="md" onClick={() => setAppearance({ fontSize: 'md' })}>
              M
            </Segment>
            <Segment current={appearance.fontSize} value="lg" onClick={() => setAppearance({ fontSize: 'lg' })}>
              L
            </Segment>
          </div>
        </SettingsRow>
        <SettingsRow label={t('appearance.font.title')} description={t('appearance.font.subtitle')}>
          <div className="flex flex-wrap gap-2 sm:justify-end">
            {FONT_PRESETS.map((opt) => {
              const active = appearance.font === opt
              return (
                <button
                  key={opt}
                  type="button"
                  aria-pressed={active}
                  onClick={() => setAppearance({ font: opt })}
                  style={{ fontFamily: FONT_PREVIEW[opt] }}
                  className={cn(
                    'flex flex-col items-start gap-0.5 rounded-[10px] border px-3 py-2 text-left interactive min-w-[6.5rem]',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    active
                      ? 'border-[var(--color-accent)] bg-[var(--color-accent-soft)]'
                      : 'border-[var(--color-border)] hover:border-[var(--color-border-strong)]',
                  )}
                >
                  <span className="text-[16px] leading-tight text-[var(--color-fg)]">Ag 字</span>
                  <span className="text-[11px] text-[var(--color-fg-muted)]">
                    {t(`appearance.font.options.${opt}`)}
                  </span>
                </button>
              )
            })}
          </div>
        </SettingsRow>
      </SettingsSection>
    </div>
  )
}

function ThemeChip({
  current,
  value,
  onClick,
  icon,
  label,
}: {
  current: string
  value: string
  onClick: () => void
  icon: React.ReactNode
  label: string
}) {
  const active = current === value
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'inline-flex items-center gap-1.5 h-9 px-3.5 rounded-[10px] text-sm font-medium interactive',
        active
          ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]'
          : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] border border-[var(--color-border)]',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
      )}
    >
      {icon}
      {label}
    </button>
  )
}

function AccentSwatch({
  preset,
  active,
  onClick,
  label,
  color,
}: {
  preset: AccentPref
  active: boolean
  onClick: () => void
  label: string
  color: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      aria-label={label}
      title={label}
      data-accent-preset={preset}
      className={cn(
        'relative inline-flex items-center justify-center size-9 rounded-full interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--color-bg)]',
        active
          ? 'shadow-[0_0_0_2px_var(--color-fg),0_0_0_4px_var(--color-bg)]'
          : 'shadow-[inset_0_0_0_1px_oklch(0%_0_0/0.12)] hover:scale-105',
      )}
      style={{ backgroundColor: color }}
    >
      {active && <Check size={16} className="text-white drop-shadow" aria-hidden />}
    </button>
  )
}

function Segment({
  current,
  value,
  onClick,
  children,
}: {
  current: string
  value: string
  onClick: () => void
  children: React.ReactNode
}) {
  const active = current === value
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'h-8 px-3 rounded-[8px] text-[13px] font-medium interactive',
        active
          ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
          : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
      )}
    >
      {children}
    </button>
  )
}
