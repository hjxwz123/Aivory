/**
 * Conversation-import parser — converts a JSON chat export into our import
 * payload. Two source formats are auto-detected:
 *
 * 1. Another platform's export: a JSON array of chat wrappers; each carries a
 *    `chat.history` tree: `{ messages: { <id>: { id, parentId, childrenIds,
 *    role, content } }, currentId }` (messages as a MAP). That maps cleanly
 *    onto our own message tree (parent_id + active_leaf_id; multiple
 *    null-parent roots = edit-branches of the first question, which our
 *    sibling model supports).
 *
 * 2. Aivory's own "Export all data" file (settings → privacy):
 *    `{ conversations: [ { title, model_id, active_leaf_id, messages: [ { id,
 *    parent_id, role, blocks: [...] } ] } ], memories, exported_at }`
 *    (messages as an ARRAY of block-carrying objects). Only the text blocks
 *    are kept; memories are not importable through this endpoint.
 *
 * Per the import spec we keep ONLY chat history + titles (+ model for our own
 * format). Everything else is dropped: uploaded files, usage, and — inside
 * assistant content — the `<details>…</details>` status/thinking blocks, the
 * source platform's internal `[openai_responses:…]` reasoning markers, and
 * embedded (base64/data-URI or markdown) images.
 */

export interface ImportMessage {
  /** Source-platform id; remapped to a fresh server id on the backend. */
  id: string
  /** Source-platform parent id ('' = root). */
  parent_id: string
  role: 'user' | 'assistant'
  content: string
}

export interface ImportConversationInput {
  title: string
  /** Source-platform id of the active leaf (currentId). */
  active_leaf_id: string
  /** Messages ordered parent-before-child (DFS from roots). */
  messages: ImportMessage[]
  /** Model to reopen the conversation with (Aivory self-export only). */
  model_id?: string
}

interface SourceMessage {
  id?: string
  parentId?: string | null
  childrenIds?: string[]
  role?: string
  content?: string
}

/**
 * Strip everything the import must ignore from a message body, leaving the plain
 * answer text. Order matters: remove block structures first, then orphan tags,
 * then inline markers and images, then normalise whitespace.
 */
export function cleanImportedContent(raw: string): string {
  if (!raw) return ''
  let s = raw
  // <details>…</details> — status / thinking / tool / reasoning blocks (these are
  // exactly the "<details> before the output" the spec says to drop).
  s = s.replace(/<details[\s\S]*?<\/details>/gi, '')
  // Any orphan <details>/<summary> tags left by a malformed/truncated block.
  s = s.replace(/<\/?(?:details|summary)\b[^>]*>/gi, '')
  // Source-platform internal reasoning markers: ref-definition lines + inline tokens.
  s = s.replace(/^[ \t]*\[openai_responses:[^\]]*\]:.*$/gim, '')
  s = s.replace(/\[openai_responses:[^\]]*\]/gi, '')
  // Embedded images — base64/data URIs, /api file refs, or any markdown image.
  s = s.replace(/!\[[^\]]*\]\([^)]*\)/g, '')
  // Collapse the blank space those removals leave behind.
  s = s.replace(/[ \t]+$/gm, '').replace(/\n{3,}/g, '\n\n').trim()
  return s
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined
}

function parseOne(item: unknown): ImportConversationInput | null {
  const obj = asRecord(item)
  if (!obj) return null
  // The tree may be under `chat.history` (wrapper export) or directly `history`.
  const chat = asRecord(obj.chat) ?? obj
  const history = asRecord(chat.history)
  const messagesMap = asRecord(history?.messages)
  if (!history || !messagesMap) return null
  const byId = messagesMap as Record<string, SourceMessage>

  // DFS from every root (parentId null), following childrenIds so parents always
  // precede children and sibling order is preserved. Object insertion order
  // (creation order) drives the order of multiple roots.
  const ordered: SourceMessage[] = []
  const seen = new Set<string>()
  const visit = (id: string | undefined) => {
    if (!id) return
    const m = byId[id]
    if (!m || seen.has(id)) return
    seen.add(id)
    ordered.push(m)
    for (const cid of m.childrenIds ?? []) visit(cid)
  }
  for (const m of Object.values(byId)) {
    if (m && !m.parentId && typeof m.id === 'string') visit(m.id)
  }
  // Safety net: include any node not reachable from a root (corrupt parent link).
  for (const m of Object.values(byId)) {
    if (m && typeof m.id === 'string' && !seen.has(m.id)) visit(m.id)
  }

  const messages: ImportMessage[] = []
  for (const m of ordered) {
    if (typeof m.id !== 'string') continue
    const role = m.role === 'user' ? 'user' : m.role === 'assistant' ? 'assistant' : null
    if (!role) continue
    messages.push({
      id: m.id,
      parent_id: typeof m.parentId === 'string' ? m.parentId : '',
      role,
      content: cleanImportedContent(typeof m.content === 'string' ? m.content : ''),
    })
  }
  if (messages.length === 0) return null

  const title =
    String((obj.title as string | undefined) || (chat.title as string | undefined) || '').trim() ||
    'Imported chat'
  let active = typeof history.currentId === 'string' ? history.currentId : ''
  if (!active || !byId[active]) active = ordered[ordered.length - 1]?.id ?? ''
  return { title, active_leaf_id: active, messages }
}

