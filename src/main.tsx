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
