import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { CornerDownLeft, Quote } from 'lucide-react'
import { ChatSidePanel, ChatSidePanelHeader } from '@/components/chat/chat-side-panel'
import { Markdown } from '@/components/chat/markdown'
import { MathText } from '@/components/chat/math-text'
import { useInlineThreadDrawer } from '@/store/inline-thread'
import { resolveArmedTurnFlags, useConversations } from '@/store/conversations'
import { useSettings } from '@/store/settings'
import { useWorkspaces } from '@/store/workspaces'
import { cn } from '@/lib/utils'
import { hasMathContent } from '@/lib/math-content'

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

  const title = t('inline.title', { defaultValue: 'Sub-conversation' })
  return (
    <ChatSidePanel open={open} title={title} onClose={close}>
      <ThreadBody quote={quote} childId={childId} onClose={close} />
    </ChatSidePanel>
  )
}

function ThreadBody({ quote, childId, onClose }: { quote: string; childId: string | null; onClose: () => void }) {
  const { t } = useTranslation('chat')
  const conv = useConversations((s) => s.conversations.find((c) => c.id === childId))
  const isWorkspaceGuest = useWorkspaces((s) =>
    conv?.workspaceId
      ? s.workspaces.find((workspace) => workspace.id === conv.workspaceId)?.role === 'guest'
      : false,
  )
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
    if (isWorkspaceGuest || !text || !childId) return
    setDraft('')
    const armed = resolveArmedTurnFlags(conv?.modelId, childId)
    void sendMessage({
      conversationId: childId,
      text,
      mode: armed.mode,
      verify: armed.verify,
      toolMode: armed.toolMode,
      webSearch: armed.webSearch,
      selectedToolIds: armed.selectedToolIds,
    })
  }

  return (
    <>
      <ChatSidePanelHeader
        title={t('inline.title', { defaultValue: 'Sub-conversation' })}
        closeLabel={t('code.previewClose', { defaultValue: 'Close' })}
        onClose={onClose}
      />

      {/* Anchored excerpt */}
      {quote ? (
        <div className="mx-3 mb-1 shrink-0 rounded-[8px] bg-[var(--color-secondary-soft)]/40 px-3 py-2.5">
          <div className="flex gap-2">
            <Quote size={13} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-secondary)]" />
            <p className="line-clamp-4 font-sans text-[12.5px] leading-relaxed tracking-normal text-[var(--color-fg-muted)]">{quote}</p>
          </div>
        </div>
      ) : null}

      {/* Messages */}
      <div ref={listRef} className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden scrollbar-thin px-4 py-4 flex flex-col gap-4">
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
                  userMessageMarkdown || hasMathContent(m.content) ? 'min-w-0' : 'whitespace-pre-wrap',
                )}
              >
                {userMessageMarkdown ? (
                  <Markdown content={m.content} blockKeyPrefix={`${m.id}-user-inline`} className="prose-user" breaks />
                ) : hasMathContent(m.content) ? (
                  <MathText content={m.content} />
                ) : (
                  m.content
                )}
              </div>
            ) : (
              <div key={m.id} className="self-start w-full text-[13.5px] text-[var(--color-fg)]">
                <Markdown
                  content={m.content}
                  artifacts={m.artifacts}
                  live={Boolean(m.streaming)}
                  blockKeyPrefix={m.id}
                />
                {m.streaming && !m.content ? (
                  <span className="text-[12px] text-[var(--color-fg-subtle)]">{t('common:common.thinking', { defaultValue: 'Thinking' })}…</span>
                ) : null}
              </div>
            ),
          )
        )}
      </div>

      {/* Composer */}
      {isWorkspaceGuest ? (
        <div className="shrink-0 bg-[var(--color-bg-muted)]/55 px-3 py-3 text-center text-[12px] text-[var(--color-fg-muted)]">
          {t('workspace.readOnlyBody', { defaultValue: 'You are a guest in this workspace. You can read shared conversations but not send messages.' })}
        </div>
      ) : <div className="shrink-0 bg-[var(--color-bg)] p-3">
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
            className="flex-1 min-w-0 resize-none max-h-32 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2 text-[13.5px] text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)] outline-none focus:border-[var(--color-border-strong)] focus:bg-[var(--color-surface)]"
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
      </div>}
    </>
  )
}
