/**
 * realtime — the §23 notify stream client (multi-device live sync).
 *
 * One long-lived `GET /api/events` SSE connection per tab. The server pushes
 * thin "something changed" events (ids only); this module re-fetches through
 * the normal authorized endpoints and merges the result into the conversations
 * store WITHOUT flashing any loading state:
 *
 *   conversation.created / .deleted / .hidden / .updated → silent sidebar-list merge
 *   conversation.updated (open, non-streaming)  → refresh that transcript
 *   hello (connect / reconnect)                 → version check + compensation
 *
 * Safety rules ("不能错乱"):
 *   - events originating from THIS tab (matching X-Device-Id echo) skip the
 *     transcript refetch — the tab already updated optimistically — but still
 *     schedule a throttled list sync, so a stale background merge that raced a
 *     local mutation (pin/rename/archive) always converges back to the server;
 *   - deleted ids are tombstoned (sync-guards) so an in-flight list response
 *     fetched before the delete can never resurrect the row;
 *   - a conversation with a live local stream, a locally-errored turn (its
 *     retry affordance is client-only state), or extra scroll-back pages
 *     loaded is never transcript-refreshed — the list merge still updates its
 *     sidebar row;
 *   - the list merge is a superset merge: it upserts server rows but never
 *     drops locally-loaded conversations beyond page 1, so a long-scrolled
 *     sidebar isn't truncated by a background sync. Deletions are applied only
 *     from explicit `conversation.deleted` events; rows that silently leave
 *     the list (archived / moved elsewhere) get a targeted metadata refetch.
 */

import { streamSSEGet } from '@/api/client'
import { conversationsApi } from '@/api/endpoints'
import { checkForUpdate } from '@/lib/app-update'
import { invalidateAccessState } from '@/lib/access-events'
import { getDeviceId } from '@/lib/device-id'
import { isConversationTombstoned, markConversationsDeleted } from '@/lib/sync-guards'
import { useAuth } from '@/store/auth'
import {
  useConversations,
  toLocalConversation,
  collectDoomedConversationIds,
  captureKnowledgeBaseSelectionGuard,
  preservePendingKnowledgeBaseSelection,
  CONV_PAGE,
  MSG_PAGE,
} from '@/store/conversations'
import { activeWorkspaceId, useWorkspaces } from '@/store/workspaces'
import { useModels } from '@/store/models'
import { envNum } from '@/lib/env-config'
import type { Conversation } from '@/types/chat'
import type { KnowledgeBaseSelectionRequestGuard } from '@/store/conversations'

const SYNC_THROTTLE_MS = envNum('VITE_AIVORY_REALTIME_SYNC_THROTTLE_MS', 800)
const RECONNECT_MAX_MS = 30_000
// Rows that vanished from the synced page get an individual metadata refetch
// (archive/move detection); bounded per sync so a pathological burst can't fan
// out into dozens of GETs.
const META_REFRESH_MAX = 5

interface RealtimeEvent {
  type?: string
  conversation_id?: string
  origin?: string
}

let initialized = false
let routeEnabled = false
let syncLifecycle: (() => void) | null = null
let lastHelloUserId: string | null = null
let currentUserId: string | null = null
let currentConnectionKey: string | null = null
let controller: AbortController | null = null
// Generation counter: a runLoop sleeping in its backoff can't be aborted, so a
// fast A→B→A user switch could wake a stale loop whose uid matches again. Each
// sync() bumps the generation; stale loops see the mismatch and exit.
let loopGen = 0

/** Wire the auth-driven lifecycle once (called from App on mount). */
export function initRealtime(): void {
  if (initialized) return
  initialized = true
  const sync = () => {
    const authenticatedUser = useAuth.getState().user
    if (!authenticatedUser || (lastHelloUserId && lastHelloUserId !== authenticatedUser.id)) {
      lastHelloUserId = null
    }
    const user = routeEnabled ? authenticatedUser : null
    const uid = user?.id ?? null
    const connectionKey = uid ? `${uid}:${user?.group_id ?? ''}` : null
    if (connectionKey === currentConnectionKey) return
    const userChanged = uid !== currentUserId
    controller?.abort()
    controller = null
    currentUserId = uid
    currentConnectionKey = connectionKey
    if (userChanged) resetListSync()
    const gen = ++loopGen
    if (uid && connectionKey) void runLoop(uid, connectionKey, gen)
  }
  syncLifecycle = sync
  useAuth.subscribe(sync)
  sync()
}

