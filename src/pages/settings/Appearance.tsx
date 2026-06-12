import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { SettingsRow, SettingsSection } from './SettingsLayout'
import { useSettings } from '@/store/settings'
import { useTheme } from '@/store/theme'
import { useLanguage } from '@/store/language'
import { SUPPORTED_LANGUAGES } from '@/i18n'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Sun, Moon, Monitor } from 'lucide-react'
import { cn } from '@/lib/utils'

export default function Appearance() {
  const pref = useTheme((s) => s.pref)
  const setPref = useTheme((s) => s.setPref)
  const syncSystem = useTheme((s) => s.syncSystem)
  const appearance = useSettings((s) => s.appearance)
  const setAppearance = useSettings((s) => s.setAppearance)
  const lang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const { t } = useTranslation('settings')

  useEffect(() => syncSystem(), [syncSystem])

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
