import { useEffect, useState, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { cn } from '@/lib/utils'

interface ChatSidePanelProps {
  open: boolean
  title: string
  onClose: () => void
  children: ReactNode
}

/** Shared shell for the mutually exclusive chat-side surfaces. */
export function ChatSidePanel({ open, title, onClose, children }: ChatSidePanelProps) {
  const isDesktop = useMediaQuery(mediaQuery.desktop)
  const [present, setPresent] = useState(open)

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

  if (isDesktop) {
    if (!present) return null
    return (
      <aside
        aria-label={title}
        data-state={open ? 'open' : 'closed'}
        onAnimationEnd={(event) => {
          if (event.currentTarget === event.target && !open) setPresent(false)
        }}
        className={cn(
          'chat-side-panel hidden h-full shrink-0 overflow-hidden bg-[var(--color-bg)] lg:block',
          !open && 'pointer-events-none',
        )}
      >
        <div className="chat-side-panel-inner flex h-full flex-col">
          {children}
        </div>
      </aside>
    )
  }

  return (
    <Sheet open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onClose() }}>
      <SheetContent
        side="right"
        size="lg"
        label={title}
        className="w-[min(28rem,94vw)] !border-l-0 bg-[var(--color-bg)] p-0"
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
