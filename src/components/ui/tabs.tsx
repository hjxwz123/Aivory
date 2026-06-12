import * as TabsPrimitive from '@radix-ui/react-tabs'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { cn } from '@/lib/utils'

export const Tabs = TabsPrimitive.Root

export const TabsList = forwardRef<
  ElementRef<typeof TabsPrimitive.List>,
  ComponentPropsWithoutRef<typeof TabsPrimitive.List> & { variant?: 'underline' | 'segmented' }
>(function TabsList({ className, variant = 'underline', ...rest }, ref) {
  return (
    <TabsPrimitive.List
      ref={ref}
      className={cn(
        variant === 'underline' &&
          'inline-flex h-10 items-end gap-6 border-b border-[var(--color-divider)]',
        variant === 'segmented' &&
          'inline-flex items-center gap-1 rounded-[10px] bg-[var(--color-bg-muted)] p-1 border border-[var(--color-border-subtle)]',
        className,
      )}
      {...rest}
    />
  )
})

interface TriggerProps extends ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger> {
  variant?: 'underline' | 'segmented'
}

export const TabsTrigger = forwardRef<ElementRef<typeof TabsPrimitive.Trigger>, TriggerProps>(
  function TabsTrigger({ className, variant = 'underline', ...rest }, ref) {
    return (
      <TabsPrimitive.Trigger
        ref={ref}
        className={cn(
          'inline-flex items-center gap-2 text-sm font-medium outline-none',
          'focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[6px]',
          'interactive',
          variant === 'underline' && [
            'pb-2.5 text-[var(--color-fg-muted)] border-b-2 border-transparent -mb-[1px]',
            'data-[state=active]:text-[var(--color-fg)] data-[state=active]:border-[var(--color-fg)]',
            'hover:text-[var(--color-fg)]',
          ],
          variant === 'segmented' && [
            'h-8 px-3 rounded-[8px] text-[var(--color-fg-muted)]',
            'data-[state=active]:bg-[var(--color-surface)] data-[state=active]:text-[var(--color-fg)]',
            'data-[state=active]:shadow-[var(--shadow-xs)]',
            'hover:text-[var(--color-fg)]',
          ],
          className,
        )}
        {...rest}
      />
    )
  },
)

export const TabsContent = forwardRef<
  ElementRef<typeof TabsPrimitive.Content>,
  ComponentPropsWithoutRef<typeof TabsPrimitive.Content>
>(function TabsContent({ className, ...rest }, ref) {
  return (
    <TabsPrimitive.Content
      ref={ref}
      className={cn('mt-4 outline-none', className)}
      {...rest}
    />
  )
})
