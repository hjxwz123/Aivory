export interface SelectableKnowledgeBase {
  id: string
  name?: string
  description?: string
}

/**
 * A project's implicit knowledge base is a locked compatibility anchor, not an
 * additional checkbox. Explicit conversation selections remain removable.
 */
export function knowledgeBaseSelectionContext<T extends SelectableKnowledgeBase>(
  knowledgeBases: T[],
  selectedIds: string[],
  lockedId?: string,
): { options: T[]; selectedIds: string[] } {
  const normalizedLockedId = lockedId?.trim()
  const explicitIds = Array.from(
    new Set(selectedIds.filter((id) => id && id !== normalizedLockedId)),
  )
  return {
    options: knowledgeBases.filter((knowledgeBase) => knowledgeBase.id !== normalizedLockedId),
    selectedIds: explicitIds,
  }
}
