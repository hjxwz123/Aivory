/**
 * Conversations store — backed by the Go backend. Exposes the same surface
 * the existing UI uses so we don't have to rewrite every consumer:
 *
 *   - `conversations` is the in-memory cache; `load()` hydrates from the
 *     backend, `loadMessages(id)` loads the active-path messages.
 *   - `createConversation()` / `deleteConversation()` / etc round-trip to
 *     the backend.
 *   - `consumeStream()` and `abortStream()` drive a real SSE session.
 *
 * The local shape we expose matches what most chat components want:
 * `Conversation { id, title, modelId, projectId, messages, ... }`.
 * Inside the store we map ApiMessage / ApiConversation into these shapes.
 */
import { create } from 'zustand'
import { ApiError, conversationsApi, streamSSE } from '@/api'
import type {
  ApiAttachment,
  ApiConversation,
  ApiMessage,
  ApiSseEvent,
} from '@/api/types'
import type { Attachment, Citation, Conversation, Message, ToolCall } from '@/types/chat'
import { uid } from '@/lib/utils'

interface ConversationStore {
  conversations: Conversation[]
  loaded: boolean
  loading: boolean
  error: string | null

  load: () => Promise<void>
  loadOne: (id: string) => Promise<Conversation | undefined>

  createConversation: (modelId?: string, projectId?: string) => Promise<Conversation>
  deleteConversation: (id: string) => Promise<void>
  renameConversation: (id: string, title: string) => Promise<void>
  togglePin: (id: string) => Promise<void>
  toggleStar: (id: string) => Promise<void>
  archiveConversation: (id: string) => Promise<void>
  setProject: (id: string, projectId: string | undefined) => Promise<void>
  setActiveLeaf: (id: string, leafId: string) => Promise<void>
  fork: (id: string, leafId?: string, title?: string) => Promise<Conversation | null>
  setModel: (id: string, modelId: string) => Promise<void>
  /** Bind knowledge bases to the conversation (§7.2-7 composer 📚 selector). */
  setKBs: (id: string, kbIds: string[]) => Promise<void>

  // Streaming
  sendMessage: (input: {
    conversationId: string
    text: string
    modelId?: string
    attachments?: Attachment[]
    parentId?: string
    params?: Record<string, unknown>
  }) => Promise<void>
  regenerate: (conversationId: string, assistantId: string, modelId?: string) => Promise<void>
  abortStream: (assistantMessageId: string) => void

  getConversation: (id: string) => Conversation | undefined
}

const streamControllers = new Map<string, AbortController>()

