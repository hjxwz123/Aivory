interface ProjectScopedConversation {
  projectId?: string
}

export interface ConversationNavigationPartition<T> {
  ordinary: T[]
  byProject: Map<string, T[]>
}

/** Keep project conversations out of global Starred/date history and group
 * them under their owning project without changing the input order. */
export function partitionConversationNavigation<T extends ProjectScopedConversation>(
  conversations: readonly T[],
): ConversationNavigationPartition<T> {
  const ordinary: T[] = []
  const byProject = new Map<string, T[]>()
  for (const conversation of conversations) {
    if (!conversation.projectId) {
      ordinary.push(conversation)
      continue
    }
    const current = byProject.get(conversation.projectId)
    if (current) current.push(conversation)
    else byProject.set(conversation.projectId, [conversation])
  }
  return { ordinary, byProject }
}
