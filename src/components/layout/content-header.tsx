import { type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { ThemeToggle } from '@/components/ui/theme-toggle'
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
  className?: string
}

/**
 * The bar that sits at the top of pages rendered inside the chat layout's
 * content panel (Settings, Subscription). It is a non-scrolling flex child —
 * the page body scrolls beneath it — so it stays put without `sticky`. Keeping
 * one component for all of these guarantees the header reads identically from
 * page to page.
 */
export function ContentHeader({ title, backTo, backLabel, actions, children, className }: ContentHeaderProps) {
  return (
    <header className={cn('shrink-0 border-b border-[var(--color-divider)] bg-[var(--color-bg)]', className)}>
      <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] flex items-center gap-3 px-5 sm:px-8 h-14">
        {backTo ? (
          <>
            <Link
              to={backTo}
              className="inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive rounded-[6px]"
            >
              <ArrowLeft size={14} aria-hidden />
              {backLabel ? <span className="max-sm:hidden">{backLabel}</span> : null}
            </Link>
            <span className="h-5 w-px bg-[var(--color-divider)]" aria-hidden />
          </>
        ) : null}
        <h1 className="font-semibold tracking-tight text-[var(--color-fg)] text-[17px]">{title}</h1>
        <div className="ml-auto flex items-center gap-2">
          {actions ?? <ThemeToggle />}
        </div>
      </div>
      {children}
    </header>
  )
}