// ── Aivory self-export (settings → privacy → Export all data) ──────────────

interface AivoryBlock {
  kind?: string
  text?: string
}

interface AivoryMessage {
  id?: string
  parent_id?: string
  role?: string
  blocks?: AivoryBlock[]
}

/** True when the object looks like one conversation from our own export:
 *  `messages` is an ARRAY whose entries carry a `blocks` array — unambiguous
 *  against the other-platform format, where messages live in a MAP under
 *  `chat.history`. */
function isAivoryConversation(obj: Record<string, unknown>): boolean {
  const msgs = obj.messages
  if (!Array.isArray(msgs)) return false
  if (msgs.length === 0) return false
  const first = asRecord(msgs[0])
  return !!first && Array.isArray(first.blocks) && typeof first.role === 'string'
}

/** Flatten a message's blocks to plain text — text blocks only; thinking /
 *  tool / citation / image blocks are dropped (history + titles only), then
 *  the shared cleanup strips any embedded markdown images. */
function aivoryBlocksToText(blocks: AivoryBlock[]): string {
  const parts: string[] = []
  for (const b of blocks) {
    if (b && b.kind === 'text' && typeof b.text === 'string' && b.text.trim() !== '') {
      parts.push(b.text)
    }
  }
  return cleanImportedContent(parts.join('\n\n'))
}

function parseAivoryConversation(obj: Record<string, unknown>): ImportConversationInput | null {
  const raw = (obj.messages as unknown[]).map(asRecord)
  const messages: ImportMessage[] = []
  const ids = new Set<string>()
  for (const m of raw) {
    if (!m) continue
    const msg = m as AivoryMessage
    if (typeof msg.id !== 'string' || msg.id === '') continue
    const role = msg.role === 'user' ? 'user' : msg.role === 'assistant' ? 'assistant' : null
    if (!role) continue // system rows etc. are not part of the visible history
    messages.push({
      id: msg.id,
      parent_id: typeof msg.parent_id === 'string' ? msg.parent_id : '',
      role,
      content: aivoryBlocksToText(Array.isArray(msg.blocks) ? msg.blocks : []),
    })
    ids.add(msg.id)
  }
  if (messages.length === 0) return null
  const title = String((obj.title as string | undefined) ?? '').trim() || 'Imported chat'
  let active = typeof obj.active_leaf_id === 'string' ? obj.active_leaf_id : ''
  if (!active || !ids.has(active)) active = messages[messages.length - 1].id
  const modelID = typeof obj.model_id === 'string' ? obj.model_id : ''
  const out: ImportConversationInput = { title, active_leaf_id: active, messages }
  if (modelID) out.model_id = modelID
  return out
}

/** Parse our own export file. Accepts the full export root
 *  (`{ conversations: [...] }`), a bare array of its conversations, or a single
 *  conversation object. Returns [] when the shape isn't ours. */
function parseAivoryExport(json: unknown): ImportConversationInput[] {
  const root = asRecord(json)
  let candidates: unknown[]
  if (root && Array.isArray(root.conversations)) {
    candidates = root.conversations
  } else if (Array.isArray(json)) {
    candidates = json
  } else if (root) {
    candidates = [root]
  } else {
    return []
  }
  const out: ImportConversationInput[] = []
  for (const item of candidates) {
    const obj = asRecord(item)
    if (!obj || !isAivoryConversation(obj)) continue
    const parsed = parseAivoryConversation(obj)
    if (parsed) out.push(parsed)
  }
  return out
}

/**
 * Parse a chat export into our import payload, auto-detecting the source:
 * Aivory's own "Export all data" file first (messages as block-carrying
 * arrays), then the other-platform wrapper format (messages as a map under
 * chat.history). Returns [] when nothing parseable is found (unsupported file).
 */
export function parseConversationExport(json: unknown): ImportConversationInput[] {
  const own = parseAivoryExport(json)
  if (own.length > 0) return own
  const arr = Array.isArray(json) ? json : [json]
  const out: ImportConversationInput[] = []
  for (const item of arr) {
    const parsed = parseOne(item)
    if (parsed) out.push(parsed)
  }
  return out
}
