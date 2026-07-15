import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { MessageCircleQuestion, CornerDownLeft } from 'lucide-react'
import { resolveArmedTurnFlags, useConversations } from '@/store/conversations'
import { useInlineThreadDrawer } from '@/store/inline-thread'
import { highlightThreads, threadIdForNode } from '@/lib/inline-highlight'

/**
 * InlineThreadLayer — owns the text-selection sub-conversation interactions for
 * one chat thread (§ inline threads):
 *  - highlights every message's annotated excerpts with clickable markers,
 *  - on selecting fresh text in an assistant message, floats an "Ask" affordance
 *    that expands into an inline question box,
 *  - on selecting / clicking an already-annotated excerpt, opens its drawer,
 *  - forbids annotating a substring of an existing thread (opens it instead).
 *
 * Rendered once inside ChatThread; the drawer itself lives in ChatLayout.
 */

interface PendingSel {
  messageId: string
  quote: string
  // Viewport coordinates of the selection end, for popover placement.
  x: number
  y: number
}

interface InlineThreadLayerProps {
  conversationId: string
  scrollRef: React.RefObject<HTMLDivElement | null>
}

export function InlineThreadLayer({ conversationId, scrollRef }: InlineThreadLayerProps) {
  const { t } = useTranslation('chat')
  const createInlineThread = useConversations((s) => s.createInlineThread)
  const sendMessage = useConversations((s) => s.sendMessage)
  const openThread = useInlineThreadDrawer((s) => s.openThread)

  // A compact signature of this conversation's inline threads. Re-renders only
  // when threads are added/removed/changed — not on every streamed token.
  const sig = useConversations((s) =>
    s.conversations
      .filter((c) => c.inline?.sourceConvId === conversationId)
      .map((c) => `${c.id}:${c.inline!.messageId}:${c.inline!.quote.length}`)
      .join('|'),
  )

  const [pending, setPending] = useState<PendingSel | null>(null)
  const [asking, setAsking] = useState(false)
  const [draft, setDraft] = useState('')
  const popoverRef = useRef<HTMLDivElement>(null)
  const observerRef = useRef<MutationObserver | null>(null)

  // ── Highlight markers ──────────────────────────────────────────────────────
  const rehighlight = useCallback(() => {
    const root = scrollRef.current
    if (!root) return
    const threads = useConversations
      .getState()
      .conversations.filter((c) => c.inline?.sourceConvId === conversationId)
    const byMsg = new Map<string, { id: string; quote: string }[]>()
    for (const c of threads) {
      const mid = c.inline!.messageId
      const list = byMsg.get(mid) ?? []
      list.push({ id: c.id, quote: c.inline!.quote })
      byMsg.set(mid, list)
    }
    // Pause our own observer while we (un)wrap <mark> elements, otherwise those
    // mutations re-trigger this pass in a perpetual loop. External DOM changes
    // still fire it because the observer is live between calls.
    observerRef.current?.disconnect()
    try {
      root.querySelectorAll<HTMLElement>('[data-inline-msg][data-inline-role="assistant"]').forEach((el) => {
        const mid = el.getAttribute('data-inline-msg') || ''
        highlightThreads(el, byMsg.get(mid) ?? [])
      })
    } finally {
      observerRef.current?.observe(root, { childList: true, subtree: true, characterData: true })
    }
  }, [conversationId, scrollRef])

  // Re-highlight when threads change, and whenever the message DOM mutates
  // (initial render, lazy-loaded older turns, finished streaming) — debounced.
  useEffect(() => {
    const root = scrollRef.current
    if (!root) return
    let timer: ReturnType<typeof setTimeout> | null = null
    const obs = new MutationObserver(() => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(rehighlight, 120)
    })
    observerRef.current = obs
    // Initial pass — markdown may still be painting, so also retry on the next
    // frame so a freshly-created thread's highlight reliably lands.
    rehighlight()
    const raf = requestAnimationFrame(rehighlight)
    return () => {
      if (timer) clearTimeout(timer)
      cancelAnimationFrame(raf)
      obs.disconnect()
      observerRef.current = null
    }
  }, [sig, rehighlight, scrollRef])

  // ── Selection + marker clicks ──────────────────────────────────────────────
  const dismiss = useCallback(() => {
    setPending(null)
    setAsking(false)
    setDraft('')
  }, [])

  useEffect(() => {
    function findMsgEl(node: Node | null): HTMLElement | null {
      let el = node instanceof HTMLElement ? node : node?.parentElement ?? null
      while (el) {
        if (el.hasAttribute?.('data-inline-msg')) return el
        el = el.parentElement
      }
      return null
    }

    function onMouseUp(e: MouseEvent) {
      // Clicks inside our own popover must not reset it.
      if (popoverRef.current?.contains(e.target as Node)) return

      // A click on an existing marker opens its thread.
      const markId = threadIdForNode(e.target as Node)
      if (markId) {
        openExistingThread(markId)
        dismiss()
        return
      }

      const sel = window.getSelection()
      const text = sel?.toString().trim() ?? ''
      if (!sel || sel.isCollapsed || !text) {
        // Click outside our popover with no active selection → dismiss, even when
        // the question box is open (clicks INSIDE already returned above). This is
        // the "click outside to close" behaviour.
        dismiss()
        return
      }
      const range = sel.getRangeAt(0)
      const msgEl = findMsgEl(range.commonAncestorContainer)
      if (!msgEl || msgEl.getAttribute('data-inline-role') !== 'assistant') {
        dismiss()
        return
      }
      // Selecting (any part of) an already-annotated excerpt opens its thread
      // instead of offering to create a nested one (§ substring rule).
      const startMark = threadIdForNode(range.startContainer)
      const endMark = threadIdForNode(range.endContainer)
      if (startMark || endMark) {
        openExistingThread(startMark || endMark!)
        dismiss()
        return
      }
      const rect = range.getBoundingClientRect()
      setPending({
        messageId: msgEl.getAttribute('data-inline-msg') || '',
        quote: text,
        x: Math.min(rect.right, window.innerWidth - 16),
        y: rect.bottom,
      })
      setAsking(false)
      setDraft('')
    }

    function openExistingThread(childId: string) {
      const child = useConversations.getState().conversations.find((c) => c.id === childId)
      openThread({ childId, quote: child?.inline?.quote ?? '' })
    }

    document.addEventListener('mouseup', onMouseUp)
    return () => document.removeEventListener('mouseup', onMouseUp)
  }, [dismiss, openThread])

  // Hide the floating affordance on scroll (its anchor rect goes stale).
  useEffect(() => {
    const root = scrollRef.current
    if (!root) return
    function onScroll() {
      setPending((p) => (p && !asking ? null : p))
    }
    root.addEventListener('scroll', onScroll, { passive: true })
    return () => root.removeEventListener('scroll', onScroll)
  }, [asking, scrollRef])

  async function submit() {
    if (!pending) return
    const question = draft.trim()
    if (!question) return
    const armed = resolveArmedTurnFlags()
    const quote = pending.quote
    const messageId = pending.messageId
    dismiss()
    const child = await createInlineThread(conversationId, messageId, quote)
    if (!child) return
    openThread({ childId: child.id, quote })
    void sendMessage({
      conversationId: child.id,
      text: question,
      mode: armed.mode,
      verify: armed.verify,
      toolMode: armed.toolMode,
      webSearch: armed.webSearch,
    })
  }

  if (!pending) return null

  // Clamp the popover within the viewport.
  const top = Math.min(pending.y + 8, window.innerHeight - 120)
  const left = Math.max(16, Math.min(pending.x, window.innerWidth - 320))

  return (
    <div
      ref={popoverRef}
      style={{ position: 'fixed', top, left, zIndex: 60 }}
      className="animate-[fade-in_120ms_var(--ease-out)]"
    >
      {!asking ? (
        <button
          type="button"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => setAsking(true)}
          className="inline-flex items-center gap-1.5 h-9 px-3 rounded-[10px] bg-[var(--color-fg)] text-[var(--color-bg)] text-[13px] font-medium shadow-[var(--shadow-md)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <MessageCircleQuestion size={15} aria-hidden />
          {t('inline.ask', { defaultValue: 'Ask' })}
        </button>
      ) : (
        <div className="w-[300px] rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-lg)] p-2">
          <p className="px-1 pt-0.5 pb-1.5 text-[11px] text-[var(--color-fg-subtle)] line-clamp-2">
            “{pending.quote}”
          </p>
          <div className="flex items-end gap-1.5">
            <textarea
              autoFocus
              rows={2}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  void submit()
                } else if (e.key === 'Escape') {
                  dismiss()
                }
              }}
              placeholder={t('inline.placeholder', { defaultValue: 'Ask about this…' })}
              className="flex-1 min-w-0 resize-none rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg)] px-2.5 py-1.5 text-[13px] text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            />
            <button
              type="button"
              onClick={() => void submit()}
              disabled={!draft.trim()}
              aria-label={t('inline.send', { defaultValue: 'Send' })}
              className="inline-flex items-center justify-center size-8 shrink-0 rounded-[8px] bg-[var(--color-accent)] text-[var(--color-accent-fg)] interactive hover:opacity-90 disabled:opacity-40 disabled:pointer-events-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <CornerDownLeft size={14} aria-hidden />
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
