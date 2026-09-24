/**
 * Docmee editor hand-off (§ AI PPT).
 *
 * Creation runs on our own UI, but slide-level editing is the one thing a bespoke
 * front end cannot reasonably rebuild — so a finished deck can be opened in the
 * vendor's editor, whose only delivery mechanism is their iframe SDK. This module
 * is therefore deliberately small: load the SDK script from the pinned URL (or a
 * self-hosted copy), instantiate the editor page, and destroy it on close.
 *
 * The token comes from our server per open and never touches the Api-Key.
 */
import { envStr } from '@/lib/env-config'

/** Pinned by default; a deployment can mirror it via VITE_AIVORY_DOCMEE_SDK_URL. */
export const DOCMEE_SDK_URL = envStr(
  'VITE_AIVORY_DOCMEE_SDK_URL',
  'https://cdn.jsdelivr.net/npm/@docmee/sdk-ui@1.6.47/dist/index.global.js',
)

export interface DocmeeEditorOptions {
  /** Element the iframe is mounted into. */
  container: HTMLElement
  token: string
  /** Upstream deck id (`page: 'editor'` requires it). */
  pptId: string
  sdkUrl?: string
  /** International build origin; omit on the China build. */
  domain?: string
  mode?: 'light' | 'dark'
  lang?: string
}

export interface DocmeeEditorHandle {
  updateToken(token: string): void
  destroy(): void
}

interface DocmeeInstance {
  updateToken?(token: string): void
  destroy?(): void
}

type DocmeeConstructor = new (options: Record<string, unknown>) => DocmeeInstance

export class DocmeeEditorError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'DocmeeEditorError'
  }
}

const GLOBAL_CANDIDATES = ['DocmeeUI', 'DocmeeSdkUI', 'Docmee', 'docmee'] as const

function resolveGlobalConstructor(): DocmeeConstructor | null {
  const scope = window as unknown as Record<string, unknown>
  for (const name of GLOBAL_CANDIDATES) {
    const candidate = scope[name]
    if (typeof candidate === 'function') return candidate as DocmeeConstructor
  }
  return null
}

async function importEsmConstructor(sdkUrl: string): Promise<DocmeeConstructor | null> {
  if (!sdkUrl.includes('index.global.js')) return null
  try {
    const url = sdkUrl.replace('index.global.js', 'index.mjs')
    const mod = (await import(/* @vite-ignore */ url)) as Record<string, unknown>
    const candidate = mod.DocmeeUI ?? mod.default
    return typeof candidate === 'function' ? (candidate as DocmeeConstructor) : null
  } catch {
    return null
  }
}

let inFlight: Promise<DocmeeConstructor> | null = null
let inFlightURL = ''

/** Load the editor SDK once per URL and resolve its constructor. */
export function loadDocmeeEditorSDK(sdkUrl: string): Promise<DocmeeConstructor> {
  const url = sdkUrl.trim() || DOCMEE_SDK_URL
  if (inFlight && inFlightURL === url) return inFlight
  const existing = resolveGlobalConstructor()
  if (existing) {
    inFlight = Promise.resolve(existing)
    inFlightURL = url
    return inFlight
  }
  inFlightURL = url
  inFlight = new Promise<DocmeeConstructor>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = url
    script.async = true
    script.dataset.docmeeEditorSdk = 'true'
    script.onload = () => {
      const ctor = resolveGlobalConstructor()
      if (ctor) {
        resolve(ctor)
        return
      }
      void importEsmConstructor(url).then((fromModule) => {
        if (fromModule) resolve(fromModule)
        else reject(new DocmeeEditorError(`The AI PPT editor SDK exposed no constructor (${url})`))
      })
    }
    script.onerror = () => {
      inFlight = null
      inFlightURL = ''
      reject(new DocmeeEditorError(`Failed to load the AI PPT editor SDK from ${url}`))
    }
    document.head.appendChild(script)
  })
  return inFlight
}

/** Mount the vendor's editor for one deck. */
export async function createDocmeeEditor(options: DocmeeEditorOptions): Promise<DocmeeEditorHandle> {
  const ctor = await loadDocmeeEditorSDK(options.sdkUrl ?? DOCMEE_SDK_URL)
  options.container.replaceChildren()
  const instance = new ctor({
    container: options.container,
    token: options.token,
    page: 'editor',
    pptId: options.pptId,
    // Decks are created through the V2 API, so the editor opens in V2 mode.
    creatorVersion: 'v2',
    mode: options.mode ?? 'light',
    lang: options.lang ?? 'zh',
    // The international build must be told explicitly; the China build resolves
    // its own origin and must not receive an empty DOMAIN.
    ...(options.domain ? { DOMAIN: options.domain } : {}),
  })
  return {
    updateToken: (token: string) => instance.updateToken?.(token),
    destroy: () => instance.destroy?.(),
  }
}
