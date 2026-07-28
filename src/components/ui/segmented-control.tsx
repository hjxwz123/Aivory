import { cn } from '@/lib/utils'

interface SegmentedControlProps<T extends string> {
  label: string
  value: T
  options: Array<{ value: T; label: string }>
  onChange: (value: T) => void
  fullWidthOnMobile?: boolean
  compact?: boolean
}

export function SegmentedControl<T extends string>({
  label,
  value,
  options,
  onChange,
  fullWidthOnMobile = false,
  compact = false,
}: SegmentedControlProps<T>) {
  return (
    <div
      role="group"
      aria-label={label}
      className={cn(
        'inline-flex max-w-full items-center gap-0.5 rounded-[8px] bg-[var(--color-bg-muted)] p-0.5',
        compact ? 'min-h-7' : 'min-h-8',
        fullWidthOnMobile && 'w-full sm:w-auto',
      )}
    >
      {options.map((option) => {
        const active = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(option.value)}
            className={cn(
              'min-w-0 rounded-[6px] text-center font-medium leading-tight transition-colors',
              compact
                ? "relative min-h-6 px-2 py-0.5 text-[11.5px] after:absolute after:-inset-y-2.5 after:inset-x-0 after:content-['']"
                : "min-h-7 px-2.5 py-1 text-[12px] max-sm:relative max-sm:min-h-8 max-sm:px-3 max-sm:text-[12.5px] max-sm:after:absolute max-sm:after:-inset-y-1.5 max-sm:after:inset-x-0 max-sm:after:content-['']",
              fullWidthOnMobile && 'flex-1 sm:flex-none',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              active
                ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
                : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
            )}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}
