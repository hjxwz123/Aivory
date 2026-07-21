import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { cn, truncate } from '@/lib/utils'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { mathContentToPlainText } from '@/lib/math-content'
import type { Conversation } from '@/types/chat'

interface ConversationMinimapProps {
  conversation: Conversation
  scrollContainerRef: RefObject<HTMLDivElement | null>
}

// Only surface the rail for genuinely long threads — a handful of turns navigate
// fine by scrolling. "Rounds" = user questions on the active line (the rail
// tracks the conversation LINE, never the branch tree).
const MIN_ROUNDS = 5
// A user turn becomes "current" once its heading has scrolled to within this many
// px of the thread viewport's top edge.
const ACTIVE_LINE_OFFSET = 96
// Preview label clip length (chars) before the reusable ellipsis helper trims.
const LABEL_MAX = 44

/**
 * A slim right-edge rail — one tick per user question — for jumping around a long
 * conversation. The tick of the turn currently in view is filled; hovering (or
 * keyboard-focusing) the rail reveals each question's text. Active path only, so
 * it stays a simple vertical line regardless of branches (§ minimap). Desktop-only
 * and hidden until the thread passes MIN_ROUNDS rounds.
 */
export function ConversationMinimap({ conversation, scrollContainerRef }: ConversationMinimapProps) {
  const { t } = useTranslation('chat')
  const isDesktop = useMediaQuery(mediaQuery.desktop)
  // The global reduced-motion CSS rule only sets the DEFAULT scroll-behavior; an
  // explicit JS `behavior:'smooth'` overrides it (CSSOM), so gate it ourselves.
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')

  const userTurns = useMemo(
    () => conversation.messages.filter((m) => m.role === 'user'),
    [conversation.messages],
  )
  const enabled = isDesktop && userTurns.length > MIN_ROUNDS
  // Key on user-turn ids only, so streaming tokens (which mutate assistant
  // content, not the turn set) don't churn the scroll-spy subscription.
  const turnsKey = useMemo(() => userTurns.map((m) => m.id).join('|'), [userTurns])

  const [activeId, setActiveId] = useState<string | null>(null)
  const navRef = useRef<HTMLElement>(null)
  const activeTickRef = useRef<HTMLButtonElement>(null)

  // Scroll-spy: the current turn is the LAST user question whose heading sits at
  // or above a line near the viewport top. The thread is reverse-paginated, so
  // only in-DOM anchors count — older windowed-out turns simply aren't "active"
  // until scrolled back into the mounted window. rAF-throttled.
  useEffect(() => {
    const container = scrollContainerRef.current
    if (!enabled || !container) return
    const ids = turnsKey ? turnsKey.split('|') : []
    let raf = 0
    const compute = () => {
      raf = 0
      const line = container.getBoundingClientRect().top + ACTIVE_LINE_OFFSET
      let active: string | null = null
      let firstMounted: string | null = null
      for (const id of ids) {
        const el = container.querySelector<HTMLElement>(`[data-message-id="${id}"]`)
        if (!el) continue
        if (firstMounted === null) firstMounted = id
        if (el.getBoundingClientRect().top <= line) active = id
        else break
      }
      const next = active ?? firstMounted
      setActiveId((prev) => (prev === next ? prev : next))
    }
    const onScroll = () => {
      if (!raf) raf = requestAnimationFrame(compute)
    }
    compute()
    container.addEventListener('scroll', onScroll, { passive: true })
    window.addEventListener('resize', onScroll)
    return () => {
      container.removeEventListener('scroll', onScroll)
      window.removeEventListener('resize', onScroll)
      if (raf) cancelAnimationFrame(raf)
    }
  }, [enabled, scrollContainerRef, turnsKey])

  // If the rail itself overflows (very long thread), keep the active tick visible
  // — scroll only the rail, never nudge the message thread.
  useEffect(() => {
    const nav = navRef.current
    const tick = activeTickRef.current
    if (!nav || !tick || nav.scrollHeight <= nav.clientHeight) return
    const nr = nav.getBoundingClientRect()
    const tr = tick.getBoundingClientRect()
    if (tr.top < nr.top) nav.scrollTop -= nr.top - tr.top + 8
    else if (tr.bottom > nr.bottom) nav.scrollTop += tr.bottom - nr.bottom + 8
  }, [activeId])

  if (!enabled) return null

  const scrollToTurn = (id: string) => {
    const container = scrollContainerRef.current
    if (!container) return
    const behavior: ScrollBehavior = reducedMotion ? 'auto' : 'smooth'
    const el = container.querySelector<HTMLElement>(`[data-message-id="${id}"]`)
    if (el) el.scrollIntoView({ behavior, block: 'start' })
    // Windowed-out (not yet mounted) target: fall back to the top, matching the
    // outline — scrolling up remounts older turns.
    else container.scrollTo({ top: 0, behavior })
  }

  return (
    <nav
      ref={navRef}
      aria-label={t('minimap.label', { defaultValue: 'Conversation map' })}
      className={cn(
        'group/rail absolute right-1.5 top-1/2 z-20 -translate-y-1/2',
        'flex max-h-[62vh] flex-col items-end gap-0.5 overflow-y-auto scrollbar-none',
        // One shared panel for the whole rail on hover/keyboard-focus — the rows
        // themselves stay chromeless (a border per row read as visual noise).
        'rounded-xl border border-transparent px-1 py-1',
        'transition-colors duration-fast ease-out',
        'hover:border-[var(--color-border)] hover:bg-[var(--color-surface)] hover:shadow-[var(--shadow-md)]',
        'focus-within:border-[var(--color-border)] focus-within:bg-[var(--color-surface)] focus-within:shadow-[var(--shadow-md)]',
      )}
    >
      {userTurns.map((m, i) => {
        const active = m.id === activeId
        const text =
          truncate(mathContentToPlainText(m.content ?? '').trim(), LABEL_MAX) ||
          t('minimap.untitled', { index: i + 1, defaultValue: 'Question {{index}}' })
        return (
          <button
            key={m.id}
            ref={active ? activeTickRef : undefined}
            type="button"
            onClick={() => scrollToTurn(m.id)}
            aria-current={active ? 'true' : undefined}
            aria-label={`${t('minimap.jumpTo', { defaultValue: 'Jump to this question' })}: ${text}`}
            className={cn(
              'flex items-center justify-end rounded-md px-1 py-0.5 outline-none',
              'transition-colors duration-fast ease-out',
              // Row affordance without chrome: the shared rail panel carries the
              // border; the hovered row only tints.
              'hover:bg-[var(--color-bg-muted)]',
              'focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            )}
          >
            {/* Question preview — clipped to zero width until the rail is hovered
                or focused, then it slides open with a small right margin. */}
            <span
              className={cn(
                'max-w-0 overflow-hidden whitespace-nowrap text-xs leading-none',
                'transition-[max-width,margin] duration-base ease-out',
                'group-hover/rail:mr-2 group-hover/rail:max-w-64',
                'group-focus-within/rail:mr-2 group-focus-within/rail:max-w-64',
                active ? 'font-medium text-[var(--color-fg)]' : 'text-[var(--color-fg-muted)]',
              )}
            >
              {text}
            </span>
            {/* Tick */}
            <span
              aria-hidden
              className={cn(
                'block h-0.5 shrink-0 rounded-full transition-all duration-fast ease-out',
                active
                  ? 'w-5 bg-[var(--color-fg)]'
                  : 'w-3.5 bg-[var(--color-border)] group-hover/rail:bg-[var(--color-fg-subtle)]',
              )}
            />
          </button>
        )
      })}
    </nav>
  )
}
