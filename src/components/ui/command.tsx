import { Command as CommandPrimitive } from 'cmdk'
import { Search } from 'lucide-react'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export const Command = forwardRef<
  ElementRef<typeof CommandPrimitive>,
  ComponentPropsWithoutRef<typeof CommandPrimitive>
>(function Command({ className, ...rest }, ref) {
  return (
    <CommandPrimitive
      ref={ref}
      className={cn(
        'flex flex-col w-full rounded-[14px] bg-[var(--color-surface-raised)] text-[var(--color-fg)]',
        'overflow-hidden',
        className,
      )}
      {...rest}
    />
  )
})

export const CommandInput = forwardRef<
  ElementRef<typeof CommandPrimitive.Input>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Input>
>(function CommandInput({ className, ...rest }, ref) {
  return (
    <div className="flex items-center gap-2.5 px-4 h-12 border-b border-[var(--color-divider)]" cmdk-input-wrapper="">
      <Search size={16} className="text-[var(--color-fg-subtle)] shrink-0" aria-hidden />
      <CommandPrimitive.Input
        ref={ref}
        className={cn(
          'flex-1 bg-transparent outline-none border-none',
          'text-[0.9375rem] text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)]',
          'disabled:cursor-not-allowed disabled:opacity-50',
          className,
        )}
        {...rest}
      />
    </div>
  )
})

export const CommandList = forwardRef<
  ElementRef<typeof CommandPrimitive.List>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.List>
>(function CommandList({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.List
      ref={ref}
      className={cn('max-h-[400px] overflow-y-auto overscroll-contain p-2', className)}
      {...rest}
    />
  )
})

export const CommandEmpty = forwardRef<
  ElementRef<typeof CommandPrimitive.Empty>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Empty>
>(function CommandEmpty({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.Empty
      ref={ref}
      className={cn('px-3 py-10 text-center text-sm text-[var(--color-fg-muted)]', className)}
      {...rest}
    />
  )
})

export const CommandGroup = forwardRef<
  ElementRef<typeof CommandPrimitive.Group>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Group>
>(function CommandGroup({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.Group
      ref={ref}
      className={cn(
        '[&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-[10px]',
        '[&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-[var(--color-fg-subtle)]',
        className,
      )}
      {...rest}
    />
  )
})

export const CommandItem = forwardRef<
  ElementRef<typeof CommandPrimitive.Item>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Item>
>(function CommandItem({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.Item
      ref={ref}
      className={cn(
        'relative flex items-center gap-2.5 cursor-pointer select-none',
        'rounded-[8px] px-2.5 py-2 text-sm text-[var(--color-fg)] outline-none',
        'data-[selected="true"]:bg-[var(--color-bg-muted)]',
        'data-[disabled="true"]:opacity-50 data-[disabled="true"]:cursor-not-allowed',
        'transition-colors duration-100',
        className,
      )}
      {...rest}
    />
  )
})

export const CommandSeparator = forwardRef<
  ElementRef<typeof CommandPrimitive.Separator>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Separator>
>(function CommandSeparator({ className, ...rest }, ref) {
  return (
    <CommandPrimitive.Separator
      ref={ref}
      className={cn('my-1 h-px bg-[var(--color-divider)]', className)}
      {...rest}
    />
  )
})

export function CommandShortcut({ children }: { children: ReactNode }) {
  return (
    <span className="ml-auto pl-3 text-[11px] tracking-wide text-[var(--color-fg-subtle)] font-mono">
      {children}
    </span>
  )
}
