import { type PointerEvent as ReactPointerEvent, type ReactNode, useMemo, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'
import { ArrowRight, ChevronDown, Menu, MessageSquare, RefreshCw, ShieldOff } from 'lucide-react'
import { Tooltip } from '@/components/ui/tooltip'
import { UserMenu } from '@/components/sidebar/sidebar'
import { MyGallery } from '@/components/chat/my-gallery'
import { SUGGESTIONS, type Suggestion } from '@/data/suggestions'
import { useAuth } from '@/store/auth'
import { useConversations, sameConvListShape } from '@/store/conversations'
import { useUI } from '@/store/ui'
import { useWorkspaces } from '@/store/workspaces'
import { usePrivateChatPermission } from '@/hooks/use-private-chat-permission'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { cn, formatTimeAgo } from '@/lib/utils'
import { runViewTransition } from '@/lib/view-transition'

gsap.registerPlugin(useGSAP)

/**
 * Navigation state for switching between the ordinary home and private mode.
 * Both render this same layout, so the swap skips the entrance choreography and
 * only the composer visibly changes.
 */
export const HOME_SWAP_STATE = { homeSwap: true } as const

function isHomeSwap(state: unknown): boolean {
  return typeof state === 'object' && state !== null && (state as { homeSwap?: unknown }).homeSwap === true
}

const PROMPT_POINTER_COOLDOWN_MS = 650
const PROMPT_POINTER_DISTANCE_PX = 48

function initialPromptIndex(variants: string[]): number {
  // The third localized variant is "What can I help you with?". Keep that
  // recognizable line as the initial banner, then let interaction reveal the
  // rest of the set.
  return Math.min(2, Math.max(variants.length - 1, 0))
}

function RotatingHomePrompt({ variants, label }: { variants: string[]; label: string }) {
  const root = useRef<HTMLButtonElement>(null)
  const currentTextRef = useRef<HTMLSpanElement>(null)
  const incomingTextRef = useRef<HTMLSpanElement>(null)
  const [currentIndex, setCurrentIndex] = useState(() => initialPromptIndex(variants))
  const [incomingIndex, setIncomingIndex] = useState<number | null>(null)
  const currentIndexRef = useRef(currentIndex)
  const transitionRunningRef = useRef(false)
  const lastRotationAtRef = useRef(Number.NEGATIVE_INFINITY)
  const pointerAnchorRef = useRef<{ x: number; y: number } | null>(null)

  const safeCurrentIndex = variants.length > 0 ? currentIndex % variants.length : 0
  currentIndexRef.current = safeCurrentIndex

  const showNext = () => {
    if (variants.length < 2 || transitionRunningRef.current) return
    const nextIndex = (currentIndexRef.current + 1) % variants.length
    const reducedMotion =
      typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reducedMotion) {
      currentIndexRef.current = nextIndex
      setCurrentIndex(nextIndex)
      return
    }
    transitionRunningRef.current = true
    setIncomingIndex(nextIndex)
  }

  useGSAP(
    () => {
      if (incomingIndex === null || !currentTextRef.current || !incomingTextRef.current) return
      const current = currentTextRef.current
      const incoming = incomingTextRef.current
      gsap.set(incoming, { yPercent: 58, autoAlpha: 0 })
      const timeline = gsap.timeline({
        onComplete: () => {
          currentIndexRef.current = incomingIndex
          setCurrentIndex(incomingIndex)
          setIncomingIndex(null)
          transitionRunningRef.current = false
        },
      })
      timeline
        .to(current, { yPercent: -46, autoAlpha: 0, duration: 0.16, ease: 'power2.in' }, 0)
        .to(incoming, { yPercent: 0, autoAlpha: 1, duration: 0.24, ease: 'power3.out' }, 0.05)
      return () => timeline.kill()
    },
    { scope: root, dependencies: [incomingIndex], revertOnUpdate: true },
  )

  const handlePointerEnter = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.pointerType !== 'mouse') return
    pointerAnchorRef.current = { x: event.clientX, y: event.clientY }
    const now = performance.now()
    if (now - lastRotationAtRef.current < PROMPT_POINTER_COOLDOWN_MS) return
    lastRotationAtRef.current = now
    showNext()
  }

  const handlePointerMove = (event: ReactPointerEvent<HTMLButtonElement>) => {
    if (event.pointerType !== 'mouse') return
    const anchor = pointerAnchorRef.current
    if (!anchor) {
      pointerAnchorRef.current = { x: event.clientX, y: event.clientY }
      return
    }
    const distance = Math.hypot(event.clientX - anchor.x, event.clientY - anchor.y)
    const now = performance.now()
    if (distance < PROMPT_POINTER_DISTANCE_PX || now - lastRotationAtRef.current < PROMPT_POINTER_COOLDOWN_MS) return
    pointerAnchorRef.current = { x: event.clientX, y: event.clientY }
    lastRotationAtRef.current = now
    showNext()
  }

  const handleClick = () => {
    const now = performance.now()
    // Pointer entry already changed the line. Ignore the immediate synthetic
    // click, while retaining click/tap and keyboard activation as fallbacks.
    if (now - lastRotationAtRef.current < PROMPT_POINTER_COOLDOWN_MS) return
    lastRotationAtRef.current = now
    showNext()
  }

  const currentText = variants[safeCurrentIndex] ?? ''
  const incomingText = incomingIndex === null ? null : variants[incomingIndex] ?? ''

  return (
    <button
      ref={root}
      type="button"
      aria-label={`${currentText}. ${label}`}
      onPointerEnter={handlePointerEnter}
      onPointerMove={handlePointerMove}
      onPointerLeave={() => {
        pointerAnchorRef.current = null
      }}
      onClick={handleClick}
      className="inline-flex max-w-full cursor-pointer align-baseline rounded-[6px] font-normal text-[var(--color-fg-muted)] interactive hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--color-bg)]"
    >
      <span className="inline-grid max-w-full overflow-hidden text-balance">
        {variants.map((variant, index) => (
          <span
            key={`${index}:${variant}`}
            aria-hidden
            className="invisible pointer-events-none col-start-1 row-start-1"
          >
            {variant}
          </span>
        ))}
        <span
          ref={currentTextRef}
          aria-live="polite"
          aria-atomic="true"
          className="col-start-1 row-start-1 will-change-transform"
        >
          {currentText}
        </span>
        {incomingText !== null ? (
          <span
            ref={incomingTextRef}
            aria-hidden
            className="col-start-1 row-start-1 will-change-transform"
          >
            {incomingText}
          </span>
        ) : null}
      </span>
    </button>
  )
}

