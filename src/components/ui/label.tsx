import type { LabelHTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

export function Label({ className, children, ...rest }: LabelHTMLAttributes<HTMLLabelElement>) {
  return (
    <label
      className={cn(
        'block text-sm font-medium text-[var(--color-fg)] leading-tight',
        className,
      )}
      {...rest}
    >
      {children}
    </label>
  )
}

interface FieldProps {
  label?: string
  hint?: string
  error?: string
  htmlFor?: string
  children: ReactNode
  className?: string
}

export function Field({ label, hint, error, htmlFor, children, className }: FieldProps) {
  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      {label ? (
        <label
          htmlFor={htmlFor}
          className="text-sm font-medium text-[var(--color-fg)] leading-tight"
        >
          {label}
        </label>
      ) : null}
      {children}
      {error ? (
        <p className="text-xs text-[var(--color-danger)]">{error}</p>
      ) : hint ? (
        <p className="text-xs text-[var(--color-fg-subtle)]">{hint}</p>
      ) : null}
    </div>
  )
}
