import { useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Composer } from '@/components/chat/composer'
import { SuggestionCard } from '@/components/chat/suggestion-card'
import { SUGGESTIONS } from '@/data/suggestions'
import { useConversations } from '@/store/conversations'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import type { Attachment, Conversation } from '@/types/chat'

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
  const user = useAuth((s) => s.user)

  // The model the user picks in the composer before the conversation exists.
  // Falls back to the (async-loaded) default until they choose, so a new chat
  // honours the picker instead of always using the default model.
  const [pickedModelId, setPickedModelId] = useState<string | null>(null)
  const modelId = pickedModelId ?? defaultModelId

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
  const cards = useMemo(() => fisherYatesPick(SUGGESTIONS, 6), [])

  async function startNew(
    text: string,
    attachments: Attachment[],
    opts: { mode?: 'default' | 'deep-research' | 'canvas'; params?: Record<string, unknown> } = {},
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
    })
  }

  return (
    <div className="flex-1 flex flex-col overflow-y-auto">
      <div className="mx-auto w-full max-w-[var(--layout-message-max-w)] px-5 sm:px-8 py-12 flex-1 flex flex-col justify-center">
        <header className="text-center">
          <h1 className="font-sans font-semibold tracking-tight text-[2rem] sm:text-[2.5rem] leading-[1.12] text-[var(--color-fg)] text-balance">
            {greeting}{' '}
            <span className="text-[var(--color-fg-muted)] font-normal">{t('empty.subtitle')}</span>
          </h1>
          <p className="mt-3.5 text-[var(--color-fg-muted)] text-sm sm:text-base text-pretty mx-auto max-w-2xl">
            {t('empty.lead')}
          </p>
        </header>

        <div className="mt-10 mx-auto w-full max-w-[44rem]">
          <Composer
            modelId={modelId}
            onModelChange={setPickedModelId}
            onSubmit={(text, atts, opts) => void startNew(text, atts, opts)}
            ensureConversationId={ensureConversation}
            autoFocus
          />
        </div>

        <div className="mt-10 mx-auto w-full max-w-[44rem]">
          {/* Single row, fixed-width cards, horizontally scrollable (snap). The
              scrollbar is hidden; cards overflow the 44rem rail and swipe. */}
          <div className="flex gap-3 overflow-x-auto px-1 -mx-1 pb-2 snap-x snap-mandatory [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
            {cards.map((s) => {
              const title = t(s.titleKey)
              const prompt = t(s.promptKey)
              return (
                <div key={s.id} className="w-[15.5rem] shrink-0 snap-start">
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
          <p className="mt-6 text-center text-[11px] text-[var(--color-fg-subtle)]">
            {t('empty.disclaimer')}
          </p>
        </div>
      </div>
    </div>
  )
}
