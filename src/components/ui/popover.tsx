import * as PopoverPrimitive from '@radix-ui/react-popover'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { cn } from '@/lib/utils'

export const Popover = PopoverPrimitive.Root
export const PopoverTrigger = PopoverPrimitive.Trigger
export const PopoverAnchor = PopoverPrimitive.Anchor
export const PopoverClose = PopoverPrimitive.Close

export const PopoverContent = forwardRef<
  ElementRef<typeof PopoverPrimitive.Content>,
  ComponentPropsWithoutRef<typeof PopoverPrimitive.Content>
>(function PopoverContent({ className, sideOffset = 6, ...rest }, ref) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        ref={ref}
        sideOffset={sideOffset}
        className={cn(
          'z-[70] min-w-[260px] outline-none',
          'rounded-[12px] bg-[var(--color-surface-raised)] border border-[var(--color-border)]',
          'shadow-[var(--shadow-popover)] p-2',
          'data-[state=open]:animate-[slide-down_180ms_var(--ease-out)]',
          'data-[state=closed]:animate-[fade-out_120ms_var(--ease-in)]',
          className,
        )}
        {...rest}
      />
    </PopoverPrimitive.Portal>
  )
})
