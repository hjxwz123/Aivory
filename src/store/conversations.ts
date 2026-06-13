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
import type { Attachment, Citation, Conversation, Message, ReasoningItem, ToolCall } from '@/types/chat'
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
  unarchiveConversation: (id: string) => Promise<void>
  /** Fetch the user's archived conversations (NOT merged into the active cache). */
  loadArchived: () => Promise<Conversation[]>
  setProject: (id: string, projectId: string | undefined) => Promise<void>
  setActiveLeaf: (id: string, leafId: string) => Promise<void>
  fork: (id: string, leafId?: string, title?: string) => Promise<Conversation | null>
  /** Re-fetch the canonical active-path (enriched with sibling metadata) and
   *  swap it into the cache. Called after a stream completes so branch pickers
   *  appear and optimistic flat-append siblings collapse into the tree (§4.15). */
  reloadActivePath: (id: string) => Promise<void>
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
    /** True when this message opens a NEW branch (edit-a-past-question, §4.15).
     *  The optimistic update then truncates the visible path to `parentId`
     *  before appending, instead of stacking onto the active leaf. */
    branch?: boolean
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

  async unarchiveConversation(id) {
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, archived: false } : c)),
    }))
    try {
      await conversationsApi.update(id, { archived: false })
      // Re-pull the active list so the restored chat reappears in the sidebar.
      await get().load()
    } catch {
      /* ignore */
    }
  },

  async loadArchived() {
    try {
      const rows = await conversationsApi.listArchived()
      return rows.map(toLocalConversation)
    } catch {
      return []
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

  async reloadActivePath(id) {
    // Never clobber an in-flight stream — only reconcile once nothing in this
    // conversation is still streaming.
    const cur = get().conversations.find((c) => c.id === id)
    if (!cur || cur.messages.some((m) => m.streaming)) return
    try {
      const resp = await conversationsApi.get(id)
      const messages = resp.messages.map(toLocalMessage)
      set((s) => ({
        conversations: s.conversations.map((c) =>
          c.id !== id
            ? c
            : // Keep client-only fields (lastParams); swap in the canonical
              // tree path + any freshly generated title.
              { ...c, title: resp.conversation.title || c.title, messages },
        ),
      }))
    } catch {
      /* keep the optimistic copy if the reconcile fetch fails */
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
    // Optimistically update the local cache. For a normal turn we append to the
    // active leaf; for an edit-branch (§4.15) we truncate the visible path to
    // the edited message's parent first, so the new question REPLACES the old
    // sub-tree on screen instead of stacking beneath it.
    set((s) => ({
      conversations: s.conversations.map((c) => {
        if (c.id !== input.conversationId) return c
        const base = input.branch ? truncateToParent(c.messages, input.parentId) : c.messages
        return {
          ...c,
          messages: [...base, userMsg, assistantMsg],
          updatedAt: Date.now(),
          // Remember the param_controls selection so regenerate reuses it.
          lastParams: input.params ?? c.lastParams,
          title:
            c.messages.length === 0 && c.title === 'New conversation'
              ? input.text.replace(/\n+/g, ' ').slice(0, 60)
              : c.title,
        }
      }),
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
            // Extend the current thinking run inside the ordered trace (§7.1-4).
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              reasoning: appendThinkingDelta(m.reasoning ?? [], ev.text ?? ''),
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
            // §6.2 dedupe: a round can emit tool_start twice. Track by id and
            // append into the ordered trace at the spot it occurred (§7.1-4).
            const tid = ev.id ?? uid('tc')
            if (toolCallsById.has(tid)) break
            const tc: ToolCall = {
              id: tid,
              name: ev.name,
              label: prettyToolLabel(ev.name),
              status: 'running',
              startedAt: Date.now(),
              input: (ev.input as Record<string, unknown>) ?? undefined,
            }
            toolCallsById.set(tid, tc)
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              reasoning: appendToolStart(m.reasoning ?? [], tc),
            }))
            break
          }
          case 'tool_input': {
            if (!ev.id) break
            const tid = ev.id
            // §6.2: partial_json streams JSON fragments — accumulate and parse
            // opportunistically so the trace shows the query/code as it forms.
            let nextInput: Record<string, unknown> | undefined
            if (ev.partial_json) {
              const buf = (toolInputBuffers.get(tid) ?? '') + ev.partial_json
              toolInputBuffers.set(tid, buf)
              try {
                nextInput = JSON.parse(buf) as Record<string, unknown>
              } catch {
                // incomplete JSON — keep accumulating
              }
            } else if (ev.input) {
              nextInput = ev.input as Record<string, unknown>
            }
            if (!nextInput) break
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              reasoning: patchReasoningTool(m.reasoning ?? [], tid, { input: nextInput }),
            }))
            break
          }
          case 'tool_result': {
            if (!ev.id) break
            const tid = ev.id
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              reasoning: patchReasoningTool(m.reasoning ?? [], tid, {
                output: ev.summary,
                status: ev.status === 'error' ? 'error' : 'complete',
                endedAt: Date.now(),
              }),
            }))
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
      // Stream finished cleanly — reconcile to the canonical tree path so the
      // user/assistant siblings collapse and the `< n/m >` picker appears.
      await get().reloadActivePath(input.conversationId)
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
      // §4.15: regenerate forks at the assistant — the new reply is a SIBLING
      // of the old one under the same user turn, not an append. Truncate the
      // visible path to that user parent (dropping the old reply and anything
      // below it) before showing the streaming placeholder, so the screen never
      // stacks two replies. The post-stream reconcile then restores the old
      // sibling behind the `< n/m >` picker.
      const oldAssistant = conv?.messages.find((m) => m.id === assistantId)
      const userParentId = oldAssistant?.parentId
      const placeholderId = uid('m')
      const placeholder: Message = {
        id: placeholderId,
        role: 'assistant',
        content: '',
        createdAt: Date.now(),
        streaming: true,
        modelId: modelId ?? conv?.modelId,
      }
      set((s) => ({
        conversations: s.conversations.map((c) => {
          if (c.id !== conversationId) return c
          const base = userParentId
            ? truncateToParent(c.messages, userParentId)
            : c.messages.filter((m) => m.id !== assistantId)
          return { ...c, messages: [...base, placeholder] }
        }),
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
            const tid = ev.id ?? uid('tc')
            if (toolCallsById.has(tid)) break
            const tc: ToolCall = {
              id: tid,
              name: ev.name,
              label: prettyToolLabel(ev.name),
              status: 'running',
              startedAt: Date.now(),
              input: (ev.input as Record<string, unknown>) ?? undefined,
            }
            toolCallsById.set(tid, tc)
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              reasoning: appendToolStart(m.reasoning ?? [], tc),
            }))
            break
          }
          case 'tool_input': {
            if (!ev.id) break
            const tid = ev.id
            let nextInput: Record<string, unknown> | undefined
            if (ev.partial_json) {
              const buf = (toolInputBuffers.get(tid) ?? '') + ev.partial_json
              toolInputBuffers.set(tid, buf)
              try {
                nextInput = JSON.parse(buf) as Record<string, unknown>
              } catch {
                /* incomplete JSON — keep accumulating */
              }
            } else if (ev.input) {
              nextInput = ev.input as Record<string, unknown>
            }
            if (!nextInput) break
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              reasoning: patchReasoningTool(m.reasoning ?? [], tid, { input: nextInput }),
            }))
            break
          }
          case 'tool_result': {
            if (!ev.id) break
            const tid = ev.id
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              reasoning: patchReasoningTool(m.reasoning ?? [], tid, {
                output: ev.summary,
                status: ev.status === 'error' ? 'error' : 'complete',
                endedAt: Date.now(),
              }),
            }))
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
              reasoning: appendThinkingDelta(m.reasoning ?? [], ev.text ?? ''),
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
      // Reconcile so the freshly generated reply and the previous one show up as
      // siblings with a `< n/m >` picker instead of two stacked bubbles (§4.15).
      await get().reloadActivePath(conversationId)
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
// --- reasoning-trace builders (§7.1-4) -------------------------------------
// The assistant's thinking runs and tool rounds arrive interleaved over SSE;
// these keep them in arrival order inside ONE ordered list so the UI can render
// "think → search → think → run" faithfully.

