import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { MessageRow } from './message-row'
import { useConversations } from '@/store/conversations'
import type { Attachment, Conversation, Message } from '@/types/chat'

interface MessageListProps {
  conversation: Conversation
}

export function MessageList({ conversation }: MessageListProps) {
  const navigate = useNavigate()
  // Pull stable selectors only — keeps this component out of the per-token
  // re-render loop while streaming.
  const sendMessage = useConversations((s) => s.sendMessage)
  const regenerate = useConversations((s) => s.regenerate)
  const setActiveLeaf = useConversations((s) => s.setActiveLeaf)
  const fork = useConversations((s) => s.fork)
  const editMessageInPlace = useConversations((s) => s.editMessageInPlace)
  const setFeedback = useConversations((s) => s.setFeedback)

  // Build an id → message lookup so we can find the parent of an edited
  // message without scanning per-row.
  const byId = useMemo(() => {
    const m = new Map<string, Message>()
    for (const msg of conversation.messages) m.set(msg.id, msg)
    return m
  }, [conversation.messages])

  function handleRegenerate(assistantId: string) {
    void regenerate(conversation.id, assistantId, conversation.modelId)
  }

  function handleEdit(id: string, newContent: string, attachments?: Attachment[]) {
    // §4.15 tree semantics: editing a past user message MUST open a NEW
    // BRANCH under the same parent — not append to the active leaf. We pass
    // the edited message's parent_id so the orchestrator creates a sibling.
    // For the root message (no parent), parent_id stays empty and the
    // orchestrator creates a sibling root.
    const edited = byId.get(id)
    const parentId = edited?.parentId ?? ''
    // Use the edited row's surviving attachments when the editor passed them
    // (so a removed image is dropped from the resend). Falling back to the
    // original message preserves the previous behaviour for callers that
    // didn't carry an attachments list.
    const carryAtts = attachments ?? edited?.attachments
    void sendMessage({
      conversationId: conversation.id,
      text: newContent,
      modelId: conversation.modelId,
      parentId,
      attachments: carryAtts,
      // §4.15: an edit always opens a sibling branch under the same parent —
      // flag it so the store truncates the visible path (handles editing the
      // ROOT question too, where parentId is empty and append would be wrong).
      branch: true,
    })
  }

  // "Save" — overwrite the question text in place, then if it already has an
  // answer, regenerate it (a new branch) so the transcript stays coherent: the
  // old answer addressed the pre-edit question and would otherwise be orphaned.
  // The previous answer remains reachable via the `< n/m >` branch switcher.
  function handleSaveEdit(id: string, newContent: string) {
    void editMessageInPlace(conversation.id, id, newContent).then(() => {
      const child = conversation.messages.find((m) => m.parentId === id && m.role === 'assistant')
      if (child) void regenerate(conversation.id, child.id, conversation.modelId)
    })
  }

  function handleBranchSwitch(leafId: string) {
    void setActiveLeaf(conversation.id, leafId)
  }

  function handleFork(leafId: string) {
    // §4.15 "fork to new conversation": copy the path ending at this node into a
    // fresh conversation and take the user there, so the fork is immediately
    // usable instead of silently appearing in the sidebar.
    void fork(conversation.id, leafId).then((created) => {
      if (created) navigate(`/chat/${created.id}`)
    })
  }

  // MessageRow passes the desired NEXT state (toggle). An "off" click clears the
  // rating (""), so a misclick can be undone. The store optimistically reflects
  // it and reverts on failure.
  function handleLike(id: string, liked: boolean) {
    void setFeedback(conversation.id, id, liked ? 'like' : '')
  }

  function handleDislike(id: string, disliked: boolean) {
    void setFeedback(conversation.id, id, disliked ? 'dislike' : '')
  }

  return (
    <div className="flex flex-col gap-8 px-4 sm:px-6 lg:px-8 py-8 mx-auto w-full max-w-[var(--layout-message-max-w)]">
      {conversation.messages.map((m) => (
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
        />
      ))}
    </div>
  )
}
