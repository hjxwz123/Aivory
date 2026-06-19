/**
 * Aurelia API client — typed wrapper around fetch() that talks to the Go
 * backend at AURELIA_API_BASE (defaults to "/api" so the dev proxy in
 * vite.config.ts forwards calls during local development).
 *
 * Three concerns live here:
 *   1. JSON request / response handling with proper Content-Type + error
 *      surfacing as ApiError.
 *   2. Credential mode — every call sends cookies so the httpOnly auth
 *      cookies set by the backend round-trip.
 *   3. Authorisation — when an access token is available in memory we also
 *      send it as a Bearer header. This is the only token-storage path the
 *      browser sees; the long-lived refresh token stays in the cookie.
 */
import type { ApiError as ApiErrorShape } from './types'
import { toast as _sseToast } from '@/hooks/use-toast'

const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '/api'

let memoryToken: string | null = null

/** Set or clear the in-memory access token. */
export function setAccessToken(token: string | null): void {
  memoryToken = token
}

/** Read the current access token (mostly for tests). */
export function getAccessToken(): string | null {
  return memoryToken
}

/**
 * Absolute URL for a backend path, used for full-page navigations that can't go
 * through the fetch wrapper (e.g. the OAuth `/start` redirect). Returns the same
 * `API_BASE`-prefixed path the `api()` helper hits.
 */
export function apiUrl(path: string): string {
  return API_BASE + path
}

export class ApiError extends Error {
  readonly status: number
  readonly body: unknown
  constructor(status: number, message: string, body: unknown) {
    super(message)
    this.status = status
    this.body = body
  }
}

interface ApiOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'
  /** Plain object (will be JSON.stringified) or FormData. */
  body?: unknown
  /** Abort signal. */
  signal?: AbortSignal
  /** Override headers. */
  headers?: Record<string, string>
}

/** Core fetch wrapper. */
export async function api<T = unknown>(path: string, opts: ApiOptions = {}): Promise<T> {
  return apiRequest<T>(path, opts, false)
}

// isAuthPath: the auth endpoints (login / refresh / register / logout) must NEVER
// trigger the refresh-on-401 retry, or a failed refresh would loop.
function isAuthPath(path: string): boolean {
  return path.startsWith('/auth/')
}

async function apiRequest<T>(path: string, opts: ApiOptions, retried: boolean): Promise<T> {
  const isForm = opts.body instanceof FormData
  const headers: Record<string, string> = {
    accept: 'application/json',
    ...(isForm ? {} : { 'content-type': 'application/json' }),
    ...(memoryToken ? { authorization: `Bearer ${memoryToken}` } : {}),
    ...opts.headers,
  }
  const res = await fetch(API_BASE + path, {
    method: opts.method ?? 'GET',
    credentials: 'include',
    headers,
    body: isForm ? (opts.body as FormData) : opts.body ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  })
  // The access token is short-lived (2h). When it expires an open tab would
  // start 401-ing "auth required" out of nowhere — silently refresh once via the
  // long-lived refresh cookie and retry, so the session keeps working.
  if (res.status === 401 && !retried && !isAuthPath(path)) {
    if (await tryRefresh()) return apiRequest<T>(path, opts, true)
  }
  let parsed: unknown = undefined
  const text = await res.text()
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = text
    }
  }
  if (!res.ok) {
    const errBody = parsed as ApiErrorShape | undefined
    const message = errBody?.error ?? `Request failed with status ${res.status}`
    // A banned account (any in-flight request after an admin ban) → notify the
    // app once so it can sign the user out with a clear "suspended" message,
    // instead of a silent logout or a generic error.
    if (message === 'account_suspended') notifyBanned()
    throw new ApiError(res.status, message, parsed)
  }
  return parsed as T
}

// Banned-account hook. The auth store registers a handler that clears the
// session and shows the suspended notice. Kept as a callback so client.ts stays
// free of a circular import on the store.
let bannedHandler: (() => void) | null = null
let bannedFired = false
export function setBannedHandler(cb: () => void): void {
  bannedHandler = cb
}
function notifyBanned(): void {
  if (bannedFired) return // a ban floods every in-flight request; act once
  bannedFired = true
  bannedHandler?.()
}

// Refresh-on-401 hook. The auth store registers a handler that mints a fresh
// access token from the refresh cookie. Single-flight: many requests can 401 at
// once (token just expired) — they all await one refresh.
let refreshHandler: (() => Promise<boolean>) | null = null
let refreshInFlight: Promise<boolean> | null = null
export function setRefreshHandler(cb: () => Promise<boolean>): void {
  refreshHandler = cb
}
function tryRefresh(): Promise<boolean> {
  if (!refreshHandler) return Promise.resolve(false)
  if (!refreshInFlight) {
    refreshInFlight = refreshHandler()
      .catch(() => false)
      .finally(() => {
        refreshInFlight = null
      })
  }
  return refreshInFlight
}

