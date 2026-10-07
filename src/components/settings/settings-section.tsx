import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * User settings and admin configuration share a flat group: typography and
 * spacing establish hierarchy, while controls carry their own state.
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
  /** Extra layout classes for the section body. */
  bodyClassName?: string
  children: ReactNode
}) {
  return (
    <section aria-labelledby={title ? id : undefined} className={cn('mb-8 last:mb-0', className)}>
      {title || description || actions ? (
        <div className="mb-3 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
          <div className="min-w-0">
            {title ? (
              <h2 id={id} className="text-[15px] font-semibold tracking-normal text-[var(--color-fg)]">{title}</h2>
            ) : null}
            {description ? (
              <p className={cn('max-w-3xl text-[13px] leading-relaxed text-[var(--color-fg-muted)]', title && 'mt-1')}>{description}</p>
            ) : null}
          </div>
          {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
        </div>
      ) : null}
      <div
        className={cn(
          'settings-section-body min-w-0',
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
    <div className={cn('settings-row flex flex-col gap-2.5 py-3.5 sm:flex-row sm:items-center sm:gap-4', className)}>
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
  return <div className={cn('py-3', className)}>{children}</div>
}

/** Right-aligned save/submit row that closes a settings page. */
export function SettingsActions({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn('mt-6 flex flex-wrap items-center justify-end gap-2', className)}>{children}</div>
}
