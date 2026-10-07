import type { ReactNode } from 'react'
import { ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'

export function AdminAdvancedDisclosure({
  title,
  children,
  className,
  open,
  onOpenChange,
}: {
  title: ReactNode
  children: ReactNode
  className?: string
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  return (
    <details open={open} className={cn('group min-w-0', className)}>
      <summary
        onClick={onOpenChange ? (event) => {
          // Native toggle events are deferred and can overwrite a programmatic reveal.
          event.preventDefault()
          onOpenChange(!open)
        } : undefined}
        className="flex min-h-10 cursor-pointer list-none items-center justify-between gap-3 rounded-[6px] px-2 text-sm font-medium text-[var(--color-fg-muted)] transition-colors motion-reduce:transition-none hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] [&::-webkit-details-marker]:hidden"
      >
        <span>{title}</span>
        <ChevronDown size={16} aria-hidden className="shrink-0 transition-transform duration-150 motion-reduce:transition-none group-open:rotate-180" />
      </summary>
      <div className="min-w-0 pt-3">{children}</div>
    </details>
  )
}
