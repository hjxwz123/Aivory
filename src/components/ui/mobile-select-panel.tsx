import * as SelectPrimitive from '@radix-ui/react-select'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { cn } from '@/lib/utils'

/** A mobile choice list with the same Radix selection and focus behavior. */
export const MobileSelectPanel = forwardRef<
  ElementRef<typeof SelectPrimitive.Content>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Content> & { label: string }
>(function MobileSelectPanel({ label, className, children, ...props }, ref) {
  return <SelectPrimitive.Portal>
    <SelectPrimitive.Content
      {...props}
      ref={ref}
      position="popper"
      data-mobile-choice=""
      aria-label={label}
      className={cn('bg-[var(--color-surface-raised)] shadow-[var(--shadow-popover)] outline-none', className)}
    >
      <div role="presentation" className="mobile-choice-header">{label}</div>
      <SelectPrimitive.Viewport className="overflow-y-auto scrollbar-thin">{children}</SelectPrimitive.Viewport>
    </SelectPrimitive.Content>
  </SelectPrimitive.Portal>
})
