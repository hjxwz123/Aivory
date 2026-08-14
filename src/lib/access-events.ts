export type AccessInvalidationKind = 'account' | 'workspace' | 'knowledge-base'

export interface AccessInvalidation {
  kind: AccessInvalidationKind
  knowledgeBaseId?: string
}

type AccessInvalidationListener = (event: AccessInvalidation) => void

const listeners = new Set<AccessInvalidationListener>()

/**
 * Broadcasts that locally cached authorization-dependent resources must be
 * reconciled with the server. The event carries no policy data: `/me` and the
 * normal authorized resource endpoints remain the source of truth.
 */
export function invalidateAccessState(event: AccessInvalidation): void {
  for (const listener of [...listeners]) listener(event)
}

export function subscribeAccessInvalidation(listener: AccessInvalidationListener): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}
