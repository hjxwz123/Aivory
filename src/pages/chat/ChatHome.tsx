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
import { useConversations, resolveArmedTurnFlags } from '@/store/conversations'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import { useUI } from '@/store/ui'
import { useComposerPrefs } from '@/store/composer-prefs'
import { useWorkspaces } from '@/store/workspaces'
import { conversationsApi } from '@/api'
import { cn } from '@/lib/utils'
import {
  clearPendingConversation,
  pendingConversationKey,
  readPendingConversation,
  writePendingConversation,
} from '@/lib/pending-conversation'
import type { Attachment } from '@/types/chat'
import type { ApiConversation } from '@/api/types'
import type { ToolMode } from '@/lib/tool-mode'

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
  const workspaceId = useWorkspaces((s) => s.activeId ?? undefined)
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
  const savedImageModelId =
    typeof user?.settings?.image_model_id === 'string' ? user.settings.image_model_id : ''
  const savedImageModelAvailable = imageModels.some((model) => model.id === savedImageModelId)
  const drawDefault = drawMode ? (savedImageModelAvailable ? savedImageModelId : imageModels[0]?.id ?? '') : ''
  const pendingStorageKey = useMemo(
    () => pendingConversationKey(user?.id, draftScope, workspaceId),
    [draftScope, user?.id, workspaceId],
  )

  // The model the user picks in the composer before the conversation exists.
  // Falls back to the draw default (if any), then the async-loaded chat default,
  // so a new chat honours the picker instead of always using the default model.
  const [pickedModelId, setPickedModelId] = useState<string | null>(null)
  const modelId = pickedModelId ?? (drawDefault || defaultModelId)
  // §fast-mode: new chats default to 快速 when a fast model is configured. Draw
  // mode (image models) is always 进阶.
  const fastAvailable = useModels((s) => s.fastAvailable)
  const [pickedFast, setPickedFast] = useState<boolean | null>(null)
  const fast = !drawMode && (pickedFast ?? fastAvailable)

  // When the user attaches a file BEFORE sending, we must create the
  // conversation up front so the upload is scoped + RAG-ingested (§4.11.2).
  // Stash it here so the eventual send reuses the SAME conversation instead of
  // spawning a second empty one. Created OUTSIDE the store on purpose: the
  // draft stays off the sidebar (no "Untitled" row from merely attaching) and
  // only enters the cache on send (adoptConversation). Its id is persisted so
  // a refresh can reclaim the draft without exposing it in the sidebar.
  const pendingConvRef = useRef<ApiConversation | null>(null)
  const pendingCreateRef = useRef<Promise<string | undefined> | null>(null)
  const pendingConsumedRef = useRef(false)
  // Set when the composer drains its last attachment while the lazy create is
  // still in flight — the create then discards its own conversation on landing
  // instead of installing a draft nobody references ("Untitled ghost").
  const draftAbandonedRef = useRef(false)
  const mountedRef = useRef(true)
  // Read synchronously on the first render so Composer starts in its restoring
  // state and cannot submit an attachment-less turn before recovery finishes.
  const [pendingConversationId, setPendingConversationId] = useState<string | undefined>(() =>
    readPendingConversation(pendingStorageKey),
  )
  const pendingStorageKeyRef = useRef(pendingStorageKey)
  pendingStorageKeyRef.current = pendingStorageKey
  // Guards startNew against a double fire (rapid re-click, two suggestion cards)
  // spawning duplicate conversations + sends.
  const startedRef = useRef(false)
  const adoptConversation = useConversations((s) => s.adoptConversation)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  // Reclaim an attachment-created conversation after a refresh. Keep the id in
  // durable browser storage instead of deleting it from pagehide: unload-time
  // DELETE requests are inherently racy and used to discard minutes of parsing.
  useEffect(() => {
    pendingConvRef.current = null
    pendingCreateRef.current = null
    pendingConsumedRef.current = false
    const savedID = readPendingConversation(pendingStorageKey)
    setPendingConversationId(savedID)
    if (!savedID) return
    let cancelled = false
    const recovery = (async () => {
      try {
        const loaded = await conversationsApi.get(savedID, { limit: 1 })
        if (loaded.messages.length > 0) {
          clearPendingConversation(pendingStorageKey)
          if (!cancelled) setPendingConversationId(undefined)
          return undefined
        }
        if (cancelled) return savedID
        pendingConvRef.current = loaded.conversation
        setPendingConversationId(savedID)
        return savedID
      } catch {
        clearPendingConversation(pendingStorageKey)
        if (!cancelled) setPendingConversationId(undefined)
        return undefined
      }
    })()
    pendingCreateRef.current = recovery
    void recovery.finally(() => {
      if (pendingCreateRef.current === recovery) pendingCreateRef.current = null
    })
    return () => {
      cancelled = true
    }
  }, [pendingStorageKey])

  // Lazily create (once) the conversation the first attachment will be scoped
  // to. Idempotent: repeat attaches in the same draft reuse the same id — the
  // in-flight promise is memoized so two quick attaches share ONE create. Does
  // NOT navigate — that happens on send, so attaching a file doesn't yank the
  // user off the home screen mid-compose. Returning undefined on failure lets
  // the composer fall back to a scope-less (non-RAG) upload instead of
  // uploading against a fabricated id the server would reject.
  function ensureConversation(): Promise<string | undefined> {
    // A fresh attach revives an abandoned draft scope (see discardDraftConversation).
    draftAbandonedRef.current = false
    if (pendingConvRef.current) return Promise.resolve(pendingConvRef.current.id)
    if (!pendingCreateRef.current) {
      const storageKey = pendingStorageKey
      const creation = (async () => {
        try {
          const created = await conversationsApi.create({
            model_id: modelId || undefined,
            workspace_id: workspaceId,
          })
          // A suggestion card can bypass the composer's upload gate while this
          // create is in flight. A mode/workspace switch also invalidates this
          // scope before any upload starts. So does removing every attachment
          // before the create lands (draft abandoned).
          if (pendingConsumedRef.current || draftAbandonedRef.current || pendingStorageKeyRef.current !== storageKey) {
            void conversationsApi.remove(created.id).catch(() => {})
            return undefined
          }
          writePendingConversation(storageKey, created.id)
          if (!mountedRef.current) return created.id
          pendingConvRef.current = created
          setPendingConversationId(created.id)
          return created.id
        } catch {
          return undefined
        }
      })()
      pendingCreateRef.current = creation
      void creation.finally(() => {
        if (pendingCreateRef.current === creation) pendingCreateRef.current = null
      })
    }
    return pendingCreateRef.current
  }

  // The composer removed its LAST attachment: the draft conversation existed
  // purely to scope those uploads, so delete it — otherwise it lingers
  // server-side forever and surfaces as an "Untitled" row on the next sidebar
  // load. A create still in flight is flagged instead (it self-discards on
  // landing); a subsequent attach simply creates a fresh scope.
  function discardDraftConversation() {
    if (pendingConsumedRef.current) return
    draftAbandonedRef.current = true
    const pending = pendingConvRef.current
    pendingConvRef.current = null
    clearPendingConversation(pendingStorageKey)
    setPendingConversationId(undefined)
    if (pending) void conversationsApi.remove(pending.id).catch(() => {})
  }

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

  // The suggestion rail is a single horizontally-scrollable row. A plain mouse
  // wheel only scrolls vertically, so without this it can't be scrolled with the
  // wheel at all — only by dragging / trackpad-panning. Translate a dominant
  // vertical wheel delta into horizontal scroll, yielding back to the page once
  // the rail reaches an edge so vertical page scroll is never trapped. Native
  // (non-passive) listener: React attaches wheel passively, so an onWheel
  // preventDefault would be ignored.
  const suggestionsRailRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = suggestionsRailRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (el.scrollWidth <= el.clientWidth) return
      if (Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return
      // deltaMode 1 = lines (Firefox wheel) — normalise to pixels.
      const delta = e.deltaMode === 1 ? e.deltaY * 24 : e.deltaY
      const atStart = el.scrollLeft <= 0
      const atEnd = el.scrollLeft + el.clientWidth >= el.scrollWidth - 1
      if ((delta < 0 && atStart) || (delta > 0 && atEnd)) return
      el.scrollLeft += delta
      e.preventDefault()
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [drawMode])

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
      toolMode: ToolMode
      webSearch?: boolean
      officialToolNames?: string[]
      fast?: boolean
    },
  ) {
    if (startedRef.current) return
    startedRef.current = true
    if (!pendingConvRef.current && pendingCreateRef.current) {
      await pendingCreateRef.current
    }
    // Mark any attachment draft as consumed so an overlapping lazy create does
    // not install a second conversation.
    pendingConsumedRef.current = true

    // Attachment flow: the conversation was already created server-side on
    // attach (so uploads were scoped/ingested). Adopt it into the store, then
    // send. This path already navigates instantly (no create round-trip here).
    const pending = pendingConvRef.current
    if (pending) {
      pendingConvRef.current = null
      setPendingConversationId(undefined)
      clearPendingConversation(pendingStorageKey)
      const conv = adoptConversation(pending)
      // The picker is the source of truth: a conversation created earlier for an
      // attachment may carry a stale model, so persist the picked one first.
      // §fast-mode: skip this for a fast turn — setModel would clear conv.fast; the
      // send's fast flag drives the (hidden) model server-side instead.
      if (!opts.fast && modelId && conv.modelId !== modelId) void setModel(conv.id, modelId)
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
        toolMode: opts.toolMode,
        webSearch: opts.webSearch,
        officialToolNames: opts.officialToolNames,
        fast: opts.fast,
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
      toolMode: opts.toolMode,
      webSearch: opts.webSearch,
      officialToolNames: opts.officialToolNames,
      fast: opts.fast,
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
                fast={fast}
                onFastChange={setPickedFast}
                onSubmit={(text, atts, opts) => void startNew(text, atts, opts)}
                draftScope={draftScope}
                conversationId={pendingConversationId}
                ensureConversationId={ensureConversation}
                onAttachmentsDrained={discardDraftConversation}
                autoFocus
              />
            </div>

            {!drawMode && (
              <div className="mt-8 sm:mt-10 mx-auto w-full max-w-[44rem]">
                {/* Single row, fixed-width cards, horizontally scrollable (snap +
                    mouse wheel, see suggestionsRailRef). Scrollbar hidden; on phones
                    the rail bleeds to the screen edges so the next card peeks. The
                    top/bottom padding leaves room for each card's hover lift +
                    shadow, which `overflow-x-auto` (which also clips the y axis)
                    would otherwise cut off. */}
                <div
                  ref={suggestionsRailRef}
                  className="flex gap-3 overflow-x-auto px-1 -mx-1 max-sm:-mx-[var(--layout-gutter-mobile)] max-sm:px-[var(--layout-gutter-mobile)] max-sm:scroll-px-[var(--layout-gutter-mobile)] pt-2 pb-2 snap-x snap-mandatory [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
                >
                  {cards.map((s) => {
                    const title = t(s.titleKey)
                    const prompt = t(s.promptKey)
                    return (
                      <div key={s.id} className="home-card w-[13.5rem] sm:w-[15.5rem] shrink-0 snap-start">
                        <SuggestionCard
                          icon={s.icon}
                          title={title}
                          prompt={prompt}
                          onClick={() => void startNew(prompt, [], { ...resolveArmedTurnFlags(modelId), fast })}
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
