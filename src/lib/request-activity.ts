import { useSyncExternalStore } from 'react'

export type RequestActivityMode = 'foreground' | 'background'

export interface RequestActivitySnapshot {
  pending: number
  active: boolean
  slow: boolean
}

export const REQUEST_ACTIVITY_SLOW_MS = 6000

const foregroundRequests = new Map<symbol, number>()
const listeners = new Set<() => void>()

let snapshot: RequestActivitySnapshot = { pending: 0, active: false, slow: false }
let slowTimer: ReturnType<typeof setTimeout> | null = null

function clearSlowTimer(): void {
  if (slowTimer === null) return
  clearTimeout(slowTimer)
  slowTimer = null
}

function oldestStartedAt(): number | null {
  let oldest: number | null = null
  for (const startedAt of foregroundRequests.values()) {
    if (oldest === null || startedAt < oldest) oldest = startedAt
  }
  return oldest
}

function publish(): void {
  clearSlowTimer()

  const pending = foregroundRequests.size
  const oldest = oldestStartedAt()
  const slow = oldest !== null && Date.now() - oldest >= REQUEST_ACTIVITY_SLOW_MS
  const next: RequestActivitySnapshot = { pending, active: pending > 0, slow }
  const changed =
    next.pending !== snapshot.pending || next.active !== snapshot.active || next.slow !== snapshot.slow

  if (changed) {
    snapshot = next
    for (const listener of listeners) listener()
  }

  if (oldest !== null && !slow) {
    const remaining = Math.max(0, REQUEST_ACTIVITY_SLOW_MS - (Date.now() - oldest))
    slowTimer = setTimeout(publish, remaining)
  }
}

/** Register a finite foreground request and return an idempotent release handle. */
export function beginRequestActivity(mode: RequestActivityMode = 'foreground'): () => void {
  if (mode === 'background') return () => undefined

  const id = Symbol('request-activity')
  foregroundRequests.set(id, Date.now())
  publish()

  let released = false
  return () => {
    if (released) return
    released = true
    foregroundRequests.delete(id)
    publish()
  }
}

/** Keep request activity active across the full async operation, including body reads. */
export async function withRequestActivity<T>(
  run: () => Promise<T>,
  mode: RequestActivityMode = 'foreground',
): Promise<T> {
  const release = beginRequestActivity(mode)
  try {
    return await run()
  } finally {
    release()
  }
}

export function subscribeRequestActivity(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

export function getRequestActivitySnapshot(): RequestActivitySnapshot {
  return snapshot
}

export function useRequestActivity(): RequestActivitySnapshot {
  return useSyncExternalStore(
    subscribeRequestActivity,
    getRequestActivitySnapshot,
    getRequestActivitySnapshot,
  )
}

/** Test-only reset; production code should always release the returned handle. */
export function __resetRequestActivityForTests(): void {
  foregroundRequests.clear()
  publish()
}