export const useConversations = create<ConversationStore>((set, get) => ({
  conversations: [],
  loaded: false,
  loading: false,
  error: null,

  async load() {
    if (get().loading) return
    set({ loading: true, error: null })
    try {
      const rows = await conversationsApi.list()
      const conversations = rows.map(toLocalConversation)
      set({ conversations, loaded: true, loading: false })
    } catch (e) {
      set({ error: errorMessage(e, 'Failed to load conversations'), loading: false })
    }
  },

  async loadOne(id) {
    try {
      const resp = await conversationsApi.get(id)
      const conv = toLocalConversation(resp.conversation)
      conv.messages = resp.messages.map(toLocalMessage)
      set((s) => {
        // Guard against a race where sendMessage already optimistically
        // appended messages (including a streaming assistant placeholder)
        // before loadOne's response arrives. If the local copy has any
        // streaming message, keep the local messages — they're more
        // up-to-date than what the backend returned.
        const existing = s.conversations.find((c) => c.id === id)
        if (existing && existing.messages.length > 0 && existing.messages.some((m) => m.streaming)) {
          // Merge metadata (title, modelId, etc.) but keep local messages.
          const merged: Conversation = {
            ...conv,
            messages: existing.messages,
            lastParams: existing.lastParams,
          }
          return { conversations: replaceOrPrepend(s.conversations, merged) }
        }
        return { conversations: replaceOrPrepend(s.conversations, conv) }
      })
      return conv
    } catch {
      return undefined
    }
  },

  async createConversation(modelId, projectId) {
    try {
      const created = await conversationsApi.create({ model_id: modelId, project_id: projectId })
      const conv = toLocalConversation(created)
      set((s) => ({ conversations: [conv, ...s.conversations] }))
      return conv
    } catch (e) {
      // Fall back to optimistic local conversation so the UI never blocks.
      const now = Date.now()
      const conv: Conversation = {
        id: uid('c'),
        title: 'New conversation',
        createdAt: now,
        updatedAt: now,
        modelId: modelId ?? '',
        projectId,
        messages: [],
      }
      set((s) => ({ conversations: [conv, ...s.conversations], error: errorMessage(e) }))
      return conv
    }
  },

  async deleteConversation(id) {
    set((s) => ({ conversations: s.conversations.filter((c) => c.id !== id) }))
    try {
      await conversationsApi.remove(id)
    } catch {
      /* ignore */
    }
  },

  async renameConversation(id, title) {
    set((s) => ({
      conversations: s.conversations.map((c) =>
        c.id === id ? { ...c, title: title.trim() || c.title, updatedAt: Date.now() } : c,
      ),
    }))
    try {
      await conversationsApi.update(id, { title })
    } catch {
      /* ignore */
    }
  },

  async togglePin(id) {
    const target = get().conversations.find((c) => c.id === id)
    const next = !target?.pinned
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, pinned: next } : c)),
    }))
    try {
      await conversationsApi.update(id, { pinned: next })
    } catch {
      /* ignore */
    }
  },

  async toggleStar(id) {
    const target = get().conversations.find((c) => c.id === id)
    const next = !target?.starred
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, starred: next } : c)),
    }))
    try {
      await conversationsApi.update(id, { starred: next })
    } catch {
      /* ignore */
    }
  },

  async archiveConversation(id) {
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, archived: true } : c)),
    }))
    try {
      await conversationsApi.update(id, { archived: true })
    } catch {
      /* ignore */
    }
  },

  async setProject(id, projectId) {
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, projectId } : c)),
    }))
    try {
      await conversationsApi.update(id, { project_id: projectId ?? '' })
    } catch {
      /* ignore */
    }
  },

  async setActiveLeaf(id, leafId) {
    try {
      const resp = await conversationsApi.setActiveLeaf(id, leafId)
      const conv = toLocalConversation(resp.conversation)
      conv.messages = resp.messages.map(toLocalMessage)
      set((s) => ({ conversations: replaceOrPrepend(s.conversations, conv) }))
    } catch {
      /* ignore */
    }
  },

  async fork(id, leafId, title) {
    try {
      const created = await conversationsApi.fork(id, { leaf_id: leafId, title })
      const conv = toLocalConversation(created)
      set((s) => ({ conversations: [conv, ...s.conversations] }))
      return conv
    } catch {
      return null
    }
  },

  async setModel(id, modelId) {
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, modelId } : c)),
    }))
    try {
      await conversationsApi.update(id, { model_id: modelId })
    } catch {
      /* ignore */
    }
  },

  async setKBs(id, kbIds) {
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, kbIds } : c)),
    }))
    try {
      await conversationsApi.update(id, { kb_ids: kbIds })
    } catch {
      /* ignore */
    }
  },

  async sendMessage(input) {
    const abort = new AbortController()
    const userMsg: Message = {
      id: uid('m'),
      role: 'user',
      content: input.text,
      createdAt: Date.now(),
      attachments: input.attachments,
    }
    const assistantId = uid('m')
    const assistantMsg: Message = {
      id: assistantId,
      role: 'assistant',
      content: '',
      createdAt: Date.now() + 1,
      streaming: true,
      modelId: input.modelId || get().conversations.find((c) => c.id === input.conversationId)?.modelId,
    }
    streamControllers.set(assistantId, abort)
    // Optimistically append to local cache.
    set((s) => ({
      conversations: s.conversations.map((c) =>
        c.id === input.conversationId
          ? {
              ...c,
              messages: [...c.messages, userMsg, assistantMsg],
              updatedAt: Date.now(),
              // Remember the param_controls selection so regenerate reuses it.
              lastParams: input.params ?? c.lastParams,
              title:
                c.messages.length === 0 && c.title === 'New conversation'
                  ? input.text.replace(/\n+/g, ' ').slice(0, 60)
                  : c.title,
            }
          : c,
      ),
    }))
    try {
      let serverAssistantId = assistantId
      let lastCitations: Citation[] = []
      const toolCallsById = new Map<string, ToolCall>()
      const toolInputBuffers = new Map<string, string>()
      for await (const frame of streamSSE(
        `/conversations/${encodeURIComponent(input.conversationId)}/messages`,
        {
          text: input.text,
          model_id: input.modelId,
          parent_id: input.parentId,
          attachments: input.attachments?.map(attachmentToApi),
          params: input.params,
        },
        abort.signal,
      )) {
        const ev = frame.data as ApiSseEvent
        switch (ev.type) {
          case 'message_start':
            serverAssistantId = ev.message_id ?? assistantId
            // Replace local id with backend id so future actions (regenerate,
            // active-leaf) use the right id.
            updateAssistant(set, input.conversationId, assistantId, (m) => ({
              ...m,
              id: serverAssistantId,
            }))
            break
          case 'text_delta':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              content: m.content + (ev.text ?? ''),
            }))
            break
          case 'thinking_delta':
            // Accumulate into a separate, collapsible thinking buffer (§1.1).
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              thinking: (m.thinking ?? '') + (ev.text ?? ''),
            }))
            break
          case 'artifact':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              artifacts: [
                ...(m.artifacts ?? []),
                {
                  id: ev.id ?? uid('art'),
                  filename: ev.title ?? 'file',
                  url: ev.url ?? '',
                  mimeType: ev.summary ?? '',
                },
              ],
            }))
            break
          case 'refusal':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              refused: true,
              content: m.content || (ev.message ?? 'The model declined to answer.'),
            }))
            break
          case 'rag': {
            // §6.2 retrieval lifecycle: the orchestrator emits one rag event
            // each time it decides to inject context (status=retrieve|full_text
            // |full_doc) or returns a warning. Surface it as a transient
            // "ragInjection" line that the UI can render above citations.
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              ragInjection: {
                strategy: (ev.status as string | undefined) ?? '',
                summary: ev.summary ?? '',
                at: Date.now(),
              },
            }))
            break
          }
          case 'tool_start': {
            // §6.2 dedupe: tool_start arrives once per tool-use round, but in
            // the wild some providers emit it twice (Anthropic's first delta
            // + an early input chunk both trigger a start). The frontend
            // tracks by id so a second emission updates rather than appends.
            const existing = ev.id ? toolCallsById.get(ev.id) : undefined
            if (existing) break
            const tc: ToolCall = {
              id: ev.id ?? uid('tc'),
              name: ev.name,
              label: prettyToolLabel(ev.name),
              status: 'running',
              startedAt: Date.now(),
              input: (ev.input as Record<string, unknown>) ?? undefined,
            }
            toolCallsById.set(tc.id, tc)
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              toolCalls: [...(m.toolCalls ?? []), tc],
            }))
            break
          }
          case 'tool_input': {
            if (!ev.id) break
            const tc = toolCallsById.get(ev.id)
            if (!tc) break
            // §6.2: partial_json streams JSON fragments — accumulate and parse
            // opportunistically so the card shows the input as it forms.
            if (ev.partial_json) {
              const buf = (toolInputBuffers.get(ev.id) ?? '') + ev.partial_json
              toolInputBuffers.set(ev.id, buf)
              try {
                tc.input = JSON.parse(buf) as Record<string, unknown>
              } catch {
                // incomplete JSON — keep accumulating
              }
            } else if (ev.input) {
              tc.input = ev.input as Record<string, unknown>
            }
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              toolCalls: m.toolCalls?.map((t) => (t.id === tc.id ? { ...t, input: tc.input } : t)),
            }))
            break
          }
          case 'tool_result': {
            const tc = ev.id ? toolCallsById.get(ev.id) : undefined
            if (tc) {
              tc.output = ev.summary
              tc.status = ev.status === 'error' ? 'error' : 'complete'
              tc.endedAt = Date.now()
              updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
                ...m,
                toolCalls: m.toolCalls?.map((t) =>
                  t.id === tc.id
                    ? { ...t, output: tc.output, status: tc.status, endedAt: tc.endedAt }
                    : t,
                ),
              }))
            }
            break
          }
          case 'citation': {
            const c = ev.citation
            const cit: Citation = {
              id: c.id,
              index: c.index,
              title: c.title,
              url: c.url,
              domain: safeDomain(c.url),
              snippet: c.snippet,
            }
            lastCitations = [...lastCitations, cit]
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              citations: lastCitations,
            }))
            break
          }
          case 'error':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              streaming: false,
              content: m.content + (m.content ? '\n\n' : '') + `*An error occurred — ${ev.message}*`,
            }))
            break
          case 'done':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              streaming: false,
            }))
            break
        }
      }
    } catch (e) {
      updateAssistant(set, input.conversationId, assistantId, (m) => ({
        ...m,
        streaming: false,
        content:
          m.content + (m.content ? '\n\n' : '') + `*Stream interrupted: ${errorMessage(e)}*`,
      }))
    } finally {
      streamControllers.delete(assistantId)
    }
  },

  async regenerate(conversationId, assistantId, modelId) {
    const abort = new AbortController()
    const conv = get().conversations.find((c) => c.id === conversationId)
    streamControllers.set(assistantId + '-regen', abort)
    try {
      // Optimistic placeholder for the new assistant sibling.
      const placeholderId = uid('m')
      set((s) => ({
        conversations: s.conversations.map((c) =>
          c.id === conversationId
            ? {
                ...c,
                messages: [
                  ...c.messages,
                  {
                    id: placeholderId,
                    role: 'assistant' as const,
                    content: '',
                    createdAt: Date.now(),
                    streaming: true,
                  },
                ],
              }
            : c,
        ),
      }))
      let serverAssistantId = placeholderId
      const toolCallsById = new Map<string, ToolCall>()
      const toolInputBuffers = new Map<string, string>()
      let lastCitations: Citation[] = []
      for await (const frame of streamSSE(
        `/conversations/${encodeURIComponent(conversationId)}/regenerate`,
        { assistant_id: assistantId, model_id: modelId, params: conv?.lastParams },
        abort.signal,
      )) {
        const ev = frame.data as ApiSseEvent
        switch (ev.type) {
          case 'tool_start': {
            const existing = ev.id ? toolCallsById.get(ev.id) : undefined
            if (existing) break
            const tc: ToolCall = {
              id: ev.id ?? uid('tc'),
              name: ev.name,
              label: prettyToolLabel(ev.name),
              status: 'running',
              startedAt: Date.now(),
              input: (ev.input as Record<string, unknown>) ?? undefined,
            }
            toolCallsById.set(tc.id, tc)
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              toolCalls: [...(m.toolCalls ?? []), tc],
            }))
            break
          }
          case 'tool_input': {
            if (!ev.id) break
            const tc = toolCallsById.get(ev.id)
            if (!tc) break
            if (ev.partial_json) {
              const buf = (toolInputBuffers.get(ev.id) ?? '') + ev.partial_json
              toolInputBuffers.set(ev.id, buf)
              try {
                tc.input = JSON.parse(buf) as Record<string, unknown>
              } catch {
                /* incomplete JSON — keep accumulating */
              }
            } else if (ev.input) {
              tc.input = ev.input as Record<string, unknown>
            }
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              toolCalls: m.toolCalls?.map((t) => (t.id === tc.id ? { ...t, input: tc.input } : t)),
            }))
            break
          }
          case 'tool_result': {
            const tc = ev.id ? toolCallsById.get(ev.id) : undefined
            if (tc) {
              tc.output = ev.summary
              tc.status = ev.status === 'error' ? 'error' : 'complete'
              tc.endedAt = Date.now()
              updateAssistant(set, conversationId, serverAssistantId, (m) => ({
                ...m,
                toolCalls: m.toolCalls?.map((t) =>
                  t.id === tc.id
                    ? { ...t, output: tc.output, status: tc.status, endedAt: tc.endedAt }
                    : t,
                ),
              }))
            }
            break
          }
          case 'citation': {
            const c = ev.citation
            lastCitations = [
              ...lastCitations,
              { id: c.id, index: c.index, title: c.title, url: c.url, domain: safeDomain(c.url), snippet: c.snippet },
            ]
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              citations: lastCitations,
            }))
            break
          }
          case 'message_start':
            serverAssistantId = ev.message_id ?? placeholderId
            updateAssistant(set, conversationId, placeholderId, (m) => ({ ...m, id: serverAssistantId }))
            break
          case 'text_delta':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              content: m.content + (ev.text ?? ''),
            }))
            break
          case 'thinking_delta':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              thinking: (m.thinking ?? '') + (ev.text ?? ''),
            }))
            break
          case 'artifact':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              artifacts: [
                ...(m.artifacts ?? []),
                { id: ev.id ?? uid('art'), filename: ev.title ?? 'file', url: ev.url ?? '', mimeType: ev.summary ?? '' },
              ],
            }))
            break
          case 'refusal':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              refused: true,
              content: m.content || (ev.message ?? 'The model declined to answer.'),
            }))
            break
          case 'done':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({ ...m, streaming: false }))
            break
          case 'error':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              streaming: false,
              content: m.content + `\n\n*Regeneration failed: ${ev.message}*`,
            }))
            break
        }
      }
    } finally {
      streamControllers.delete(assistantId + '-regen')
    }
  },

  abortStream(assistantMessageId) {
    // Two-phase stop: tell the backend to stop generating (so partial blocks are
    // persisted with status='stopped' instead of being abandoned), then abort
    // the local SSE reader so we stop accepting late frames.
    // We look up the conversation id by walking the cache — abortStream is
    // called with an assistantId and not the conv id, but the assistant always
    // belongs to exactly one conversation.
    const state = get()
    const conv = state.conversations.find((c) =>
      c.messages.some((m) => m.id === assistantMessageId),
    )
    if (conv) {
      // Fire-and-forget — the orchestrator subscribes on conv:<id>:stop and
      // cancels its context, which makes the SSE writer flush the in-progress
      // text as the final block before persisting.
      void conversationsApi.stop(conv.id).catch(() => {
        /* best effort — the local abort below still stops the stream */
      })
    }
    const ctrl = streamControllers.get(assistantMessageId)
    ctrl?.abort()
    // Also try regen channel if streaming was triggered via regenerate().
    const regen = streamControllers.get(assistantMessageId + '-regen')
    regen?.abort()
  },

  getConversation(id) {
    return get().conversations.find((c) => c.id === id)
  },
}))

