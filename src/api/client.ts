/**
 * Aivory API client — typed wrapper around fetch() that talks to the Go
 * backend at AIVORY_API_BASE (defaults to "/api" so the dev proxy in
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
import { getClientInstanceId, getDeviceId } from '@/lib/device-id'
import { blockReload } from '@/lib/sync-guards'
import { hmacSha256, sha256 } from '@/lib/hmac-sha256'
import {
  withRequestActivity,
  type RequestActivityMode,
} from '@/lib/request-activity'


const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '/api'

// Per-request HMAC proof. The server consumes every nonce once and binds the
// proof to the HTTP method, complete request target, device id, and payload.
//
// `crypto.subtle` only exists in secure contexts (https / localhost). A
// deployment reached over plain http on a LAN IP or domain has no SubtleCrypto
// and would throw "Cannot read properties of undefined (reading 'importKey')"
// on every authed request — fall back to the pure-JS HMAC (byte-identical
// output) so those deployments keep working.
const REQUEST_SIGNATURE_VERSION = 'v2'
const UNSIGNED_PAYLOAD = 'UNSIGNED-PAYLOAD'

async function _dk(jwt: string, ts: number): Promise<CryptoKey> {
  const raw = new TextEncoder().encode(jwt)
  const base = await crypto.subtle.importKey('raw', raw, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  const epoch = Math.floor(ts / 3600)
  const derived = await crypto.subtle.sign('HMAC', base, new TextEncoder().encode(String(epoch)))
  return crypto.subtle.importKey('raw', derived, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
}

async function _sign(jwt: string, ts: number, message: string): Promise<string> {
  if (typeof crypto.subtle === 'undefined') {
    const encoder = new TextEncoder()
    const epoch = Math.floor(ts / 3600)
    const derived = hmacSha256(encoder.encode(jwt), encoder.encode(String(epoch)))
    const sig = hmacSha256(derived, encoder.encode(message))
    return btoa(String.fromCharCode(...sig))
  }
  const key = await _dk(jwt, ts)
  const msg = new TextEncoder().encode(message)
  const sig = await crypto.subtle.sign('HMAC', key, msg)
  return btoa(String.fromCharCode(...new Uint8Array(sig)))
}

function _nonce(): string {
  const b = new Uint8Array(16)
  crypto.getRandomValues(b)
  return btoa(String.fromCharCode(...b)).replace(/[+/=]/g, (c) => ({ '+': '-', '/': '_', '=': '' })[c]!)
}

function _sha256Hex(value: string): string {
  const digest = sha256(new TextEncoder().encode(value))
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

function _canonicalTarget(path: string): string {
  const parsed = new URL(path, 'http://aivory.invalid')
  return parsed.pathname + parsed.search
}

let memoryToken: string | null = null
let memoryRequestSigningKey: string | null = null

/** Set or clear the in-memory access token. */
export function setAccessToken(token: string | null, requestSigningKey?: string): void {
  memoryToken = token
  memoryRequestSigningKey = token && requestSigningKey ? requestSigningKey : null
}

/** Read the current access token (mostly for tests). */
export function getAccessToken(): string | null {
  return memoryToken
}

