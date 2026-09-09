import type { ApiSseEvent } from '@/api/types'
import type { Message } from '@/types/chat'

type RagEvent = Extract<ApiSseEvent, { type: 'rag' }>

/** Pure wire-to-view mapping shared by every SSE consumption path. */
export function ragInjectionFromEvent(
  event: RagEvent,
  at: number,
): NonNullable<Message['ragInjection']> {
  return {
    strategy: event.status ?? '',
    summary: event.summary ?? '',
    sourceCount:
      typeof event.source_count === 'number' && Number.isFinite(event.source_count)
        ? Math.max(0, Math.trunc(event.source_count))
        : undefined,
    at,
  }
}

/** Attachment progress is transient; keep failures visible with the answer. */
export function visibleRagInjection(message: Pick<Message, 'ragInjection' | 'streaming' | 'content'>): Message['ragInjection'] {
  const injection = message.ragInjection
  if (!injection || !injection.strategy.startsWith('document_')) return injection
  if (injection.strategy === 'document_error') return injection
  if (injection.strategy === 'document_skipped' || !message.streaming || message.content) return undefined
  return injection
}
