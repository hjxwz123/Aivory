import { forwardRef, type TextareaHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { className, invalid, ...rest },
  ref,
) {
  return (
    <textarea
      ref={ref}
      aria-invalid={invalid || undefined}
      className={cn(
        'block w-full min-h-[88px] rounded-[10px] p-3',
        'bg-[var(--color-surface-sunken)] border border-[var(--color-border)]',
        'text-[0.9375rem] leading-[1.55] text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)]',
        'resize-none outline-none transition-[border-color,box-shadow,background-color] duration-150',
        'focus:border-[var(--color-border-strong)] focus:bg-[var(--color-surface)]',
        'focus:ring-[3px] focus:ring-[var(--color-ring)]',
        invalid && 'border-[var(--color-danger)] focus:border-[var(--color-danger)] focus:ring-[var(--color-danger)]/30',
        rest.disabled && 'opacity-60 pointer-events-none',
        className,
      )}
      {...rest}
    />
  )
})
