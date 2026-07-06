import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

interface CardProps extends HTMLAttributes<HTMLDivElement> {
  interactive?: boolean
  variant?: 'default' | 'sunken' | 'raised'
}

export function Card({ interactive, variant = 'default', className, ...rest }: CardProps) {
  return (
    <div
      className={cn(
        'rounded-2xl border',
        variant === 'default' && 'bg-[var(--color-surface)] border-[var(--color-border)]',
        variant === 'sunken' && 'bg-[var(--color-surface-sunken)] border-[var(--color-border-subtle)]',
        variant === 'raised' && 'bg-[var(--color-surface-raised)] border-[var(--color-border)] shadow-[var(--shadow-md)]',
        interactive && 'interactive cursor-pointer hover:border-[var(--color-border-strong)] hover:shadow-[var(--shadow-sm)]',
        className,
      )}
      {...rest}
    />
  )
}

export function CardHeader({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('p-5 pb-3', className)} {...rest} />
}

export function CardBody({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('p-5 pt-2', className)} {...rest} />
}

export function CardFooter({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('p-5 pt-3 border-t border-[var(--color-divider)] flex items-center justify-end gap-2', className)}
      {...rest}
    />
  )
}

export function CardTitle({ className, ...rest }: HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h3 className={cn('text-xl text-[var(--color-fg)] tracking-tight', className)} {...rest} />
  )
}

export function CardDescription({ className, ...rest }: HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn('text-sm text-[var(--color-fg-muted)] mt-1.5', className)} {...rest} />
}