function greetingKey(): 'morning' | 'afternoon' | 'evening' | 'stillUp' {
  const h = new Date().getHours()
  if (h < 5) return 'stillUp'
  if (h < 12) return 'morning'
  if (h < 18) return 'afternoon'
  if (h < 22) return 'evening'
  return 'stillUp'
}

const SUGGESTIONS_PER_PAGE = 4

function shuffled<T>(items: readonly T[]): T[] {
  const result = [...items]
  for (let i = result.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1))
    ;[result[i], result[j]] = [result[j], result[i]]
  }
  return result
}

interface SuggestionDeck {
  order: Suggestion[]
  page: number
}

// One shuffled deck per page load. Keeping it outside the component means the
// home and private-mode screens (and repeat visits) show the same set until
// the user asks for another batch.
let sessionDeck: SuggestionDeck | null = null

function currentDeck(): SuggestionDeck {
  sessionDeck ??= { order: shuffled(SUGGESTIONS), page: 0 }
  return sessionDeck
}

function useSuggestionDeck() {
  const [deck, setDeck] = useState(currentDeck)
  const pages = Math.max(1, Math.ceil(deck.order.length / SUGGESTIONS_PER_PAGE))
  const visible = deck.order.slice(deck.page * SUGGESTIONS_PER_PAGE, (deck.page + 1) * SUGGESTIONS_PER_PAGE)
  const next = () => {
    // Walk the remaining pages before reshuffling, so consecutive batches
    // never repeat a suggestion.
    const nextDeck = deck.page + 1 < pages
      ? { order: deck.order, page: deck.page + 1 }
      : { order: shuffled(SUGGESTIONS), page: 0 }
    sessionDeck = nextDeck
    setDeck(nextDeck)
  }
  return { visible, next, canShuffle: pages > 1 || deck.order.length > SUGGESTIONS_PER_PAGE, key: `${deck.page}:${deck.order[0]?.id ?? ''}` }
}

