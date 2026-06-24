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
