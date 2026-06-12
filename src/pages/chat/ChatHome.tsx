import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Composer } from '@/components/chat/composer'
import { SuggestionCard } from '@/components/chat/suggestion-card'
import { SUGGESTIONS } from '@/data/suggestions'
import { useConversations } from '@/store/conversations'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import type { Attachment } from '@/types/chat'

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
  const defaultModelId = useModels((s) => s.defaultId)
  const user = useAuth((s) => s.user)

  const firstName = (user?.name || user?.email?.split('@')[0] || 'friend').split(' ')[0]
  // Greeting depends on the active language; recompute whenever t changes.
  const greeting = useMemo(
    () => `${t(`greeting.${greetingKey()}`)}, ${firstName}.`,
    [t, firstName],
  )
  const cards = useMemo(() => fisherYatesPick(SUGGESTIONS, 4), [])

  async function startNew(text: string, attachments: Attachment[], opts: { params?: Record<string, unknown> } = {}) {
    const conv = await createConversation(defaultModelId)
    if (!conv) return
    navigate(`/chat/${conv.id}`)
    // Fire-and-forget the stream; the ChatThread page will react to store updates.
    void sendMessage({
      conversationId: conv.id,
      text,
      modelId: conv.modelId || defaultModelId,
      attachments,
      params: opts.params,
    })
  }

  return (
    <div className="flex-1 flex flex-col overflow-y-auto">
      <div className="mx-auto w-full max-w-[var(--layout-message-max-w)] px-5 sm:px-8 pt-16 sm:pt-24 pb-8 flex-1 flex flex-col">
        <header className="text-center">
          <h1 className="font-serif tracking-tight text-[2rem] sm:text-[2.5rem] leading-[1.08] text-[var(--color-fg)] text-balance">
            {greeting}
            <br />
            <span className="text-[var(--color-fg-muted)] italic">{t('empty.subtitle')}</span>
          </h1>
          <p className="mt-3.5 text-[var(--color-fg-muted)] text-sm sm:text-base text-pretty mx-auto max-w-md">
            {t('empty.lead')}
          </p>
        </header>

        <div className="mt-10 mx-auto w-full max-w-[44rem]">
          <Composer
            modelId={defaultModelId}
            onModelChange={() => undefined}
            onSubmit={(text, atts, opts) => void startNew(text, atts, opts)}
            autoFocus
          />
        </div>

        <div className="mt-10 mx-auto w-full max-w-[44rem]">
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {cards.map((s) => {
              const title = t(s.titleKey)
              const prompt = t(s.promptKey)
              return (
                <SuggestionCard
                  key={s.id}
                  icon={s.icon}
                  title={title}
                  prompt={prompt}
                  onClick={() => void startNew(prompt, [])}
                />
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