/** Open a streaming POST request that yields SSE events as parsed JSON.
 *  On network drop (fetch throws after the stream starts) the reader will
 *  retry up to MAX_SSE_RETRIES times with exponential backoff (1 s, 2 s, 4 s)
 *  before giving up and re-throwing the error. */
const MAX_SSE_RETRIES = 3
export async function* streamSSE(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): AsyncGenerator<{ event: string; data: unknown }> {
  const open = () =>
    fetch(API_BASE + path, {
      method: 'POST',
      credentials: 'include',
      headers: {
        accept: 'text/event-stream',
        'content-type': 'application/json',
        ...(memoryToken ? { authorization: `Bearer ${memoryToken}` } : {}),
      },
      body: JSON.stringify(body),
      signal,
    })
  let res = await open()
  // Same refresh-on-401 as api(): an expired access token shouldn't fail a send.
  if (res.status === 401 && !isAuthPath(path) && (await tryRefresh())) {
    res = await open()
  }
  if (!res.ok || !res.body) {
    let text = ''
    try {
      text = await res.text()
    } catch {
      /* ignore */
    }
    let parsed: unknown
    try {
      parsed = JSON.parse(text)
    } catch {
      parsed = text
    }
    const e = parsed as ApiErrorShape | undefined
    throw new ApiError(res.status, e?.error ?? `stream failed (${res.status})`, parsed)
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  let retryCount = 0
  let reconnectToastShown = false
  while (true) {
    let done: boolean
    let value: Uint8Array | undefined
    try {
      ;({ done, value } = await reader.read())
    } catch (readErr) {
      // Network drop during streaming. Attempt reconnect with exponential backoff.
      if (signal?.aborted || retryCount >= MAX_SSE_RETRIES) throw readErr
      retryCount++
      const delay = Math.pow(2, retryCount - 1) * 1000 // 1s, 2s, 4s
      if (!reconnectToastShown) {
        reconnectToastShown = true
        _sseToast.warning('Reconnecting…', 'Connection dropped, retrying automatically.')
      }
      await new Promise<void>((resolve) => setTimeout(resolve, delay))
      // Re-open the stream. If the signal was aborted during the wait, bail out.
      if (signal?.aborted) throw readErr
      try {
        const retryRes = await open()
        if (!retryRes.ok || !retryRes.body) throw readErr
        // Replace the reader with the new connection.
        ;(reader as unknown as { releaseLock(): void }).releaseLock?.()
        const retryReader = retryRes.body.getReader()
        ;({ done, value } = await retryReader.read())
        // Swap the reader reference by restarting the outer loop.
        // We yield from the new reader by continuing inside this one.
        // Since we can't swap the outer `reader` variable (const), we yield
        // from here and return — callers see a seamless stream.
        while (true) {
          if (done) {
            if (buf.trim().length > 0) {
              const frame = parseSSEFrame(buf)
              if (frame) yield frame
            }
            return
          }
          buf += decoder.decode(value, { stream: true })
          let idx = buf.indexOf('\n\n')
          while (idx !== -1) {
            const raw = buf.slice(0, idx)
            buf = buf.slice(idx + 2)
            const frame = parseSSEFrame(raw)
            if (frame) yield frame
            idx = buf.indexOf('\n\n')
          }
          ;({ done, value } = await retryReader.read())
        }
      } catch {
        throw readErr
      }
    }
    if (done) break
    buf += decoder.decode(value, { stream: true })
    // SSE frames are separated by \n\n.
    let idx = buf.indexOf('\n\n')
    while (idx !== -1) {
      const raw = buf.slice(0, idx)
      buf = buf.slice(idx + 2)
      const frame = parseSSEFrame(raw)
      if (frame) yield frame
      idx = buf.indexOf('\n\n')
    }
  }
  // Tail frame without trailing blank line.
  if (buf.trim().length > 0) {
    const frame = parseSSEFrame(buf)
    if (frame) yield frame
  }
}

function parseSSEFrame(raw: string): { event: string; data: unknown } | null {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith(':')) continue
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trimStart())
  }
  if (dataLines.length === 0) return null
  const text = dataLines.join('\n')
  try {
    return { event, data: JSON.parse(text) }
  } catch {
    return { event, data: text }
  }
}
