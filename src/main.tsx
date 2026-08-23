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

const aivoryConsoleMark = [
  '     _    ___ __     _____  _______   __',
  '    / \\  |_ _|\\ \\   / / _ \\|  _ \\ \\ / /',
  '   / _ \\  | ||\\ \\ / / | | | |_) | \\ V /',
  '  / ___ \\ | || \\ V /| |_| |  _ <   | |',
  ' /_/   \\_\\___|  \\_/  \\___/|_| \\_\\  |_|',
].join('\n')

console.info(
  `%c${aivoryConsoleMark}%c\n\n%cWELCOME TO AIVORY%c\n%cClear thinking starts here.`,
  'color: #5eead4; background: #042f2e; font: 700 15px/1.12 Geist Mono, monospace; padding: 12px 16px; border: 1px solid #0f766e; border-radius: 6px;',
  '',
  'color: #0f766e; font: 700 15px Geist, sans-serif;',
  '',
  'color: #64748b; font: 13px Geist, sans-serif;',
)

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
    <BrowserRouter useTransitions={false}>
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
    navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' }).catch(() => {
      // Installability is a progressive enhancement — ignore failures.
    })
  })
}
