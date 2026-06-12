import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface EmptyStateProps {
  icon?: ReactNode
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
  className?: string
}

export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center text-center mx-auto max-w-md',
        'py-16 px-6',
        className,
      )}
    >
      {icon ? (
        <div className="mb-5 inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
          {icon}
        </div>
      ) : null}
      <h3 className="font-serif text-2xl tracking-tight text-[var(--color-fg)]">{title}</h3>
      {description ? (
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)] leading-relaxed text-pretty">
          {description}
        </p>
      ) : null}
      {action ? <div className="mt-6">{action}</div> : null}
    </div>
  )
}
