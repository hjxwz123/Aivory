import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface AdminPageHeaderProps {
  title: ReactNode
  /** One-line lead under the title. */
  description?: ReactNode
  /** Page-level commands. Keep to one primary button; the rest secondary. */
  actions?: ReactNode
  /** Inline marker beside the title (e.g. an "Archived" badge). */
  titleAdornment?: ReactNode
  /** Marks the title as still loading while a skeleton is shown in it. */
  titleBusy?: boolean
  className?: string
  descriptionClassName?: string
  actionsClassName?: string
  /** Extra content under the lead (links, scope hints). */
  children?: ReactNode
}

/**
 * Title block shared by every admin page. It uses the same sans heading as the
 * user settings pages so the console follows the Appearance → Font setting;
 * the editorial serif stays reserved for brand surfaces.
 */
export function AdminPageHeader({
  title,
  description,
  actions,
  titleAdornment,
  titleBusy,
  className,
  descriptionClassName,
  actionsClassName,
  children,
}: AdminPageHeaderProps) {
  return (
    <header
      className={cn(
        'flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between',
        className,
      )}
    >
      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h1
            aria-busy={titleBusy || undefined}
            className="min-w-0 break-words text-xl font-semibold tracking-normal text-[var(--color-fg)]"
          >
            {title}
          </h1>
          {titleAdornment}
        </div>
        {description ? (
          <p className={cn('mt-1.5 max-w-2xl text-sm leading-relaxed text-[var(--color-fg-muted)]', descriptionClassName)}>
            {description}
          </p>
        ) : null}
        {children}
      </div>
      {actions ? (
        <div className={cn('flex w-full flex-wrap items-center gap-2 sm:w-auto sm:shrink-0 sm:justify-end', actionsClassName)}>
          {actions}
        </div>
      ) : null}
    </header>
  )
}
