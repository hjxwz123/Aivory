import { Check } from 'lucide-react'
import { forwardRef, type InputHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export type CheckboxProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'type'>

export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(function Checkbox(
  { className, disabled, ...rest },
  ref,
) {
  return (
    <span className={cn('relative inline-flex size-[18px] shrink-0', disabled && 'cursor-not-allowed')}>
      <input
        ref={ref}
        type="checkbox"
        disabled={disabled}
        className={cn(
          'peer size-[18px] cursor-pointer appearance-none rounded-[4px]',
          'border border-[var(--color-border-strong)] bg-[var(--color-surface-sunken)]',
          'transition-[background-color,border-color,box-shadow] duration-150',
          'checked:border-[var(--color-accent)] checked:bg-[var(--color-accent)]',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--color-bg)]',
          'aria-invalid:border-[var(--color-danger)]',
          'disabled:cursor-not-allowed disabled:opacity-50',
          className,
        )}
        {...rest}
      />
      <Check
        size={13}
        strokeWidth={3}
        aria-hidden
        className="pointer-events-none absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 text-[var(--color-accent-fg)] opacity-0 peer-checked:opacity-100"
      />
    </span>
  )
})
