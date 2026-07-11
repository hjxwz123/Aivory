import { envNum } from '@/lib/env-config'
import { fallbackHighlight } from './fallback-highlight'
import { normalizeLanguage } from './shiki-languages'

export type HighlightEngine = 'shiki' | 'fallback'

export interface HighlightRequest {
  code: string
  lang?: string
  theme: 'light' | 'dark'
  signal?: AbortSignal
}

export interface HighlightResult {
  html: string
  lang: string
  engine: HighlightEngine
}

interface ShikiWorkerRequest {
  id: string
  code: string
  lang: string
}

interface ShikiWorkerSuccess {
  id: string
  ok: true
  html: string
  lang: string
}

interface ShikiWorkerFailure {
  id: string
  ok: false
  error: string
  reason: 'unsupported_language' | 'highlight_failed' | 'init_failed'
}

type ShikiWorkerResponse = ShikiWorkerSuccess | ShikiWorkerFailure

const MAX_SHIKI_CODE_LENGTH = envNum('VITE_AURELIA_MAX_SHIKI_CODE_LENGTH', 200_000)
const FINAL_RENDER_TIMEOUT_MS = envNum('VITE_AURELIA_FINAL_RENDER_TIMEOUT_MS', 15_000)
const CACHE_LIMIT = envNum('VITE_AURELIA_CACHE_LIMIT', 160)

let worker: Worker | null = null
let seq = 0

const pending = new Map<
  string,
  {
    resolve: (value: HighlightResult) => void
    reject: (reason?: unknown) => void
    timer: number
    fallback: HighlightResult
  }
>()

const cache = new Map<string, HighlightResult>()

function cacheKey(theme: 'light' | 'dark', lang: string, code: string): string {
  return `${theme}:${lang}:${code.length}:${hashString(code)}`
}

function hashString(value: string): string {
  let hash = 5381
  for (let i = 0; i < value.length; i++) {
    hash = (hash * 33) ^ value.charCodeAt(i)
  }
  return (hash >>> 0).toString(36)
}

function getCached(key: string): HighlightResult | undefined {
  const hit = cache.get(key)
  if (!hit) return undefined
  cache.delete(key)
  cache.set(key, hit)
  return hit
}

function setCached(key: string, value: HighlightResult) {
  cache.set(key, value)
  if (cache.size <= CACHE_LIMIT) return
  const first = cache.keys().next().value
  if (typeof first === 'string') cache.delete(first)
}

function cleanup(id: string) {
  const item = pending.get(id)
  if (!item) return
  window.clearTimeout(item.timer)
  pending.delete(id)
}

function getWorker(): Worker {
  if (worker) return worker
  worker = new Worker(new URL('./shiki-worker.ts', import.meta.url), { type: 'module' })
  worker.onmessage = (event: MessageEvent<ShikiWorkerResponse>) => {
    const item = pending.get(event.data.id)
    if (!item) return

    cleanup(event.data.id)
    if (!event.data.ok) {
      item.resolve(item.fallback)
      return
    }

    const result: HighlightResult = {
      html: extractCodeHtml(event.data.html),
      lang: event.data.lang,
      engine: 'shiki',
    }
    item.resolve(result)
  }
  worker.onerror = () => {
    const open = [...pending.entries()]
    pending.clear()
    for (const [, item] of open) {
      window.clearTimeout(item.timer)
      item.resolve(item.fallback)
    }
    worker?.terminate()
    worker = null
  }
  return worker
}

function fallbackResult(code: string, lang?: string): HighlightResult {
  return {
    html: fallbackHighlight(code, lang),
    lang: lang ?? 'text',
    engine: 'fallback',
  }
}

function extractCodeHtml(html: string): string {
  if (typeof document === 'undefined') return html
  const tpl = document.createElement('template')
  tpl.innerHTML = html
  const code = tpl.content.querySelector('code')
  return sanitizeHighlightedCodeHtml(code?.innerHTML ?? html)
}

function sanitizeHighlightedCodeHtml(html: string): string {
  const tpl = document.createElement('template')
  tpl.innerHTML = html

  const walk = (node: Node) => {
    if (node.nodeType === Node.ELEMENT_NODE) {
      const el = node as HTMLElement
      const tag = el.tagName.toLowerCase()
      if (tag !== 'span' && tag !== 'br') {
        const text = document.createTextNode(el.textContent ?? '')
        el.replaceWith(text)
        return
      }

      for (const attr of [...el.attributes]) {
        const name = attr.name.toLowerCase()
        if (tag === 'span' && name === 'style' && isSafeTokenStyle(attr.value)) continue
        el.removeAttribute(attr.name)
      }
    } else if (node.nodeType === Node.COMMENT_NODE) {
      node.parentNode?.removeChild(node)
      return
    }

    for (const child of [...node.childNodes]) walk(child)
  }

  for (const child of [...tpl.content.childNodes]) walk(child)
  const out = document.createElement('div')
  out.appendChild(tpl.content)
  return out.innerHTML
}

function isSafeTokenStyle(value: string): boolean {
  return value
    .split(';')
    .map((part) => part.trim())
    .filter(Boolean)
    .every((decl) =>
      /^(?:color|background-color|font-style|font-weight|text-decoration):(?:\s*(?:var\(--shiki-[\w-]+(?:,\s*currentColor)?\)|currentColor|inherit|italic|normal|bold|underline|none|[1-9]00))$/i.test(decl),
    )
}

export function highlightCode({ code, lang, theme, signal }: HighlightRequest): Promise<HighlightResult> {
  const normalized = normalizeLanguage(lang)
  const fallback = fallbackResult(code, normalized ?? lang)

  if (signal?.aborted) return Promise.resolve(fallback)
  if (!normalized || normalized === 'text' || code.length > MAX_SHIKI_CODE_LENGTH) {
    return Promise.resolve(fallback)
  }

  const key = cacheKey(theme, normalized, code)
  const cached = getCached(key)
  if (cached) return Promise.resolve(cached)

  let abort: (() => void) | undefined
  return new Promise<HighlightResult>((resolve, reject) => {
    const id = `shiki-${Date.now().toString(36)}-${++seq}`
    const timer = window.setTimeout(() => {
      cleanup(id)
      resolve(fallback)
    }, FINAL_RENDER_TIMEOUT_MS)

    pending.set(id, { resolve, reject, timer, fallback })

    abort = () => {
      cleanup(id)
      resolve(fallback)
    }
    signal?.addEventListener('abort', abort, { once: true })

    try {
      const w = getWorker()
      const payload: ShikiWorkerRequest = { id, code, lang: normalized }
      w.postMessage(payload)
    } catch {
      signal?.removeEventListener('abort', abort)
      cleanup(id)
      resolve(fallback)
    }
  }).then((result: HighlightResult) => {
    if (result.engine === 'shiki') setCached(key, result)
    return result
  }).finally(() => {
    if (abort) signal?.removeEventListener('abort', abort)
  })
}
