import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

interface SkeletonProps extends HTMLAttributes<HTMLDivElement> {
  shape?: 'rect' | 'circle' | 'line'
}

export function Skeleton({ shape = 'rect', className, ...rest }: SkeletonProps) {
  return (
    <div
      aria-hidden
      className={cn(
        'relative overflow-hidden',
        'bg-[var(--color-bg-muted)]',
        shape === 'circle' && 'rounded-full',
        shape === 'rect' && 'rounded-[8px]',
        shape === 'line' && 'rounded-full h-3',
        'before:absolute before:inset-0 before:-translate-x-full',
        'before:bg-gradient-to-r before:from-transparent before:via-[var(--color-surface)] before:to-transparent',
        'before:animate-[shimmer_2s_linear_infinite]',
        className,
      )}
      {...rest}
    />
  )
}
