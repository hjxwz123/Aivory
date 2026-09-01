import type { ApiConversation, ApiMemory, ApiMessage } from '@/api/types'
import { conversationsApi, memoriesApi } from '@/api'
import JSZip from 'jszip'

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

function triggerDownload(blob: Blob, filename: string): void {
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

function triggerJsonDownload(value: unknown, filename: string): void {
  triggerDownload(new Blob([JSON.stringify(value, null, 2)], { type: 'application/json' }), filename)
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

/** Build one portable ZIP containing bounded JSON parts. The parts remain plain
 * JSON so each can be imported independently after extracting the archive. */
export async function exportAllConversationZip(includeMemories: boolean): Promise<{ batches: number; conversations: number }> {
  const plan = await prepareConversationExport(includeMemories)
  const zip = new JSZip()
  zip.file('manifest.json', JSON.stringify({
    format: 'aivory-conversations-archive',
    version: 1,
    exported_at: new Date().toISOString(),
    batch_size: CONVERSATION_EXPORT_BATCH_SIZE,
    batches: plan.total,
    conversations: plan.conversations.length,
  }, null, 2))
  const date = new Date().toISOString().slice(0, 10)
  for (let index = 1; index <= plan.total; index += 1) {
    const start = (index - 1) * CONVERSATION_EXPORT_BATCH_SIZE
    const batchConversations = plan.conversations.slice(start, start + CONVERSATION_EXPORT_BATCH_SIZE)
    const detailed = []
    for (const conversation of batchConversations) {
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
      batch: { index, total: plan.total },
      conversations: detailed,
      ...(index === 1 && plan.memories ? { memories: plan.memories } : {}),
    }
    zip.file(`conversations-${String(index).padStart(4, '0')}.json`, JSON.stringify(envelope, null, 2))
  }
  const blob = await zip.generateAsync({ type: 'blob', compression: 'DEFLATE', compressionOptions: { level: 6 } })
  triggerDownload(blob, `aivory-export-${date}.zip`)
  return { batches: plan.total, conversations: plan.conversations.length }
}

const MAX_IMPORT_ZIP_ENTRIES = 1000
const MAX_IMPORT_ZIP_UNCOMPRESSED_BYTES = 512 * 1024 * 1024

/** Reads a ZIP created by exportAllConversationZip and returns its JSON parts.
 * Other JSON files are ignored, allowing harmless metadata files in archives. */
export async function readConversationExportFile(file: File): Promise<unknown[]> {
  if (!file.name.toLowerCase().endsWith('.zip') && file.type !== 'application/zip') {
    return [JSON.parse(await file.text())]
  }
  const zip = await JSZip.loadAsync(await file.arrayBuffer(), { createFolders: false, checkCRC32: true })
  const entries = Object.values(zip.files).filter((entry) => !entry.dir && entry.name.toLowerCase().endsWith('.json'))
  if (entries.length === 0) throw new Error('The ZIP contains no JSON conversation files.')
  if (entries.length > MAX_IMPORT_ZIP_ENTRIES) throw new Error('The ZIP contains too many files.')
  let expanded = 0
  const values: unknown[] = []
  for (const entry of entries) {
    const bytes = await entry.async('uint8array')
    expanded += bytes.byteLength
    if (expanded > MAX_IMPORT_ZIP_UNCOMPRESSED_BYTES) throw new Error('The ZIP is too large to import.')
    const text = new TextDecoder().decode(bytes)
    try {
      values.push(JSON.parse(text))
    } catch {
      throw new Error(`Invalid JSON in ${entry.name}.`)
    }
  }
  return values
}