// -------- conversion helpers ----------------------------------------------

function toLocalConversation(c: ApiConversation): Conversation {
  return {
    id: c.id,
    title: c.title,
    createdAt: c.created_at * 1000,
    updatedAt: c.updated_at * 1000,
    modelId: c.model_id,
    projectId: c.project_id || undefined,
    kbIds: c.kb_ids ?? [],
    pinned: c.pinned,
    starred: c.starred,
    archived: c.archived,
    messages: [],
  }
}

// Exported so the admin thread viewer (AdminUserConversation) can reuse the
// exact same ApiMessage → UI Message mapping the chat surface relies on —
// keeps tool-call/citation/artifact rendering identical across both surfaces.
export function toLocalMessage(m: ApiMessage): Message {
  // Extract text content from blocks (skipping tool_call summary blocks, those
  // become toolCalls below).
  const toolCalls: ToolCall[] = []
  const artifacts: Message['artifacts'] = []
  let content = ''
  let thinking = ''
  for (const b of m.blocks ?? []) {
    if (b.kind === 'text') {
      content += b.text ?? ''
    } else if (b.kind === 'thinking') {
      thinking += b.text ?? ''
    } else if (b.kind === 'artifact') {
      artifacts.push({
        id: b.file_ref ?? uid('art'),
        filename: b.title ?? 'file',
        url: b.url ?? '',
        // §4.12 reload fidelity: persist mime type from the block so reloaded
        // artifacts still render as <img> instead of falling back to a generic
        // download chip. The orchestrator writes the mime onto the artifact
        // block's `summary` field at finalize (it's already on the SSE event),
        // so picking it up here closes the reload gap.
        mimeType: b.summary ?? '',
      })
    } else if (b.kind === 'tool_call') {
      toolCalls.push({
        id: b.tool_id ?? uid('tc'),
        name: b.tool_name ?? 'tool',
        label: prettyToolLabel(b.tool_name ?? 'tool'),
        status: 'complete',
        startedAt: m.created_at * 1000,
        endedAt: m.created_at * 1000,
        output: b.summary,
      })
    }
  }
  return {
    id: m.id,
    parentId: m.parent_id || undefined,
    role: m.role,
    content,
    thinking: thinking || undefined,
    artifacts: artifacts.length > 0 ? artifacts : undefined,
    modelId: m.model_id || undefined,
    createdAt: m.created_at * 1000,
    streaming: m.status === 'streaming',
    cost: m.cost > 0 ? m.cost : undefined,
    currency: m.currency || undefined,
    toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
    citations:
      m.citations && m.citations.length > 0
        ? m.citations.map((c) => ({
            id: c.id,
            index: c.index,
            title: c.title,
            url: c.url,
            domain: safeDomain(c.url),
            snippet: c.snippet,
          }))
        : undefined,
    attachments:
      m.attachments && m.attachments.length > 0
        ? m.attachments.map((a) => ({
            id: a.id,
            name: a.filename,
            size: 0,
            kind: (a.kind as Attachment['kind']) || 'other',
            previewUrl: a.url,
          }))
        : undefined,
    branchIndex: m.branch_index,
    branchCount: m.branch_count,
    siblings: m.siblings,
  }
}

