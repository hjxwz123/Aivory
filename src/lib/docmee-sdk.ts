/**
 * Loader + minimal typings for the Docmee (文多多 AiPPT) iframe UI SDK —
 * "接入方案二": the SDK is a plain <script> file served from a URL the admin
 * pins in settings, not an npm dependency. Loading it at runtime keeps the
 * third-party bundle out of our build (and out of our bundle-size budget) and
 * lets a deployment self-host or proxy the file by changing one setting.
 *
 * The SDK is intentionally treated as an untrusted global: we only touch the
 * constructor and the documented instance methods, and every value that comes
 * back through `onMessage` is re-validated by the caller.
 */

/** Numeric creator types (JS cannot import the TS enum from the script build). */
export const DocmeeCreatorType = {
  /** 智能生成（主题、要求） */
  AI_GEN: 1,
  /** 上传文件 */
  UPLOAD_FILES: 2,
  /** 上传思维导图 */
  UPLOAD_MIND: 3,
  /** 通过 word 精准转 ppt */
  WORD: 4,
  /** 通过网页链接生成 */
  URL: 5,
  /** 粘贴文本内容生成 */
  CONTENT: 6,
  /** Markdown 大纲生成 */
  MD: 7,
} as const

export interface DocmeeCreatorData {
  type?: number
  subject?: string
  /** v2 body content. v1 used `text`; v2 must use `content`. */
  content?: string
  files?: File[]
  options?: Record<string, unknown>
}

export interface DocmeeInitOptions {
  container: HTMLElement | string
  token: string
  page: 'dashboard' | 'creator' | 'editor' | 'customTemplate'
  /** Required when page is 'editor'. */
  pptId?: string
  creatorVersion?: 'v1' | 'v2'
  onMessage?: (message: DocmeeMessage) => unknown
  mode?: 'light' | 'dark'
  lang?: string
  isMobile?: boolean
  background?: string
  backgroundSize?: string
  padding?: string
  downloadButton?: boolean | Array<'pptx' | 'pdf'>
  creatorData?: DocmeeCreatorData
  /** International build origin, e.g. https://app.xpptx.com. */
  DOMAIN?: string
  /** Alternative API base when the deployment proxies Docmee. */
  baseURL?: string
  css?: string
}

export interface DocmeeMessage {
  type: string
  data?: Record<string, unknown>
}

export interface DocmeeInstance {
  updateToken(token: string): void
  destroy(): void
  getInfo(): void
  navigate?(target: { page: string; pptId?: string }): void
  sendMessage?(message: { type?: string; content: string }): void
}

export type DocmeeConstructor = new (options: DocmeeInitOptions) => DocmeeInstance

export class DocmeeSDKError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'DocmeeSDKError'
  }
}

/** Globals the IIFE build has shipped under across SDK versions. */
const GLOBAL_CANDIDATES = ['DocmeeUI', 'DocmeeSdkUI', 'Docmee', 'docmee'] as const

function resolveGlobalConstructor(): DocmeeConstructor | null {
  const scope = window as unknown as Record<string, unknown>
  for (const name of GLOBAL_CANDIDATES) {
    const candidate = scope[name]
    if (typeof candidate === 'function') return candidate as DocmeeConstructor
  }
  return null
}

/**
 * Best-effort ESM sibling of a CDN bundle, used only when the script tag loaded
 * but no documented global appeared (e.g. a build that dropped its IIFE
 * `globalName`). Harmless for the pinned default, which is an IIFE.
 */
function esmFallbackURL(scriptURL: string): string | null {
  if (!scriptURL.includes('index.global.js')) return null
  return scriptURL.replace('index.global.js', 'index.mjs')
}

async function importEsmConstructor(scriptURL: string): Promise<DocmeeConstructor | null> {
  const url = esmFallbackURL(scriptURL)
  if (!url) return null
  try {
    const mod = (await import(/* @vite-ignore */ url)) as Record<string, unknown>
    const candidate = mod.DocmeeUI ?? mod.default
    return typeof candidate === 'function' ? (candidate as DocmeeConstructor) : null
  } catch {
    return null
  }
}

let inFlight: Promise<DocmeeConstructor> | null = null
let inFlightURL = ''

/**
 * Load the SDK once per URL and resolve its constructor. Repeated calls share a
 * single in-flight script so two mounts cannot inject duplicate tags, and a
 * failed load clears the cache so the user can retry without a reload.
 */
export function loadDocmeeSDK(scriptURL: string): Promise<DocmeeConstructor> {
  const url = scriptURL.trim()
  if (!url) return Promise.reject(new DocmeeSDKError('AI PPT SDK URL is not configured'))
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
    script.dataset.docmeeSdk = 'true'
    script.onload = () => {
      const ctor = resolveGlobalConstructor()
      if (ctor) {
        resolve(ctor)
        return
      }
      // The tag loaded but exposed nothing we recognize: try the module build
      // before giving up, then report the URL so an admin can fix the setting.
      void importEsmConstructor(url).then((fromModule) => {
        if (fromModule) resolve(fromModule)
        else reject(new DocmeeSDKError(`AI PPT SDK loaded but exposed no constructor: ${url}`))
      })
    }
    script.onerror = () => {
      inFlight = null
      inFlightURL = ''
      reject(new DocmeeSDKError(`Failed to load the AI PPT SDK from ${url}`))
    }
    document.head.appendChild(script)
  })
  return inFlight
}

/** Test-only: forget the cached loader promise. */
export function resetDocmeeSDKCache(): void {
  inFlight = null
  inFlightURL = ''
}
