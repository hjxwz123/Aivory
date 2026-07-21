import { useEffect, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft, Menu } from 'lucide-react'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { useUI } from '@/store/ui'
import { cn } from '@/lib/utils'

interface ContentHeaderProps {
  /** Compact serif page title shown in the bar. */
  title: string
  /** Optional back link target (e.g. '/chat'). */
  backTo?: string
  /** Visible label for the back link (hidden on small screens). */
  backLabel?: string
  /** Right-aligned controls. Defaults to the theme toggle (language switching
   * lives in the avatar menu). */
  actions?: ReactNode
  /** Optional secondary row (e.g. a tab nav) rendered under the title row. */
  children?: ReactNode
  /** Let full-width workspaces opt out of the standard centered content cap. */
  fluid?: boolean
  className?: string
}

/**
 * The bar that sits at the top of pages rendered inside the chat layout's
 * content panel (Settings, Subscription). It is a non-scrolling flex child —
 * the page body scrolls beneath it — so it stays put without `sticky`. Keeping
 * one component for all of these guarantees the header reads identically from
 * page to page.
 */
export function ContentHeader({ title, backTo, backLabel, actions, children, fluid = false, className }: ContentHeaderProps) {
  // This bar IS the page's mobile top bar — tell the shell to drop its standalone
  // brand bar so settings/subscription show one bar (this one + a hamburger),
  // not two stacked rows. (§ mobile redesign — reuses the pageOwnsTopBar flag.)
  useEffect(() => {
    useUI.getState().setPageOwnsTopBar(true)
    return () => useUI.getState().setPageOwnsTopBar(false)
  }, [])
  return (
    <header className={cn('shrink-0 bg-[var(--color-bg)]', className)}>
      <div
        className={cn(
          'flex h-14 w-full items-center gap-2 px-2 sm:gap-3 sm:px-8',
          !fluid && 'mx-auto max-w-[var(--layout-content-max-w)]',
        )}
      >
        {backTo ? (
          <>
            <Link
              to={backTo}
              className="inline-flex items-center justify-center gap-1.5 min-h-[var(--tap-min)] min-w-[var(--tap-min)] sm:min-w-0 px-2 -ml-1 sm:ml-0 sm:px-0 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive rounded-[8px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <ArrowLeft size={16} aria-hidden />
              {backLabel ? <span className="max-sm:hidden">{backLabel}</span> : null}
            </Link>
            <span className="h-5 w-px bg-[var(--color-divider)]" aria-hidden />
          </>
        ) : (
          /* No back link → a hamburger opens the nav drawer (phone/tablet only). */
          <button
            type="button"
            aria-label="Open navigation"
            onClick={() => useUI.getState().toggleNav()}
            className="lg:hidden inline-flex items-center justify-center size-[var(--tap-min)] -ml-1 rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <Menu size={18} aria-hidden />
          </button>
        )}
        <h1 className="font-semibold tracking-tight text-[var(--color-fg)] text-[17px] truncate">{title}</h1>
        <div className="ml-auto flex items-center gap-2">
          {actions ?? <ThemeToggle />}
        </div>
      </div>
      {children}
    </header>
  )
}
