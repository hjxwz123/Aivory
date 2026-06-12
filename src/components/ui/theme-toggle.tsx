import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Monitor, Moon, Sun } from 'lucide-react'
import { useTheme } from '@/store/theme'
import { Tooltip } from './tooltip'
import { cn } from '@/lib/utils'

const opts = [
  { value: 'light', icon: Sun, labelKey: 'settings:appearance.light' },
  { value: 'dark', icon: Moon, labelKey: 'settings:appearance.dark' },
  { value: 'system', icon: Monitor, labelKey: 'settings:appearance.system' },
] as const

export function ThemeToggle({ className }: { className?: string }) {
  const pref = useTheme((s) => s.pref)
  const setPref = useTheme((s) => s.setPref)
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation(['common', 'settings'])

  useEffect(() => {
    syncSystem()
  }, [syncSystem])

  return (
    <div
      role="radiogroup"
      aria-label={t('common:aria.themeGroup')}
      className={cn(
        'inline-flex items-center gap-0.5 p-0.5 rounded-[10px] bg-[var(--color-bg-muted)] border border-[var(--color-border)]',
        className,
      )}
    >
      {opts.map(({ value, labelKey, icon: Icon }) => {
        const active = pref === value
        const label = t(labelKey)
        return (
          <Tooltip key={value} content={label} side="bottom">
            <button
              type="button"
              role="radio"
              aria-checked={active}
              aria-label={t('common:aria.themeValue', { label })}
              onClick={() => setPref(value)}
              className={cn(
                'inline-flex items-center justify-center size-7 rounded-[7px]',
                'interactive text-[var(--color-fg-muted)]',
                active
                  ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
                  : 'hover:text-[var(--color-fg)]',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              )}
            >
              <Icon size={14} aria-hidden />
            </button>
          </Tooltip>
        )
      })}
    </div>
  )
}
