import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface AdminPageHeaderProps {
  title: ReactNode
  /** Supporting copy; routine page help is available to screen readers. */
  description?: ReactNode
  /** Show descriptions that identify the current item, such as its model ID. */
  showDescription?: boolean
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
  showDescription = false,
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
        'admin-page-header flex flex-wrap items-center justify-between gap-x-4 gap-y-2',
        className,
      )}
    >
      <div className="min-w-[min(100%,8rem)] flex-1">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h1
            aria-busy={titleBusy || undefined}
            className="min-w-0 break-words text-lg font-semibold leading-6 tracking-normal text-[var(--color-fg)]"
          >
            {title}
          </h1>
          {titleAdornment}
        </div>
        {description ? (
          <p className={showDescription ? cn('mt-1 max-w-2xl text-xs leading-5 text-[var(--color-fg-muted)]', descriptionClassName) : 'sr-only'}>
            {description}
          </p>
        ) : null}
        {children}
      </div>
      {actions ? (
        <div
          className={cn(
            'ml-auto flex max-w-full flex-wrap items-center justify-end gap-2',
            '[&_[data-button-size=md]]:h-9 [&_[data-button-size=md]]:rounded-[8px] [&_[data-button-size=md]]:px-3 [&_[data-button-size=md]]:text-[13px]',
            '[&_[data-button-size=sm]]:h-9 [&_[data-button-size=sm]]:rounded-[8px] [&_[data-button-size=sm]]:px-3 [&_[data-button-size=sm]]:text-[13px]',
            'max-sm:[&_[data-button-size=md]]:h-11 max-sm:[&_[data-button-size=sm]]:h-11',
            actionsClassName,
          )}
        >
          {actions}
        </div>
      ) : null}
    </header>
  )
}
