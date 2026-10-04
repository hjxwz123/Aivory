import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * The bordered group that the user settings dialog and the admin console both
 * use for configuration pages: a plain sans heading above one rounded container
 * whose rows are separated by dividers. Keeping a single implementation is what
 * makes an admin settings page read like a user settings page.
 */
export function SettingsSection({
  title,
  description,
  actions,
  id,
  className,
  bodyClassName,
  children,
}: {
  /** Omit on single-group pages where the page header already names it. */
  title?: ReactNode
  description?: ReactNode
  /** Right-aligned controls beside the heading (e.g. "Manage" links). */
  actions?: ReactNode
  /** Heading id, used as the section's accessible name. */
  id?: string
  className?: string
  /** Overrides for the bordered body; pass `divide-y-0` for free-form content. */
  bodyClassName?: string
  children: ReactNode
}) {
  return (
    <section aria-labelledby={title ? id : undefined} className={cn('mb-8 last:mb-0', className)}>
      {title || description || actions ? (
        <div className="mb-3 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
          <div className="min-w-0">
            {title ? (
              <h2 id={id} className="text-lg font-medium tracking-normal text-[var(--color-fg)]">{title}</h2>
            ) : null}
            {description ? (
              <p className={cn('max-w-3xl text-sm text-[var(--color-fg-muted)]', title && 'mt-1.5')}>{description}</p>
            ) : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </div>
      ) : null}
      <div
        className={cn(
          'divide-y divide-[var(--color-divider)] rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]',
          bodyClassName,
        )}
      >
        {children}
      </div>
    </section>
  )
}

/** One label/description + control line inside a SettingsSection. */
export function SettingsRow({
  label,
  description,
  htmlFor,
  className,
  children,
}: {
  label: ReactNode
  description?: ReactNode
  /** When set, the label becomes a <label> bound to this control id. */
  htmlFor?: string
  className?: string
  children?: ReactNode
}) {
  const Label = htmlFor ? 'label' : 'div'
  return (
    <div className={cn('flex flex-col gap-2.5 px-4 py-3 sm:flex-row sm:items-center sm:gap-4', className)}>
      <div className="min-w-0 flex-1">
        <Label htmlFor={htmlFor} className="block text-sm font-medium text-[var(--color-fg)]">
          {label}
        </Label>
        {description ? (
          <p className="mt-0.5 max-w-md text-xs leading-normal text-[var(--color-fg-muted)]">{description}</p>
        ) : null}
      </div>
      {children !== undefined ? <div className="sm:shrink-0">{children}</div> : null}
    </div>
  )
}

/** Free-form padded block inside a SettingsSection (forms, lists, editors). */
export function SettingsBlock({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn('px-4 py-4', className)}>{children}</div>
}

/** Right-aligned save/submit row that closes a settings page. */
export function SettingsActions({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn('mt-6 flex flex-wrap items-center justify-end gap-2', className)}>{children}</div>
}