/** Keep the conversation event stream scoped to routes that render chat data. */
export function setRealtimeEnabled(enabled: boolean): void {
  if (routeEnabled === enabled) return
  routeEnabled = enabled
  syncLifecycle?.()
}

async function runLoop(uid: string, connectionKey: string, gen: number): Promise<void> {
  let attempt = 0
  let everConnected = false
  const resumingRoute = lastHelloUserId === uid
  while (currentUserId === uid && currentConnectionKey === connectionKey && gen === loopGen) {
    const ctl = new AbortController()
    controller = ctl
    try {
      for await (const frame of streamSSEGet('/events', ctl.signal, undefined, { silentReconnect: true })) {
        // An async iterator may yield one already-buffered frame after abort.
        // Re-check ownership for every frame so an old account/group connection
        // cannot recreate notifications or mutate the new session after cleanup.
        if (
          ctl.signal.aborted ||
          currentUserId !== uid ||
          currentConnectionKey !== connectionKey ||
          gen !== loopGen
        ) return
        const ev = (frame.data ?? null) as RealtimeEvent | null
        if (!ev || typeof ev !== 'object') continue
        if (ev.type === 'hello') {
          attempt = 0
          // The first hello immediately follows AuthGate's authoritative startup
          // loads, so replaying them here only doubles the request burst. A real
          // reconnect can have missed deploy/config/events and must reconcile.
          if (everConnected || resumingRoute) {
            void checkForUpdate()
            void reconcileAccessState('account')
          }
          lastHelloUserId = uid
          everConnected = true
          continue
        }
        handleEvent(ev)
      }
    } catch {
      /* connection failed — fall through to backoff */
    }
    if (
      ctl.signal.aborted ||
      currentUserId !== uid ||
      currentConnectionKey !== connectionKey ||
      gen !== loopGen
    ) return
    attempt++
    const delay = Math.min(RECONNECT_MAX_MS, 1000 * 2 ** Math.min(attempt, 5)) + Math.floor(Math.random() * 500)
    await new Promise((resolve) => setTimeout(resolve, delay))
  }
}

function handleEvent(ev: RealtimeEvent): void {
  if (ev.type?.startsWith('compaction.')) {
    // Automatic compaction is background maintenance. Only an explicit
    // /compact command owns user-visible progress and result feedback.
    return
  }
  switch (ev.type) {
    case 'account.permissions_updated':
      void reconcileAccessState('account')
      return
    case 'workspace.permissions_updated':
    case 'workspace.membership_updated':
    case 'workspace.policy_updated':
      void reconcileAccessState('workspace')
      return
    case 'knowledge_base.access_updated':
      invalidateAccessState({ kind: 'knowledge-base' })
      scheduleListSync(true)
      return
  }
  // Own echo: this tab caused the change and already shows it optimistically.
  // Still converge the sidebar via a throttled list sync — a background sync
  // fetched BEFORE this mutation but merged AFTER it may have clobbered the
  // optimistic row, and with the echo fully suppressed nothing would ever
  // re-correct it. The transcript refetch stays skipped.
  if (ev.origin && ev.origin === getDeviceId()) {
    scheduleListSync(false)
    return
  }
  switch (ev.type) {
    case 'conversation.deleted':
      if (ev.conversation_id) applyRemoteDelete(ev.conversation_id)
      scheduleListSync(false)
      break
    case 'conversation.hidden':
      if (ev.conversation_id) applyRemoteHide(ev.conversation_id)
      scheduleListSync(false)
      break
    case 'conversation.created':
      scheduleListSync(false)
      break
    case 'conversation.updated': {
      scheduleListSync(false)
      const id = ev.conversation_id
      if (!id) break
      const conv = useConversations.getState().conversations.find((c) => c.id === id)
      if (!conv) break
      if (conv.messages.length === 0) {
        // Not hydrated locally — the list sync updates its sidebar row, but if
        // the row left the list (archived / moved), only a targeted metadata
        // fetch can learn that. runListSync picks these up after its merge.
        pendingMetaIds.add(id)
        break
      }
      // Refresh the transcript only when it's actually hydrated locally (the
      // user has it open / recently open) and the refetch cannot destroy
      // anything client-only: a live stream, an errored turn's retry
      // affordance, or scroll-back pages beyond the first (loadOne replaces
      // the transcript with page 1). loadOne repeats the streaming guard.
      const risky =
        conv.messages.some((m) => m.streaming || m.error) || conv.messages.length > MSG_PAGE
      if (!risky) void useConversations.getState().loadOne(id)
      break
    }
  }
}

