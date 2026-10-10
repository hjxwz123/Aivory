/*
 * Aivory service worker: installability with network-only requests.
 * Private resources must reach the API on every read so account changes,
 * workspace revocations and resource deletions take effect immediately.
 */
self.addEventListener('install', () => {
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    // Remove caches left by earlier releases, including cached private images.
    const names = await caches.keys()
    await Promise.all(names.filter((name) => name.startsWith('aivory-img-')).map((name) => caches.delete(name)))
    await self.clients.claim()
  })())
})

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url)
  // Earlier releases also allowed the browser's HTTP cache to retain private
  // images. Bypass it as well as Cache API, including after upgrading the SW.
  const privateResource = url.origin === self.location.origin &&
    /^\/api\/(files|artifacts)\//.test(url.pathname)
  event.respondWith(fetch(event.request, privateResource ? { cache: 'no-store' } : undefined))
})
