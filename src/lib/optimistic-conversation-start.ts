interface EnterOptimisticConversationInput {
  createConversation: () => string
  beforeNavigate?: () => void
  navigate: (conversationId: string) => void
  startBackgroundWork: (conversationId: string) => void | Promise<void>
}

/**
 * Preserve the first-send interaction contract in one place: local state and
 * the target route are committed before any conversation preparation or send
 * promise gets a chance to block the visible transition.
 */
export function enterOptimisticConversation({
  createConversation,
  beforeNavigate,
  navigate,
  startBackgroundWork,
}: EnterOptimisticConversationInput): string {
  const conversationId = createConversation()
  beforeNavigate?.()
  navigate(conversationId)
  void startBackgroundWork(conversationId)
  return conversationId
}
