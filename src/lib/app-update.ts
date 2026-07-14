/**
 * app-update — invisible in-place upgrade after a deploy (§23).
 *
 * Every build bakes a version id into the bundle (__APP_VERSION__) and emits
 * the same id as /version.json. An open tab compares the two and, when they
 * diverge, reloads itself at a SAFE moment so the user never notices:
 *
 *   - immediately, if the tab is hidden (user isn't looking);
 *   - on the next route navigation (a full reload at a navigation boundary is
 *     indistinguishable from the navigation itself) — except overlay
 *     navigations (the settings modal), which users perceive as staying put;
 *   - NEVER while any message is streaming, an upload/recording is in flight,
 *     or unsent composer text exists (sync-guards' reload blockers) — a reload
 *     would silently destroy work.
 *
 * Check triggers (no blind polling): the §23 notify stream's (re)connect
 * `hello` (a backend deploy always breaks those connections, so reconnect ≈
 * deploy signal), tab-visible transitions (throttled), and a slow safety
 * interval. All checks are a single no-store GET of a ~40-byte static file.
 * Detection is shared across tabs via localStorage so every tab upgrades at
 * its own safe moment.
 */

import { useConversations } from '@/store/conversations'
import { isReloadBlocked } from '@/lib/sync-guards'

const LS_KEY = 'aivory.update-ready'
// Reload-loop breaker: if we already reloaded FOR this exact version and the
// drift persists (a stale-cached index.html, a mid-rollout instance), the
// reload didn't take — hammering location.reload() would soft-brick the tab.
const SS_RELOAD_KEY = 'aivory.version-reload'
const CHECK_THROTTLE_MS = 30_000
const RELOAD_RETRY_MS = 5 * 60_000
const SAFETY_INTERVAL_MS = 15 * 60_000

const bootVersion = typeof __APP_VERSION__ !== 'undefined' ? __APP_VERSION__ : 'dev'

let pending = false
let pendingVersion = ''
let checking = false
let lastCheckAt = 0
let initialized = false

/** True while any conversation has a live streaming message in this tab. */
function anyStreaming(): boolean {
  try {
    return useConversations.getState().conversations.some((c) => c.messages.some((m) => m.streaming))
  } catch {
    return true // fail safe: better to postpone the upgrade than kill a stream
  }
}

async function fetchRemoteVersion(): Promise<string | null> {
  try {
    const res = await fetch('/version.json', { cache: 'no-store' })
    if (!res.ok) return null
    const parsed = (await res.json()) as { version?: unknown }
    return typeof parsed.version === 'string' && parsed.version !== '' ? parsed.version : null
  } catch {
    return null
  }
}

/** The previous reload attempt for a version that evidently didn't take. */
function reloadedRecentlyFor(version: string): boolean {
  try {
    const raw = sessionStorage.getItem(SS_RELOAD_KEY)
    if (!raw) return false
    const parsed = JSON.parse(raw) as { v?: unknown; at?: unknown }
    return parsed.v === version && typeof parsed.at === 'number' && Date.now() - parsed.at < RELOAD_RETRY_MS
  } catch {
    return false
  }
}

/** Compare the deployed version against this bundle; arm an upgrade on drift. */
export async function checkForUpdate(): Promise<void> {
  // Dev builds have no version.json and reload via HMR anyway.
  if (!import.meta.env.PROD || pending || checking) return
  const now = Date.now()
  if (now - lastCheckAt < CHECK_THROTTLE_MS) return
  lastCheckAt = now
  checking = true
  try {
    const remote = await fetchRemoteVersion()
    if (remote && remote !== bootVersion && !reloadedRecentlyFor(remote)) {
      armUpdate(remote, true)
    }
  } finally {
    checking = false
  }
}

/** An update is known to exist; reload at the next safe moment. */
function armUpdate(version: string, broadcast: boolean): void {
  if (pending) return
  pending = true
  pendingVersion = version
  if (broadcast) {
    try {
      // Tell sibling tabs WHICH version exists; each compares against its own
      // bundle and applies at its own safe moment.
      localStorage.setItem(LS_KEY, version)
    } catch {
      /* storage unavailable — this tab still upgrades */
    }
  }
  maybeApplyUpdate('armed')
}

function applyReload(): void {
  try {
    // Remember what we reloaded for: if the same drift is still there after
    // boot, the new bundle isn't actually reachable yet — back off instead of
    // reload-looping (see reloadedRecentlyFor).
    sessionStorage.setItem(SS_RELOAD_KEY, JSON.stringify({ v: pendingVersion, at: Date.now() }))
  } catch {
    /* ignore */
  }
  window.location.reload()
}

/**
 * Reload now if it's safe. Called on: arm, tab-hidden transitions, and route
 * navigations (from App.tsx). Never interrupts a live generation stream, an
 * in-flight upload/recording, or unsent composer text (reload blockers).
 */
export function maybeApplyUpdate(trigger: 'armed' | 'hidden' | 'navigation' | 'storage'): void {
  if (!pending) return
  if (anyStreaming() || isReloadBlocked()) return
  if (document.visibilityState === 'hidden') {
    applyReload()
    return
  }
  if (trigger === 'navigation') {
    applyReload()
  }
}

/** Wire the listeners once (called from App on mount). */
export function initAppUpdate(): void {
  if (initialized) return
  initialized = true

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') {
      maybeApplyUpdate('hidden')
    } else {
      void checkForUpdate()
    }
  })

  // A sibling tab detected an update — arm locally without re-broadcasting,
  // but only if the broadcast version actually differs from THIS tab's bundle
  // (a tab opened after the deploy is already current and must not reload).
  window.addEventListener('storage', (e) => {
    if (e.key === LS_KEY && e.newValue && e.newValue !== bootVersion && !reloadedRecentlyFor(e.newValue)) {
      armUpdate(e.newValue, false)
    }
  })

  // Slow safety net for tabs that stay visible and never reconnect (e.g. a
  // frontend-only redeploy that doesn't restart the backend).
  if (import.meta.env.PROD) {
    window.setInterval(() => {
      if (document.visibilityState === 'visible') void checkForUpdate()
    }, SAFETY_INTERVAL_MS)
  }
}
