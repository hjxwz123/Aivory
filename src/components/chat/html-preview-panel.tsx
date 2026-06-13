import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { AppWindow, RotateCw, X } from 'lucide-react'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { useHtmlPreview } from '@/store/html-preview'
import { useMediaQuery } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'

/**
 * HtmlPreviewPanel — renders assistant-produced HTML in a sandboxed iframe.
 * Desktop (≥1024px): an inline split panel on the right edge of the chat
 * area, so the conversation stays usable while markup streams in live.
 * Mobile: the same content inside a right-side Sheet.
 *
 * Security: model HTML is hostile input. The iframe grants `allow-scripts`
 * and NOTHING else:
 * - no `allow-same-origin` → opaque origin; no cookies, storage, parent DOM.
 *   (NEVER add it: combined with allow-scripts it voids the sandbox.)
 * - no `allow-forms` → forms can't submit/navigate to an attacker URL with
 *   whatever a user might type into a rendered page; JS-driven interactivity
 *   keeps working.
 * - no `allow-top-navigation` / `allow-popups` / `allow-modals` /
 *   `allow-downloads` → the preview can't redirect, spawn windows, throw
 *   native dialogs, or drop files.
 * - `referrerPolicy="no-referrer"` keeps our URL out of any subresource it
 *   loads.
 */
export function HtmlPreviewPanel() {
  const open = useHtmlPreview((s) => s.open)
  const html = useHtmlPreview((s) => s.html)
  const close = useHtmlPreview((s) => s.close)
  const isDesktop = useMediaQuery('(min-width: 1024px)')
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

  if (isDesktop) {
    if (!open) return null
    return (
      <aside
        aria-label={t('code.previewTitle')}
        className={cn(
          'hidden lg:flex flex-col shrink-0 h-full w-[clamp(22rem,38vw,40rem)]',
          'border-l border-[var(--color-divider)] bg-[var(--color-bg)]',
          'animate-[panel-in_240ms_var(--ease-out)]',
        )}
      >
        <PreviewBody
          doc={doc}
          reloadKey={reloadKey}
          onRefresh={() => setReloadKey((k) => k + 1)}
          onClose={close}
        />
      </aside>
    )
  }

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) close() }}>
      <SheetContent side="right" size="lg" label={t('code.previewTitle')} className="w-[min(28rem,94vw)]">
        <PreviewBody
          doc={doc}
          reloadKey={reloadKey}
          onRefresh={() => setReloadKey((k) => k + 1)}
          onClose={close}
        />
      </SheetContent>
    </Sheet>
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
  return (
    <>
      <header className="flex items-center gap-2 h-12 px-3 border-b border-[var(--color-divider)]">
        <AppWindow size={14} aria-hidden className="text-[var(--color-fg-muted)]" />
        <span className="flex-1 min-w-0 truncate font-serif tracking-tight text-[15px] text-[var(--color-fg)]">
          {t('code.previewTitle')}
        </span>
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
        <Tooltip content={t('code.previewClose')}>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('code.previewClose')}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <X size={14} aria-hidden />
          </button>
        </Tooltip>
      </header>

      <div className="flex-1 min-h-0 p-3">
        <iframe
          key={reloadKey}
          title={t('code.previewTitle')}
          sandbox="allow-scripts"
          referrerPolicy="no-referrer"
          srcDoc={doc}
          className="size-full rounded-[12px] border border-[var(--color-border)] bg-[var(--color-preview-canvas)]"
        />
      </div>

      <footer className="px-4 py-2 border-t border-[var(--color-divider)]">
        <p className="text-[11px] text-[var(--color-fg-subtle)]">{t('code.previewSandboxHint')}</p>
      </footer>
    </>
  )
}
