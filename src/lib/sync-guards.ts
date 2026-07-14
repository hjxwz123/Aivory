/**
 * sync-guards — tiny shared registries that keep §23 background sync and the
 * invisible version upgrade from corrupting or destroying local state. Zero
 * imports on purpose: both the conversations store and the realtime/app-update
 * modules depend on this file, so it must sit below them in the module graph.
 */

// ---- conversation tombstones ------------------------------------------------
// A list-sync response fetched BEFORE a delete but merged AFTER it would
// resurrect the deleted row (the superset merge only upserts). Both delete
// paths (local optimistic + remote event) tombstone the ids; the merge refuses
// to re-insert them. TTL'd — after a few minutes any genuine re-appearance
// (e.g. an undelete feature someday) wins again.

const TOMBSTONE_TTL_MS = 5 * 60_000

const tombstones = new Map<string, number>()

export function markConversationsDeleted(ids: Iterable<string>): void {
  const expiry = Date.now() + TOMBSTONE_TTL_MS
  for (const id of ids) tombstones.set(id, expiry)
}

/** Rollback hook: a failed DELETE restores the rows, so drop their tombstones. */
export function unmarkConversationsDeleted(ids: Iterable<string>): void {
  for (const id of ids) tombstones.delete(id)
}

export function isConversationTombstoned(id: string): boolean {
  const expiry = tombstones.get(id)
  if (!expiry) return false
  if (Date.now() > expiry) {
    tombstones.delete(id)
    return false
  }
  return true
}

// ---- reload blockers --------------------------------------------------------
// The invisible upgrade may only reload the page when nothing of value would
// be lost. Message streaming is checked separately (app-update reads the
// conversations store); everything else that must survive — unsent composer
// text, an in-flight upload, an active voice recording — registers here.

let reloadBlockers = 0

/** Marks the page unsafe to auto-reload; call the returned release exactly once. */
export function blockReload(): () => void {
  reloadBlockers++
  let released = false
  return () => {
    if (released) return
    released = true
    reloadBlockers--
  }
}

export function isReloadBlocked(): boolean {
  return reloadBlockers > 0
}
