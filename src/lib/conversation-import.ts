/**
 * Conversation-import parser — converts another platform's JSON chat export into
 * our import payload.
 *
 * The source format is a JSON array of chat wrappers; each carries a
 * `chat.history` tree: `{ messages: { <id>: { id, parentId, childrenIds, role,
 * content } }, currentId }`. That maps cleanly onto our own message tree
 * (parent_id + active_leaf_id; multiple null-parent roots = edit-branches of the
 * first question, which our sibling model supports).
 *
 * Per the import spec we keep ONLY chat history + titles. Everything else is
 * dropped: uploaded files, usage, and — inside assistant content — the
 * `<details>…</details>` status/thinking blocks, the source platform's internal
 * `[openai_responses:…]` reasoning markers, and embedded (base64/data-URI or
 * markdown) images.
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

/**
 * Parse a chat export (array of chat wrappers, or a single one) into our import
 * payload. Returns [] when nothing parseable is found (unsupported file).
 */
export function parseConversationExport(json: unknown): ImportConversationInput[] {
  const arr = Array.isArray(json) ? json : [json]
  const out: ImportConversationInput[] = []
  for (const item of arr) {
    const parsed = parseOne(item)
    if (parsed) out.push(parsed)
  }
  return out
}
