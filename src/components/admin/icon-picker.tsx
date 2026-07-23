/**
 * IconPicker — a searchable dropdown over the full lucide-react icon set, used
 * by the param_controls visual editor so admins pick an icon instead of typing
 * its name. The chosen value is stored in PascalCase (e.g. "Brain"); the shared
 * Lucide resolver also keeps legacy kebab-case and snake_case values renderable.
 */
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, Search, X } from 'lucide-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Input } from '@/components/ui/input'
import { LucideGlyph } from '@/components/ui/lucide-icon'
import { LUCIDE_ICON_NAMES, resolveLucideIconName } from '@/lib/lucide-icons'
import { cn } from '@/lib/utils'

// "Brain" → "brain", "ArrowUp" → "arrow up" for fuzzy search matching.
function searchable(name: string): string {
  return name.replace(/([a-z0-9])([A-Z])/g, '$1 $2').toLowerCase()
}

interface IconPickerProps {
  id?: string
  value: string
  onChange: (name: string) => void
  className?: string
  'aria-label'?: string
}

export function IconPicker({ id, value, onChange, className, 'aria-label': ariaLabel }: IconPickerProps) {
  const { t } = useTranslation(['admin', 'common'])
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')

  const previewName = useMemo(() => resolveLucideIconName(value) ?? '', [value])

  const results = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return LUCIDE_ICON_NAMES.slice(0, 120)
    return LUCIDE_ICON_NAMES.filter((name) => searchable(name).includes(q)).slice(0, 120)
  }, [query])

  return (
    <div className="relative min-w-0 w-full">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <button
            id={id}
            type="button"
            aria-label={ariaLabel}
            className={cn(
              'flex h-9 w-full items-center gap-2 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg)] px-2.5 text-left text-[13px] text-[var(--color-fg)] interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              value && 'pr-9',
              className,
            )}
          >
            {previewName ? <LucideGlyph name={previewName} size={15} aria-hidden /> : null}
            <span className={cn('flex-1 min-w-0 truncate font-mono text-[12px]', !value && 'text-[var(--color-fg-faint)]')}>
              {value || t('admin:icon.select', { defaultValue: 'Select icon' })}
            </span>
            {!value ? <ChevronDown size={14} aria-hidden className="text-[var(--color-fg-faint)]" /> : null}
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-[280px] max-w-[calc(100vw-1rem)] p-2">
          <div className="relative mb-2">
            <Search size={13} aria-hidden className="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]" />
            <Input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t('admin:icon.search', { defaultValue: 'Search icons' })}
              className="h-8 pl-8 text-[12px]"
            />
          </div>
          <div className="grid grid-cols-6 gap-1 max-h-[240px] overflow-y-auto scrollbar-thin">
            {results.map((name) => (
              <button
                key={name}
                type="button"
                title={name}
                aria-label={name}
                onClick={() => {
                  onChange(name)
                  setOpen(false)
                  setQuery('')
                }}
                className={cn(
                  'inline-flex items-center justify-center size-9 rounded-[8px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  previewName === name && 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]',
                )}
              >
                <LucideGlyph name={name} size={16} aria-hidden />
              </button>
            ))}
            {results.length === 0 ? (
              <p className="col-span-6 px-1 py-3 text-center text-[12px] text-[var(--color-fg-subtle)]">
                {t('admin:icon.noResults', { defaultValue: 'No matching icons' })}
              </p>
            ) : null}
          </div>
        </PopoverContent>
      </Popover>
      {value ? (
        <button
          type="button"
          aria-label={t('admin:icon.clear', { defaultValue: 'Clear icon' })}
          title={t('admin:icon.clear', { defaultValue: 'Clear icon' })}
          onClick={() => onChange('')}
          className="absolute right-1 top-1/2 inline-flex size-7 -translate-y-1/2 items-center justify-center rounded-[7px] text-[var(--color-fg-faint)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <X size={13} aria-hidden />
        </button>
      ) : null}
    </div>
  )
}
