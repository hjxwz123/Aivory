import { Link } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { cn } from '@/lib/utils'

interface AdminDetailHeaderProps {
  backTo: string
  backLabel: string
  className?: string
}

/** Keeps detail-page navigation available while the page body scrolls. */
export function AdminDetailHeader({ backTo, backLabel, className }: AdminDetailHeaderProps) {
  return (
    <header
      className={cn(
        'sticky top-0 z-[var(--z-sticky)] -mx-1 mb-4 flex min-h-10 items-center bg-[var(--color-bg)] px-1 py-1.5',
        className,
      )}
    >
      <Link
        to={backTo}
        className="inline-flex items-center gap-1.5 rounded-[6px] px-2 py-1.5 text-[12.5px] text-[var(--color-fg-subtle)] interactive hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <ArrowLeft size={12} aria-hidden />
        {backLabel}
      </Link>
    </header>
  )
}