let accessReconcile: Promise<void> | null = null
let accessNeedsAccount = false
let accessNeedsWorkspace = false

function reconcileAccessState(kind: 'account' | 'workspace'): Promise<void> {
  if (kind === 'account') accessNeedsAccount = true
  else accessNeedsWorkspace = true
  if (accessReconcile) return accessReconcile
  const uid = currentUserId
  accessReconcile = (async () => {
    while ((accessNeedsAccount || accessNeedsWorkspace) && uid && useAuth.getState().user?.id === uid) {
      const refreshAccount = accessNeedsAccount
      const refreshWorkspace = accessNeedsWorkspace
      accessNeedsAccount = false
      accessNeedsWorkspace = false
      if (refreshAccount) await useAuth.getState().refreshProfile()
      if (useAuth.getState().user?.id !== uid) return
      await Promise.all([
        refreshAccount || refreshWorkspace ? useWorkspaces.getState().load() : Promise.resolve(),
        // §workspace RBAC: workspace policy changes reshape the model catalog
        // (and account changes always do) — reload without waiting for a
        // re-login.
        refreshAccount || refreshWorkspace ? useModels.getState().load() : Promise.resolve(),
      ])
      if (useAuth.getState().user?.id !== uid) return
      invalidateAccessState({ kind: refreshAccount ? 'account' : 'workspace' })
      scheduleListSync(true)
    }
  })().finally(() => {
    accessReconcile = null
  })
  return accessReconcile
}

function applyRemoteDelete(id: string): void {
  // Cascade like the local delete: inline sub-conversations anchored to the
  // deleted conversation die with it (mirrors the backend cascade), and every
  // doomed id is tombstoned so an in-flight list sync can't resurrect it.
  const doomed = collectDoomedConversationIds(useConversations.getState().conversations, id)
  markConversationsDeleted(doomed)
  useConversations.setState((s) => ({
    conversations: s.conversations.filter((c) => !doomed.has(c.id)),
  }))
}

function applyRemoteHide(id: string): void {
  // Visibility can be restored later, so unlike a real delete this must not
  // tombstone the id. A later conversation.created event can reinsert it.
  useConversations.setState((s) => ({
    conversations: s.conversations.filter((conversation) => conversation.id !== id),
  }))
}

// ---- silent sidebar-list sync (throttled, superset merge) ------------------

let syncTimer: ReturnType<typeof setTimeout> | null = null
let syncing = false
let syncQueued = false
const pendingMetaIds = new Set<string>()

/** Drop all queued sync work (user switched — none of it is theirs anymore). */
function resetListSync(): void {
  if (syncTimer) {
    clearTimeout(syncTimer)
    syncTimer = null
  }
  syncQueued = false
  pendingMetaIds.clear()
}

function scheduleListSync(immediate: boolean): void {
  if (immediate) {
    if (syncTimer) {
      clearTimeout(syncTimer)
      syncTimer = null
    }
    void runListSync()
    return
  }
  if (syncTimer) return // trailing throttle — one sync absorbs an event burst
  syncTimer = setTimeout(() => {
    syncTimer = null
    void runListSync()
  }, SYNC_THROTTLE_MS)
}