/** Build the authorization and one-time proof headers for every transport. */
export async function authenticatedRequestHeaders(
  path: string,
  method: string = 'GET',
  serializedBody: string = '',
  unsignedPayload = false,
): Promise<Record<string, string>> {
  const deviceId = getDeviceId()
  const headers: Record<string, string> = {
    'x-device-id': deviceId,
    'x-client-id': getClientInstanceId(),
  }
  const jwt = memoryToken
  if (!jwt) return headers
  headers.authorization = `Bearer ${jwt}`
  const requestSigningKey = memoryRequestSigningKey
  if (!requestSigningKey) return headers

  const ts = Math.floor(Date.now() / 1000)
  const nonce = _nonce()
  const payloadDigest = unsignedPayload ? UNSIGNED_PAYLOAD : _sha256Hex(serializedBody)
  const message = [
    REQUEST_SIGNATURE_VERSION,
    String(ts),
    nonce,
    method.toUpperCase(),
    _canonicalTarget(path),
    deviceId,
    payloadDigest,
    _sha256Hex(jwt),
  ].join('\x00')
  headers['x-req-ts'] = String(ts)
  headers['x-req-nonce'] = nonce
  headers['x-req-content-sha256'] = payloadDigest
  headers['x-req-token'] = await _sign(requestSigningKey, ts, message)
  return headers
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

const NETWORK_OFFLINE_MESSAGE = 'No network connection. Check your connection and try again.'

/** Whether the browser explicitly reports that its network connection is down. */
export function isNetworkOnline(): boolean {
  return typeof navigator === 'undefined' || navigator.onLine !== false
}

/**
 * Avoid starting browser requests that are guaranteed to fail while offline.
 * Callers still receive an ApiError so their existing loading and feedback
 * paths settle normally, while DevTools is spared a needless network request.
 */
export function assertNetworkOnline(): void {
  if (!isNetworkOnline()) throw new ApiError(0, NETWORK_OFFLINE_MESSAGE, null)
}

interface ApiOptions {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'
  /** Plain object (will be JSON.stringified) or FormData. */
  body?: unknown
  /** Abort signal. */
  signal?: AbortSignal
  /** Override headers. */
  headers?: Record<string, string>
  /** Background polling stays out of the administrator's foreground status. */
  activity?: RequestActivityMode
  /** Allow a small request to finish while the page is navigating/reloading. */
  keepalive?: boolean
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
  /** Background polling stays out of the administrator's foreground status. */
  activity?: RequestActivityMode
}

/** Core fetch wrapper. */
export async function api<T = unknown>(path: string, opts: ApiOptions = {}): Promise<T> {
  return withRequestActivity(() => apiRequest<T>(path, opts, false), opts.activity)
}

/** Multipart upload wrapper with browser upload progress. `fetch()` still does
 * not expose upload progress events, so file uploads use XHR while keeping the
 * same credentials, bearer token and request-signature behavior as api(). */
export async function apiUpload<T = unknown>(path: string, body: FormData, opts: UploadOptions = {}): Promise<T> {
  return withRequestActivity(() => apiUploadRequest<T>(path, body, opts, false), opts.activity)
}

// isAuthPath: the auth endpoints (login / refresh / register / logout) must NEVER
// trigger the refresh-on-401 retry, or a failed refresh would loop.
function isAuthPath(path: string): boolean {
  return path.startsWith('/auth/')
}

async function apiRequest<T>(path: string, opts: ApiOptions, retried: boolean): Promise<T> {
  assertNetworkOnline()
  const isForm = opts.body instanceof FormData
  const method = opts.method ?? 'GET'
  const serializedBody = isForm || !opts.body ? '' : JSON.stringify(opts.body)
  const authHeaders = await authenticatedRequestHeaders(path, method, serializedBody, isForm)
  const headers: Record<string, string> = {
    accept: 'application/json',
    ...(isForm ? {} : { 'content-type': 'application/json' }),
    ...opts.headers,
    ...authHeaders,
  }
  const res = await fetch(API_BASE + path, {
    method,
    credentials: 'include',
    headers,
    body: isForm ? (opts.body as FormData) : serializedBody || undefined,
    signal: opts.signal,
    keepalive: opts.keepalive,
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
    if (message === 'initial_password_required') notifyInitialPasswordRequired()
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
  assertNetworkOnline()
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
    if (message === 'initial_password_required') notifyInitialPasswordRequired()
    throw new ApiError(res.status, message, res.parsed)
  }
  return res.parsed as T
}

async function xhrUpload(
  path: string,
  body: FormData,
  opts: UploadOptions,
): Promise<{ status: number; ok: boolean; parsed: unknown }> {
  const method = opts.method ?? 'POST'
  const authHeaders = await authenticatedRequestHeaders(path, method, '', true)
  const headers: Record<string, string> = {
    accept: 'application/json',
    ...opts.headers,
    ...authHeaders,
  }

  // §23: an auto-reload (invisible upgrade) must never kill an upload mid-
  // flight — register as a reload blocker for the XHR's lifetime.
  const releaseReloadBlock = blockReload()
  return new Promise<{ status: number; ok: boolean; parsed: unknown }>((resolve, reject) => {
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

    xhr.open(method, API_BASE + path)
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
  }).finally(releaseReloadBlock)
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

let initialPasswordRequiredHandler: (() => void) | null = null
export function setInitialPasswordRequiredHandler(cb: () => void): void {
  initialPasswordRequiredHandler = cb
}
function notifyInitialPasswordRequired(): void {
  initialPasswordRequiredHandler?.()
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
// Exponential SSE reconnect backoff: delay = SSE_RECONNECT_BACKOFF_BASE_MS * factor^(retryCount - 1).
const SSE_RECONNECT_BACKOFF_BASE_MS = 1000
const SSE_RECONNECT_BACKOFF_FACTOR = 2
export async function* streamSSE(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): AsyncGenerator<{ event: string; data: unknown; id?: string }> {
  assertNetworkOnline()
  const serializedBody = JSON.stringify(body)
  const open = async () => {
    assertNetworkOnline()
    const authHeaders = await authenticatedRequestHeaders(path, 'POST', serializedBody)
    return fetch(API_BASE + path, {
      method: 'POST',
      credentials: 'include',
      headers: {
        accept: 'text/event-stream',
        'content-type': 'application/json',
        ...authHeaders,
      },
      body: serializedBody,
      signal,
    })
  }
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
    const message = e?.error ?? `stream failed (${res.status})`
    if (message === 'initial_password_required') notifyInitialPasswordRequired()
    throw new ApiError(res.status, message, parsed)
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
  sseOpts?: { silentReconnect?: boolean },
): AsyncGenerator<{ event: string; data: unknown; id?: string }> {
  let currentLastId = lastEventId ?? ''
  let retryCount = 0
  let reconnectToastShown = false
  const open = async () => {
    assertNetworkOnline()
    const authHeaders = await authenticatedRequestHeaders(path, 'GET')
    return fetch(API_BASE + path, {
      method: 'GET',
      credentials: 'include',
      headers: {
        accept: 'text/event-stream',
        ...(currentLastId ? { 'Last-Event-ID': currentLastId } : {}),
        ...authHeaders,
      },
      signal,
    })
  }

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
      const message = e?.error ?? `stream failed (${res.status})`
      if (message === 'initial_password_required') notifyInitialPasswordRequired()
      throw new ApiError(res.status, message, parsed)
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
      const delay = Math.pow(SSE_RECONNECT_BACKOFF_FACTOR, retryCount - 1) * SSE_RECONNECT_BACKOFF_BASE_MS
      // Background streams (the §23 notify stream) reconnect silently — a toast
      // for an invisible plumbing connection would only alarm the user.
      if (!reconnectToastShown && !sseOpts?.silentReconnect) {
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
