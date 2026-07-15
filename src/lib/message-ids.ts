import type { Message } from '@/types/chat'

/**
 * Returns true only when the target is a message that this client explicitly
 * created as an optimistic placeholder, either on the current path or in the
 * caller's exact-id registry. Unknown ids remain valid because a branch sibling
 * may be persisted on the server without being in the loaded active path.
 */
export function isKnownLocalMessageId(
  messages: readonly Pick<Message, 'id' | 'localOnly'>[],
  targetId: string,
  generatedLocalIds: ReadonlySet<string> = new Set(),
): boolean {
  return (
    generatedLocalIds.has(targetId) ||
    messages.some((message) => message.id === targetId && message.localOnly === true)
  )
}

/**
 * Returns a message reference only when it is safe to serialize to the API.
 * Empty roots and client-only optimistic ids both become undefined; `branch`
 * remains the wire-level distinction between a root sibling and a normal append.
 */
export function persistedMessageReference(
  messages: readonly Pick<Message, 'id' | 'localOnly'>[],
  targetId: string | undefined,
  generatedLocalIds: ReadonlySet<string> = new Set(),
): string | undefined {
  if (!targetId || isKnownLocalMessageId(messages, targetId, generatedLocalIds)) return undefined
  return targetId
}
