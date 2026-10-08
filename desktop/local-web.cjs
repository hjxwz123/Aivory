const path = require('node:path')
const { stat, realpath } = require('node:fs/promises')
const { pathToFileURL } = require('node:url')
const { REQUEST_HEADER } = require('./api-requests.cjs')

// An Electron protocol handler resolves this origin locally. No HTTP listener,
// DNS lookup or remote page is involved when loading the packaged frontend.
const APP_URL = 'https://app.aivory.invalid/'
const APP_ORIGIN = new URL(APP_URL).origin
const TYPES = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png',
  '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp', '.gif': 'image/gif',
  '.ico': 'image/x-icon', '.woff': 'font/woff', '.woff2': 'font/woff2',
  '.ttf': 'font/ttf', '.wasm': 'application/wasm', '.pdf': 'application/pdf',
  '.webmanifest': 'application/manifest+json', '.mp3': 'audio/mpeg', '.mp4': 'video/mp4',
}

function localAppUrl(value) {
  try { const url = new URL(value); return url.protocol === 'https:' && url.origin === APP_ORIGIN && !url.username && !url.password } catch { return false }
}

function serverUrl(value, baseUrl) {
  const url = new URL(value, APP_URL)
  return new URL(url.pathname + url.search + url.hash, baseUrl).href
}

function createLocalHandler({ webDir, baseUrl, fetch, fileFetch, onFailure, requests, session }) {
  const webRoot = webDir ? realpath(webDir) : null
  return async (request) => {
    const url = new URL(request.url)
    if (!localAppUrl(url.href)) return fetch(request, { bypassCustomProtocolHandlers: true })
    if (url.pathname === '/api' || url.pathname.startsWith('/api/')) {
      const headers = new Headers(request.headers)
      const tracked = requests?.attach(headers.get(REQUEST_HEADER), session)
      headers.delete(REQUEST_HEADER)
      for (const name of ['host', 'cookie', 'origin', 'referer', 'sec-fetch-site', 'sec-fetch-mode', 'sec-fetch-dest']) headers.delete(name)
      // The native transport owns the selected server's cookie jar. Present its
      // origin to the existing CSRF checks without weakening the web backend.
      headers.set('Origin', new URL(baseUrl).origin)
      const options = {
        method: request.method, headers, credentials: 'include', redirect: 'manual',
        cache: 'no-store', signal: tracked ? AbortSignal.any([tracked.signal, request.signal]) : request.signal, bypassCustomProtocolHandlers: true,
      }
      if (!['GET', 'HEAD'].includes(request.method)) {
        options.body = request.body
        options.duplex = 'half'
      }
      try {
        const response = await fetch(serverUrl(url.href, baseUrl), options)
        const responseHeaders = new Headers(response.headers)
        // Cookies remain HttpOnly in the native server session, never copied
        // onto the local frontend origin or exposed through the bridge.
        responseHeaders.delete('set-cookie')
        responseHeaders.delete('content-encoding')
        responseHeaders.delete('content-length')
        const location = responseHeaders.get('location')
        if (location) {
          const destination = new URL(location, baseUrl)
          if (destination.origin === new URL(baseUrl).origin && destination.pathname.startsWith('/api/')) {
            responseHeaders.set('location', new URL(destination.pathname + destination.search, APP_URL).href)
          }
        }
        // Pass through the ReadableStream; never wait for SSE to finish or
        // buffer a large download/upload before delivering its first bytes.
        let body = response.body
        if (tracked && body) {
          const reader = body.getReader()
          body = new ReadableStream({
            async pull(controller) {
              try {
                const { done, value } = await reader.read()
                if (done) { tracked.finish(); controller.close() }
                else controller.enqueue(value)
              } catch (error) { tracked.finish(); controller.error(error) }
            },
            cancel(reason) { tracked.finish(); return reader.cancel(reason) },
          }, { highWaterMark: 0 })
        } else tracked?.finish()
        return new Response(body, { status: response.status, statusText: response.statusText, headers: responseHeaders })
      } catch (error) {
        tracked?.finish()
        if (!options.signal.aborted) onFailure?.(error)
        throw error
      }
    }
    if (!['GET', 'HEAD'].includes(request.method)) return new Response('Method not allowed', { status: 405 })
    let relative
    try { relative = decodeURIComponent(url.pathname).replace(/^\/+/, '') } catch { return new Response('Not found', { status: 404 }) }
    if (relative.includes('\\') || relative.includes('\0')) return new Response('Not found', { status: 404 })
    const root = await webRoot
    let file = path.resolve(root, relative || 'index.html')
    if (file !== root && !file.startsWith(root + path.sep)) return new Response('Not found', { status: 404 })
    try {
      if (!(await stat(file)).isFile()) throw new Error('Not a file')
    } catch {
      // BrowserRouter deep links resolve to the bundled entry point. Missing
      // scripts/images retain a real 404 instead of receiving index.html.
      if (path.extname(relative) || relative.startsWith('assets/')) return new Response('Not found', { status: 404 })
      file = path.join(root, 'index.html')
    }
    file = await realpath(file)
    if (!file.startsWith(root + path.sep)) return new Response('Not found', { status: 404 })
    const response = await fileFetch(pathToFileURL(file).href)
    const headers = new Headers(response.headers)
    headers.set('Content-Type', TYPES[path.extname(file)] || 'application/octet-stream')
    headers.set('Cache-Control', 'no-store')
    return new Response(request.method === 'HEAD' ? null : response.body, { status: response.status, headers })
  }
}

module.exports = { APP_URL, APP_ORIGIN, localAppUrl, serverUrl, createLocalHandler }