function appendThinkingDelta(reasoning: ReasoningItem[], text: string): ReasoningItem[] {
  if (!text) return reasoning
  const last = reasoning[reasoning.length - 1]
  if (last && last.kind === 'thinking') {
    return [...reasoning.slice(0, -1), { ...last, text: last.text + text }]
  }
  return [...reasoning, { kind: 'thinking', id: uid('rt'), text }]
}

function appendToolStart(reasoning: ReasoningItem[], tool: ToolCall): ReasoningItem[] {
  if (reasoning.some((it) => it.kind === 'tool' && it.id === tool.id)) return reasoning
  return [...reasoning, { kind: 'tool', id: tool.id, tool }]
}

function patchReasoningTool(
  reasoning: ReasoningItem[],
  id: string,
  patch: Partial<ToolCall>,
): ReasoningItem[] {
  return reasoning.map((it) =>
    it.kind === 'tool' && it.id === id ? { ...it, tool: { ...it.tool, ...patch } } : it,
  )
}

export function toLocalMessage(m: ApiMessage): Message {
  // Walk blocks IN ORDER so the reasoning trace interleaves thinking runs and
  // tool rounds exactly as they occurred (§7.1-4). Text blocks accumulate into
  // the final answer; artifacts are collected separately.
  const reasoning: ReasoningItem[] = []
  const artifacts: Message['artifacts'] = []
  let content = ''
  let idx = 0
  for (const b of m.blocks ?? []) {
    idx++
    if (b.kind === 'text') {
      content += b.text ?? ''
    } else if (b.kind === 'thinking') {
      const text = b.text ?? ''
      if (!text) continue
      const last = reasoning[reasoning.length - 1]
      if (last && last.kind === 'thinking') {
        // Merge consecutive thinking blocks into one run.
        last.text += text
      } else {
        reasoning.push({ kind: 'thinking', id: `${m.id}-r${idx}`, text })
      }
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
      const id = b.tool_id ?? `${m.id}-r${idx}`
      reasoning.push({
        kind: 'tool',
        id,
        tool: {
          id,
          name: b.tool_name ?? 'tool',
          label: prettyToolLabel(b.tool_name ?? 'tool'),
          status: 'complete',
          startedAt: m.created_at * 1000,
          endedAt: m.created_at * 1000,
          output: b.summary,
          // Reloaded tool rounds keep their input so the subtitle (query/code)
          // still renders (§7.1-4).
          input:
            b.input && typeof b.input === 'object'
              ? (b.input as Record<string, unknown>)
              : undefined,
        },
      })
    }
  }
  return {
    id: m.id,
    parentId: m.parent_id || undefined,
    role: m.role,
    content,
    reasoning: reasoning.length > 0 ? reasoning : undefined,
    artifacts: artifacts.length > 0 ? artifacts : undefined,
    modelId: m.model_id || undefined,
    createdAt: m.created_at * 1000,
    streaming: m.status === 'streaming',
    cost: m.cost > 0 ? m.cost : undefined,
    currency: m.currency || undefined,
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

// truncateToParent returns the visible path up to and INCLUDING `parentId`,
// dropping everything after it — the optimistic basis for opening a new branch
// (§4.15 edit / regenerate). An empty/undefined parent means "branch from the
// root", so the whole path is cleared. A parent that isn't on the current path
// (shouldn't happen) falls back to keeping the path intact.
function truncateToParent(messages: Message[], parentId: string | undefined): Message[] {
  if (!parentId) return []
  const idx = messages.findIndex((m) => m.id === parentId)
  if (idx < 0) return messages.slice()
  return messages.slice(0, idx + 1)
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
