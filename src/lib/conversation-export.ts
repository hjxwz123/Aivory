import type { ApiConversation, ApiMemory, ApiMessage } from '@/api/types'
import { conversationsApi, memoriesApi } from '@/api'

/** The import parser accepts this envelope and deliberately keeps only the
 * conversation tree. Non-text blocks are retained in the export so a user can
 * preserve the original record; importing later strips provider-only payloads. */
export interface ConversationExportEnvelope {
  format: 'aivory-conversations'
  version: 2
  exported_at: string
  conversations: Array<{
    id: string
    title: string
    model_id?: string
    active_leaf_id: string
    messages: ApiMessage[]
  }>
  memories?: ApiMemory[]
  batch?: { index: number; total: number }
}

export const CONVERSATION_EXPORT_BATCH_SIZE = 10

function triggerJsonDownload(value: unknown, filename: string): void {
  const blob = new Blob([JSON.stringify(value, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  // Let the browser start the download before releasing the object URL.
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export async function exportConversation(conversationID: string): Promise<void> {
  const [{ conversation }, messages] = await Promise.all([
    conversationsApi.get(conversationID),
    conversationsApi.messages(conversationID, 'tree'),
  ])
  const envelope: ConversationExportEnvelope = {
    format: 'aivory-conversations',
    version: 2,
    exported_at: new Date().toISOString(),
    conversations: [{
      id: conversation.id,
      title: conversation.title,
      model_id: conversation.model_id || undefined,
      active_leaf_id: conversation.active_leaf_id,
      messages,
    }],
  }
  const date = new Date().toISOString().slice(0, 10)
  triggerJsonDownload(envelope, `aivory-conversation-${conversation.id}-${date}.json`)
}

async function listAllConversations(): Promise<ApiConversation[]> {
  const all: ApiConversation[] = []
  const seen = new Set<string>()
  async function collect(archived: boolean) {
    let offset = 0
    for (;;) {
      const page = archived
        ? await conversationsApi.listArchived(100, offset)
        : await conversationsApi.list(undefined, 100, offset)
      for (const conversation of page.conversations) {
        if (!seen.has(conversation.id)) {
          seen.add(conversation.id)
          all.push(conversation)
        }
      }
      if (!page.has_more || page.conversations.length === 0) break
      offset += page.conversations.length
    }
  }
  await collect(false)
  await collect(true)
  return all
}

export interface ConversationExportPlan {
  conversations: ApiConversation[]
  memories?: ApiMemory[]
  total: number
}

export async function prepareConversationExport(includeMemories: boolean): Promise<ConversationExportPlan> {
  const [conversations, memories] = await Promise.all([
    listAllConversations(),
    includeMemories ? memoriesApi.list() : Promise.resolve(undefined),
  ])
  return {
    conversations,
    memories,
    total: Math.max(1, Math.ceil(conversations.length / CONVERSATION_EXPORT_BATCH_SIZE)),
  }
}

/** Downloads exactly one bounded batch. Keeping each click to one file avoids
 * browser multi-download blocking and keeps memory/network work predictable. */
export async function downloadConversationExportBatch(plan: ConversationExportPlan, index: number): Promise<void> {
  if (index < 1 || index > plan.total) throw new Error('Invalid export batch')
  const { conversations, memories, total } = plan
  const date = new Date().toISOString().slice(0, 10)
  const start = (index - 1) * CONVERSATION_EXPORT_BATCH_SIZE
  const batchConversations = conversations.slice(start, start + CONVERSATION_EXPORT_BATCH_SIZE)
  const detailed = []
  for (const conversation of batchConversations) {
    // Fetch the full tree, including inactive branches. A failed fetch aborts
    // the batch rather than silently producing an incomplete archive.
    const messages = await conversationsApi.messages(conversation.id, 'tree')
    detailed.push({
      id: conversation.id,
      title: conversation.title,
      model_id: conversation.model_id || undefined,
      active_leaf_id: conversation.active_leaf_id,
      messages,
    })
  }
  const envelope: ConversationExportEnvelope = {
    format: 'aivory-conversations',
    version: 2,
    exported_at: new Date().toISOString(),
    batch: { index, total },
    conversations: detailed,
    ...(index === 1 && memories ? { memories } : {}),
  }
  triggerJsonDownload(envelope, `aivory-export-${date}-part-${index}-of-${total}.json`)
}
