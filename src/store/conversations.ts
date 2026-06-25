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
import { createWithEqualityFn } from 'zustand/traditional'
import { ApiError, conversationsApi, streamSSE } from '@/api'
import type {
  ApiAttachment,
  ApiConversation,
  ApiMessage,
  ApiSseEvent,
} from '@/api/types'
import type {
  Attachment,
  Citation,
  Conversation,
  Message,
  ReasoningItem,
  ResearchSource,
  ResearchState,
  ResearchTask,
  ToolCall,
} from '@/types/chat'
import { uid } from '@/lib/utils'
import { toast } from '@/hooks/use-toast'

// Sidebar conversation page size. Kept at the server default so users with ≤200
// conversations load everything up front (no behaviour change); heavier users
// page in older conversations on scroll via loadMore().
const CONV_PAGE = 200

// Server-side pagination cursor for the sidebar list. Tracked separately from the
// cache size so that conversations PREPENDED out-of-order (loadOne of an old chat
// opened by URL/search, createConversation, fork) can't corrupt the offset and
// make loadMore skip a real server row. Only ever advances by the number of rows
// the paged list endpoint actually returned.
let convServerOffset = 0

// Messages per page when opening a conversation. A bit above the render window
// (INITIAL_WINDOW=24) so the first screen is full with a little buffer; older
// pages are fetched on scroll-up via loadOlderMessages().
export const MSG_PAGE = 40

interface ConversationStore {
  conversations: Conversation[]
  loaded: boolean
  loading: boolean
  /** True while a loadMore() page is in flight. */
  loadingMore: boolean
  /** True when the server reported more conversations beyond what's loaded. */
  hasMore: boolean
  error: string | null

  load: () => Promise<void>
  /** Append the next page of (older) conversations — sidebar infinite scroll. */
  loadMore: () => Promise<void>
  /** Load a conversation. By default only the latest page of messages is
   *  fetched (older loaded on scroll); pass {full:true} to load the whole active
   *  path up front (used when jumping to a specific message). */
  loadOne: (id: string, opts?: { full?: boolean }) => Promise<Conversation | undefined>
  /** Prepend the next older page of messages to a conversation (scroll-up). */
  loadOlderMessages: (id: string) => Promise<void>

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
  /** Fetch the inline (text-selection) sub-conversations anchored to a source
   *  conversation and merge them into the cache (flagged `inline`, hidden from
   *  the sidebar). Drives the inline-thread markers on messages. */
  loadInlineThreads: (sourceConvId: string) => Promise<void>
  /** Create a new inline sub-conversation anchored to a quoted excerpt of a
   *  message, returning the child conversation (already in the cache). */
  createInlineThread: (sourceConvId: string, messageId: string, quote: string) => Promise<Conversation | undefined>
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
    /** Alternate turn pipeline: 'deep-research' runs the research engine. */
    mode?: 'default' | 'deep-research' | 'canvas'
    /** §4.20 image mode: selected style id, sent for an image-model turn. */
    imageStyleId?: string
  }) => Promise<void>
  regenerate: (conversationId: string, assistantId: string, modelId?: string) => Promise<void>
  /** Edit a user message's text IN PLACE — overwrite, no new branch, no
   *  regeneration (§4.15 "save" vs "save & resend"). */
  editMessageInPlace: (conversationId: string, messageId: string, text: string) => Promise<void>
  setFeedback: (conversationId: string, messageId: string, next: 'like' | 'dislike' | '') => Promise<void>
  /** Delete the whole round (user message + all its assistant answers) that the
   *  given message belongs to. Branch-safe: earlier/later turns and sibling
   *  branches are preserved (§ message deletion). */
  deleteMessage: (conversationId: string, messageId: string) => Promise<void>
  abortStream: (assistantMessageId: string) => void

  getConversation: (id: string) => Conversation | undefined
}

const streamControllers = new Map<string, AbortController>()

// Stop every in-progress generation in a conversation: tell the backend to
// halt (so partial output is persisted) and abort the local SSE readers. Used
// when the user abandons the current branch — editing a past turn into a new
// branch, or switching to a different branch — so the old stream can't keep
// running invisibly and its late completion can't clobber the new active leaf.
function stopConversationStreams(convId: string, messages: Message[]): void {
  const live = messages.filter((m) => m.streaming)
  if (live.length === 0) return
  void conversationsApi.stop(convId).catch(() => {})
  for (const m of live) {
    streamControllers.get(m.id)?.abort()
    streamControllers.get(m.id + '-regen')?.abort()
  }
}

