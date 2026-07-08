import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'
import { ChevronDown, Menu } from 'lucide-react'
import { Composer } from '@/components/chat/composer'
import { SuggestionCard } from '@/components/chat/suggestion-card'
import { MyGallery } from '@/components/chat/my-gallery'
import { SUGGESTIONS } from '@/data/suggestions'
import { useConversations } from '@/store/conversations'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import { useUI } from '@/store/ui'
import { useComposerPrefs } from '@/store/composer-prefs'
import { activeWorkspaceId } from '@/store/workspaces'
import { apiUrl, conversationsApi, getAccessToken } from '@/api'
import { cn } from '@/lib/utils'
import type { Attachment } from '@/types/chat'
import type { ApiConversation } from '@/api/types'

gsap.registerPlugin(useGSAP)

function fisherYatesPick<T>(arr: T[], count: number): T[] {
  const a = [...arr]
  for (let i = a.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1))
    ;[a[i], a[j]] = [a[j], a[i]]
  }
  return a.slice(0, count)
}

function greetingKey(): 'morning' | 'afternoon' | 'evening' | 'stillUp' {
  const h = new Date().getHours()
  if (h < 5) return 'stillUp'
  if (h < 12) return 'morning'
  if (h < 18) return 'afternoon'
  if (h < 22) return 'evening'
  return 'stillUp'
}