/**
 * Compact prompt starters. Picking one places its prompt in the composer —
 * several prompts reference material the user still has to add, so they are
 * never sent on the user's behalf.
 */
function HomeSuggestions({ onPick, layout }: { onPick: (prompt: string) => void; layout: 'wrap' | 'rail' }) {
  const { t } = useTranslation('chat')
  const { visible, next, canShuffle, key } = useSuggestionDeck()
  // Only batches the user asked for fade in; the first one is part of the
  // page's entrance choreography.
  const [shuffledOnce, setShuffledOnce] = useState(false)
  const rail = layout === 'rail'

  return (
    <div
      className={cn(
        'flex gap-2',
        rail
          ? '-mx-3 overflow-x-auto overscroll-x-contain px-3 pb-2.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden'
          : 'flex-wrap items-center justify-center',
      )}
    >
      {visible.map((suggestion) => {
        const Icon = suggestion.icon
        return (
          <button
            key={`${key}:${suggestion.id}`}
            type="button"
            onClick={() => onPick(t(suggestion.promptKey))}
            className={cn(
              'home-card group/suggestion inline-flex h-9 shrink-0 items-center gap-2 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] pl-3 pr-3.5 text-[13px] text-[var(--color-fg-muted)]',
              // No `interactive` here: its opacity/transform transitions would
              // fight the GSAP entrance that animates those same properties.
              'transition-[color,border-color,background-color] duration-[var(--duration-fast)] ease-[var(--ease-out)] hover:border-[var(--color-border-strong)] hover:text-[var(--color-fg)]',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              shuffledOnce && 'page-enter',
            )}
          >
            <Icon
              size={14}
              aria-hidden
              className="shrink-0 text-[var(--color-fg-subtle)] transition-colors duration-[var(--duration-fast)] group-hover/suggestion:text-[var(--color-accent)]"
            />
            <span className="whitespace-nowrap">{t(suggestion.titleKey)}</span>
          </button>
        )
      })}
      {canShuffle ? (
        <Tooltip content={t('empty.shuffleSuggestions')}>
          <button
            type="button"
            aria-label={t('empty.shuffleSuggestions')}
            onClick={() => {
              setShuffledOnce(true)
              next()
            }}
            className="home-card inline-flex size-9 shrink-0 items-center justify-center rounded-full text-[var(--color-fg-subtle)] transition-[color,background-color] duration-[var(--duration-fast)] ease-[var(--ease-out)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <RefreshCw size={14} aria-hidden />
          </button>
        </Tooltip>
      ) : null}
    </div>
  )
}

const RECENT_CONVERSATIONS = 2

/**
 * "Pick up where you left off": the latest conversations in the current
 * space, one click from the new-chat screen.
 */