function attachmentToApi(a: Attachment): ApiAttachment {
  return {
    id: a.id,
    filename: a.name,
    mime_type: '',
    kind: a.kind,
    url: a.previewUrl ?? '',
  }
}

// SendInput is the public surface used by ChatHome / ChatThread.
export interface SendInput {
  conversationId: string
  text: string
  modelId?: string
  attachments?: Attachment[]
  parentId?: string
  /** param_controls values (§2.3-G). */
  params?: Record<string, unknown>
}

function replaceOrPrepend(list: Conversation[], next: Conversation): Conversation[] {
  const idx = list.findIndex((c) => c.id === next.id)
  if (idx < 0) return [next, ...list]
  const out = list.slice()
  out[idx] = next
  return out
}

function safeDomain(u: string): string {
  try {
    return new URL(u).hostname
  } catch {
    return u
  }
}

function prettyToolLabel(name: string): string {
  switch (name) {
    case 'web_search':
      return 'Searching the web'
    case 'web_fetch':
      return 'Reading a web page'
    case 'python_execute':
      return 'Running Python'
    case 'image_generate':
      return 'Generating an image'
    case 'search_knowledge_base':
      return 'Searching documents'
    case 'use_skill':
      return 'Loading a skill'
    case 'save_memory':
      return 'Saving memory'
    default:
      return name
  }
}

function errorMessage(e: unknown, fallback = 'Something went wrong'): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return fallback
}

// updateAssistant edits an assistant message inside a conversation by id.
function updateAssistant(
  set: (fn: (s: ConversationStore) => Partial<ConversationStore>) => void,
  conversationId: string,
  assistantId: string,
  patch: (m: Message) => Message,
) {
  set((s) => ({
    conversations: s.conversations.map((c) =>
      c.id !== conversationId
        ? c
        : { ...c, messages: c.messages.map((m) => (m.id === assistantId ? patch(m) : m)), updatedAt: Date.now() },
    ),
  }))
}

/** Used by the legacy mock-data path; preserved so any seed-derived stuff
 *  that still calls these helpers does not break. */
export function buildUserMessage(content: string, attachments?: Attachment[]): Message {
  return {
    id: uid('m'),
    role: 'user',
    content,
    createdAt: Date.now(),
    attachments: attachments?.length ? attachments : undefined,
  }
}

export function buildAssistantPlaceholder(): Message {
  return {
    id: uid('m'),
    role: 'assistant',
    content: '',
    createdAt: Date.now(),
    streaming: true,
  }
}
