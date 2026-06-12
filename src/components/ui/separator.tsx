import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export function Separator({
  orientation = 'horizontal',
  className,
  ...rest
}: HTMLAttributes<HTMLDivElement> & { orientation?: 'horizontal' | 'vertical' }) {
  return (
    <div
      role="separator"
      aria-orientation={orientation}
      className={cn(
        'bg-[var(--color-divider)] shrink-0',
        orientation === 'horizontal' && 'h-px w-full',
        orientation === 'vertical' && 'w-px h-full',
        className,
      )}
      {...rest}
    />
  )
}
