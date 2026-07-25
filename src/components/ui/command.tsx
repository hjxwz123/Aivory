import { Command as CommandPrimitive } from 'cmdk'
import { Search, X } from 'lucide-react'
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
        'flex flex-col w-full rounded-popup bg-[var(--color-surface-raised)] text-[var(--color-fg)]',
        'overflow-hidden',
        className,
      )}
      {...rest}
    />
  )
})

export const CommandInput = forwardRef<
  ElementRef<typeof CommandPrimitive.Input>,
  ComponentPropsWithoutRef<typeof CommandPrimitive.Input> & {
    /** When set, a touch-only close button is shown (the menu goes full-screen
     *  on phones, so the scrim tap that closes it on desktop isn't reachable). */
    onClose?: () => void
  }
>(function CommandInput({ className, onClose, ...rest }, ref) {
  return (
    <div className="flex items-center gap-2.5 px-4 h-12 max-sm:h-14 border-b border-[var(--color-divider)]" cmdk-input-wrapper="">
      <Search size={16} className="text-[var(--color-fg-subtle)] shrink-0" aria-hidden />
      <CommandPrimitive.Input
        ref={ref}
        className={cn(
          'flex-1 bg-transparent outline-none border-none',
          // ≥16px on phones so iOS Safari doesn't zoom on focus.
          'text-[0.9375rem] max-sm:text-[length:var(--text-input-mobile)] text-[var(--color-fg)] placeholder:text-[var(--color-fg-faint)]',
          'disabled:cursor-not-allowed disabled:opacity-50',
          className,
        )}
        {...rest}
      />
      {onClose ? (
        <button
          type="button"
          onClick={onClose}
          aria-label="Close"
          className="sm:hidden -mr-1.5 inline-flex size-9 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <X size={18} aria-hidden />
        </button>
      ) : null}
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
      className={cn('max-h-[400px] max-sm:max-h-none max-sm:flex-1 overflow-y-auto overscroll-contain p-2', className)}
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
        'rounded-[8px] px-2.5 py-2 max-sm:py-3 text-sm text-[var(--color-fg)] outline-none',
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
  // Hidden on touch — there's no keyboard to invoke the shortcut.
  return (
    <span className="ml-auto pl-3 text-[11px] tracking-wide text-[var(--color-fg-subtle)] font-mono max-sm:hidden">
      {children}
    </span>
  )
}
