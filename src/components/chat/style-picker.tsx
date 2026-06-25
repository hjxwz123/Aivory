import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Palette, Check } from 'lucide-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Tooltip } from '@/components/ui/tooltip'
import { imageApi } from '@/api/endpoints'
import type { ApiImageStyle } from '@/api/types'
import { cn } from '@/lib/utils'

interface StylePickerProps {
  value: string
  onChange: (id: string) => void
  className?: string
}

/**
 * StylePicker — §4.20 image-style chooser shown in the composer when an image
 * model is selected. A popover of example-thumbnail swatches; the style's hidden
 * prompt lives server-side and is never fetched here.
 */
export function StylePicker({ value, onChange, className }: StylePickerProps) {
  const { t } = useTranslation('chat')
  const [styles, setStyles] = useState<ApiImageStyle[]>([])
  const [loaded, setLoaded] = useState(false)
  const selected = styles.find((s) => s.id === value)

  const load = async () => {
    try {
      setStyles(await imageApi.styles())
    } catch {
      /* ignore — picker just shows no styles */
    } finally {
      setLoaded(true)
    }
  }

  return (
    <Popover>
      <Tooltip content={t('composer.style', { defaultValue: 'Style' })}>
        <PopoverTrigger asChild>
          <button
            type="button"
            aria-label={t('composer.style', { defaultValue: 'Style' })}
            className={cn(
              'inline-flex h-8 max-sm:h-9 items-center gap-1.5 rounded-[8px] px-2 text-[12px] font-medium interactive',
              value
                ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
                : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              className,
            )}
          >
            <Palette size={14} aria-hidden />
            <span className="max-w-[7rem] truncate">
              {selected ? selected.name : t('composer.style', { defaultValue: 'Style' })}
            </span>
          </button>
        </PopoverTrigger>
      </Tooltip>
      <PopoverContent
        side="top"
        align="start"
        // collisionPadding keeps the popover ≥12px from every viewport edge and
        // makes Radix expose the remaining space as --radix-popper-available-height,
        // so a long style list SCROLLS instead of overflowing off-screen.
        collisionPadding={12}
        className="w-[22rem] max-w-[calc(100vw-1.5rem)] max-h-[var(--radix-popper-available-height)] overflow-y-auto p-2"
        onOpenAutoFocus={() => {
          if (!loaded) void load()
        }}
      >
        {/* Sticky heading so it stays visible while the grid scrolls. */}
        <p className="sticky top-0 z-10 -mx-2 -mt-2 mb-1 bg-[var(--color-surface-raised)] px-3 pb-1.5 pt-2 text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
          {t('composer.styleHeading', { defaultValue: 'Image style' })}
        </p>
        {styles.length === 0 ? (
          <p className="px-1 py-2 text-sm text-[var(--color-fg-muted)]">
            {loaded ? t('composer.noStyles', { defaultValue: 'No styles configured.' }) : '…'}
          </p>
        ) : (
          <div className="grid grid-cols-3 gap-2">
            <Swatch active={!value} label={t('composer.styleNone', { defaultValue: 'None' })} onClick={() => onChange('')} />
            {styles.map((s) => (
              <Swatch
                key={s.id}
                active={value === s.id}
                label={s.name}
                image={s.example_image_url}
                onClick={() => onChange(s.id)}
              />
            ))}
          </div>
        )}
      </PopoverContent>
    </Popover>
  )
}

function Swatch({
  active,
  label,
  image,
  onClick,
}: {
  active: boolean
  label: string
  image?: string
  onClick: () => void
}): ReactNode {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex flex-col gap-1 rounded-[8px] p-1 text-left interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        active && 'bg-[var(--color-bg-muted)]',
      )}
    >
      <span
        className={cn(
          'relative aspect-square w-full overflow-hidden rounded-[6px] border',
          active ? 'border-[var(--color-accent)]' : 'border-[var(--color-border-subtle)]',
        )}
      >
        {image ? (
          <img src={image} alt="" className="size-full object-cover" />
        ) : (
          <span className="grid size-full place-items-center bg-[var(--color-bg-muted)] text-[var(--color-fg-faint)]">
            <Palette size={16} aria-hidden />
          </span>
        )}
        {active ? (
          <span className="absolute right-0.5 top-0.5 grid size-4 place-items-center rounded-full bg-[var(--color-accent)] text-[var(--color-accent-fg)]">
            <Check size={10} aria-hidden />
          </span>
        ) : null}
      </span>
      <span className="truncate text-[11px] text-[var(--color-fg-muted)]">{label}</span>
    </button>
  )
}
