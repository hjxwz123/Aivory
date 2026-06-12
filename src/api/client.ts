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
    throw new ApiError(res.status, message, parsed)
  }
  return parsed as T
}

/** Open a streaming POST request that yields SSE events as parsed JSON. */
export async function* streamSSE(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): AsyncGenerator<{ event: string; data: unknown }> {
  const res = await fetch(API_BASE + path, {
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
