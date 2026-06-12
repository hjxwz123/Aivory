import { Languages } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tooltip } from '@/components/ui/tooltip'
import { SUPPORTED_LANGUAGES } from '@/i18n'
import { useLanguage } from '@/store/language'
import { cn } from '@/lib/utils'

interface LanguageToggleProps {
  variant?: 'icon' | 'pill'
  className?: string
}

export function LanguageToggle({ variant = 'icon', className }: LanguageToggleProps) {
  const lang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const current = SUPPORTED_LANGUAGES.find((l) => l.code === lang) ?? SUPPORTED_LANGUAGES[0]
  const { t } = useTranslation('common')

  return (
    <DropdownMenu>
      <Tooltip content={current.label} side="bottom">
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={t('aria.languageValue', { label: current.label })}
            className={cn(
              'inline-flex items-center justify-center gap-1.5 interactive',
              'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              variant === 'icon' &&
                'size-8 rounded-[8px] hover:bg-[var(--color-bg-muted)]',
              variant === 'pill' &&
                'h-9 px-3 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-sm font-medium',
              className,
            )}
          >
            <Languages size={14} aria-hidden />
            <span className={cn(variant === 'icon' && 'sr-only')}>{current.label}</span>
          </button>
        </DropdownMenuTrigger>
      </Tooltip>
      <DropdownMenuContent align="end">
        {SUPPORTED_LANGUAGES.map((l) => (
          <DropdownMenuItem
            key={l.code}
            onSelect={() => setLang(l.code)}
            className="justify-between gap-6"
          >
            <span>{l.label}</span>
            {l.code === lang ? (
              <span className="size-1.5 rounded-full bg-[var(--color-accent)]" aria-label={t('aria.selected')} />
            ) : null}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
