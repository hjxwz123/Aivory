import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Maximize2, Minimize2, RotateCw } from 'lucide-react'
import { ChatSidePanel, ChatSidePanelHeader } from '@/components/chat/chat-side-panel'
import { Tooltip } from '@/components/ui/tooltip'
import { useHtmlPreview } from '@/store/html-preview'

/**
 * HtmlPreviewPanel — renders assistant-produced HTML in a sandboxed iframe.
 * Desktop (≥1024px): an inline split panel on the right edge of the chat
 * area, so the conversation stays usable while markup streams in live.
 * Mobile: the same content inside a right-side Sheet.
 *
 * External resources: the preview is allowed to load external images, CSS,
 * fonts, media and scripts so a model-built page renders as intended, and to
 * open external links in a new tab. Security rests on the opaque origin, NOT on
 * blocking the network:
 * - no `allow-same-origin` → opaque origin; the frame can never read our
 *   cookies, storage, or DOM, so external resources it loads can't reach our
 *   data. (NEVER add allow-same-origin: with allow-scripts it voids the sandbox.)
 * - `allow-popups allow-popups-to-escape-sandbox` lets a user-clicked link open
 *   normally in a new tab (the requested "external links work"); popups are
 *   user-initiated and open as ordinary top-level tabs governed by normal
 *   browser security.
 * - injected `<base target="_blank" rel="noopener noreferrer">` routes link
 *   clicks to a new tab without exposing our window via `opener`.
 * - injected `upgrade-insecure-requests` CSP upgrades any `http://` subresource
 *   to https so it isn't blocked as mixed content on an https deployment.
 * - no `allow-forms` / `allow-modals` / `allow-downloads` → the page can't
 *   submit forms, throw native dialogs, or drop files.
 * - `referrerPolicy="no-referrer"` keeps our URL out of any subresource it loads.
 */

// EXTERNAL_HEAD is injected into the previewed document's <head> so external
// resources render (mixed-content upgrade) and links open in a new tab safely.
const EXTERNAL_HEAD =
  '<meta http-equiv="Content-Security-Policy" content="upgrade-insecure-requests">' +
  '<base target="_blank" rel="noopener noreferrer">'

// withExternalResources injects EXTERNAL_HEAD into the document head. Handles a
// full document (insert after <head> or <html>) and a bare fragment (prepend —
// the browser hoists <base>/<meta> into the implicit head).
function withExternalResources(html: string): string {
  if (!html) return html
  const headOpen = /<head[^>]*>/i
  if (headOpen.test(html)) return html.replace(headOpen, (m) => m + EXTERNAL_HEAD)
  const htmlOpen = /<html[^>]*>/i
  if (htmlOpen.test(html)) return html.replace(htmlOpen, (m) => `${m}<head>${EXTERNAL_HEAD}</head>`)
  return EXTERNAL_HEAD + html
}
export function HtmlPreviewPanel() {
  const open = useHtmlPreview((s) => s.open)
  const html = useHtmlPreview((s) => s.html)
  const close = useHtmlPreview((s) => s.close)
  const { t } = useTranslation('chat')
  const { pathname } = useLocation()

  // Leaving the current page closes the preview — a drawer pinned to a
  // conversation shouldn't follow the user to the next one.
  const prevPath = useRef(pathname)
  useEffect(() => {
    if (prevPath.current === pathname) return
    prevPath.current = pathname
    close()
  }, [pathname, close])

  // Re-setting srcDoc reloads the whole document, so streaming markup is
  // applied on a trailing debounce: live enough to feel real-time, calm
  // enough not to flicker on every token.
  const [doc, setDoc] = useState('')
  useEffect(() => {
    if (!open) return
    const timer = setTimeout(() => setDoc(html), doc ? 350 : 0)
    return () => clearTimeout(timer)
  }, [open, html, doc])

  const [reloadKey, setReloadKey] = useState(0)

  return (
    <ChatSidePanel open={open} title={t('code.previewTitle')} onClose={close}>
      <PreviewBody
        doc={doc}
        reloadKey={reloadKey}
        onRefresh={() => setReloadKey((k) => k + 1)}
        onClose={close}
      />
    </ChatSidePanel>
  )
}

interface PreviewBodyProps {
  doc: string
  reloadKey: number
  onRefresh: () => void
  onClose: () => void
}

function PreviewBody({ doc, reloadKey, onRefresh, onClose }: PreviewBodyProps) {
  const { t } = useTranslation('chat')
  const rootRef = useRef<HTMLDivElement>(null)
  const [isFullscreen, setIsFullscreen] = useState(false)

  // Keep local state in sync with the native fullscreen lifecycle (Esc exits).
  useEffect(() => {
    const onChange = () => setIsFullscreen(document.fullscreenElement === rootRef.current)
    document.addEventListener('fullscreenchange', onChange)
    return () => document.removeEventListener('fullscreenchange', onChange)
  }, [])

  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement) await document.exitFullscreen()
      else if (rootRef.current) await rootRef.current.requestFullscreen()
    } catch {
      /* fullscreen can be denied (permissions / unsupported) — no-op */
    }
  }

  return (
    <div ref={rootRef} className="flex h-full flex-col bg-[var(--color-bg)]">
      <ChatSidePanelHeader
        title={t('code.previewTitle')}
        closeLabel={t('code.previewClose')}
        onClose={onClose}
      >
        <Tooltip content={t(isFullscreen ? 'code.previewExitFullscreen' : 'code.previewFullscreen', { defaultValue: isFullscreen ? 'Exit fullscreen' : 'Fullscreen' })}>
          <button
            type="button"
            onClick={() => void toggleFullscreen()}
            aria-label={t(isFullscreen ? 'code.previewExitFullscreen' : 'code.previewFullscreen', { defaultValue: isFullscreen ? 'Exit fullscreen' : 'Fullscreen' })}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            {isFullscreen ? <Minimize2 size={14} aria-hidden /> : <Maximize2 size={14} aria-hidden />}
          </button>
        </Tooltip>
        <Tooltip content={t('code.previewRefresh')}>
          <button
            type="button"
            onClick={onRefresh}
            aria-label={t('code.previewRefresh')}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <RotateCw size={14} aria-hidden />
          </button>
        </Tooltip>
      </ChatSidePanelHeader>

      <div className="min-h-0 flex-1 bg-[var(--color-preview-canvas)]">
        <iframe
          key={reloadKey}
          title={t('code.previewTitle')}
          sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox"
          referrerPolicy="no-referrer"
          srcDoc={withExternalResources(doc)}
          className="block size-full border-0 bg-[var(--color-preview-canvas)]"
        />
      </div>
    </div>
  )
}
