import type { ReactNode } from 'react'
import { Tooltip } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

interface CodeActionProps {
  onClick: () => void
  label: string
  tooltip?: string
  children: ReactNode
  disabled?: boolean
  pressed?: boolean
}

export function CodeAction({ onClick, label, tooltip, children, disabled = false, pressed }: CodeActionProps) {
  return (
    <Tooltip content={tooltip ?? label}>
      <button
        type="button"
        onClick={onClick}
        disabled={disabled}
        aria-label={label}
        aria-pressed={pressed}
        className={cn(
          'inline-flex shrink-0 items-center justify-center size-7 max-sm:size-[var(--tap-min)] rounded-[7px]',
          'text-[var(--color-fg-subtle)] interactive',
          'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-50',
          pressed && 'bg-[var(--color-bg-muted)] text-[var(--color-fg)]',
        )}
      >
        {children}
      </button>
    </Tooltip>
  )
}
