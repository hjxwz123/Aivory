import type { Conversation, Message } from '@/types/chat'

export function isEmptyStoppedMessage(message: Message): boolean {
  return Boolean(
    message.role === 'assistant' &&
      message.stopped &&
      !message.content.trim() &&
      (message.reasoning?.length ?? 0) === 0 &&
      (message.artifacts?.length ?? 0) === 0 &&
      (message.citations?.length ?? 0) === 0 &&
      !message.research,
  )
}

export function messageHasActions(message: Message): boolean {
  // Optimistic ids are not valid API targets yet. Keep the stopped status
  // visible, but wait for message_start/reconciliation before exposing delete,
  // regenerate, feedback, or fork commands that would otherwise send a local id.
  if (message.localOnly) return false
  return Boolean(message.content || message.error || message.stopped || (message.artifacts?.length ?? 0) > 0)
}

export function protectedFirstRoundMessageIds(conversation: Conversation): Set<string> {
  const ids = new Set<string>()
  if (conversation.hasOlder) return ids
  const messages = conversation.messages
  const firstUserIdx = messages.findIndex((message) => message.role === 'user')
  if (firstUserIdx < 0) return ids
  const firstUser = messages[firstUserIdx]
  // Only the original root sibling anchors the undeletable opening exchange.
  // An edited root branch is first on its own path but remains removable.
  const isAlternateRoot =
    (typeof firstUser.branchIndex === 'number' && firstUser.branchIndex > 0) ||
    (firstUser.branchIndex === undefined &&
      (firstUser.siblings?.length ?? 0) > 1 &&
      firstUser.siblings?.[0] !== firstUser.id)
  if (firstUser.parentId || isAlternateRoot) return ids
  for (let index = firstUserIdx; index < messages.length; index++) {
    if (index > firstUserIdx && messages[index].role === 'user') break
    const message = messages[index]
    // Deleting an assistant that has regenerated sibling answers removes only
    // that answer branch. Keep the original question protected, but allow each
    // redundant answer to be removed until only one remains; the final answer
    // becomes protected again because deleting it would remove the whole round.
    if (message.role === 'assistant' && (message.branchCount ?? 1) > 1) continue
    ids.add(message.id)
  }
  return ids
}
