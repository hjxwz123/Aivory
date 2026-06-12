import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  leadingIcon?: ReactNode
  trailingSlot?: ReactNode
  invalid?: boolean
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { leadingIcon, trailingSlot, invalid, className, ...rest },
  ref,
) {
  return (
    <div
      className={cn(
        'group/input flex items-center gap-2.5 h-10 rounded-[10px] px-3.5',
        'bg-[var(--color-surface-sunken)] border border-[var(--color-border)]',
        'transition-[border-color,background-color,box-shadow] duration-150',
        'focus-within:border-[var(--color-border-strong)] focus-within:bg-[var(--color-surface)]',
        'focus-within:ring-[3px] focus-within:ring-[var(--color-ring)]',
        invalid && 'border-[var(--color-danger)] focus-within:border-[var(--color-danger)] focus-within:ring-[var(--color-danger)]/30',
        rest.disabled && 'opacity-60 pointer-events-none',
      )}
    >
      {leadingIcon ? (
        <span className="text-[var(--color-fg-subtle)] inline-flex shrink-0">
          {leadingIcon}
        </span>
      ) : null}
      <input
        ref={ref}
        aria-invalid={invalid || undefined}
        className={cn(
          'flex-1 bg-transparent border-none outline-none',
          'text-[0.9375rem] text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)]',
          'tabular-nums',
          className,
        )}
        {...rest}
      />
      {trailingSlot}
    </div>
  )
})
