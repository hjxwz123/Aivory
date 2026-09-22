import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { X } from 'lucide-react'
import { ArtifactPanelResizeHandle } from '@/components/chat/artifact-panel-resize-handle'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { ARTIFACT_PANEL_DEFAULT_WIDTH } from '@/lib/artifact-panel-width'
import { useSettings } from '@/store/settings'
import { cn } from '@/lib/utils'

interface ChatSidePanelProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
}

/**
 * Shared shell for the mutually exclusive chat-side surfaces.
 *
 * Desktop renders a docked column whose width the user controls by dragging the
 * divider on its left edge (or with the arrow keys / Home / End / Enter while it
 * is focused). The width lives in the settings store, so it survives reloads and
 * is shared by every right-edge panel — the HTML preview, the document viewer
 * and the file editors all resize the same way.
 */
export function ChatSidePanel({ open, title, onClose, children }: ChatSidePanelProps) {
  const isDesktop = useMediaQuery(mediaQuery.desktop)
  const { t } = useTranslation('common')
  const [present, setPresent] = useState(open)
  const panelRef = useRef<HTMLElement>(null)
  const panelWidth = useSettings((s) => s.artifactPanelWidth)
  const setPanelWidth = useSettings((s) => s.setArtifactPanelWidth)

  useEffect(() => {
    if (open) {
      setPresent(true)
      return
    }
    if (!present) return

    // Animation events normally remove the panel. This fallback also covers
    // browsers that suppress them and reduced-motion environments.
    const timer = window.setTimeout(() => setPresent(false), 240)
    return () => window.clearTimeout(timer)
  }, [open, present])

  // The persisted width has to reach CSS before the panel's open animation ends,
  // otherwise the first frame after a reload would use the default width and
  // visibly snap once React committed the stored value.
  useEffect(() => {
    if (typeof document === 'undefined') return
    document.documentElement.style.setProperty('--chat-side-panel-width', `${panelWidth}px`)
  }, [panelWidth])

  if (isDesktop) {
    if (!present) return null
    return (
      <div
        data-state={open ? 'open' : 'closed'}
        className={cn(
          'chat-side-panel-frame flex h-full shrink-0 overflow-hidden',
          !open && 'pointer-events-none',
        )}
      >
        <ArtifactPanelResizeHandle
          label={t('aria.panelResize', { defaultValue: 'Resize the preview panel' })}
          controlsId="chat-side-panel"
          targetRef={panelRef}
          width={panelWidth}
          onCommit={setPanelWidth}
          onReset={() => setPanelWidth(ARTIFACT_PANEL_DEFAULT_WIDTH)}
        />
        <aside
          ref={panelRef}
          id="chat-side-panel"
          aria-label={title}
          data-state={open ? 'open' : 'closed'}
          onAnimationEnd={(event) => {
            if (event.currentTarget === event.target && !open) setPresent(false)
          }}
          className="chat-side-panel hidden h-full min-w-0 flex-1 overflow-hidden bg-[var(--color-surface-sunken)] lg:block"
        >
          <div className="chat-side-panel-inner flex h-full flex-col">
            {children}
          </div>
        </aside>
      </div>
    )
  }

  return (
    <Sheet open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onClose() }}>
      <SheetContent
        side="right"
        size="lg"
        label={title}
        className="w-[min(28rem,94vw)] !border-l-0 bg-[var(--color-surface-sunken)] p-0"
      >
        {children}
      </SheetContent>
    </Sheet>
  )
}

interface ChatSidePanelHeaderProps {
  title: string
  closeLabel: string
  onClose: () => void
  children?: ReactNode
}

export function ChatSidePanelHeader({
  title,
  closeLabel,
  onClose,
  children,
}: ChatSidePanelHeaderProps) {
  return (
    <header className="flex h-12 shrink-0 items-center gap-1 px-4">
      <h2 className="min-w-0 flex-1 truncate text-[15px] font-medium text-[var(--color-fg)]">
        {title}
      </h2>
      {children}
      <Tooltip content={closeLabel}>
        <button
          type="button"
          onClick={onClose}
          aria-label={closeLabel}
          className="interactive inline-flex size-8 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <X size={14} aria-hidden />
        </button>
      </Tooltip>
    </header>
  )
}
