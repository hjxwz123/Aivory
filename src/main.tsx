import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
// Self-hosted brand fonts (bundled, no Google Fonts CDN — must load behind the
// GFW). These register the 'Fraunces Variable' / 'Geist Variable' families.
import '@fontsource-variable/fraunces'
import '@fontsource-variable/geist'
import '@fontsource-variable/geist-mono'
import './i18n'
import '@/store/accent' // eager init — sets data-accent on <html> before first render
import App from './App'
import './styles/globals.css'
import 'katex/dist/katex.min.css'

// §23: after a deploy the old tab's next lazy-chunk request 404s (hashed files
// were replaced) and React.lazy would white-screen. Vite surfaces that failure
// as a `vite:preloadError` event — reload once to pick up the new build. The
// timestamp guard stops a reload loop when the server is genuinely down.
window.addEventListener('vite:preloadError', (event) => {
  const KEY = 'aivory.chunk-reload-at'
  let last = 0
  try {
    last = Number(sessionStorage.getItem(KEY) || 0)
  } catch {
    /* storage unavailable — still attempt one reload */
  }
  if (Date.now() - last < 60_000) return // already retried recently — let the error surface
  try {
    sessionStorage.setItem(KEY, String(Date.now()))
  } catch {
    /* ignore */
  }
  event.preventDefault()
  window.location.reload()
})

const root = document.getElementById('root')
if (!root) throw new Error('Root element not found')

createRoot(root).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)

// Register the service worker so the app is installable to the home screen and
// opens in standalone (fullscreen) mode. Production only — in dev a SW would
// interfere with Vite's HMR. The SW itself is a no-cache passthrough (see
// public/sw.js), so there is no stale-build risk after a deploy.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // Installability is a progressive enhancement — ignore failures.
    })
  })
}
