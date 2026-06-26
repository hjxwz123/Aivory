import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { MessagesSquare, X, CornerDownLeft, Quote } from 'lucide-react'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { Markdown } from '@/components/chat/markdown'
import { useInlineThreadDrawer } from '@/store/inline-thread'
import { useConversations } from '@/store/conversations'
import { useSettings } from '@/store/settings'
import { useMediaQuery } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'

/**
 * InlineThreadPanel — the right-edge drawer that renders a text-selection
 * sub-conversation (§ inline threads). Mirrors HtmlPreviewPanel's layout
 * (desktop inline aside / mobile Sheet) and shares the right edge with it
 * (mutual exclusion enforced in the stores). The child conversation streams
 * through the normal conversations store, so answers never touch the main thread.
 */
export function InlineThreadPanel() {
  const open = useInlineThreadDrawer((s) => s.open)
  const childId = useInlineThreadDrawer((s) => s.childId)
  const quote = useInlineThreadDrawer((s) => s.quote)
  const close = useInlineThreadDrawer((s) => s.close)
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const { t } = useTranslation('chat')
  const { pathname } = useLocation()

  const loadOne = useConversations((s) => s.loadOne)
  useEffect(() => {
    if (!open || !childId) return
    // Only fetch when we don't already have the thread locally (i.e. opening an
    // EXISTING thread from a marker). A freshly-created thread is populated by
    // its live stream — refetching would race it and orphan the streaming reply
    // (the stuck "thinking…" bubble).
    const conv = useConversations.getState().conversations.find((c) => c.id === childId)
    if (conv && (conv.messages.length > 0 || conv.messages.some((m) => m.streaming))) return
    // Inline threads render without the scroll-up older-fetch UI, so load the
    // whole (short) sub-conversation up front.
    void loadOne(childId, { full: true })
  }, [open, childId, loadOne])

  // Leaving the conversation closes the drawer.
  const prevPath = useRef(pathname)
  useEffect(() => {
    if (prevPath.current === pathname) return
    prevPath.current = pathname
    close()
  }, [pathname, close])

  if (isDesktop) {
    if (!open) return null
    return (
      <aside
        aria-label={t('inline.title', { defaultValue: 'Sub-conversation' })}
        className={cn(
          'hidden lg:flex flex-col shrink-0 h-full w-[clamp(22rem,34vw,34rem)]',
          'border-l border-[var(--color-divider)] bg-[var(--color-bg)]',
          'animate-[panel-in_240ms_var(--ease-out)]',
        )}
      >
        <ThreadBody quote={quote} childId={childId} onClose={close} />
      </aside>
    )
  }

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) close() }}>
      <SheetContent side="right" size="lg" label={t('inline.title', { defaultValue: 'Sub-conversation' })} className="w-[min(28rem,94vw)]">
        <ThreadBody quote={quote} childId={childId} onClose={close} />
      </SheetContent>
    </Sheet>
  )
}

function ThreadBody({ quote, childId, onClose }: { quote: string; childId: string | null; onClose: () => void }) {
  const { t } = useTranslation('chat')
  const conv = useConversations((s) => s.conversations.find((c) => c.id === childId))
  const sendMessage = useConversations((s) => s.sendMessage)
  const userMessageMarkdown = useSettings((s) => s.appearance.userMessageMarkdown)
  const [draft, setDraft] = useState('')
  const listRef = useRef<HTMLDivElement>(null)

  const messages = conv?.messages ?? []
  const streaming = messages.some((m) => m.streaming)

  // Keep pinned to the latest answer as it streams.
  useEffect(() => {
    const el = listRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [conv?.messages])

  function submit() {
    const text = draft.trim()
    if (!text || !childId) return
    setDraft('')
    void sendMessage({ conversationId: childId, text })
  }

  return (
    <>
      <header className="flex items-center gap-2 h-12 px-3 border-b border-[var(--color-divider)] shrink-0">
        <MessagesSquare size={14} aria-hidden className="text-[var(--color-secondary)]" />
        <span className="flex-1 min-w-0 truncate font-serif tracking-tight text-[15px] text-[var(--color-fg)]">
          {t('inline.title', { defaultValue: 'Sub-conversation' })}
        </span>
        <Tooltip content={t('code.previewClose', { defaultValue: 'Close' })}>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('code.previewClose', { defaultValue: 'Close' })}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <X size={14} aria-hidden />
          </button>
        </Tooltip>
      </header>

      {/* Anchored excerpt */}
      {quote ? (
        <div className="shrink-0 px-4 py-3 border-b border-[var(--color-divider)] bg-[var(--color-secondary-soft)]/40">
          <div className="flex gap-2">
            <Quote size={13} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-secondary)]" />
            <p className="text-[12.5px] leading-relaxed text-[var(--color-fg-muted)] line-clamp-4">{quote}</p>
          </div>
        </div>
      ) : null}

      {/* Messages */}
      <div ref={listRef} className="flex-1 min-h-0 overflow-y-auto scrollbar-thin px-4 py-4 flex flex-col gap-4">
        {messages.length === 0 ? (
          <p className="text-[13px] text-[var(--color-fg-subtle)]">
            {t('inline.empty', { defaultValue: 'Ask anything about the highlighted passage.' })}
          </p>
        ) : (
          messages.map((m) =>
            m.role === 'user' ? (
              <div
                key={m.id}
                className={cn(
                  'self-end max-w-[85%] rounded-[12px] bg-[var(--color-bg-muted)] px-3 py-2 text-[13.5px] text-[var(--color-fg)]',
                  userMessageMarkdown ? 'min-w-0' : 'whitespace-pre-wrap',
                )}
              >
                {userMessageMarkdown ? (
                  <Markdown content={m.content} blockKeyPrefix={`${m.id}-user-inline`} className="prose-user" />
                ) : (
                  m.content
                )}
              </div>
            ) : (
              <div key={m.id} className="self-start w-full text-[13.5px] text-[var(--color-fg)]">
                <Markdown content={m.content} live={Boolean(m.streaming)} blockKeyPrefix={m.id} />
                {m.streaming && !m.content ? (
                  <span className="text-[12px] text-[var(--color-fg-subtle)]">{t('common:common.thinking', { defaultValue: 'Thinking' })}…</span>
                ) : null}
              </div>
            ),
          )
        )}
      </div>

      {/* Composer */}
      <div className="shrink-0 border-t border-[var(--color-divider)] p-3">
        <div className="flex items-end gap-1.5">
          <textarea
            rows={1}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                submit()
              }
            }}
            placeholder={t('inline.placeholder', { defaultValue: 'Ask about this…' })}
            className="flex-1 min-w-0 resize-none max-h-32 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-[13.5px] text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          />
          <button
            type="button"
            onClick={submit}
            disabled={!draft.trim() || streaming}
            aria-label={t('inline.send', { defaultValue: 'Send' })}
            className="inline-flex items-center justify-center size-9 shrink-0 rounded-[10px] bg-[var(--color-accent)] text-[var(--color-accent-fg)] interactive hover:opacity-90 disabled:opacity-40 disabled:pointer-events-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <CornerDownLeft size={15} aria-hidden />
          </button>
        </div>
      </div>
    </>
  )
}
