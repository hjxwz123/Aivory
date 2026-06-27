import type { LucideIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

interface SuggestionCardProps {
  icon: LucideIcon
  title: string
  prompt: string
  onClick: () => void
  className?: string
}

export function SuggestionCard({ icon: Icon, title, prompt, onClick, className }: SuggestionCardProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'group/sug w-full text-left rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]',
        'p-3.5 sm:p-4 transition-[border-color,transform,box-shadow] duration-[180ms] ease-[var(--ease-out)]',
        'hover:border-[var(--color-border-strong)] hover:-translate-y-0.5 hover:shadow-[var(--shadow-sm)]',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        className,
      )}
    >
      <span className="inline-flex items-center justify-center size-7 rounded-[8px] bg-[var(--color-secondary-soft)] text-[var(--color-secondary)] mb-2.5 sm:mb-3">
        <Icon size={14} aria-hidden />
      </span>
      <div className="font-medium text-sm text-[var(--color-fg)] leading-tight">{title}</div>
      <p className="mt-1.5 text-xs text-[var(--color-fg-muted)] leading-relaxed max-sm:line-clamp-1 line-clamp-2">
        {prompt}
      </p>
    </button>
  )
}
