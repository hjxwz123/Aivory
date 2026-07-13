/**
 * icon-cache — tiny persistent cache for model icon images.
 *
 * Model icons (admin-uploaded `/api/icons/...` paths or external URLs) render
 * in every model-picker row and message header; without caching each fresh
 * mount refetches them from the backend/CDN, which flashes on slow links.
 * First successful fetch converts the image to a data URL, stores it in
 * localStorage (shared across sessions) and a module Map (shared across
 * mounts); afterwards icons render instantly with zero network.
 *
 * Failure behavior: anything that can't be fetched (external host without
 * CORS, 404, over the size cap) is negative-cached for the session and the
 * ORIGINAL url is used — the <img> tag itself is not subject to CORS, so the
 * icon still displays exactly as before, just without persistence.
 */

const LS_KEY = 'aivory.icon-cache.v1'
const MAX_ENTRIES = 48
// Icons are small PNGs; refuse to persist anything bigger than ~64 KB of
// data-URL so one oversized upload can't evict everything else.
const MAX_DATA_URL_LENGTH = 64 * 1024

type Persisted = Record<string, { d: string; t: number }>

// url → dataURL; '' = fetch failed this session (don't retry on every mount).
const mem = new Map<string, string>()
const inflight = new Map<string, Promise<string | undefined>>()

let persisted: Persisted | null = null
function loadPersisted(): Persisted {
  if (persisted) return persisted
  try {
    persisted = JSON.parse(localStorage.getItem(LS_KEY) || '{}') as Persisted
  } catch {
    persisted = {}
  }
  return persisted
}

function savePersisted() {
  if (!persisted) return
  const entries = Object.entries(persisted)
  if (entries.length > MAX_ENTRIES) {
    // Evict oldest beyond the cap.
    entries.sort((a, b) => a[1].t - b[1].t)
    persisted = Object.fromEntries(entries.slice(entries.length - MAX_ENTRIES))
  }
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(persisted))
  } catch {
    // Quota pressure — drop the oldest half and retry once; give up quietly.
    const kept = Object.entries(persisted).sort((a, b) => b[1].t - a[1].t)
    persisted = Object.fromEntries(kept.slice(0, Math.max(1, kept.length >> 1)))
    try {
      localStorage.setItem(LS_KEY, JSON.stringify(persisted))
    } catch {
      /* private mode / disabled storage — memory cache still works */
    }
  }
}

/** Synchronous lookup — memory first, then localStorage. */
export function cachedIconSrc(url: string): string | undefined {
  const hit = mem.get(url)
  if (hit) return hit
  if (hit === '') return undefined // negative-cached failure
  const row = loadPersisted()[url]
  if (row?.d) {
    mem.set(url, row.d)
    return row.d
  }
  return undefined
}

/** Fetch + persist an icon; resolves with the data URL or undefined on failure. */
export function ensureIconCached(url: string): Promise<string | undefined> {
  const hit = cachedIconSrc(url)
  if (hit) return Promise.resolve(hit)
  if (mem.get(url) === '') return Promise.resolve(undefined)
  const pending = inflight.get(url)
  if (pending) return pending
  const p = (async (): Promise<string | undefined> => {
    try {
      const res = await fetch(url, {
        // Same-origin icon uploads need the session cookie; external hosts
        // must not receive credentials.
        credentials: url.startsWith('/') ? 'include' : 'omit',
        mode: 'cors',
      })
      if (!res.ok || !(res.headers.get('content-type') || '').startsWith('image/')) throw new Error('not an image')
      const blob = await res.blob()
      const dataURL = await new Promise<string>((resolve, reject) => {
        const r = new FileReader()
        r.onload = () => resolve(String(r.result))
        r.onerror = () => reject(r.error)
        r.readAsDataURL(blob)
      })
      if (dataURL.length > MAX_DATA_URL_LENGTH) throw new Error('icon too large to persist')
      mem.set(url, dataURL)
      const store = loadPersisted()
      store[url] = { d: dataURL, t: Date.now() }
      savePersisted()
      return dataURL
    } catch {
      mem.set(url, '') // session-scoped negative cache; <img src={url}> still renders
      return undefined
    } finally {
      inflight.delete(url)
    }
  })()
  inflight.set(url, p)
  return p
}
