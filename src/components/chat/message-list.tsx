import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Loader2 } from 'lucide-react'
import { MessageRow } from './message-row'
import { useConversations, MSG_PAGE } from '@/store/conversations'
import { toast } from '@/hooks/use-toast'
import type { Attachment, Conversation } from '@/types/chat'

interface MessageListProps {
  conversation: Conversation
  /** When set, expand the lazy window to include this message, scroll it into
   *  view, and briefly highlight it (content-search jump). */
  scrollToMessageId?: string
  /** Changes when the user re-selects the same search result, forcing a re-jump
   *  even though scrollToMessageId is unchanged. */
  jumpKey?: string
}

// Long transcripts render lazily: only the latest INITIAL_WINDOW turns mount;
// scrolling toward the top reveals BATCH more at a time. Keeps first paint fast
// on conversations with hundreds of messages (§ perf).
const INITIAL_WINDOW = 24
const BATCH = 24

export function MessageList({ conversation, scrollToMessageId, jumpKey }: MessageListProps) {
  const navigate = useNavigate()
  const { t } = useTranslation('chat')
  // Pull stable selectors only — keeps this component out of the per-token
  // re-render loop while streaming.
  const sendMessage = useConversations((s) => s.sendMessage)
  const regenerate = useConversations((s) => s.regenerate)
  const setActiveLeaf = useConversations((s) => s.setActiveLeaf)
  const fork = useConversations((s) => s.fork)
  const editMessageInPlace = useConversations((s) => s.editMessageInPlace)
  const setFeedback = useConversations((s) => s.setFeedback)
  const deleteMessage = useConversations((s) => s.deleteMessage)
  const loadOlderMessages = useConversations((s) => s.loadOlderMessages)

  // ── Lazy window over the active path ──────────────────────────────────────
  const total = conversation.messages.length
  // Server has older messages beyond what's loaded (reverse pagination).
  const hasOlder = Boolean(conversation.hasOlder)
  const convId = conversation.id
  const [visible, setVisible] = useState(() => Math.min(INITIAL_WINDOW, total))
  // Reset the window whenever we switch conversations.
  useEffect(() => {
    setVisible(Math.min(INITIAL_WINDOW, conversation.messages.length))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conversation.id])
  // Grow the window if new messages arrive at the tail (so streaming/regenerate
  // never get clipped) — keep at least the newest INITIAL_WINDOW shown.
  useEffect(() => {
    setVisible((v) => (v < INITIAL_WINDOW ? Math.min(INITIAL_WINDOW, total) : v))
  }, [total])

  const start = Math.max(0, total - visible)
  const hasMore = start > 0
  const shown = hasMore ? conversation.messages.slice(start) : conversation.messages

  // Reveal older turns when the top sentinel scrolls into view. Two stages:
  // first reveal messages already loaded but windowed out (cheap, client-side);
  // once those are exhausted, fetch the next OLDER page from the server and
  // reveal it. rootMargin pre-loads a screen early so the loader rarely shows.
  const sentinelRef = useRef<HTMLDivElement>(null)
  const loadingOlderRef = useRef(false)
  useEffect(() => {
    if (!hasMore && !hasOlder) return
    const node = sentinelRef.current
    if (!node) return
    const root = node.closest('[data-scroll-root]')
    const scroller = root as HTMLElement | null
    const io = new IntersectionObserver(
      (entries) => {
        if (!entries[0]?.isIntersecting) return
        if (hasMore) {
          setVisible((v) => Math.min(total, v + BATCH))
        } else if (hasOlder && !loadingOlderRef.current) {
          // Fetch + reveal the next older page. Capture scroll height BEFORE the
          // prepend so we can restore the viewport afterwards — this both keeps
          // the user's reading position (no jump) AND pushes the sentinel out of
          // the pre-load zone so it doesn't chain-load the whole history.
          loadingOlderRef.current = true
          const prevHeight = scroller ? scroller.scrollHeight : 0
          const prevTop = scroller ? scroller.scrollTop : 0
          void loadOlderMessages(convId).finally(() => {
            setVisible((v) => v + MSG_PAGE)
            // Two rAFs: let React commit the new rows, then anchor scroll.
            requestAnimationFrame(() =>
              requestAnimationFrame(() => {
                if (scroller) scroller.scrollTop = prevTop + (scroller.scrollHeight - prevHeight)
                loadingOlderRef.current = false
              }),
            )
          })
        }
      },
      { root, rootMargin: '600px 0px 0px 0px' },
    )
    io.observe(node)
    return () => io.disconnect()
  }, [hasMore, hasOlder, total, convId, loadOlderMessages])

  // ── Jump-to-message (content search) ──────────────────────────────────────
  // The active path holds the message in conversation.messages; find its index so
  // we can pull it inside the lazy window (it may be far above the initial 24).
  const targetIdx = useMemo(
    () => (scrollToMessageId ? conversation.messages.findIndex((m) => m.id === scrollToMessageId) : -1),
    [scrollToMessageId, conversation.messages],
  )
  // Grow the window so the target — plus a little context above it — is mounted.
  useEffect(() => {
    if (targetIdx < 0) return
    setVisible((v) => Math.max(v, Math.min(total, total - targetIdx + 3)))
  }, [targetIdx, total])
  // If the searched message isn't on the active path (it lives on a sibling
  // branch the user created by editing/regenerating), the full load won't contain
  // it — tell the user instead of silently landing nowhere.
  const notFoundRef = useRef<string | null>(null)
  useEffect(() => {
    if (!scrollToMessageId || conversation.messages.length === 0 || targetIdx >= 0) return
    if (notFoundRef.current === scrollToMessageId) return
    notFoundRef.current = scrollToMessageId
    toast.info(
      t('thread.jumpOtherBranch', { defaultValue: 'That message is on a different branch of this conversation.' }),
    )
  }, [scrollToMessageId, targetIdx, conversation.messages.length, t])
  // Once the target is actually in the DOM (window wide enough), scroll it to the
  // centre and flash a highlight — once per target id.
  const jumpedRef = useRef<string | null>(null)
  useEffect(() => {
    if (!scrollToMessageId || targetIdx < 0 || targetIdx < start) return
    // Key on id + nonce so re-selecting the same result (new jumpKey) re-jumps.
    const token = `${scrollToMessageId}@${jumpKey ?? ''}`
    if (jumpedRef.current === token) return
    const el = document.querySelector(`[data-message-id="${CSS.escape(scrollToMessageId)}"]`)
    if (!el) return
    jumpedRef.current = token
    requestAnimationFrame(() => {
      el.scrollIntoView({ block: 'center', behavior: 'smooth' })
      el.classList.add('msg-jump-highlight')
      window.setTimeout(() => el.classList.remove('msg-jump-highlight'), 2200)
    })
  }, [scrollToMessageId, jumpKey, targetIdx, start])

  // Handlers are memoised on stable deps (conversation id/model + store actions)
  // so the per-token `messages` churn during streaming doesn't give MessageRow
  // new function props — which would defeat its React.memo and re-render every
  // visible row 60×/s. The two that need the latest message list read it from
  // the store at call time (a user click) instead of closing over it. (convId is
  // declared above with the window state.)
  const modelId = conversation.modelId

  const handleRegenerate = useCallback(
    (assistantId: string) => void regenerate(convId, assistantId, modelId),
    [convId, modelId, regenerate],
  )

  const handleEdit = useCallback(
    (id: string, newContent: string, attachments?: Attachment[]) => {
      // §4.15 tree semantics: editing a past user message MUST open a NEW BRANCH
      // under the SAME parent. The parent is the message immediately BEFORE the
      // edited one on the rendered active path (its preceding assistant) — derive
      // it from POSITION rather than `edited.parentId`. A not-yet-reconciled
      // optimistic message has an empty `parentId`; trusting it would send
      // parent_id='' and the backend would re-root the edit onto the FIRST message
      // (the merge bug, §4.15 R3). '' is correct ONLY for a genuine root edit
      // (idx === 0). Read from the store at click time so the closure stays stable
      // across streamed tokens.
      const msgs = useConversations.getState().conversations.find((c) => c.id === convId)?.messages ?? []
      const idx = msgs.findIndex((m) => m.id === id)
      const edited = idx >= 0 ? msgs[idx] : undefined
      const parentId = edited?.parentId ?? (idx > 0 ? msgs[idx - 1].id : '')
      const carryAtts = attachments ?? edited?.attachments
      void sendMessage({
        conversationId: convId,
        text: newContent,
        modelId,
        parentId,
        attachments: carryAtts,
        branch: true,
      })
    },
    [convId, modelId, sendMessage],
  )

  const handleSaveEdit = useCallback(
    (id: string, newContent: string) => {
      void editMessageInPlace(convId, id, newContent).then(() => {
        const msgs = useConversations.getState().conversations.find((c) => c.id === convId)?.messages
        const child = msgs?.find((m) => m.parentId === id && m.role === 'assistant')
        if (child) void regenerate(convId, child.id, modelId)
      })
    },
    [convId, modelId, editMessageInPlace, regenerate],
  )

  const handleBranchSwitch = useCallback(
    (leafId: string) => void setActiveLeaf(convId, leafId),
    [convId, setActiveLeaf],
  )

  const handleFork = useCallback(
    (leafId: string) => {
      void fork(convId, leafId).then((created) => {
        if (created) navigate(`/chat/${created.id}`)
      })
    },
    [convId, fork, navigate],
  )

  const handleLike = useCallback(
    (id: string, liked: boolean) => void setFeedback(convId, id, liked ? 'like' : ''),
    [convId, setFeedback],
  )

  const handleDislike = useCallback(
    (id: string, disliked: boolean) => void setFeedback(convId, id, disliked ? 'dislike' : ''),
    [convId, setFeedback],
  )

  const handleDelete = useCallback(
    (id: string) => void deleteMessage(convId, id),
    [convId, deleteMessage],
  )

  return (
    <div
      className="chat-thread flex flex-col gap-8 px-4 sm:px-6 lg:px-8 py-8 mx-auto w-full max-w-[var(--layout-message-max-w)]"
      aria-live="polite"
      aria-atomic="false"
      aria-relevant="additions text"
    >
      {hasMore || hasOlder ? (
        <div ref={sentinelRef} className="flex items-center justify-center py-2 text-[12px] text-[var(--color-fg-subtle)]">
          <Loader2 size={14} className="mr-2 animate-spin" aria-hidden />
          {t('thread.loadingEarlier', { defaultValue: 'Loading earlier messages…' })}
        </div>
      ) : null}
      {shown.map((m) => (
        <MessageRow
          key={m.id}
          message={m}
          onRegenerate={handleRegenerate}
          onEdit={handleEdit}
          onSaveEdit={handleSaveEdit}
          onLike={handleLike}
          onDislike={handleDislike}
          onBranchSwitch={handleBranchSwitch}
          onFork={handleFork}
          onDelete={handleDelete}
        />
      ))}
    </div>
  )
}
