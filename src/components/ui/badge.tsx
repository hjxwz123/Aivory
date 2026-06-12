import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

type Variant = 'neutral' | 'accent' | 'sage' | 'success' | 'warning' | 'danger' | 'info'
type Size = 'xs' | 'sm'

const variants: Record<Variant, string> = {
  neutral:
    'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] border-[var(--color-border)]',
  accent:
    'bg-[var(--color-accent-soft)] text-[var(--color-accent)] border-[var(--color-accent)]/15',
  sage:
    'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)] border-[var(--color-secondary)]/20',
  success:
    'bg-[var(--color-success-soft)] text-[var(--color-success)] border-[var(--color-success)]/20',
  warning:
    'bg-[var(--color-warning-soft)] text-[var(--color-warning)] border-[var(--color-warning)]/25',
  danger:
    'bg-[var(--color-danger-soft)] text-[var(--color-danger)] border-[var(--color-danger)]/20',
  info:
    'bg-[var(--color-info-soft)] text-[var(--color-info)] border-[var(--color-info)]/20',
}

interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  variant?: Variant
  size?: Size
  leadingIcon?: ReactNode
}

export function Badge({ variant = 'neutral', size = 'sm', leadingIcon, className, children, ...rest }: BadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full border whitespace-nowrap',
        variants[variant],
        size === 'xs' && 'h-5 px-1.5 text-[10px] font-medium',
        size === 'sm' && 'h-6 px-2 text-xs font-medium',
        className,
      )}
      {...rest}
    >
      {leadingIcon}
      {children}
    </span>
  )
}