function HomeRecentConversations() {
  const { t, i18n } = useTranslation('chat')
  const activeWorkspaceId = useWorkspaces((s) => s.activeId)
  // Summary-only subscription (see sidebar): streamed tokens don't re-render.
  const conversations = useConversations((s) => s.conversations, sameConvListShape)
  const recent = useMemo(
    () =>
      conversations
        .filter((conversation) =>
          !conversation.archived &&
          !conversation.inline &&
          (conversation.workspaceId ?? '') === (activeWorkspaceId ?? ''),
        )
        .slice()
        .sort((a, b) => b.updatedAt - a.updatedAt)
        .slice(0, RECENT_CONVERSATIONS),
    [activeWorkspaceId, conversations],
  )
  if (recent.length === 0) return null

  return (
    <nav aria-label={t('empty.continue')} className="mx-auto mt-9 w-full max-w-[30rem]">
      <p className="home-card mb-1 px-3 text-[12px] font-medium text-[var(--color-fg-subtle)]">{t('empty.continue')}</p>
      <ul>
        {recent.map((conversation) => (
          <li key={conversation.id}>
            <Link
              to={`/chat/${conversation.id}`}
              className="home-card group/recent flex h-9 items-center gap-2.5 rounded-[10px] px-3 text-[13px] text-[var(--color-fg-muted)] transition-colors duration-[var(--duration-fast)] ease-[var(--ease-out)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <MessageSquare size={14} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
              <span className="min-w-0 flex-1 truncate">{conversation.title || t('share.untitled')}</span>
              <span className="shrink-0 text-[12px] tabular-nums text-[var(--color-fg-subtle)] group-hover/recent:hidden group-focus-visible/recent:hidden">
                {formatTimeAgo(conversation.updatedAt, i18n.language)}
              </span>
              <ArrowRight
                size={13}
                aria-hidden
                className="hidden shrink-0 text-[var(--color-fg-muted)] group-hover/recent:block group-focus-visible/recent:block"
              />
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  )
}

interface HomeLayoutProps {
  /** `private` renders the same screen with the private toggle engaged. */
  variant: 'chat' | 'draw' | 'private'
  /** Rendered once, in either the phone or the desktop slot. */
  composer: ReactNode
  /** Shown directly above the composer (e.g. a private-mode request error). */
  notice?: ReactNode
  /** Puts a suggestion's prompt into the composer. Omit to hide suggestions. */
  onSuggestion?: (prompt: string) => void
}

/**
 * The new-conversation screen: greeting, composer, suggestions and the corner
 * actions. The ordinary home, drawing mode and private mode all render it, so
 * entering private mode swaps the composer and nothing else.
 */
export function HomeLayout({ variant, composer, notice, onSuggestion }: HomeLayoutProps) {
  const { t } = useTranslation('chat')
  const navigate = useNavigate()
  const location = useLocation()
  const user = useAuth((s) => s.user)
  const { allowed: canUsePrivateChat } = usePrivateChatPermission()
  const isPhone = useMediaQuery(mediaQuery.phone)
  const drawMode = variant === 'draw'
  const privateMode = variant === 'private'
  // Read once: a swap between home and private mode must not replay the
  // entrance when either side re-renders.
  const [skipEntrance] = useState(() => isHomeSwap(location.state))

  const firstName = (user?.name || user?.email?.split('@')[0] || 'friend').split(' ')[0]
  // Greeting depends on the active language; recompute whenever t changes.
  const greeting = useMemo(
    () => `${t(`greeting.${greetingKey()}`)}, ${firstName}.`,
    [t, firstName],
  )
  // The prompt banner starts with the familiar help question, then cycles
  // through the localized alternatives when the user moves across it.
  const subtitleVariants = useMemo(() => {
    const raw = t('empty.subtitleVariants', { returnObjects: true }) as unknown
    const pool = Array.isArray(raw) && raw.length > 0 ? (raw as string[]) : [t('empty.subtitle')]
    return pool
  }, [t])

  // Entrance choreography — the heading, lead, composer and suggestions rise +
  // fade in sequence, with a whisper-faint accent glow breathing behind the
  // greeting for depth. All gated behind prefers-reduced-motion via
  // gsap.matchMedia (reduced → static, fully visible). useGSAP sets the `from`
  // state before paint, so there's no flash.
  const root = useRef<HTMLDivElement>(null)
  // Drawing mode: the gallery sits below the centered hero; the scroll cue jumps
  // to it, and the gallery itself defers loading until scrolled into view.
  const galleryRef = useRef<HTMLDivElement>(null)
  useGSAP(
    () => {
      const mm = gsap.matchMedia()
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        if (!skipEntrance) {
          const tl = gsap.timeline({ defaults: { ease: 'power3.out' } })
          // opacity (not autoAlpha) so the composer stays focusable while fading —
          // autoAlpha's visibility:hidden would swallow the textarea's autoFocus.
          tl.from('.home-rise', { y: 16, opacity: 0, duration: 0.6, stagger: 0.09 })
            .from('.home-card', { y: 10, opacity: 0, duration: 0.45, stagger: 0.05 }, '-=0.3')
            // Land at the faint 0.07 the class defines (autoAlpha would force 1).
            .fromTo('.home-glow', { opacity: 0, scale: 0.9 }, { opacity: 0.07, scale: 1, duration: 1.1 }, 0)
        }
        gsap.to('.home-glow', {
          scale: 1.12,
          opacity: '+=0.04',
          duration: 7,
          ease: 'sine.inOut',
          repeat: -1,
          yoyo: true,
          delay: skipEntrance ? 0 : 1.1,
        })
      })
    },
    { scope: root },
  )

  const greetingHeading = (className: string) => (
    <h1 className={className}>
      {greeting}{' '}
      <RotatingHomePrompt variants={subtitleVariants} label={t('empty.changeSubtitle')} />
    </h1>
  )

  const privateLabel = privateMode ? t('private.exit') : t('private.enter')

  return (
    <div
      ref={root}
      className={cn(
        'relative flex-1 flex flex-col overflow-hidden sm:overflow-y-auto sm:overflow-x-hidden',
        // Drawing keeps its existing scrollable gallery on phones. The normal
        // chat home below is a fixed-height mobile workspace instead.
        drawMode && 'max-sm:overflow-y-auto',
      )}
    >
      {/* Mobile home: a compact, direct way to reach the navigation drawer. */}
      <button
        type="button"
        aria-label={t('commandMenu.actions.toggleSidebar')}
        onClick={() => useUI.getState().setNavOpen(true)}
        className="lg:hidden absolute left-3 top-3 z-20 inline-flex size-[var(--tap-min)] items-center justify-center rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:left-2 max-sm:top-2 max-sm:size-10 max-sm:rounded-[8px]"
      >
        <Menu size={17} aria-hidden />
      </button>
      {/* Top-right: the private-mode toggle (engaged while private) and, on
          mobile, the account avatar that opens the sidebar footer's menu. */}
      <div className="absolute right-3 top-3 z-20 flex items-center gap-2 max-sm:right-2 max-sm:top-2">
        {canUsePrivateChat && (
          <Tooltip content={privateLabel}>
            <button
              type="button"
              aria-label={privateLabel}
              aria-pressed={privateMode}
              // Only the composer differs between the two screens, so the swap
              // runs as a view transition that cross-fades it in place.
              onClick={() => {
                void runViewTransition('private', () =>
                  flushSync(() => navigate(privateMode ? '/' : '/private-chat', { state: HOME_SWAP_STATE })),
                )
              }}
              className={cn(
                'inline-flex size-11 items-center justify-center rounded-[10px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                privateMode
                  ? 'bg-[var(--color-accent-soft)] text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)] hover:text-[var(--color-accent-hover)]'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <ShieldOff size={19} aria-hidden />
            </button>
          </Tooltip>
        )}
        <div className="lg:hidden"><UserMenu placement="header" /></div>
      </div>
      {/* Desktop-only ambient depth; the phone layout stays deliberately direct. */}
      <div
        className="home-glow pointer-events-none absolute left-1/2 top-[14%] -z-0 hidden size-[34rem] max-w-[88vw] -translate-x-1/2 rounded-full bg-[var(--color-accent)] opacity-[0.07] blur-[90px] sm:block"
        aria-hidden
      />

      {/* Phone chat home: welcome copy occupies the available center space while
          the suggestions and composer remain in a dedicated bottom work area. */}
      {!drawMode && isPhone && (
        <div className="relative z-10 flex min-h-0 flex-1 flex-col px-3 sm:hidden">
          <header className="flex min-h-0 flex-1 flex-col items-center justify-center pb-8 pt-12 text-center">
            {greetingHeading('home-rise max-w-[18rem] text-balance font-sans text-[1.6rem] font-semibold leading-[1.14] tracking-tight text-[var(--color-fg)]')}
          </header>
          <div className="home-rise shrink-0 pb-2">
            {onSuggestion ? <HomeSuggestions layout="rail" onPick={onSuggestion} /> : null}
            {notice}
            {composer}
          </div>
        </div>
      )}

      {(drawMode || !isPhone) && <div
        className={cn(
          'relative z-10 mx-auto min-h-full w-full max-w-[var(--layout-message-max-w)] flex-col px-[var(--layout-gutter-mobile)] sm:px-8',
          'flex',
        )}
      >
        {/* HERO — greeting + composer, vertically centered in the first screenful
            (both chat and drawing mode, PC and mobile). In drawing mode it caps at
            ~one viewport so the gallery sits just below the fold. */}
        <div className={cn('flex flex-col', drawMode ? 'min-h-[90dvh]' : 'flex-1')}>
          <div className="flex flex-1 flex-col justify-center py-10 sm:py-12">
            <header className="text-center">
              {greetingHeading('home-rise font-sans font-semibold tracking-tight text-[1.6rem] sm:text-[2.5rem] leading-[1.14] sm:leading-[1.12] text-[var(--color-fg)] text-balance')}
              <p
                className={cn(
                  'home-rise mt-3.5 text-[var(--color-fg-muted)] text-sm sm:text-base text-pretty mx-auto max-w-2xl',
                  // The lead is a desktop nicety; on a phone it just pushes the
                  // input down, so hide it for chat (drawing mode keeps its line).
                  !drawMode && 'max-sm:hidden',
                )}
              >
                {drawMode
                  ? t('empty.drawLead', { defaultValue: 'Describe what you want to create — your gallery is below.' })
                  : t('empty.lead')}
              </p>
            </header>

            {/* Fixed, comfortable width — deliberately NOT --layout-message-max-w,
                so the home input doesn't widen with the appearance → chat-width
                ("full") setting (that governs the conversation column, not this). */}
            <div className="home-rise mt-7 sm:mt-10 mx-auto w-full max-w-[44rem]">
              {notice}
              {composer}
            </div>

            {!drawMode && (
              <div className="mt-5 mx-auto w-full max-w-[44rem]">
                {onSuggestion ? <HomeSuggestions layout="wrap" onPick={onSuggestion} /> : null}
                <HomeRecentConversations />
                <p className="mt-8 text-center text-xs text-[var(--color-fg-subtle)]">
                  {t('empty.disclaimer')}
                </p>
              </div>
            )}
          </div>

          {/* Drawing mode: a bobbing cue at the bottom of the first screen that
              jumps to the (below-the-fold, lazily-loaded) gallery. */}
          {drawMode && (
            <button
              type="button"
              onClick={() => galleryRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
              aria-label={t('empty.galleryScrollCue', { defaultValue: '下拉查看我的画廊' })}
              className="home-rise mx-auto mb-6 inline-flex size-10 items-center justify-center rounded-full text-[var(--color-fg-faint)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <ChevronDown size={20} strokeWidth={1.5} aria-hidden className="animate-[bob_1.6s_ease-in-out_infinite]" />
            </button>
          )}
        </div>

        {/* §4.20 gallery — below the fold; defers its own image fetch until it
            scrolls into view (shows just the heading + a "scroll to view" hint). */}
        {drawMode && (
          <div ref={galleryRef} className="pb-16 sm:pb-20">
            <MyGallery />
          </div>
        )}
      </div>}
    </div>
  )
}
