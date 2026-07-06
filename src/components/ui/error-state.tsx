import { AlertTriangle, RefreshCw } from 'lucide-react'
import type { ReactNode } from 'react'
import { Button } from './button'
import { cn } from '@/lib/utils'

interface ErrorStateProps {
  title?: ReactNode
  description?: ReactNode
  retry?: () => void
  className?: string
}

export function ErrorState({ title = 'Something went sideways.', description, retry, className }: ErrorStateProps) {
  return (
    <div
      className={cn(
        'mx-auto max-w-md text-center py-12 px-6',
        'flex flex-col items-center',
        className,
      )}
    >
      <div className="inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-danger-soft)] text-[var(--color-danger)] mb-5">
        <AlertTriangle size={20} aria-hidden />
      </div>
      <h3 className="text-2xl tracking-tight text-[var(--color-fg)]">{title}</h3>
      {description ? (
        <p className="mt-2.5 text-sm text-[var(--color-fg-muted)] leading-relaxed text-pretty">
          {description}
        </p>
      ) : null}
      {retry ? (
        <Button variant="secondary" size="sm" onClick={retry} leadingIcon={<RefreshCw size={14} aria-hidden />} className="mt-6">
          Try again
        </Button>
      ) : null}
    </div>
  )
}
