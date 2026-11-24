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
import { StartupBoundary } from '@/components/ui/startup-screen'

const aivoryConsoleMark = [
  '     _    ___ __     _____  _______   __',
  '    / \\  |_ _|\\ \\   / / _ \\|  _ \\ \\ / /',
  '   / _ \\  | ||\\ \\ / / | | | |_) | \\ V /',
  '  / ___ \\ | || \\ V /| |_| |  _ <   | |',
  ' /_/   \\_\\___|  \\_/  \\___/|_| \\_\\  |_|',
].join('\n')

console.info(
  `%c${aivoryConsoleMark}%c\n\n%cWELCOME TO AIVORY%c\n%cClear thinking starts here.`,
  'color: #c4b5fd; background: #211636; font: 700 15px/1.12 Geist Mono, monospace; padding: 12px 16px; border: 1px solid #6d4faa; border-radius: 6px;',
  '',
  'color: #6d4faa; font: 700 15px Geist, sans-serif;',
  '',
  'color: #64748b; font: 13px Geist, sans-serif;',
)

const root = document.getElementById('root')
if (!root) throw new Error('Root element not found')

createRoot(root).render(
  <StrictMode>
    <StartupBoundary>
      <BrowserRouter useTransitions={false}>
        <App />
      </BrowserRouter>
    </StartupBoundary>
  </StrictMode>,
)

// Register the service worker so the app is installable to the home screen and
// opens in standalone (fullscreen) mode. Production only — in dev a SW would
// interfere with Vite's HMR. The SW only intercepts private file reads to
// bypass old HTTP caches (see public/sw.js); other requests use the browser's
// network stack directly, keeping long uploads outside the SW event deadline.
if (import.meta.env.PROD && !window.aivoryDesktop && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' }).catch(() => {
      // Installability is a progressive enhancement — ignore failures.
    })
  })
}
