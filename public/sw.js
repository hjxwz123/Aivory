/*
 * Aivory service worker.
 *
 * 1) Installability: Chrome/Android only offer "Install app" when a SW with a
 *    fetch handler exists. Navigations pass straight through to the network — no
 *    app-shell precache, so a deploy never pins users to a stale index.html.
 *
 * 2) Cache-first for IMAGES (generated artifacts + uploaded files): the gallery
 *    and chat re-render the same images constantly. We serve them from a local
 *    Cache-API store and only hit the network for images we haven't seen yet —
 *    fast repeat loads, no wasted server bandwidth. Generated images (image
 *    models / image_generate tool) and the user's gallery are /api/artifacts/*;
 *    uploaded chat images are /api/files/*. The image LIST (/api/me/images) is
 *    NOT cached, so it always comes fresh from the backend. Only full, successful
 *    image/* GETs are cached; the store is capped (FIFO) so it can't grow
 *    unbounded.
 */
const IMG_CACHE = 'aivory-img-v1'
const IMG_CACHE_MAX = 400

self.addEventListener('install', () => {
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      // Drop any older image-cache versions on activate.
      const names = await caches.keys()
      await Promise.all(
        names.filter((n) => n.startsWith('aivory-img-') && n !== IMG_CACHE).map((n) => caches.delete(n)),
      )
      await self.clients.claim()
    })(),
  )
})

function isImageRequest(req) {
  if (req.method !== 'GET') return false
  try {
    const url = new URL(req.url)
    if (url.origin !== self.location.origin) return false
    return url.pathname.startsWith('/api/artifacts/') || url.pathname.startsWith('/api/files/')
  } catch {
    return false
  }
}

// FIFO cap: when over the limit, evict the oldest entries (caches.keys() returns
// them in insertion order).
async function trimCache(cache) {
  const keys = await cache.keys()
  const over = keys.length - IMG_CACHE_MAX
  for (let i = 0; i < over; i++) await cache.delete(keys[i])
}

async function cacheFirstImage(req) {
  const cache = await caches.open(IMG_CACHE)
  const hit = await cache.match(req)
  if (hit) return hit
  const res = await fetch(req)
  // Cache only full, same-origin, successful IMAGE responses — never errors,
  // redirects, opaque/cross-origin, partial (206) ranges, or non-image files
  // (e.g. PDFs/docs served from /api/files).
  const ct = res.headers.get('content-type') || ''
  if (res.status === 200 && res.type === 'basic' && ct.startsWith('image/')) {
    // clone() before the page consumes the body; cache write is best-effort.
    cache.put(req, res.clone()).then(() => trimCache(cache)).catch(() => {})
  }
  return res
}

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.mode === 'navigate') {
    event.respondWith(fetch(req))
    return
  }
  if (isImageRequest(req)) {
    // On any failure, fall back to a plain network fetch so a SW bug never breaks
    // image loading.
    event.respondWith(cacheFirstImage(req).catch(() => fetch(req)))
  }
})