export default function ChatHome() {
  const navigate = useNavigate()
  const { t } = useTranslation('chat')
  const beginOptimisticConversation = useConversations((s) => s.beginOptimisticConversation)
  const sendMessage = useConversations((s) => s.sendMessage)
  const setModel = useConversations((s) => s.setModel)
  const defaultModelId = useModels((s) => s.defaultId)
  const imageModels = useModels((s) => s.imageModels)
  const user = useAuth((s) => s.user)
  const clearComposerDraft = useComposerPrefs((s) => s.clearDraft)

  // The home screen has no title to show, so on mobile it drops the layout's
  // standalone brand bar entirely (§ mobile home redesign) — a light floating
  // button below replaces it for opening the sidebar drawer.
  useEffect(() => {
    useUI.getState().setPageOwnsTopBar(true)
    return () => useUI.getState().setPageOwnsTopBar(false)
  }, [])

  // §4.20: the sidebar "Draw" entry links here with ?mode=draw to open the
  // composer pre-set to an image model (drawing mode).
  const [searchParams] = useSearchParams()
  const drawMode = searchParams.get('mode') === 'draw'
  const draftScope = drawMode ? 'new-draw' : 'new-chat'
  const drawDefault = drawMode && imageModels[0] ? imageModels[0].id : ''

  // The model the user picks in the composer before the conversation exists.
  // Falls back to the draw default (if any), then the async-loaded chat default,
  // so a new chat honours the picker instead of always using the default model.
  const [pickedModelId, setPickedModelId] = useState<string | null>(null)
  const modelId = pickedModelId ?? (drawDefault || defaultModelId)

  // When the user attaches a file BEFORE sending, we must create the
  // conversation up front so the upload is scoped + RAG-ingested (§4.11.2).
  // Stash it here so the eventual send reuses the SAME conversation instead of
  // spawning a second empty one. Created OUTSIDE the store on purpose: the
  // draft stays off the sidebar (no "Untitled" row from merely attaching) and
  // only enters the cache on send (adoptConversation). If the draft is
  // abandoned instead, the cleanup below deletes it server-side.
  const pendingConvRef = useRef<ApiConversation | null>(null)
  const pendingCreateRef = useRef<Promise<string | undefined> | null>(null)
  const pendingConsumedRef = useRef(false)
  // Guards startNew against a double fire (rapid re-click, two suggestion cards)
  // spawning duplicate conversations + sends.
  const startedRef = useRef(false)
  // True once this mount's cleanup ran — a create that resolves AFTER the user
  // left the page must delete itself instead of stashing into a dead ref.
  const disposedRef = useRef(false)
  const adoptConversation = useConversations((s) => s.adoptConversation)

  // Lazily create (once) the conversation the first attachment will be scoped
  // to. Idempotent: repeat attaches in the same draft reuse the same id — the
  // in-flight promise is memoized so two quick attaches share ONE create. Does
  // NOT navigate — that happens on send, so attaching a file doesn't yank the
  // user off the home screen mid-compose. Returning undefined on failure lets
  // the composer fall back to a scope-less (non-RAG) upload instead of
  // uploading against a fabricated id the server would reject.
  function ensureConversation(): Promise<string | undefined> {
    if (pendingConvRef.current) return Promise.resolve(pendingConvRef.current.id)
    if (!pendingCreateRef.current) {
      pendingCreateRef.current = (async () => {
        try {
          const created = await conversationsApi.create({
            model_id: modelId || undefined,
            workspace_id: activeWorkspaceId(),
          })
          // The user may have navigated away, or sent meanwhile (a suggestion
          // card bypasses the composer's upload gate), while the create was in
          // flight — this draft must delete itself instead of leaking.
          if (disposedRef.current || pendingConsumedRef.current) {
            void conversationsApi.remove(created.id).catch(() => {})
            return undefined
          }
          pendingConvRef.current = created
          return created.id
        } catch {
          return undefined
        } finally {
          pendingCreateRef.current = null
        }
      })()
    }
    return pendingCreateRef.current
  }

  // Abandoned draft cleanup — navigating away from home (unmount) or closing
  // the tab (pagehide, keepalive) deletes a draft conversation that never got
  // its first message, so attach-then-leave doesn't leak "Untitled" rows.
  useEffect(() => {
    // Reset on (re)mount — StrictMode's dev double-mount reuses the refs, so a
    // stale true here would make every future draft delete itself.
    disposedRef.current = false
    const cleanup = (keepalive: boolean) => {
      const draft = pendingConvRef.current
      if (!draft || pendingConsumedRef.current) return
      pendingConvRef.current = null
      if (keepalive) {
        const token = getAccessToken()
        void fetch(apiUrl(`/conversations/${encodeURIComponent(draft.id)}`), {
          method: 'DELETE',
          keepalive: true,
          credentials: 'include',
          headers: token ? { Authorization: `Bearer ${token}` } : undefined,
        }).catch(() => {})
      } else {
        void conversationsApi.remove(draft.id).catch(() => {})
      }
    }
    const onPageHide = (e: PageTransitionEvent) => {
      // bfcache round-trip: the page may be RESTORED with its attachment state
      // intact — deleting the draft would orphan it. Accept the rare leak.
      if (e.persisted) return
      cleanup(true)
    }
    window.addEventListener('pagehide', onPageHide)
    return () => {
      window.removeEventListener('pagehide', onPageHide)
      disposedRef.current = true
      cleanup(false)
    }
  }, [])

  const firstName = (user?.name || user?.email?.split('@')[0] || 'friend').split(' ')[0]
  // Greeting depends on the active language; recompute whenever t changes.
  const greeting = useMemo(
    () => `${t(`greeting.${greetingKey()}`)}, ${firstName}.`,
    [t, firstName],
  )
  // The trailing question is no longer fixed — pick a fresh variant each time the
  // home screen mounts (and on language change) so the prompt feels alive.
  const subtitle = useMemo(() => {
    const raw = t('empty.subtitleVariants', { returnObjects: true }) as unknown
    const pool = Array.isArray(raw) && raw.length > 0 ? (raw as string[]) : [t('empty.subtitle')]
    return pool[Math.floor(Math.random() * pool.length)]
  }, [t])
  const cards = useMemo(() => fisherYatesPick(SUGGESTIONS, 6), [])

  // Entrance choreography — the home screen used to pop in flat. Now the
  // heading, lead, composer and suggestion cards rise + fade in sequence, with a
  // whisper-faint accent glow breathing behind the greeting for depth. All gated
  // behind prefers-reduced-motion via gsap.matchMedia (reduced → static, fully
  // visible). useGSAP sets the `from` state before paint, so there's no flash.
  const root = useRef<HTMLDivElement>(null)
  // Drawing mode: the gallery sits below the centered hero; the scroll cue jumps
  // to it, and the gallery itself defers loading until scrolled into view.
  const galleryRef = useRef<HTMLDivElement>(null)
  useGSAP(
    () => {
      const mm = gsap.matchMedia()
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        const tl = gsap.timeline({ defaults: { ease: 'power3.out' } })
        // opacity (not autoAlpha) so the composer stays focusable while fading —
        // autoAlpha's visibility:hidden would swallow the textarea's autoFocus.
        tl.from('.home-rise', { y: 16, opacity: 0, duration: 0.6, stagger: 0.09 })
          .from('.home-card', { y: 14, opacity: 0, duration: 0.5, stagger: 0.06 }, '-=0.28')
          // Land at the faint 0.07 the class defines (autoAlpha would force 1).
          .fromTo('.home-glow', { opacity: 0, scale: 0.9 }, { opacity: 0.07, scale: 1, duration: 1.1 }, 0)
        gsap.to('.home-glow', {
          scale: 1.12,
          opacity: '+=0.04',
          duration: 7,
          ease: 'sine.inOut',
          repeat: -1,
          yoyo: true,
          delay: 1.1,
        })
      })
    },
    { scope: root },
  )

  async function startNew(
    text: string,
    attachments: Attachment[],
    opts: {
      mode?: 'default' | 'deep-research' | 'canvas'
      params?: Record<string, unknown>
      imageStyleId?: string
      verify?: boolean
    } = {},
  ) {
    if (startedRef.current) return
    startedRef.current = true
    // Mark any attachment draft as consumed so its cleanup won't delete it.
    pendingConsumedRef.current = true

    // Attachment flow: the conversation was already created server-side on
    // attach (so uploads were scoped/ingested). Adopt it into the store, then
    // send. This path already navigates instantly (no create round-trip here).
    const pending = pendingConvRef.current
    if (pending) {
      pendingConvRef.current = null
      const conv = adoptConversation(pending)
      // The picker is the source of truth: a conversation created earlier for an
      // attachment may carry a stale model, so persist the picked one first.
      if (modelId && conv.modelId !== modelId) void setModel(conv.id, modelId)
      clearComposerDraft(draftScope)
      navigate(`/chat/${conv.id}`)
      void sendMessage({
        conversationId: conv.id,
        text,
        modelId,
        attachments,
        mode: opts.mode,
        params: opts.params,
        imageStyleId: opts.imageStyleId,
        verify: opts.verify,
      })
      return
    }

    // No attachment: navigate to the thread INSTANTLY on an optimistic (temp-id)
    // conversation, and let sendMessage create the real one server-side, re-key
    // the cache, and swap the id in the URL. So the user lands on the thread the
    // moment they hit send — never staring at the home screen during the create
    // round-trip (and never re-clicking because "nothing happened").
    const tempId = beginOptimisticConversation(text, modelId)
    clearComposerDraft(draftScope)
    navigate(`/chat/${tempId}`)
    void sendMessage({
      conversationId: tempId,
      createFirst: true,
      text,
      modelId,
      attachments,
      mode: opts.mode,
      params: opts.params,
      imageStyleId: opts.imageStyleId,
      verify: opts.verify,
      // Swap temp→real id in the URL only if the user is STILL on the optimistic
      // thread. If they navigated elsewhere during the create round-trip, leave
      // them be — the stream still lands in the (re-keyed) real conversation,
      // reachable from the sidebar; yanking them would be worse than a stale URL.
      onConversationId: (realId) => {
        if (window.location.pathname === `/chat/${tempId}`) {
          navigate(`/chat/${realId}`, { replace: true })
        }
      },
    })
  }

  return (
    <div ref={root} className="relative flex-1 flex flex-col overflow-y-auto overflow-x-hidden">
      {/* Mobile: no title bar on the home screen — this light floating button is
          the only way to reach the sidebar drawer here (§ mobile home redesign). */}
      <button
        type="button"
        aria-label={t('commandMenu.actions.toggleSidebar')}
        onClick={() => useUI.getState().setNavOpen(true)}
        className="lg:hidden absolute left-3 top-3 z-20 inline-flex items-center justify-center size-[var(--tap-min)] rounded-[10px] bg-[var(--color-bg)]/85 backdrop-blur-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <Menu size={18} aria-hidden />
      </button>
      {/* Ambient warmth behind the greeting — faint, blurred, slowly breathing. */}
      <div
        className="home-glow pointer-events-none absolute left-1/2 top-[14%] -z-0 size-[20rem] sm:size-[34rem] max-w-[88vw] -translate-x-1/2 rounded-full bg-[var(--color-accent)] opacity-[0.07] blur-[90px]"
        aria-hidden
      />
      <div className="relative z-10 mx-auto flex min-h-full w-full max-w-[var(--layout-message-max-w)] flex-col px-[var(--layout-gutter-mobile)] sm:px-8">
        {/* HERO — greeting + composer, vertically centered in the first screenful
            (both chat and drawing mode, PC and mobile). In drawing mode it caps at
            ~one viewport so the gallery sits just below the fold. */}
        <div className={cn('flex flex-col', drawMode ? 'min-h-[90dvh]' : 'flex-1')}>
          <div className="flex flex-1 flex-col justify-center py-10 sm:py-12">
            <header className="text-center">
              <h1 className="home-rise font-sans font-semibold tracking-tight text-[1.6rem] sm:text-[2.5rem] leading-[1.14] sm:leading-[1.12] text-[var(--color-fg)] text-balance">
                {greeting}{' '}
                <span className="text-[var(--color-fg-muted)] font-normal">{subtitle}</span>
              </h1>
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
              <Composer
                modelId={modelId}
                onModelChange={setPickedModelId}
                onSubmit={(text, atts, opts) => void startNew(text, atts, opts)}
                draftScope={draftScope}
                ensureConversationId={ensureConversation}
                autoFocus
              />
            </div>

            {!drawMode && (
              <div className="mt-8 sm:mt-10 mx-auto w-full max-w-[44rem]">
                {/* Single row, fixed-width cards, horizontally scrollable (snap).
                    Scrollbar hidden; on phones the rail bleeds to the screen edges
                    so the next card peeks. */}
                <div className="flex gap-3 overflow-x-auto px-1 -mx-1 max-sm:-mx-[var(--layout-gutter-mobile)] max-sm:px-[var(--layout-gutter-mobile)] max-sm:scroll-px-[var(--layout-gutter-mobile)] pb-2 snap-x snap-mandatory [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
                  {cards.map((s) => {
                    const title = t(s.titleKey)
                    const prompt = t(s.promptKey)
                    return (
                      <div key={s.id} className="home-card w-[13.5rem] sm:w-[15.5rem] shrink-0 snap-start">
                        <SuggestionCard
                          icon={s.icon}
                          title={title}
                          prompt={prompt}
                          onClick={() => void startNew(prompt, [])}
                          className="h-full"
                        />
                      </div>
                    )
                  })}
                </div>
                <p className="mt-6 text-center text-xs text-[var(--color-fg-subtle)]">
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
      </div>
    </div>
  )
}
