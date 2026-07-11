const PREFIX = 'aivory:pending-conversation:v1'

export function pendingConversationKey(userId: string | undefined, scope: string, workspaceId?: string): string {
  return [PREFIX, userId || 'anonymous', workspaceId || 'personal', scope].join(':')
}

export function readPendingConversation(key: string): string | undefined {
  try {
    return window.localStorage.getItem(key) || undefined
  } catch {
    return undefined
  }
}

export function writePendingConversation(key: string, conversationId: string): void {
  try {
    window.localStorage.setItem(key, conversationId)
  } catch {
    // Storage can be unavailable in hardened/private browser contexts. The
    // server-side draft still remains visible in the conversation list.
  }
}

export function clearPendingConversation(key: string): void {
  try {
    window.localStorage.removeItem(key)
  } catch {
    // Best effort; ownership checks make a stale id harmless on the next load.
  }
}
