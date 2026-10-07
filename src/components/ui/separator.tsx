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
        'bg-transparent shrink-0',
        orientation === 'horizontal' && 'h-1 w-full',
        orientation === 'vertical' && 'w-2 h-full',
        className,
      )}
      {...rest}
    />
  )
}