export const useConversations = createWithEqualityFn<ConversationStore>((set, get) => ({
  conversations: [],
  loaded: false,
  loading: false,
  loadingMore: false,
  hasMore: false,
  error: null,

  async load() {
    if (get().loading) return
    set({ loading: true, error: null })
    try {
      const { conversations: rows, has_more } = await conversationsApi.list(undefined, CONV_PAGE, 0)
      const conversations = rows.map(toLocalConversation)
      convServerOffset = rows.length
      set({ conversations, loaded: true, loading: false, hasMore: has_more })
    } catch (e) {
      set({ error: errorMessage(e, 'Failed to load conversations'), loading: false })
    }
  },

  async loadMore() {
    const { loading, loadingMore, hasMore } = get()
    if (loading || loadingMore || !hasMore) return
    set({ loadingMore: true })
    try {
      // Page from the tracked server cursor (NOT the cache size), so out-of-order
      // prepends can't skip a real row. Advance the cursor by the rows the server
      // returned; the `seen` filter only de-dupes what we add to the cache.
      const { conversations: rows, has_more } = await conversationsApi.list(undefined, CONV_PAGE, convServerOffset)
      convServerOffset += rows.length
      set((s) => {
        const seen = new Set(s.conversations.map((c) => c.id))
        const fresh = rows.map(toLocalConversation).filter((c) => !seen.has(c.id))
        return { conversations: [...s.conversations, ...fresh], loadingMore: false, hasMore: has_more }
      })
    } catch {
      set({ loadingMore: false })
    }
  },

  async loadOne(id, opts) {
    try {
      // Paginate by default (latest MSG_PAGE messages); load the whole path when
      // a caller needs every message present up front (e.g. jump-to-message).
      const resp = await conversationsApi.get(id, opts?.full ? undefined : { limit: MSG_PAGE })
      const conv = toLocalConversation(resp.conversation)
      conv.messages = resp.messages.map(toLocalMessage)
      conv.hasOlder = !opts?.full && Boolean(resp.has_more)
      conv.olderCursor = resp.next_before
      set((s) => {
        // Guard against a race where sendMessage already optimistically
        // appended messages (including a streaming assistant placeholder)
        // before loadOne's response arrives. If the local copy has any
        // streaming message, keep the local messages — they're more
        // up-to-date than what the backend returned.
        const existing = s.conversations.find((c) => c.id === id)
        if (existing && existing.messages.length > 0 && existing.messages.some((m) => m.streaming)) {
          // Merge metadata (title, modelId, etc.) but keep local messages +
          // whatever pagination state the local copy already had.
          const merged: Conversation = {
            ...conv,
            // Keep the optimistic first-message title if the backend hasn't
            // committed its own (clip/model) title yet — don't flash back to blank.
            title: conv.title || existing.title,
            messages: existing.messages,
            lastParams: existing.lastParams,
            hasOlder: existing.hasOlder,
            olderCursor: existing.olderCursor,
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

  async loadOlderMessages(id) {
    const c = get().conversations.find((cv) => cv.id === id)
    if (!c || !c.hasOlder || !c.olderCursor) return
    try {
      const resp = await conversationsApi.get(id, { limit: MSG_PAGE, before: c.olderCursor })
      const older = resp.messages.map(toLocalMessage)
      set((s) => ({
        conversations: s.conversations.map((cv) => {
          if (cv.id !== id) return cv
          // Prepend, de-duping against what we already hold (a concurrent
          // reconcile could have re-added some).
          const seen = new Set(cv.messages.map((m) => m.id))
          const fresh = older.filter((m) => !seen.has(m.id))
          return {
            ...cv,
            messages: [...fresh, ...cv.messages],
            hasOlder: Boolean(resp.has_more),
            olderCursor: resp.next_before,
          }
        }),
      }))
    } catch {
      /* non-fatal: older messages just stay unloaded */
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
    const prevConversations = get().conversations
    set((s) => {
      // Remove the conversation and every inline sub-conversation transitively
      // anchored to it, mirroring the backend cascade so markers/drawers for the
      // doomed sub-threads vanish immediately.
      const doomed = new Set<string>([id])
      for (let grew = true; grew; ) {
        grew = false
        for (const c of s.conversations) {
          if (!doomed.has(c.id) && c.inline && doomed.has(c.inline.sourceConvId)) {
            doomed.add(c.id)
            grew = true
          }
        }
      }
      return { conversations: s.conversations.filter((c) => !doomed.has(c.id)) }
    })
    try {
      await conversationsApi.remove(id)
    } catch (e) {
      set({ conversations: prevConversations })
      toast.error(errorMessage(e, 'Failed to delete conversation'))
    }
  },

  async renameConversation(id, title) {
    const prevConversations = get().conversations
    set((s) => ({
      conversations: s.conversations.map((c) =>
        c.id === id ? { ...c, title: title.trim() || c.title, updatedAt: Date.now() } : c,
      ),
    }))
    try {
      await conversationsApi.update(id, { title })
    } catch (e) {
      set({ conversations: prevConversations })
      toast.error(errorMessage(e, 'Failed to rename conversation'))
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
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to update pin'))
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
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to update star'))
    }
  },

  async archiveConversation(id) {
    const prevConversations = get().conversations
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, archived: true } : c)),
    }))
    try {
      await conversationsApi.update(id, { archived: true })
    } catch (e) {
      set({ conversations: prevConversations })
      toast.error(errorMessage(e, 'Failed to archive conversation'))
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
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to unarchive conversation'))
    }
  },

  async loadArchived() {
    try {
      const { conversations: rows } = await conversationsApi.listArchived()
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
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to update project'))
    }
  },

  async setActiveLeaf(id, leafId) {
    // §4.15 R4: switching branches must NOT interrupt a sibling that is still
    // generating. The server-side generation is detached (context.WithoutCancel)
    // and active_leaf is only written at message creation, so a switch can never
    // clobber it — we deliberately do NOT publish a conversation-wide stop here.
    // The off-path stream keeps running and is picked up when its branch reopens.
    try {
      const resp = await conversationsApi.setActiveLeaf(id, leafId)
      const conv = toLocalConversation(resp.conversation)
      conv.messages = resp.messages.map(toLocalMessage)
      set((s) => ({ conversations: replaceOrPrepend(s.conversations, conv) }))
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to switch branch'))
    }
  },

  async deleteMessage(conversationId, messageId) {
    // Deleting under a live stream would race the writer — stop it first.
    const cur = get().conversations.find((c) => c.id === conversationId)
    stopConversationStreams(conversationId, cur?.messages ?? [])
    try {
      const resp = await conversationsApi.deleteMessage(conversationId, messageId)
      // The response carries the refreshed (full) active path; swap it in and
      // clear pagination state since everything is loaded.
      set((s) => ({
        conversations: s.conversations.map((c) =>
          c.id === conversationId
            ? { ...c, messages: resp.messages.map(toLocalMessage), hasOlder: false, olderCursor: undefined }
            : c,
        ),
      }))
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to delete message'))
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

  async loadInlineThreads(sourceConvId) {
    try {
      const rows = await conversationsApi.inlineThreads(sourceConvId)
      const threads = rows.map(toLocalConversation)
      set((s) => {
        let list = s.conversations
        for (const th of threads) {
          // Preserve already-loaded messages if we have them (drawer may be open).
          const existing = list.find((c) => c.id === th.id)
          const merged = existing ? { ...th, messages: existing.messages } : th
          list = replaceOrPrepend(list, merged)
        }
        return { conversations: list }
      })
    } catch {
      // Non-fatal: markers just won't show.
    }
  },

  async createInlineThread(sourceConvId, messageId, quote) {
    try {
      const created = await conversationsApi.createInlineThread(sourceConvId, {
        message_id: messageId,
        quote,
      })
      const conv = toLocalConversation(created)
      set((s) => ({ conversations: [conv, ...s.conversations] }))
      return conv
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to start sub-conversation'))
      return undefined
    }
  },

  async reloadActivePath(id) {
    // Never clobber an in-flight stream — only reconcile once nothing in this
    // conversation is still streaming.
    const cur = get().conversations.find((c) => c.id === id)
    if (!cur || cur.messages.some((m) => m.streaming)) return
    try {
      // Post-turn reconcile fetches only the LATEST page (the just-finished turn
      // is at the tail), so the cost is bounded by MSG_PAGE rather than the whole
      // thread length. Older messages are re-fetched on scroll-up if needed.
      const resp = await conversationsApi.get(id, { limit: MSG_PAGE })
      const messages = resp.messages.map(toLocalMessage)
      set((s) => ({
        conversations: s.conversations.map((c) =>
          c.id !== id
            ? c
            : {
                ...c,
                title: resp.conversation.title || c.title,
                // Adopt the server's fresh updated_at so a regenerated/edited
                // conversation rises to the top of the sidebar (the optimistic
                // path bumps it on send but NOT on regenerate).
                updatedAt: resp.conversation.updated_at ? resp.conversation.updated_at * 1000 : c.updatedAt,
                messages,
                hasOlder: Boolean(resp.has_more),
                olderCursor: resp.next_before,
              },
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
    } catch (e) {
      toast.error(errorMessage(e, 'Failed to update model'))
    }
  },

  async setKBs(id, kbIds) {
    const prevConversations = get().conversations
    set((s) => ({
      conversations: s.conversations.map((c) => (c.id === id ? { ...c, kbIds } : c)),
    }))
    try {
      await conversationsApi.update(id, { kb_ids: kbIds })
    } catch (e) {
      set({ conversations: prevConversations })
      toast.error(errorMessage(e, 'Failed to update knowledge bases'))
    }
  },

  async sendMessage(input) {
    // §4.15: editing a past turn into a NEW branch while the old branch is still
    // generating must stop the old stream first — otherwise two streams race and
    // the old one's late completion clobbers the new branch's active leaf (the
    // new branch appeared next to the first sibling instead of the current one).
    if (input.branch) {
      const cur = get().conversations.find((c) => c.id === input.conversationId)
      stopConversationStreams(input.conversationId, cur?.messages ?? [])
    }
    const abort = new AbortController()
    const conv0 = get().conversations.find((c) => c.id === input.conversationId)
    const userId = uid('m')
    const userMsg: Message = {
      id: userId,
      role: 'user',
      content: input.text,
      createdAt: Date.now(),
      attachments: input.attachments,
      // Give the optimistic turn a real tree position so it is never parentless
      // before the post-stream reconcile. A normal append hangs off the current
      // active leaf (the last message on the visible path); an edit-branch uses
      // the explicit parent. Closes the §4.15 merge bug at the source (an empty
      // parentId on a later edit would re-root onto the first message).
      parentId: input.branch
        ? input.parentId || undefined
        : conv0?.messages[conv0.messages.length - 1]?.id,
    }
    const assistantId = uid('m')
    const assistantMsg: Message = {
      id: assistantId,
      role: 'assistant',
      content: '',
      createdAt: Date.now() + 1,
      streaming: true,
      parentId: userId,
      modelId: input.modelId || conv0?.modelId,
      mode: input.mode,
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
        // §4.15 R1: editing a past question opens a NEW sibling QUESTION under the
        // same parent — so the `< n/m >` picker belongs on the USER bubble (the old
        // question + the new one), NOT on the answer. Seed it optimistically so the
        // switcher shows under the user bubble the instant we resend
        // (reloadActivePath later swaps in server truth). The assistant reply is
        // the SOLE child of the new question, so it gets NO branch metadata — its
        // switcher must not appear.
        let uMsg = userMsg
        if (input.branch) {
          const oldQs = c.messages.filter(
            (m) => m.role === 'user' && (input.parentId ? m.parentId === input.parentId : !m.parentId),
          )
          const prevSiblings = oldQs.flatMap((q) => (q.siblings && q.siblings.length > 0 ? q.siblings : [q.id]))
          const uniq = Array.from(new Set(prevSiblings))
          const prevCount = Math.max(uniq.length, 1)
          uMsg = {
            ...userMsg,
            branchCount: prevCount + 1,
            branchIndex: prevCount,
            siblings: [...uniq, userMsg.id],
          }
        }
        return {
          ...c,
          messages: [...base, uMsg, assistantMsg],
          updatedAt: Date.now(),
          // Remember the param_controls selection so regenerate reuses it.
          lastParams: input.params ?? c.lastParams,
          // Give a brand-new conversation an immediate default title from the
          // first message (first line, clipped) — so the sidebar/header never
          // shows an empty title in the window before the title model (or the
          // backend's deterministic clip) lands. Applies whenever there's no
          // meaningful title yet: empty (the backend creates conversations with
          // a blank title) OR the placeholder, regardless of locale.
          title:
            c.messages.length === 0 && (!c.title.trim() || c.title === 'New conversation')
              ? input.text.replace(/\s+/g, ' ').trim().slice(0, 60) || c.title
              : c.title,
        }
      }),
    }))
    // Hoisted out of the try so the catch below targets the message by its
    // CURRENT id: after `message_start` the message is re-keyed to the backend
    // id, and a mid-stream network drop must patch THAT id (else streaming:true
    // never clears and the spinner spins forever). Falls back to the local id
    // for a failure before message_start (§ stream-error E7).
    let serverAssistantId = assistantId
    // When the turn errors we keep the optimistic message (with its error flag +
    // retry button) and SKIP the tree reconcile — otherwise reloadActivePath
    // would replace it with the server's empty/partial row and the error (a
    // client-only field) would vanish, leaving a blank reply with no retry.
    let errored = false
    try {
      let lastCitations: Citation[] = []
      const toolCallsById = new Map<string, ToolCall>()
      const toolInputBuffers = new Map<string, string>()
      for await (const frame of streamSSE(
        `/conversations/${encodeURIComponent(input.conversationId)}/messages`,
        {
          text: input.text,
          model_id: input.modelId,
          parent_id: input.parentId,
          // §4.15: tell the backend this is a branch edit so an empty parent
          // (editing the root question) stays a root sibling instead of being
          // appended to the active leaf.
          branch: input.branch,
          // 'deep-research' switches the backend to the research engine.
          mode: input.mode,
          attachments: input.attachments?.map(attachmentToApi),
          params: input.params,
          image_style_id: input.imageStyleId,
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
            // Re-key the abort controller so abortStream (which uses the server
            // id from the UI message) can actually cancel the local SSE reader.
            if (serverAssistantId !== assistantId) {
              const ctrl = streamControllers.get(assistantId)
              if (ctrl) {
                streamControllers.set(serverAssistantId, ctrl)
                streamControllers.delete(assistantId)
              }
            }
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
          case 'image_status':
            // §4.20 drawing phase → dedicated generating UI. Normalise the status
            // (don't blind-cast an arbitrary string into the union).
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              imageStatus: ev.status === 'optimizing' ? 'optimizing' : 'generating',
            }))
            break
          case 'artifact':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              // The image arrived — drop the drawing placeholder.
              imageStatus: undefined,
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
          case 'research_plan':
          case 'research_task':
          case 'research_source':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              research: applyResearchEvent(m.research, ev),
            }))
            break
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
            // Pre-tool narration the model already streamed becomes a reasoning
            // step, so the final answer is only the post-tool text (§4.3).
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => {
              const flushed = flushNarration(m)
              return {
                ...m,
                content: flushed.content,
                reasoning: appendToolStart(flushed.reasoning, tc),
              }
            })
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
              source: c.source,
            }
            lastCitations = [...lastCitations, cit]
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              citations: lastCitations,
            }))
            break
          }
          case 'error':
            errored = true
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              streaming: false,
              imageStatus: undefined,
              error: ev.message || 'error',
            }))
            break
          case 'done':
            updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
              ...m,
              streaming: false,
              imageStatus: undefined,
              credits: ev.credits && ev.credits > 0 ? ev.credits : m.credits,
              moderation: ev.stop_reason === 'content_moderation' ? true : m.moderation,
              quotaExceeded: ev.stop_reason === 'quota_exceeded' ? true : m.quotaExceeded,
            }))
            break
        }
      }
      // The stream ended. If we never received a terminal `done`/`error` (a
      // clean EOF mid-flight, or the upstream closed without a final event), the
      // assistant could be stuck `streaming:true` — never leave an empty,
      // spinning bubble. Finalize it: keep partial content, else mark it failed.
      {
        const am = get()
          .conversations.find((c) => c.id === input.conversationId)
          ?.messages.find((m) => m.id === serverAssistantId)
        if (am?.streaming) {
          const hasOutput =
            Boolean(am.content?.trim()) || (am.reasoning?.length ?? 0) > 0 || (am.artifacts?.length ?? 0) > 0
          // A user-initiated stop is a deliberate halt, not a failure — keep the
          // partial reply and never show the retry banner.
          const stopped = abort.signal.aborted
          updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
            ...m,
            streaming: false,
            // §4.20: clear the drawing placeholder on any non-terminal stream end
            // (the `done`/`error`/`artifact` cases already do) so it can't spin forever.
            imageStatus: undefined,
            error: stopped || hasOutput ? m.error : m.error || 'The reply ended unexpectedly. Please try again.',
          }))
          if (!hasOutput && !stopped) errored = true
        }
      }
      // Stream finished cleanly — reconcile to the canonical tree path so the
      // user/assistant siblings collapse and the `< n/m >` picker appears. Skip
      // it on an error turn so the error message + retry button survive (a
      // reconcile would replace them with the server's empty row).
      if (!errored) await get().reloadActivePath(input.conversationId)
    } catch (e) {
      // Target the CURRENT server id so the patch lands even after the
      // message_start re-key; always clear streaming so the spinner stops. A
      // user-initiated stop aborts the local reader before the terminal `done`
      // frame arrives — that AbortError is NOT a failure, so keep the partial
      // reply and skip the retry banner. Otherwise mark the turn failed. We do
      // NOT reconcile here — that would wipe the (client-only) error flag.
      updateAssistant(set, input.conversationId, serverAssistantId, (m) => ({
        ...m,
        streaming: false,
        // §4.20: a stop / mid-stream drop on a drawing turn must clear the
        // ImageGenerating placeholder (it renders independent of streaming/error).
        imageStatus: undefined,
        error: abort.signal.aborted ? m.error : errorMessage(e),
      }))
    } finally {
      streamControllers.delete(serverAssistantId)
      streamControllers.delete(assistantId)
    }
  },

  async regenerate(conversationId, assistantId, modelId) {
    const abort = new AbortController()
    const conv = get().conversations.find((c) => c.id === conversationId)
    streamControllers.set(assistantId + '-regen', abort)
    // §4.15: regenerate forks at the assistant — the new reply is a SIBLING
    // of the old one under the same user turn, not an append. Truncate the
    // visible path to that user parent (dropping the old reply and anything
    // below it) before showing the streaming placeholder, so the screen never
    // stacks two replies. The post-stream reconcile then restores the old
    // sibling behind the `< n/m >` picker.
    const oldAssistant = conv?.messages.find((m) => m.id === assistantId)
    const userParentId = oldAssistant?.parentId
    // Carry the original turn's mode so regenerating a deep-research reply
    // re-runs the research engine (and shows the panel) instead of downgrading.
    const mode = oldAssistant?.mode
    const placeholderId = uid('m')
    // §4.15 R2: regenerate forks at the assistant — the new reply is a SIBLING of
    // the old one under the same user turn. Seed branch metadata on the
    // placeholder so the `< n/m >` switcher shows under the reply IMMEDIATELY,
    // before any token arrives (reloadActivePath later swaps in server truth). The
    // user message is left untouched.
    const oldReplies = conv?.messages.filter((m) => m.role === 'assistant' && m.parentId === userParentId) ?? []
    const prevReplySiblings =
      oldAssistant?.siblings && oldAssistant.siblings.length > 0
        ? oldAssistant.siblings
        : oldReplies.length > 0
          ? oldReplies.map((m) => m.id)
          : oldAssistant
            ? [oldAssistant.id]
            : []
    const uniqReplies = Array.from(new Set(prevReplySiblings))
    const prevReplyCount = Math.max(uniqReplies.length, 1)
    // Hoisted so the catch can clear the streaming placeholder by its CURRENT
    // id (re-keyed to the backend id after message_start), mirroring sendMessage
    // (§ stream-error E7).
    let serverAssistantId = placeholderId
    try {
      const placeholder: Message = {
        id: placeholderId,
        role: 'assistant',
        content: '',
        createdAt: Date.now(),
        streaming: true,
        modelId: modelId ?? conv?.modelId,
        mode,
        branchCount: prevReplyCount + 1,
        branchIndex: prevReplyCount,
        siblings: [...uniqReplies, placeholderId],
      }
      set((s) => ({
        conversations: s.conversations.map((c) => {
          if (c.id !== conversationId) return c
          const base = userParentId
            ? truncateToParent(c.messages, userParentId)
            : c.messages.filter((m) => m.id !== assistantId)
          // Bump updatedAt so a regenerated conversation floats to the top of the
          // sidebar immediately (parity with sendMessage).
          return { ...c, messages: [...base, placeholder], updatedAt: Date.now() }
        }),
      }))
      const toolCallsById = new Map<string, ToolCall>()
      const toolInputBuffers = new Map<string, string>()
      let lastCitations: Citation[] = []
      for await (const frame of streamSSE(
        `/conversations/${encodeURIComponent(conversationId)}/regenerate`,
        { assistant_id: assistantId, model_id: modelId, mode, params: conv?.lastParams },
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
            updateAssistant(set, conversationId, serverAssistantId, (m) => {
              const flushed = flushNarration(m)
              return {
                ...m,
                content: flushed.content,
                reasoning: appendToolStart(flushed.reasoning, tc),
              }
            })
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
              { id: c.id, index: c.index, title: c.title, url: c.url, domain: safeDomain(c.url), snippet: c.snippet, source: c.source },
            ]
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              citations: lastCitations,
            }))
            break
          }
          case 'message_start':
            serverAssistantId = ev.message_id ?? placeholderId
            updateAssistant(set, conversationId, placeholderId, (m) => ({
              ...m,
              id: serverAssistantId,
              // Re-point the optimistic sibling list (R2) at the server id so the
              // `< n/m >` picker stays self-consistent until reloadActivePath.
              siblings: m.siblings?.map((sid) => (sid === placeholderId ? serverAssistantId : sid)),
            }))
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
          case 'research_plan':
          case 'research_task':
          case 'research_source':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              research: applyResearchEvent(m.research, ev),
            }))
            break
          case 'image_status':
            // §4.20 drawing phase (regenerated image turn) → dedicated UI.
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              imageStatus: ev.status === 'optimizing' ? 'optimizing' : 'generating',
            }))
            break
          case 'artifact':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              imageStatus: undefined,
              artifacts: [
                ...(m.artifacts ?? []),
                { id: ev.id ?? uid('art'), filename: ev.title ?? 'file', url: ev.url ?? '', mimeType: ev.summary ?? '' },
              ],
            }))
            break
          case 'refusal':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              imageStatus: undefined,
              refused: true,
              content: m.content || (ev.message ?? 'The model declined to answer.'),
            }))
            break
          case 'done':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              streaming: false,
              imageStatus: undefined,
              moderation: ev.stop_reason === 'content_moderation' ? true : m.moderation,
            }))
            break
          case 'error':
            updateAssistant(set, conversationId, serverAssistantId, (m) => ({
              ...m,
              streaming: false,
              imageStatus: undefined,
              content: m.content + `\n\n*Regeneration failed: ${ev.message}*`,
            }))
            break
        }
      }
      // Reconcile so the freshly generated reply and the previous one show up as
      // siblings with a `< n/m >` picker instead of two stacked bubbles (§4.15).
      await get().reloadActivePath(conversationId)
    } catch (e) {
      // A user-initiated stop aborts the reader before the terminal frame — keep
      // the partial reply and reconcile to the server's persisted stopped turn,
      // without the interrupted note or error toast.
      if (abort.signal.aborted) {
        updateAssistant(set, conversationId, serverAssistantId, (m) => ({ ...m, streaming: false }))
        await get().reloadActivePath(conversationId)
      } else {
        // A mid-stream drop on regenerate must clear the placeholder's
        // streaming state (by its CURRENT id) so the spinner stops, surface a
        // toast, and reconcile to the canonical path — mirroring sendMessage
        // (§ stream-error E7).
        updateAssistant(set, conversationId, serverAssistantId, (m) => ({
          ...m,
          streaming: false,
          content: m.content + (m.content ? '\n\n' : '') + `*Regeneration interrupted: ${errorMessage(e)}*`,
        }))
        toast.error(errorMessage(e, 'Regeneration failed'))
        await get().reloadActivePath(conversationId)
      }
    } finally {
      streamControllers.delete(assistantId + '-regen')
    }
  },

  async editMessageInPlace(conversationId, messageId, text) {
    // Optimistic overwrite of the visible content; persist in the background.
    set((s) => ({
      conversations: s.conversations.map((c) =>
        c.id !== conversationId
          ? c
          : {
              ...c,
              messages: c.messages.map((m) => (m.id === messageId ? { ...m, content: text } : m)),
            },
      ),
    }))
    try {
      await conversationsApi.editMessage(conversationId, messageId, text)
    } catch {
      /* keep the optimistic copy if the PATCH fails */
    }
  },

  async setFeedback(conversationId, messageId, next) {
    // Optimistically reflect the rating (mutually exclusive); revert on failure.
    const prev = get()
      .conversations.find((c) => c.id === conversationId)
      ?.messages.find((m) => m.id === messageId)
    const prevLiked = prev?.liked ?? false
    const prevDisliked = prev?.disliked ?? false
    updateAssistant(set, conversationId, messageId, (m) => ({
      ...m,
      liked: next === 'like',
      disliked: next === 'dislike',
    }))
    try {
      await conversationsApi.feedback(conversationId, messageId, next)
    } catch {
      updateAssistant(set, conversationId, messageId, (m) => ({ ...m, liked: prevLiked, disliked: prevDisliked }))
      toast.error('Failed to save feedback')
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
    inline: c.inline_source_conv
      ? {
          sourceConvId: c.inline_source_conv,
          messageId: c.inline_parent_id ?? '',
          quote: c.inline_quote ?? '',
        }
      : undefined,
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

// --- Deep Research panel state (§ deep-research mode) ----------------------
// Folds research_plan/research_task/research_source SSE events into one
// ResearchState. Shared by the sendMessage + regenerate stream loops.
function parseRound(name?: string): number | undefined {
  if (!name) return undefined
  const m = name.match(/(\d+)/)
  return m ? Number(m[1]) : undefined
}

function applyResearchEvent(prev: ResearchState | undefined, ev: ApiSseEvent): ResearchState {
  const r: ResearchState = prev ?? { title: '', tasks: [], sources: [] }
  if (ev.type === 'research_plan') {
    return { ...r, title: ev.text ?? r.title }
  }
  if (ev.type === 'research_task') {
    const tasks = r.tasks.slice()
    const i = tasks.findIndex((t) => t.id === ev.id)
    const status = (ev.status as ResearchTask['status']) ?? (i >= 0 ? tasks[i].status : 'pending')
    const round = parseRound(ev.name) ?? (i >= 0 ? tasks[i].round : undefined)
    if (i >= 0) {
      tasks[i] = { ...tasks[i], status, round, question: ev.text ?? tasks[i].question }
    } else {
      tasks.push({ id: ev.id, question: ev.text ?? '', status, round })
    }
    return { ...r, tasks }
  }
  if (ev.type === 'research_source') {
    const sources = r.sources.slice()
    const i = sources.findIndex((s) => s.id === ev.id)
    if (i >= 0) {
      sources[i] = {
        ...sources[i],
        status: (ev.status as ResearchSource['status']) ?? sources[i].status,
        verdict: ev.summary || sources[i].verdict,
        url: ev.url ?? sources[i].url,
        title: ev.title ?? sources[i].title,
      }
    } else {
      sources.push({
        id: ev.id,
        url: ev.url ?? '',
        title: ev.title ?? ev.url ?? '',
        domain: safeDomain(ev.url ?? ''),
        status: (ev.status as ResearchSource['status']) ?? 'found',
        verdict: ev.summary,
      })
    }
    return { ...r, sources }
  }
  return r
}

// appendNarration moves the model's pre-tool "let me look this up…" text into
// the reasoning trace (§4.3) so it doesn't pollute the final answer. Merges
// into a trailing narration run if one is already open.
function appendNarration(reasoning: ReasoningItem[], text: string): ReasoningItem[] {
  if (!text.trim()) return reasoning
  const last = reasoning[reasoning.length - 1]
  if (last && last.kind === 'narration') {
    return [...reasoning.slice(0, -1), { ...last, text: last.text + text }]
  }
  return [...reasoning, { kind: 'narration', id: uid('rn'), text }]
}

// flushNarration moves the in-progress answer text into the trace as a
// narration step and clears the answer buffer — called when a tool round starts
// mid-answer, so the answer ends up being only the final (post-tool) text.
function flushNarration(m: Message): { content: string; reasoning: ReasoningItem[] } {
  if (m.content.trim()) {
    return { content: '', reasoning: appendNarration(m.reasoning ?? [], m.content) }
  }
  return { content: m.content, reasoning: m.reasoning ?? [] }
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
  let research: ResearchState | undefined
  // Text accumulates into `pendingText`; when a tool_call follows, that text was
  // pre-tool narration → flush it into the trace. Only the trailing text (after
  // the last tool_call) is the final answer (§4.3 — mirrors the live flush).
  let pendingText = ''
  let idx = 0
  for (const b of m.blocks ?? []) {
    idx++
    if (b.kind === 'text') {
      pendingText += b.text ?? ''
    } else if (b.kind === 'research') {
      // Deep Research panel state — rehydrate it; presence implies the turn was
      // a deep-research turn.
      try {
        research = JSON.parse(b.text ?? '{}') as ResearchState
      } catch {
        /* ignore malformed state */
      }
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
      // Flush any narration that preceded this tool into the trace.
      if (pendingText.trim()) {
        reasoning.push({ kind: 'narration', id: `${m.id}-n${idx}`, text: pendingText })
        pendingText = ''
      }
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
  // Trailing text after the last tool_call (or all text when there were no
  // tools) is the final answer.
  const content = pendingText
  return {
    id: m.id,
    parentId: m.parent_id || undefined,
    role: m.role,
    content,
    reasoning: reasoning.length > 0 ? reasoning : undefined,
    research,
    mode: research ? 'deep-research' : undefined,
    artifacts: artifacts.length > 0 ? artifacts : undefined,
    modelId: m.model_id || undefined,
    modelLabel: m.model_label || undefined,
    createdAt: m.created_at * 1000,
    streaming: m.status === 'streaming',
    cost: m.cost > 0 ? m.cost : undefined,
    credits: m.credits && m.credits > 0 ? m.credits : undefined,
    genMs: m.gen_ms && m.gen_ms > 0 ? m.gen_ms : undefined,
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
            source: c.source,
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
    liked: m.feedback === 'like',
    disliked: m.feedback === 'dislike',
    moderation: m.stop_reason === 'content_moderation',
    quotaExceeded: m.stop_reason === 'quota_exceeded',
    refused:
      m.stop_reason === 'content_moderation' ||
      m.stop_reason === 'content_filter' ||
      m.stop_reason === 'refusal' ||
      m.stop_reason === 'safety',
    // Never render an empty bubble: surface a persisted error, and treat a
    // finished-but-empty assistant turn (upstream failed without a usable reply,
    // no refusal/moderation/quota) as a failure so the retry banner shows.
    error: errorFromApiMessage(m, content, reasoning.length, artifacts.length, Boolean(research)),
  }
}

// errorFromApiMessage decides whether a reloaded message should show the red
// "reply failed — retry" banner: an explicit error status/string, or an
// assistant turn that finished with no content, reasoning, or artifacts and
// wasn't a refusal/moderation/quota stop.
function errorFromApiMessage(
  m: ApiMessage,
  content: string,
  reasoningCount: number,
  artifactCount: number,
  hasResearch: boolean,
): string | undefined {
  if (m.error && m.error.trim()) return m.error.trim()
  const refusalLike =
    m.stop_reason === 'content_moderation' ||
    m.stop_reason === 'content_filter' ||
    m.stop_reason === 'refusal' ||
    m.stop_reason === 'safety' ||
    m.stop_reason === 'quota_exceeded' ||
    // A user-stopped turn is a deliberate halt, not a failure — even if it
    // produced no content before the stop, never show the retry banner.
    m.stop_reason === 'stopped' ||
    m.status === 'stopped'
  const emptyAssistant =
    m.role === 'assistant' &&
    m.status !== 'streaming' &&
    !content.trim() &&
    reasoningCount === 0 &&
    artifactCount === 0 &&
    !hasResearch &&
    !refusalLike
  if (m.status === 'error' || emptyAssistant) {
    return 'The model returned no response. Please try again.'
  }
  return undefined
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
        : // NOTE: do NOT bump updatedAt here. This runs on every streamed token;
          // changing updatedAt would re-sort the sidebar and (via the summary
          // equality below) force every list subscriber to re-render 60×/s. The
          // conversation is already hoisted to the top by sendMessage/regenerate
          // when the turn starts, which is the only time its position should move.
          { ...c, messages: c.messages.map((m) => (m.id === assistantId ? patch(m) : m)) },
    ),
  }))
}

/**
 * sameConvListShape is an equality fn for list subscribers (sidebar, command
 * menu) that care about a conversation's SUMMARY — id, title, flags, position —
 * but NOT its message content. Returning true skips the re-render. This is what
 * stops the per-token streaming updates (which only mutate `messages`) from
 * re-running the sidebar's filter/sort/bucket pipeline and reconciling every row.
 * Pass it as the second arg to useConversations(selector, sameConvListShape).
 */
export function sameConvListShape(a: Conversation[], b: Conversation[]): boolean {
  if (a === b) return true
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    if (x === y) continue
    if (
      x.id !== y.id ||
      x.title !== y.title ||
      x.updatedAt !== y.updatedAt ||
      x.pinned !== y.pinned ||
      x.starred !== y.starred ||
      x.archived !== y.archived ||
      x.projectId !== y.projectId ||
      Boolean(x.inline) !== Boolean(y.inline)
    ) {
      return false
    }
  }
  return true
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
