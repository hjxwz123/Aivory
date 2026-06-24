/*
 * Aurelia service worker — intentionally minimal.
 *
 * Its only job is to make the app installable (Chrome/Android require a SW with
 * a fetch handler before offering "Install app"). It deliberately does NOT
 * precache the app shell: the SPA is served same-origin by the Go backend and a
 * cached index.html would pin users to a stale build after each deploy.
 *
 * Navigations pass straight through to the network. If you later want offline
 * support, add a versioned cache here with a network-first strategy.
 */
self.addEventListener('install', () => {
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim())
})

self.addEventListener('fetch', (event) => {
  // Only intercept top-level navigations; everything else uses the default
  // browser fetch. No caching → no stale assets.
  if (event.request.mode === 'navigate') {
    event.respondWith(fetch(event.request))
  }
})
