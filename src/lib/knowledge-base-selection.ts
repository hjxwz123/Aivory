export interface KnowledgeBaseEmbeddingIdentity {
  embedding_model_id: string
  embedding_dim: number
}

export interface SelectableKnowledgeBase extends KnowledgeBaseEmbeddingIdentity {
  id: string
}

/**
 * Knowledge bases can share a retrieval request only when both the configured
 * embedding model and the vectors already stored for them agree.
 */
export function knowledgeBasesHaveCompatibleEmbeddings(
  left: KnowledgeBaseEmbeddingIdentity,
  right: KnowledgeBaseEmbeddingIdentity,
): boolean {
  return (
    left.embedding_model_id.trim() === right.embedding_model_id.trim() &&
    left.embedding_dim === right.embedding_dim
  )
}

/**
 * A project's implicit knowledge base is a locked compatibility anchor, not an
 * additional checkbox. Explicit conversation selections remain removable.
 */
export function knowledgeBaseSelectionContext<T extends SelectableKnowledgeBase>(
  knowledgeBases: T[],
  selectedIds: string[],
  lockedId?: string,
  lockedEmbedding?: KnowledgeBaseEmbeddingIdentity,
): { options: T[]; anchors: SelectableKnowledgeBase[]; selectedIds: string[] } {
  const normalizedLockedId = lockedId?.trim()
  const explicitIds = Array.from(
    new Set(selectedIds.filter((id) => id && id !== normalizedLockedId)),
  )
  const anchorIds = new Set(explicitIds)
  if (normalizedLockedId) anchorIds.add(normalizedLockedId)
  const anchors: SelectableKnowledgeBase[] = knowledgeBases.filter((knowledgeBase) =>
    anchorIds.has(knowledgeBase.id),
  )

  const lockedKnowledgeBaseIsListed = anchors.some(
    (knowledgeBase) => knowledgeBase.id === normalizedLockedId,
  )
  if (
    normalizedLockedId &&
    !lockedKnowledgeBaseIsListed &&
    lockedEmbedding &&
    Boolean(lockedEmbedding.embedding_model_id.trim()) &&
    Number.isInteger(lockedEmbedding.embedding_dim) &&
    lockedEmbedding.embedding_dim > 0
  ) {
    anchors.push({
      id: normalizedLockedId,
      embedding_model_id: lockedEmbedding.embedding_model_id,
      embedding_dim: lockedEmbedding.embedding_dim,
    })
  }

  return {
    options: knowledgeBases.filter((knowledgeBase) => knowledgeBase.id !== normalizedLockedId),
    anchors,
    selectedIds: explicitIds,
  }
}
