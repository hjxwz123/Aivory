import { useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'
import { ChevronDown } from 'lucide-react'
import { Composer } from '@/components/chat/composer'
import { SuggestionCard } from '@/components/chat/suggestion-card'
import { MyGallery } from '@/components/chat/my-gallery'
import { SUGGESTIONS } from '@/data/suggestions'
import { useConversations } from '@/store/conversations'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import { cn } from '@/lib/utils'
import type { Attachment, Conversation } from '@/types/chat'

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
  const createConversation = useConversations((s) => s.createConversation)
  const sendMessage = useConversations((s) => s.sendMessage)
  const setModel = useConversations((s) => s.setModel)
  const defaultModelId = useModels((s) => s.defaultId)
  const imageModels = useModels((s) => s.imageModels)
  const user = useAuth((s) => s.user)

  // §4.20: the sidebar "Draw" entry links here with ?mode=draw to open the
  // composer pre-set to an image model (drawing mode).
  const [searchParams] = useSearchParams()
  const drawMode = searchParams.get('mode') === 'draw'
  const drawDefault = drawMode && imageModels[0] ? imageModels[0].id : ''

  // The model the user picks in the composer before the conversation exists.
  // Falls back to the draw default (if any), then the async-loaded chat default,
  // so a new chat honours the picker instead of always using the default model.
  const [pickedModelId, setPickedModelId] = useState<string | null>(null)
  const modelId = pickedModelId ?? (drawDefault || defaultModelId)

  // When the user attaches a file BEFORE sending, we must create the
  // conversation up front so the upload is scoped + RAG-ingested (§4.11.2).
  // Stash it here so the eventual send reuses the SAME conversation instead of
  // spawning a second empty one.
  const pendingConvRef = useRef<Conversation | null>(null)

  // Lazily create (once) the conversation the first attachment will be scoped
  // to. Idempotent: repeat attaches in the same draft reuse the same id. Does
  // NOT navigate — that happens on send, so attaching a file doesn't yank the
  // user off the home screen mid-compose.
  async function ensureConversation(): Promise<string | undefined> {
    if (pendingConvRef.current) return pendingConvRef.current.id
    const conv = await createConversation(modelId)
    if (!conv) return undefined
    pendingConvRef.current = conv
    return conv.id
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

  // Entrance choreography — the home screen used to pop in flat. Now the
  // heading, lead, composer and suggestion cards rise + fade in sequence, with a
  // whisper-faint accent glow breathing behind the greeting for depth. All gated
  // behind prefers-reduced-motion via gsap.matchMedia (reduced → static, fully
  // visible). useGSAP sets the `from` state before paint, so there's no flash.
  const root = useRef<HTMLDivElement>(null)
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
    } = {},
  ) {
    // Reuse the conversation created up front for an attachment (so its uploads
    // stay scoped/ingested); otherwise create a fresh one now.
    const conv = pendingConvRef.current ?? (await createConversation(modelId))
    pendingConvRef.current = null
    if (!conv) return
    // The picker is the source of truth for a new chat. A conversation created
    // earlier for an attachment may carry a stale model, so persist the picked
    // model onto it before sending; the first turn always uses `modelId` directly
    // (never the conversation's possibly-stale model).
    if (modelId && conv.modelId !== modelId) {
      void setModel(conv.id, modelId)
    }
    navigate(`/chat/${conv.id}`)
    // Fire-and-forget the stream; the ChatThread page will react to store updates.
    void sendMessage({
      conversationId: conv.id,
      text,
      modelId,
      attachments,
      mode: opts.mode,
      params: opts.params,
      imageStyleId: opts.imageStyleId,
    })
  }

  return (
    <div ref={root} className="relative flex-1 flex flex-col overflow-y-auto">
      {/* Ambient warmth behind the greeting — faint, blurred, slowly breathing. */}
      <div
        className="home-glow pointer-events-none absolute left-1/2 top-[14%] -z-0 size-[20rem] sm:size-[34rem] max-w-[88vw] -translate-x-1/2 rounded-full bg-[var(--color-accent)] opacity-[0.07] blur-[90px]"
        aria-hidden
      />
      <div
        className={cn(
          'relative z-10 mx-auto w-full max-w-[var(--layout-message-max-w)] px-[var(--layout-gutter-mobile)] sm:px-8 pt-6 pb-8 sm:py-12 flex flex-col',
          // Phones top-align (composer stays in thumb reach, keyboard-safe);
          // ≥sm centers the hero vertically. Drawing mode always top-aligns so the
          // gallery below the composer is reachable by scrolling.
          !drawMode && 'flex-1 sm:justify-center',
        )}
      >
        <header className="text-center">
          <h1 className="home-rise font-sans font-semibold tracking-tight text-[1.6rem] sm:text-[2.5rem] leading-[1.14] sm:leading-[1.12] text-[var(--color-fg)] text-balance">
            {greeting}{' '}
            <span className="text-[var(--color-fg-muted)] font-normal">{subtitle}</span>
          </h1>
          <p
            className={cn(
              'home-rise mt-3.5 text-[var(--color-fg-muted)] text-sm sm:text-base text-pretty mx-auto max-w-2xl',
              // The lead is a desktop nicety; on a phone it just pushes the input
              // down, so hide it for chat (drawing mode keeps its instruction).
              !drawMode && 'max-sm:hidden',
            )}
          >
            {drawMode
              ? t('empty.drawLead', { defaultValue: 'Describe what you want to create — your gallery is below.' })
              : t('empty.lead')}
          </p>
        </header>

        <div className="home-rise mt-7 sm:mt-10 mx-auto w-full max-w-[var(--layout-message-max-w)]">
          <Composer
            modelId={modelId}
            onModelChange={setPickedModelId}
            onSubmit={(text, atts, opts) => void startNew(text, atts, opts)}
            ensureConversationId={ensureConversation}
            autoFocus
          />
        </div>

        {drawMode ? (
          /* §4.20 drawing mode: no suggestion prompts — a gentle scroll cue, then
             the gallery "plate section" (which owns its editorial header + reveal).
             Wider than the composer column so images get a larger field. */
          <div className="mt-16 w-full sm:mt-20">
            <div className="home-rise mb-12 flex justify-center text-[var(--color-fg-faint)]">
              <ChevronDown size={18} strokeWidth={1.5} aria-hidden className="animate-[bob_1.6s_ease-in-out_infinite]" />
            </div>
            <MyGallery />
          </div>
        ) : (
          <div className="mt-8 sm:mt-10 mx-auto w-full max-w-[var(--layout-message-max-w)]">
            {/* Single row, fixed-width cards, horizontally scrollable (snap). The
                scrollbar is hidden; cards overflow the rail and swipe. On phones
                the rail bleeds to the screen edges so the next card peeks. */}
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
    </div>
  )
}
