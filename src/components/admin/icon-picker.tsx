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

const ICON_BATCH_SIZE = 120

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
  const [visibleCount, setVisibleCount] = useState(ICON_BATCH_SIZE)

  const previewName = useMemo(() => resolveLucideIconName(value) ?? '', [value])

  const filteredResults = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return LUCIDE_ICON_NAMES
    const compactQuery = q.replace(/\s+/g, '')
    return LUCIDE_ICON_NAMES.filter((name) => {
      const indexedName = searchable(name)
      return indexedName.includes(q) || indexedName.replace(/\s+/g, '').includes(compactQuery)
    })
  }, [query])
  const results = filteredResults.slice(0, visibleCount)
  const hasMore = results.length < filteredResults.length

  function setPickerOpen(nextOpen: boolean) {
    setOpen(nextOpen)
    if (!nextOpen) {
      setQuery('')
      setVisibleCount(ICON_BATCH_SIZE)
    }
  }

  function revealNextBatch() {
    setVisibleCount((current) => Math.min(current + ICON_BATCH_SIZE, filteredResults.length))
  }

  return (
    <div className="relative min-w-0 w-full">
      <Popover modal open={open} onOpenChange={setPickerOpen}>
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
        <PopoverContent
          align="start"
          collisionPadding={12}
          className="flex w-[280px] max-w-[calc(100vw-1rem)] flex-col overflow-hidden p-2"
          style={{
            maxHeight: 'min(24rem, var(--radix-popover-content-available-height), calc(100dvh - 1.5rem))',
          }}
        >
          <div className="relative mb-2 shrink-0">
            <Search size={13} aria-hidden className="absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]" />
            <Input
              autoFocus
              value={query}
              onChange={(e) => {
                setQuery(e.target.value)
                setVisibleCount(ICON_BATCH_SIZE)
              }}
              placeholder={t('admin:icon.search', { defaultValue: 'Search icons' })}
              className="h-8 pl-8 text-[12px]"
            />
          </div>
          <div
            role="group"
            aria-label={t('admin:icon.select', { defaultValue: 'Select icon' })}
            className="grid min-h-0 flex-1 grid-cols-6 gap-1 overflow-y-auto overscroll-contain pr-1 scrollbar-thin"
            onScroll={(event) => {
              const target = event.currentTarget
              if (hasMore && target.scrollHeight - target.scrollTop - target.clientHeight < 72) {
                revealNextBatch()
              }
            }}
          >
            {results.map((name) => (
              <button
                key={name}
                type="button"
                title={name}
                aria-label={name}
                aria-pressed={previewName === name}
                onClick={() => {
                  onChange(name)
                  setPickerOpen(false)
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
            {hasMore ? (
              <button
                type="button"
                onClick={revealNextBatch}
                className="col-span-6 min-h-8 rounded-[8px] px-2 text-[12px] font-medium text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                {t('common:actions.more')}
              </button>
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
