import { useState, type ReactNode } from 'react'
import { ChevronDown, Lock } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { useModels } from '@/store/models'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { ModelIcon } from '@/components/chat/model-icon'
import { cn } from '@/lib/utils'

interface ModelPickerProps {
  value: string
  onChange: (id: string) => void
  className?: string
}

/**
 * ModelPicker — driven by the backend model registry. Falls back to the local
 * mock model bundle when the registry hasn't loaded yet, so the picker is
 * always populated.
 */
export function ModelPicker({ value, onChange, className }: ModelPickerProps) {
  const models = useModels((s) => s.models)
  const tags = useModels((s) => s.tags)
  const current = models.find((m) => m.id === value) ?? models[0]
  const { t } = useTranslation('chat')
  const navigate = useNavigate()

  // Tag filter (§ model tags): null = all models (the default). Only tags that
  // are actually assigned to at least one model are worth showing as chips.
  const [activeTag, setActiveTag] = useState<string | null>(null)
  const usedTagIds = new Set(models.flatMap((m) => m.tags ?? []))
  const shownTags = tags.filter((tag) => usedTagIds.has(tag.id))
  const visibleModels =
    activeTag && shownTags.some((tag) => tag.id === activeTag)
      ? models.filter((m) => (m.tags ?? []).includes(activeTag))
      : models

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className={cn(
          'inline-flex items-center gap-1.5 h-8 px-2.5 rounded-[8px]',
          'text-[13px] font-medium text-[var(--color-fg-muted)]',
          'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          'max-w-[180px]',
          className,
        )}
        aria-label={t('modelPicker.label', { name: current?.label ?? 'Model' })}
      >
        <ModelIcon icon={current?.icon} size={13} />
        <span className="truncate">{current?.label ?? 'Model'}</span>
        <ChevronDown size={13} aria-hidden />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        side="top"
        align="end"
        // collisionPadding keeps the menu ≥12px from every viewport edge and makes
        // Radix subtract that gap from --radix-popper-available-height — the exact
        // vertical space left on whichever side the menu opens. Capping max-height
        // to that var means the list scrolls instead of clipping off-screen, no
        // matter how many models exist or where the trigger sits (e.g. the
        // vertically-centred composer on the welcome screen).
        collisionPadding={12}
        className="w-[320px] max-h-[var(--radix-popper-available-height)]"
        // The shared menu base class sets `overflow:hidden`; an inline longhand
        // reliably wins over it (inline > class) so the list actually SCROLLS when
        // it's taller than the available height instead of clipping models off the
        // bottom. (A className override is fragile here — tailwind-merge / source
        // order can leave the base `overflow-hidden` in play.)
        style={{ overflowX: 'hidden', overflowY: 'auto' }}
      >
        <DropdownMenuLabel>{t('modelPicker.section')}</DropdownMenuLabel>
        {shownTags.length > 0 && (
          // Tag filter chips (§ model tags). Sticky so they stay reachable while
          // the model list scrolls. Plain buttons (not menu items) so a click
          // filters without closing the menu.
          <div className="sticky top-0 z-10 -mx-1.5 mb-1 flex gap-1 overflow-x-auto border-b border-[var(--color-divider)] bg-[var(--color-surface-raised)] px-1.5 pb-2 pt-0.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
            <TagChip active={activeTag === null} onClick={() => setActiveTag(null)}>
              {t('modelPicker.allTags')}
            </TagChip>
            {shownTags.map((tag) => (
              <TagChip key={tag.id} active={activeTag === tag.id} onClick={() => setActiveTag(tag.id)}>
                {tag.name}
              </TagChip>
            ))}
          </div>
        )}
        {visibleModels.map((m) => {
          const active = m.id === value
          const locked = Boolean(m.locked)
          return (
            <DropdownMenuItem
              key={m.id}
              onSelect={(e) => {
                // Locked models stay visible (§ user groups) but route to the
                // subscription page to upgrade rather than becoming selectable.
                if (locked) {
                  e.preventDefault()
                  navigate('/subscription')
                  return
                }
                onChange(m.id)
              }}
              className={cn('items-start py-2.5 gap-2', locked && 'opacity-70')}
            >
              <ModelIcon icon={m.icon} size={16} className={cn('mt-0.5', locked && 'grayscale')} />
              <div className="flex flex-col gap-1 flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-[var(--color-fg)] truncate">{m.label}</span>
                  {locked ? (
                    <Lock size={12} aria-hidden className="ml-auto shrink-0 text-[var(--color-fg-subtle)]" />
                  ) : active ? (
                    <span
                      className="ml-auto size-1.5 rounded-full bg-[var(--color-accent)] shrink-0"
                      aria-label={t('modelPicker.current')}
                    />
                  ) : null}
                </div>
                {locked ? (
                  <span className="text-[11.5px] text-[var(--color-accent)] leading-snug">
                    {t('modelPicker.upgrade')}
                  </span>
                ) : m.description ? (
                  <span className="text-[11.5px] text-[var(--color-fg-muted)] leading-snug">
                    {m.description}
                  </span>
                ) : null}
              </div>
            </DropdownMenuItem>
          )
        })}
        {visibleModels.length === 0 && (
          <div className="px-2.5 py-3 text-center text-[12px] text-[var(--color-fg-subtle)]">
            {t('modelPicker.noneForTag')}
          </div>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** A single tag filter chip in the picker header. */
function TagChip({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'shrink-0 rounded-full px-2.5 py-1 text-[12px] font-medium whitespace-nowrap interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        active
          ? 'bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
          : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
      )}
    >
      {children}
    </button>
  )
}
