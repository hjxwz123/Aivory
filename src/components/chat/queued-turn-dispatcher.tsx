import { useEffect, useRef } from 'react'

import { sameConvListShape, useConversations } from '@/store/conversations'
import { useQueuedTurns } from '@/store/queued-turns'
import { blockReload } from '@/lib/sync-guards'

/** Dispatches queued follow-ups even when their conversation is not open. */
export function QueuedTurnDispatcher() {
  const conversations = useConversations((state) => state.conversations, sameConvListShape)
  const sendMessage = useConversations((state) => state.sendMessage)
  const turnsByConversation = useQueuedTurns((state) => state.turnsByConversation)
  const beginDispatch = useQueuedTurns((state) => state.beginDispatch)
  const markStarted = useQueuedTurns((state) => state.markStarted)
  const release = useQueuedTurns((state) => state.release)
  const attempted = useRef(new Set<string>())

  const hasQueuedTurns = Object.keys(turnsByConversation).length > 0
  useEffect(() => {
    if (!hasQueuedTurns) return
    return blockReload()
  }, [hasQueuedTurns])

  useEffect(() => {
    for (const turn of Object.values(turnsByConversation)) {
      const conversation = conversations.find((item) => item.id === turn.conversationId)
      if (!conversation) continue

      const streaming = conversation.messages.some((message) => message.streaming)
      if (streaming) {
        // A realtime race may have rejected an earlier attempt because another
        // stream started first. Once that stream settles, this turn may retry.
        attempted.current.delete(turn.id)
        continue
      }
      if (turn.status !== 'queued' || attempted.current.has(turn.id)) continue

      const claimed = beginDispatch(turn.conversationId)
      if (!claimed) continue
      attempted.current.add(claimed.id)
      let started = false
      void sendMessage({
        conversationId: claimed.conversationId,
        text: claimed.text,
        modelId: claimed.modelId,
        attachments: claimed.attachments,
        mode: claimed.mode,
        params: claimed.params,
        imageStyleId: claimed.imageStyleId,
        optimizeImagePrompt: claimed.optimizeImagePrompt,
        verify: claimed.verify,
        toolMode: claimed.toolMode,
        webSearch: claimed.webSearch,
        selectedUserSkillIds: claimed.selectedUserSkillIds,
        selectedToolIds: claimed.selectedToolIds,
        fast: claimed.fast,
        onStarted: () => {
          started = true
          attempted.current.delete(claimed.id)
          markStarted(claimed.conversationId, claimed.id)
        },
      }).finally(() => {
        // Early store guards can reject before the optimistic turn exists. Keep
        // the user's message recoverable instead of silently consuming it.
        if (!started) release(claimed.conversationId, claimed.id)
      })
    }
  }, [beginDispatch, conversations, markStarted, release, sendMessage, turnsByConversation])

  return null
}
