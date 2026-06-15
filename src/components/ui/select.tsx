import * as SelectPrimitive from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { cn } from '@/lib/utils'

export const Select = SelectPrimitive.Root
export const SelectGroup = SelectPrimitive.Group
export const SelectValue = SelectPrimitive.Value

export const SelectTrigger = forwardRef<
  ElementRef<typeof SelectPrimitive.Trigger>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Trigger> & { hideChevron?: boolean }
>(function SelectTrigger({ className, children, hideChevron = false, ...rest }, ref) {
  return (
    <SelectPrimitive.Trigger
      ref={ref}
      className={cn(
        'inline-flex items-center justify-between gap-2 h-10 px-3.5 rounded-[10px]',
        'bg-[var(--color-surface-sunken)] border border-[var(--color-border)]',
        'text-sm text-[var(--color-fg)]',
        'transition-[border-color,box-shadow] duration-150',
        'focus:outline-none focus:border-[var(--color-border-strong)] focus:ring-[3px] focus:ring-[var(--color-ring)]',
        'data-[placeholder]:text-[var(--color-fg-faint)]',
        'data-[disabled]:opacity-50 data-[disabled]:cursor-not-allowed',
        'w-full',
        className,
      )}
      {...rest}
    >
      {children}
      {hideChevron ? null : (
        <SelectPrimitive.Icon asChild>
          <ChevronDown size={14} className="text-[var(--color-fg-muted)]" aria-hidden />
        </SelectPrimitive.Icon>
      )}
    </SelectPrimitive.Trigger>
  )
})

export const SelectContent = forwardRef<
  ElementRef<typeof SelectPrimitive.Content>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Content>
>(function SelectContent({ className, children, position = 'popper', ...rest }, ref) {
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        ref={ref}
        position={position}
        sideOffset={6}
        className={cn(
          'z-[70] min-w-[var(--radix-select-trigger-width)] overflow-hidden',
          'rounded-[12px] bg-[var(--color-surface-raised)] border border-[var(--color-border)]',
          'shadow-[var(--shadow-popover)] p-1',
          'data-[state=open]:animate-[slide-down_180ms_var(--ease-out)]',
          'data-[state=closed]:animate-[fade-out_120ms_var(--ease-in)]',
          className,
        )}
        {...rest}
      >
        <SelectPrimitive.Viewport className="p-1">{children}</SelectPrimitive.Viewport>
      </SelectPrimitive.Content>
    </SelectPrimitive.Portal>
  )
})

export const SelectItem = forwardRef<
  ElementRef<typeof SelectPrimitive.Item>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Item>
>(function SelectItem({ className, children, ...rest }, ref) {
  return (
    <SelectPrimitive.Item
      ref={ref}
      className={cn(
        'relative flex items-center cursor-pointer select-none',
        'rounded-[8px] py-1.5 pl-7 pr-3 text-sm text-[var(--color-fg)] outline-none',
        'data-[highlighted]:bg-[var(--color-bg-muted)]',
        'data-[disabled]:opacity-50 data-[disabled]:cursor-not-allowed',
        className,
      )}
      {...rest}
    >
      <span className="absolute left-2 inline-flex items-center justify-center">
        <SelectPrimitive.ItemIndicator>
          <Check size={14} aria-hidden />
        </SelectPrimitive.ItemIndicator>
      </span>
      <SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText>
    </SelectPrimitive.Item>
  )
})

export const SelectLabel = forwardRef<
  ElementRef<typeof SelectPrimitive.Label>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Label>
>(function SelectLabel({ className, ...rest }, ref) {
  return (
    <SelectPrimitive.Label
      ref={ref}
      className={cn('px-2 py-1 text-[10px] uppercase tracking-wider text-[var(--color-fg-subtle)]', className)}
      {...rest}
    />
  )
})

export const SelectSeparator = SelectPrimitive.Separator
