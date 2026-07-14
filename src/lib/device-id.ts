/**
 * device-id — a per-PAGE-LOAD identifier (§23 realtime sync echo suppression).
 *
 * Sent as `X-Device-Id` on every API call; the server stamps it onto the
 * realtime events it broadcasts (`origin`), so the tab that caused a change can
 * ignore its own echo instead of re-fetching state it already updated
 * optimistically.
 *
 * Deliberately in-memory only — NOT sessionStorage: browsers copy
 * sessionStorage into duplicated/restored tabs, and two tabs sharing one id
 * would each treat the other's changes as their own echo and silently stop
 * live-syncing. Echo suppression only matters for requests issued during the
 * current page load anyway (a reloaded tab refetches everything), so a fresh
 * id per load loses nothing.
 *
 * No crypto APIs (the production origin is plain HTTP — no crypto.randomUUID);
 * uniqueness only needs to hold across one user's own open tabs.
 */

let cached = ''

export function getDeviceId(): string {
  if (!cached) cached = 'd-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10)
  return cached
}
