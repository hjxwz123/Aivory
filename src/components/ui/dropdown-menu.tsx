import * as DropdownPrimitive from '@radix-ui/react-dropdown-menu'
import { Check, ChevronRight } from 'lucide-react'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export const DropdownMenu = DropdownPrimitive.Root
export const DropdownMenuTrigger = DropdownPrimitive.Trigger
export const DropdownMenuGroup = DropdownPrimitive.Group
export const DropdownMenuPortal = DropdownPrimitive.Portal
export const DropdownMenuSub = DropdownPrimitive.Sub
export const DropdownMenuRadioGroup = DropdownPrimitive.RadioGroup

const menuClass = cn(
  'z-[70] min-w-[200px] overflow-hidden',
  'rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface-raised)]',
  'shadow-[var(--shadow-popover)]',
  'p-1.5',
  'data-[state=open]:animate-[slide-down_180ms_var(--ease-out)]',
  'data-[state=closed]:animate-[fade-out_120ms_var(--ease-in)]',
)

export const DropdownMenuContent = forwardRef<
  ElementRef<typeof DropdownPrimitive.Content>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.Content>
>(function DropdownMenuContent({ className, sideOffset = 6, ...rest }, ref) {
  return (
    <DropdownPrimitive.Portal>
      <DropdownPrimitive.Content
        ref={ref}
        sideOffset={sideOffset}
        className={cn(menuClass, className)}
        {...rest}
      />
    </DropdownPrimitive.Portal>
  )
})

const itemClass = cn(
  'group/item relative flex items-center gap-2.5 cursor-pointer select-none',
  'rounded-[8px] px-2.5 py-1.5 text-sm text-[var(--color-fg)] outline-none',
  'data-[highlighted]:bg-[var(--color-bg-muted)] data-[highlighted]:text-[var(--color-fg)]',
  'data-[disabled]:opacity-40 data-[disabled]:cursor-not-allowed',
  'transition-colors duration-100',
)

export const DropdownMenuItem = forwardRef<
  ElementRef<typeof DropdownPrimitive.Item>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.Item> & { destructive?: boolean }
>(function DropdownMenuItem({ className, destructive, ...rest }, ref) {
  return (
    <DropdownPrimitive.Item
      ref={ref}
      className={cn(
        itemClass,
        destructive && 'text-[var(--color-danger)] data-[highlighted]:text-[var(--color-danger)] data-[highlighted]:bg-[var(--color-danger-soft)]',
        className,
      )}
      {...rest}
    />
  )
})

export const DropdownMenuSubTrigger = forwardRef<
  ElementRef<typeof DropdownPrimitive.SubTrigger>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.SubTrigger>
>(function DropdownMenuSubTrigger({ className, children, ...rest }, ref) {
  return (
    <DropdownPrimitive.SubTrigger
      ref={ref}
      className={cn(itemClass, 'data-[state=open]:bg-[var(--color-bg-muted)]', className)}
      {...rest}
    >
      {children}
      <ChevronRight size={14} aria-hidden className="ml-auto text-[var(--color-fg-subtle)]" />
    </DropdownPrimitive.SubTrigger>
  )
})

export const DropdownMenuSubContent = forwardRef<
  ElementRef<typeof DropdownPrimitive.SubContent>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.SubContent>
>(function DropdownMenuSubContent({ className, ...rest }, ref) {
  return (
    <DropdownPrimitive.Portal>
      <DropdownPrimitive.SubContent
        ref={ref}
        className={cn(menuClass, 'max-h-[min(60vh,22rem)] overflow-y-auto', className)}
        {...rest}
      />
    </DropdownPrimitive.Portal>
  )
})

export const DropdownMenuCheckboxItem = forwardRef<
  ElementRef<typeof DropdownPrimitive.CheckboxItem>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.CheckboxItem>
>(function DropdownMenuCheckboxItem({ className, children, checked, ...rest }, ref) {
  return (
    <DropdownPrimitive.CheckboxItem
      ref={ref}
      checked={checked}
      className={cn(itemClass, 'pl-7', className)}
      {...rest}
    >
      <DropdownPrimitive.ItemIndicator className="absolute left-2 inline-flex">
        <Check size={14} aria-hidden />
      </DropdownPrimitive.ItemIndicator>
      {children}
    </DropdownPrimitive.CheckboxItem>
  )
})

export const DropdownMenuRadioItem = forwardRef<
  ElementRef<typeof DropdownPrimitive.RadioItem>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.RadioItem>
>(function DropdownMenuRadioItem({ className, children, ...rest }, ref) {
  return (
    <DropdownPrimitive.RadioItem ref={ref} className={cn(itemClass, 'pl-7', className)} {...rest}>
      <DropdownPrimitive.ItemIndicator className="absolute left-2 inline-flex">
        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" />
      </DropdownPrimitive.ItemIndicator>
      {children}
    </DropdownPrimitive.RadioItem>
  )
})

export const DropdownMenuLabel = forwardRef<
  ElementRef<typeof DropdownPrimitive.Label>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.Label>
>(function DropdownMenuLabel({ className, ...rest }, ref) {
  return (
    <DropdownPrimitive.Label
      ref={ref}
      className={cn('px-2.5 py-1 text-[10px] uppercase tracking-wider text-[var(--color-fg-subtle)]', className)}
      {...rest}
    />
  )
})

export const DropdownMenuSeparator = forwardRef<
  ElementRef<typeof DropdownPrimitive.Separator>,
  ComponentPropsWithoutRef<typeof DropdownPrimitive.Separator>
>(function DropdownMenuSeparator({ className, ...rest }, ref) {
  return (
    <DropdownPrimitive.Separator
      ref={ref}
      className={cn('my-1 h-px bg-[var(--color-divider)] -mx-1', className)}
      {...rest}
    />
  )
})

export function DropdownMenuShortcut({ className, ...rest }: HTMLAttributes<HTMLSpanElement>) {
  return (
    <span
      className={cn('ml-auto pl-3 text-[11px] tracking-wide text-[var(--color-fg-subtle)] font-mono', className)}
      {...rest}
    />
  )
}
