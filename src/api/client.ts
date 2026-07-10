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

// Per-request HMAC signing. The frontend derives an intermediate key from the
// JWT + a slow-rotating epoch (changes every hour), then signs ts:nonce:path
// with it. Every request produces a unique token; the path is bound into the
// signature so a token captured from one endpoint is invalid on another.
async function _dk(jwt: string): Promise<CryptoKey> {
  const raw = new TextEncoder().encode(jwt)
  const base = await crypto.subtle.importKey('raw', raw, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  const epoch = Math.floor(Date.now() / 1000 / 3600)
  const derived = await crypto.subtle.sign('HMAC', base, new TextEncoder().encode(String(epoch)))
  return crypto.subtle.importKey('raw', derived, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
}

async function _sign(jwt: string, ts: number, nonce: string, path: string): Promise<string> {
  const key = await _dk(jwt)
  // The server verifies over r.URL.Path — the path WITHOUT the query string
  // (middleware.go). Strip any `?query` here or a request like `?mode=tree` signs
  // a different message than the server checks and gets a 403 "invalid request
  // signature" (this silently broke the conversation-tree / paginated fetches).
  const signPath = path.split('?')[0]
  const msg = new TextEncoder().encode(`${ts}\x00${nonce}\x00${signPath}`)
  const sig = await crypto.subtle.sign('HMAC', key, msg)
  return btoa(String.fromCharCode(...new Uint8Array(sig)))
}

function _nonce(): string {
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  return btoa(String.fromCharCode(...b)).replace(/[+/=]/g, (c) => ({ '+': '-', '/': '_', '=': '' })[c]!)
}

let memoryToken: string | null = null

/** Set or clear the in-memory access token. */
export function setAccessToken(token: string | null): void {
  memoryToken = token
}

/** Read the current access token (mostly for tests). */
export function getAccessToken(): string | null {
  return memoryToken
}

/** Reset one-shot auth failure guards after a deliberate new session starts. */
export function resetAuthFailureState(): void {
  authLostFired = false
  suppressRefreshAfterAuthLost = false
  bannedFired = false
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

export interface UploadProgress {
  loaded: number
  total?: number
  percent?: number
}

interface UploadOptions {
  method?: 'POST' | 'PATCH' | 'PUT'
  signal?: AbortSignal
  headers?: Record<string, string>
  onProgress?: (progress: UploadProgress) => void
}

/** Core fetch wrapper. */
export async function api<T = unknown>(path: string, opts: ApiOptions = {}): Promise<T> {
  return apiRequest<T>(path, opts, false)
}

/** Multipart upload wrapper with browser upload progress. `fetch()` still does
 * not expose upload progress events, so file uploads use XHR while keeping the
 * same credentials, bearer token and request-signature behavior as api(). */
export async function apiUpload<T = unknown>(path: string, body: FormData, opts: UploadOptions = {}): Promise<T> {
  return apiUploadRequest<T>(path, body, opts, false)
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
  if (memoryToken) {
    const ts = Math.floor(Date.now() / 1000)
    const nonce = _nonce()
    headers['x-req-ts'] = String(ts)
    headers['x-req-nonce'] = nonce
    headers['x-req-token'] = await _sign(memoryToken, ts, nonce, path)
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
    if (res.status === 401 && retried && !isAuthPath(path) && isAuthExpiredMessage(message)) {
      notifyAuthLost()
    }
    // A banned account (any in-flight request after an admin ban) → notify the
    // app once so it can sign the user out with a clear "suspended" message,
    // instead of a silent logout or a generic error.
    if (message === 'account_suspended') notifyBanned()
    throw new ApiError(res.status, message, parsed)
  }
  return parsed as T
}

async function apiUploadRequest<T>(
  path: string,
  body: FormData,
  opts: UploadOptions,
  retried: boolean,
): Promise<T> {
  const res = await xhrUpload(path, body, opts)
  if (res.status === 401 && !retried && !isAuthPath(path)) {
    if (await tryRefresh()) return apiUploadRequest<T>(path, body, opts, true)
  }
  if (!res.ok) {
    const errBody = res.parsed as ApiErrorShape | undefined
    const message = errBody?.error ?? `Request failed with status ${res.status}`
    if (res.status === 401 && retried && !isAuthPath(path) && isAuthExpiredMessage(message)) {
      notifyAuthLost()
    }
    if (message === 'account_suspended') notifyBanned()
    throw new ApiError(res.status, message, res.parsed)
  }
  return res.parsed as T
}

async function xhrUpload(
  path: string,
  body: FormData,
  opts: UploadOptions,
): Promise<{ status: number; ok: boolean; parsed: unknown }> {
  const headers: Record<string, string> = {
    accept: 'application/json',
    ...(memoryToken ? { authorization: `Bearer ${memoryToken}` } : {}),
    ...opts.headers,
  }
  if (memoryToken) {
    const ts = Math.floor(Date.now() / 1000)
    const nonce = _nonce()
    headers['x-req-ts'] = String(ts)
    headers['x-req-nonce'] = nonce
    headers['x-req-token'] = await _sign(memoryToken, ts, nonce, path)
  }

  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    let settled = false
    const cleanup = () => {
      if (opts.signal) opts.signal.removeEventListener('abort', abort)
    }
    const finishReject = (error: unknown) => {
      if (settled) return
      settled = true
      cleanup()
      reject(error)
    }
    const abort = () => {
      xhr.abort()
      finishReject(new DOMException('Upload aborted', 'AbortError'))
    }
    if (opts.signal?.aborted) {
      abort()
      return
    }
    if (opts.signal) opts.signal.addEventListener('abort', abort, { once: true })

    xhr.open(opts.method ?? 'POST', API_BASE + path)
    xhr.withCredentials = true
    for (const [key, value] of Object.entries(headers)) {
      xhr.setRequestHeader(key, value)
    }
    xhr.upload.onprogress = (event) => {
      if (!opts.onProgress) return
      if (event.lengthComputable && event.total > 0) {
        opts.onProgress({
          loaded: event.loaded,
          total: event.total,
          percent: Math.max(0, Math.min(100, Math.round((event.loaded / event.total) * 100))),
        })
      } else {
        opts.onProgress({ loaded: event.loaded })
      }
    }
    xhr.onerror = () => finishReject(new TypeError('Network request failed'))
    xhr.ontimeout = () => finishReject(new TypeError('Upload timed out'))
    xhr.onload = () => {
      if (settled) return
      settled = true
      cleanup()
      let parsed: unknown = undefined
      const text = xhr.responseText
      if (text.length > 0) {
        try {
          parsed = JSON.parse(text)
        } catch {
          parsed = text
        }
      }
      resolve({ status: xhr.status, ok: xhr.status >= 200 && xhr.status < 300, parsed })
    }
    xhr.send(body)
  })
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

let authLostHandler: (() => void) | null = null
let authLostFired = false
export function setAuthLostHandler(cb: () => void): void {
  authLostHandler = cb
}
function notifyAuthLost(): void {
  if (authLostFired) return
  authLostFired = true
  suppressRefreshAfterAuthLost = true
  authLostHandler?.()
}
function isAuthExpiredMessage(message: string): boolean {
  return message === 'auth required' || message === 'session expired, please log in again'
}

// Refresh-on-401 hook. The auth store registers a handler that mints a fresh
// access token from the refresh cookie. Single-flight: many requests can 401 at
// once (token just expired) — they all await one refresh.
let refreshHandler: (() => Promise<boolean>) | null = null
let refreshInFlight: Promise<boolean> | null = null
let suppressRefreshAfterAuthLost = false
export function isAuthRefreshSuppressed(): boolean {
  return suppressRefreshAfterAuthLost
}
export function setRefreshHandler(cb: () => Promise<boolean>): void {
  refreshHandler = cb
}
function tryRefresh(): Promise<boolean> {
  if (suppressRefreshAfterAuthLost) return Promise.resolve(false)
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
 *  Creation streams are not retried: re-sending the POST would start a second
 *  generation. Message-specific GET replay streams handle reconnect/resume. */
const MAX_SSE_RETRIES = 3
export async function* streamSSE(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): AsyncGenerator<{ event: string; data: unknown; id?: string }> {
  const sseTs = Math.floor(Date.now() / 1000)
  const sseNonce = _nonce()
  const sseSig = memoryToken ? await _sign(memoryToken, sseTs, sseNonce, path) : ''
  const open = () =>
    fetch(API_BASE + path, {
      method: 'POST',
      credentials: 'include',
      headers: {
        accept: 'text/event-stream',
        'content-type': 'application/json',
        ...(memoryToken ? { authorization: `Bearer ${memoryToken}` } : {}),
        ...(memoryToken ? { 'x-req-ts': String(sseTs), 'x-req-nonce': sseNonce, 'x-req-token': sseSig } : {}),
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
  while (true) {
    const { done, value } = await reader.read()
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

export async function* streamSSEGet(
  path: string,
  signal?: AbortSignal,
  lastEventId?: string,
): AsyncGenerator<{ event: string; data: unknown; id?: string }> {
  let currentLastId = lastEventId ?? ''
  let retryCount = 0
  let reconnectToastShown = false
  const open = () =>
    fetch(API_BASE + path, {
      method: 'GET',
      credentials: 'include',
      headers: {
        accept: 'text/event-stream',
        ...(memoryToken ? { authorization: `Bearer ${memoryToken}` } : {}),
        ...(currentLastId ? { 'Last-Event-ID': currentLastId } : {}),
      },
      signal,
    })

  while (true) {
    let res = await open()
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
    try {
      for await (const frame of readSSEBody(res.body)) {
        if (frame.id) currentLastId = frame.id
        retryCount = 0
        yield frame
        const typ = typeof frame.data === 'object' && frame.data ? (frame.data as { type?: string }).type : undefined
        if (typ === 'done' || typ === 'error') return
      }
      return
    } catch (readErr) {
      if (signal?.aborted || retryCount >= MAX_SSE_RETRIES) throw readErr
      retryCount++
      const delay = Math.pow(2, retryCount - 1) * 1000
      if (!reconnectToastShown) {
        reconnectToastShown = true
        _sseToast.warning('Reconnecting…', 'Connection dropped, retrying automatically.')
      }
      await new Promise<void>((resolve) => setTimeout(resolve, delay))
      if (signal?.aborted) throw readErr
    }
  }
}

async function* readSSEBody(body: ReadableStream<Uint8Array>): AsyncGenerator<{ event: string; data: unknown; id?: string }> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let idx = buf.indexOf('\n\n')
    while (idx !== -1) {
      const raw = buf.slice(0, idx)
      buf = buf.slice(idx + 2)
      const frame = parseSSEFrame(raw)
      if (frame) yield frame
      idx = buf.indexOf('\n\n')
    }
  }
  if (buf.trim().length > 0) {
    const frame = parseSSEFrame(buf)
    if (frame) yield frame
  }
}

function parseSSEFrame(raw: string): { event: string; data: unknown; id?: string } | null {
  let event = 'message'
  let id: string | undefined
  const dataLines: string[] = []
  for (const line of raw.split('\n')) {
    if (line.startsWith(':')) continue
    if (line.startsWith('id:')) id = line.slice(3).trim()
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) dataLines.push(line.slice(5).trimStart())
  }
  if (dataLines.length === 0) return null
  const text = dataLines.join('\n')
  try {
    return { event, data: JSON.parse(text), id }
  } catch {
    return { event, data: text, id }
  }
}