async function runListSync(): Promise<void> {
  if (syncing) {
    syncQueued = true
    return
  }
  // Snapshot who this fetch is FOR — comparing live values on both sides after
  // the await would pass even when the whole identity changed underneath us.
  const uid = currentUserId
  if (!uid) return
  const kbSelectionGuards = new Map(
    useConversations.getState().conversations.map((conversation) => [conversation.id, captureKnowledgeBaseSelectionGuard(conversation.id)]),
  )
  syncing = true
  try {
    const ws = activeWorkspaceId()
    const { conversations: rows } = await conversationsApi.list(undefined, CONV_PAGE, 0, ws)
    // A workspace/user switch mid-flight: this page belongs to the old
    // identity — merging it would bleed foreign rows into the new view.
    if (ws !== activeWorkspaceId() || currentUserId !== uid || useAuth.getState().user?.id !== uid) return
    const incoming = rows.map(toLocalConversation)
    useConversations.setState((s) => ({
      conversations: mergeRemoteList(s.conversations, incoming, kbSelectionGuards),
    }))
    await refreshMissingMeta(incoming, uid, ws)
  } catch {
    /* silent — the next event or reconnect tries again */
  } finally {
    syncing = false
    if (syncQueued) {
      syncQueued = false
      scheduleListSync(false)
    }
  }
}

/**
 * A `conversation.updated` for a non-hydrated row that then does NOT appear in
 * the synced page means the row left the list (archived, moved project/space).
 * The superset merge deliberately never drops rows, so fetch those few ids
 * individually and merge their committed metadata (archived etc. — the sidebar
 * filters archived rows out).
 */
async function refreshMissingMeta(incoming: Conversation[], uid: string, ws: string | undefined): Promise<void> {
  if (pendingMetaIds.size === 0) return
  const inPage = new Set(incoming.map((c) => c.id))
  const missing: string[] = []
  for (const id of pendingMetaIds) {
    if (!inPage.has(id)) missing.push(id)
  }
  pendingMetaIds.clear()
  for (const id of missing.slice(0, META_REFRESH_MAX)) {
    try {
      const kbSelectionGuard = captureKnowledgeBaseSelectionGuard(id)
      const resp = await conversationsApi.get(id, { limit: 1 })
      if (ws !== activeWorkspaceId() || currentUserId !== uid) return
      const meta = toLocalConversation(resp.conversation)
      useConversations.setState((s) => ({
        conversations: s.conversations.map((c) =>
          c.id === id
            ? {
                ...preservePendingKnowledgeBaseSelection(meta, c, kbSelectionGuard),
                messages: c.messages,
                lastParams: c.lastParams,
                hasOlder: c.hasOlder,
                olderCursor: c.olderCursor,
              }
            : c,
        ),
      }))
    } catch {
      /* 404 = deleted meanwhile — the conversation.deleted event handles it */
    }
  }
}

/**
 * Superset merge: upsert `incoming` (newest server page) into `existing`.
 * Local transcript state (messages, pagination cursors) always survives; a
 * locally-streaming conversation additionally keeps its optimistic title and
 * sort bump. Rows beyond the synced page are left untouched; tombstoned ids
 * (deleted while this page was in flight) are never re-inserted.
 */
function mergeRemoteList(
  existing: Conversation[],
  incoming: Conversation[],
  kbSelectionGuards?: Map<string, KnowledgeBaseSelectionRequestGuard>,
): Conversation[] {
  const merged = new Map<string, Conversation>(existing.map((c) => [c.id, c]))
  for (const next of incoming) {
    if (isConversationTombstoned(next.id)) continue
    const cur = merged.get(next.id)
    if (!cur) {
      merged.set(next.id, next)
      continue
    }
    const streaming = cur.messages.some((m) => m.streaming)
    const protectedNext = preservePendingKnowledgeBaseSelection(next, cur, kbSelectionGuards?.get(next.id))
    merged.set(next.id, {
      ...protectedNext,
      messages: cur.messages,
      lastParams: cur.lastParams,
      hasOlder: cur.hasOlder,
      olderCursor: cur.olderCursor,
      updatedAt: streaming ? Math.max(cur.updatedAt, next.updatedAt) : next.updatedAt,
      title: streaming ? next.title || cur.title : next.title,
    })
  }
  return [...merged.values()].sort((a, b) => b.updatedAt - a.updatedAt)
}
