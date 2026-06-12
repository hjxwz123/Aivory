import * as SwitchPrimitive from '@radix-ui/react-switch'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { cn } from '@/lib/utils'

export const Switch = forwardRef<
  ElementRef<typeof SwitchPrimitive.Root>,
  ComponentPropsWithoutRef<typeof SwitchPrimitive.Root>
>(function Switch({ className, ...rest }, ref) {
  return (
    <SwitchPrimitive.Root
      ref={ref}
      className={cn(
        'peer inline-flex h-[22px] w-[40px] shrink-0 cursor-pointer items-center rounded-full',
        'border border-[var(--color-border)] bg-[var(--color-surface-sunken)]',
        'transition-[background-color,border-color] duration-200',
        'data-[state=checked]:bg-[var(--color-accent)] data-[state=checked]:border-[var(--color-accent)]',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--color-bg)]',
        'disabled:opacity-50 disabled:cursor-not-allowed',
        className,
      )}
      {...rest}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          'pointer-events-none block size-[16px] rounded-full bg-white',
          'shadow-[0_1px_2px_rgba(0,0,0,0.18)]',
          'transition-transform duration-200 ease-[cubic-bezier(0.2,0.8,0.2,1)]',
          'translate-x-[3px] data-[state=checked]:translate-x-[20px]',
        )}
      />
    </SwitchPrimitive.Root>
  )
})
