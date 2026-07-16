import { useState, type ReactNode } from 'react'
import { ChevronDown, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'
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
  /** §fast-mode: current 快速/进阶 selection. */
  fast?: boolean
  /** §fast-mode: pick 快速 (true) — picking an advanced model via onChange resets it. */
  onFastChange?: (fast: boolean) => void
  className?: string
}

/**
 * ModelPicker — a two-level 快速/进阶 selector driven by the backend model
 * registry. "快速" (fast, §fast-mode) is the admin-configured model shown only as
 * "快速" (never its real name); "进阶" opens the concrete model list. When no fast
 * model is configured the picker degrades to the plain model list.
 */
export function ModelPicker({ value, onChange, fast, onFastChange, className }: ModelPickerProps) {
  const models = useModels((s) => s.models)
  const imageModels = useModels((s) => s.imageModels)
  const tags = useModels((s) => s.tags)
  const fastAvailable = useModels((s) => s.fastAvailable)
  // §fast-mode: a fast selection only holds while a fast model is actually
  // configured (fastAvailable) — otherwise fall through to the advanced model.
  const isFast = Boolean(fast) && fastAvailable
  // §4.20: a selected value can be a chat OR an image model — look in both so the
  // trigger shows the right name + icon (incl. an image model when drawing).
  const current = models.find((m) => m.id === value) ?? imageModels.find((m) => m.id === value) ?? models[0]
  const { t } = useTranslation('chat')
  // Picking any concrete model exits fast mode; 快速 re-enters it.
  const pickModel = (id: string) => {
    onFastChange?.(false)
    onChange(id)
  }

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
          'inline-flex min-w-0 items-center gap-1.5 h-8 px-2.5 rounded-[8px]',
          'text-[13px] font-medium text-[var(--color-fg-muted)]',
          'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          'max-w-[180px]',
          className,
        )}
        aria-label={isFast ? t('fastMode.label', { defaultValue: '快速' }) : t('modelPicker.label', { name: current?.label ?? 'Model' })}
      >
        {isFast ? (
          <Zap size={13} aria-hidden />
        ) : (
          <ModelIcon icon={current?.icon} size={13} />
        )}
        <span className="truncate">{isFast ? t('fastMode.label', { defaultValue: '快速' }) : current?.label ?? 'Model'}</span>
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
        className="min-w-0 overscroll-contain"
        // The shared menu base class sets `overflow:hidden`; an inline longhand
        // reliably wins over it (inline > class) so the list actually SCROLLS when
        // it's taller than the available height instead of clipping models off the
        // bottom. (A className override is fragile here — tailwind-merge / source
        // order can leave the base `overflow-hidden` in play.)
        style={{
          width: 'min(20rem, calc(100vw - var(--safe-left) - var(--safe-right) - 1.5rem))',
          maxWidth: 'calc(100vw - var(--safe-left) - var(--safe-right) - 1.5rem)',
          maxHeight: 'min(34rem, var(--radix-dropdown-menu-content-available-height))',
          overflowX: 'hidden',
          overflowY: 'auto',
        }}
      >
        {fastAvailable ? (
          <>
            {/* §fast-mode: the 快速 option — shown only as "快速", never the model name. */}
            <DropdownMenuItem onSelect={() => onFastChange?.(true)} className="items-start py-2.5 gap-2">
              <Zap size={16} className="mt-0.5 text-[var(--color-fg-muted)]" />
              <div className="flex flex-col gap-1 flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-[var(--color-fg)] truncate">
                    {t('fastMode.label', { defaultValue: '快速' })}
                  </span>
                  {isFast && (
                    <span
                      className="ml-auto size-1.5 rounded-full bg-[var(--color-accent)] shrink-0"
                      aria-label={t('modelPicker.current')}
                    />
                  )}
                </div>
                <span className="text-[11.5px] text-[var(--color-fg-muted)] leading-snug">
                  {t('fastMode.pickerDesc', { defaultValue: '更快的对话，模型由平台优选。' })}
                </span>
              </div>
            </DropdownMenuItem>
            <DropdownMenuLabel className="mt-1 border-t border-[var(--color-divider)] pt-2">
              {t('fastMode.advancedSection', { defaultValue: '进阶' })}
            </DropdownMenuLabel>
          </>
        ) : (
          <DropdownMenuLabel>{t('modelPicker.section')}</DropdownMenuLabel>
        )}
        {shownTags.length > 0 && (
          // Tag filter chips (§ model tags). Sticky so they stay reachable while
          // the model list scrolls. Plain buttons (not menu items) so a click
          // filters without closing the menu. -top-1.5/pt-2 offset the menu's
          // p-1.5 scroll padding — with top-0 the padding band stays see-through
          // and scrolled model rows peek out above the pinned chips.
          <div className="sticky -top-1.5 z-10 -mx-1.5 mb-1 flex gap-1 overflow-x-auto border-b border-[var(--color-divider)] bg-[var(--color-surface-raised)] px-1.5 pb-2 pt-2 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
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
          const active = !isFast && m.id === value
          // Models past the group's free allotment are charged in credits — show
          // the relative rate (×N) next to the name instead of locking (§ credits).
          const showMultiplier = Boolean(m.uses_credits) && typeof m.multiplier === 'number' && m.multiplier > 0
          return (
            <DropdownMenuItem
              key={m.id}
              onSelect={() => pickModel(m.id)}
              className="items-start py-2.5 gap-2"
            >
              <ModelIcon icon={m.icon} size={16} className="mt-0.5" />
              <div className="flex flex-col gap-1 flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-[var(--color-fg)] truncate">{m.label}</span>
                  {showMultiplier ? (
                    <span
                      className="ml-auto shrink-0 rounded-full bg-[var(--color-bg-muted)] px-1.5 py-0.5 text-[10.5px] font-medium tabular-nums text-[var(--color-fg-muted)]"
                      title={t('modelPicker.creditRate', { defaultValue: 'Credit rate' })}
                    >
                      ×{m.multiplier!.toFixed(1)}
                    </span>
                  ) : active ? (
                    <span
                      className="ml-auto size-1.5 rounded-full bg-[var(--color-accent)] shrink-0"
                      aria-label={t('modelPicker.current')}
                    />
                  ) : null}
                </div>
                {m.description ? (
                  <span className="text-[11.5px] text-[var(--color-fg-muted)] leading-snug">
                    {m.description}
                  </span>
                ) : null}
              </div>
            </DropdownMenuItem>
          )
        })}
        {visibleModels.length === 0 && activeTag !== null && (
          <div className="px-2.5 py-3 text-center text-[12px] text-[var(--color-fg-subtle)]">
            {t('modelPicker.noneForTag')}
          </div>
        )}

        {/* §4.20 image models — picking one puts the conversation in drawing mode. */}
        {imageModels.length > 0 && (activeTag === null) && (
          <>
            <DropdownMenuLabel className="mt-1 border-t border-[var(--color-divider)] pt-2">
              {t('modelPicker.imageSection', { defaultValue: 'Image generation' })}
            </DropdownMenuLabel>
            {imageModels.map((m) => {
              const active = !isFast && m.id === value
              // Per-image credit price (§4.20): when the model's free allotment is
              // spent, show "N credits" after the name instead of the active dot —
              // mirrors the chat section's ×multiplier badge.
              const credits = typeof m.credits_per_image === 'number' ? m.credits_per_image : 0
              const showCredits = Boolean(m.uses_credits) && credits > 0
              return (
                <DropdownMenuItem key={m.id} onSelect={() => pickModel(m.id)} className="items-start gap-2 py-2.5">
                  <ModelIcon icon={m.icon} size={16} className="mt-0.5" />
                  <div className="flex min-w-0 flex-1 flex-col gap-1">
                    <div className="flex items-center gap-2">
                      <span className="truncate font-medium text-[var(--color-fg)]">{m.label}</span>
                      {showCredits ? (
                        <span
                          className="ml-auto shrink-0 rounded-full bg-[var(--color-bg-muted)] px-1.5 py-0.5 text-[10.5px] font-medium tabular-nums text-[var(--color-fg-muted)]"
                          title={t('modelPicker.creditsPerImage', { defaultValue: 'Credits per image' })}
                        >
                          {String(Math.round(credits * 100) / 100)} {t('modelPicker.creditsUnit', { defaultValue: 'credits' })}
                        </span>
                      ) : active ? (
                        <span
                          className="ml-auto size-1.5 shrink-0 rounded-full bg-[var(--color-accent)]"
                          aria-label={t('modelPicker.current')}
                        />
                      ) : null}
                    </div>
                    {m.description ? (
                      <span className="text-[11.5px] leading-snug text-[var(--color-fg-muted)]">{m.description}</span>
                    ) : null}
                  </div>
                </DropdownMenuItem>
              )
            })}
          </>
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
