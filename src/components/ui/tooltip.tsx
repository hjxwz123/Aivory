import * as TooltipPrimitive from '@radix-ui/react-tooltip'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export const TooltipProvider = TooltipPrimitive.Provider

interface TooltipProps {
  content: ReactNode
  children: ReactNode
  side?: 'top' | 'right' | 'bottom' | 'left'
  align?: 'start' | 'center' | 'end'
  delayDuration?: number
  shortcut?: string
}

export function Tooltip({ content, children, side = 'top', align = 'center', delayDuration = 280, shortcut }: TooltipProps) {
  // No content → no popup. Callers pass '' to disable (e.g. the sidebar rows
  // only tooltip while collapsed); without the guard an empty pill + arrow
  // pops up after the hover delay. The Root stays mounted either way so the
  // trigger children never remount (and lose focus) when content toggles.
  return (
    <TooltipPrimitive.Root delayDuration={delayDuration}>
      <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
      {content ? (
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            side={side}
            align={align}
            sideOffset={6}
            className={cn(
              'z-[90] inline-flex items-center gap-1.5',
              'px-2.5 py-1.5 rounded-[8px]',
              'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]',
              'text-xs font-medium',
              'shadow-[var(--shadow-md)]',
              'data-[state=delayed-open]:animate-[slide-down_140ms_var(--ease-out)]',
            )}
          >
            {content}
            {shortcut ? (
              <span className="ml-1 text-[10px] tracking-wide opacity-60 font-mono">{shortcut}</span>
            ) : null}
            <TooltipPrimitive.Arrow className="fill-[var(--color-fg)]" width={8} height={4} />
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      ) : null}
    </TooltipPrimitive.Root>
  )
}

/** Lower-level wrapper for cases where the simple `Tooltip` doesn't fit. */
export const TooltipRoot = TooltipPrimitive.Root
export const TooltipTrigger = TooltipPrimitive.Trigger
export const TooltipPortal = TooltipPrimitive.Portal
export const TooltipContent = forwardRef<
  ElementRef<typeof TooltipPrimitive.Content>,
  ComponentPropsWithoutRef<typeof TooltipPrimitive.Content>
>(function TooltipContent({ className, sideOffset = 6, ...rest }, ref) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        ref={ref}
        sideOffset={sideOffset}
        className={cn(
          'z-[90] px-2.5 py-1.5 rounded-[8px] bg-[var(--color-fg)] text-[var(--color-fg-inverted)] text-xs font-medium shadow-[var(--shadow-md)]',
          className,
        )}
        {...rest}
      />
    </TooltipPrimitive.Portal>
  )
})
