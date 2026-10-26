import * as SelectPrimitive from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import { createContext, forwardRef, useCallback, useContext, useEffect, useMemo, useRef, useState, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useQuietSurface } from '@/contexts/quiet-surface'
import { useMobileLayout } from '@/hooks/use-mobile-layout'
import { useAppViewport } from '@/hooks/use-app-viewport'
import { MobileSelectPanel } from './mobile-select-panel'

const selectContext = createContext({ open: false, setOpen: (_open: boolean) => {}, label: '', setLabel: (_label: string) => {} })

export function Select({ open: controlledOpen, defaultOpen = false, onOpenChange, children, ...props }: ComponentPropsWithoutRef<typeof SelectPrimitive.Root>) {
  const [uncontrolledOpen, setUncontrolledOpen] = useState(defaultOpen)
  const [label, setLabel] = useState('')
  const open = controlledOpen ?? uncontrolledOpen
  const setOpen = useCallback((next: boolean) => {
    if (controlledOpen === undefined) setUncontrolledOpen(next)
    onOpenChange?.(next)
  }, [controlledOpen, onOpenChange])
  const context = useMemo(() => ({ open, setOpen, label, setLabel }), [open, setOpen, label])
  return <selectContext.Provider value={context}>
    <SelectPrimitive.Root {...props} open={open} onOpenChange={setOpen}>{children}</SelectPrimitive.Root>
  </selectContext.Provider>
}
export const SelectGroup = SelectPrimitive.Group
export const SelectValue = SelectPrimitive.Value

export const SelectTrigger = forwardRef<
  ElementRef<typeof SelectPrimitive.Trigger>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Trigger> & { hideChevron?: boolean }
>(function SelectTrigger({ className, children, hideChevron = false, ...rest }, ref) {
  const quiet = useQuietSurface()
  const { setLabel } = useContext(selectContext)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const ariaLabel = rest['aria-label']
  const labelledBy = rest['aria-labelledby']
  useEffect(() => {
    const node = triggerRef.current
    const labelledByText = labelledBy?.split(/\s+/).map((id) => document.getElementById(id)?.textContent).filter(Boolean).join(' ')
    setLabel(ariaLabel || labelledByText || node?.labels?.[0]?.textContent || '')
  }, [ariaLabel, labelledBy, setLabel])
  return (
    <SelectPrimitive.Trigger
      ref={(node) => { triggerRef.current = node; if (typeof ref === 'function') ref(node); else if (ref) ref.current = node }}
      data-slot="select-trigger"
      className={cn(
        'inline-flex items-center justify-between gap-2 h-10 px-3.5 rounded-[10px]',
        'bg-[var(--color-surface-sunken)] border border-[var(--color-border)]',
        'text-sm text-[var(--color-fg)]',
        'transition-[border-color,background-color] duration-150',
        // Keep the same quiet focus treatment as text inputs: the border
        // carries the state, without an outer glow around the control.
        'focus:outline-none focus:border-[var(--color-border-strong)] focus:bg-[var(--color-surface)]',
        'data-[placeholder]:text-[var(--color-fg-faint)]',
        quiet && 'border-transparent bg-[var(--quiet-field-bg,var(--color-bg-muted))] data-[placeholder]:text-[var(--color-fg-muted)] focus:border-[var(--color-ring)] focus:bg-[var(--quiet-field-focus,var(--color-surface))]',
        'data-[disabled]:opacity-50 data-[disabled]:cursor-not-allowed',
        'w-full min-w-0 max-w-full [&>span]:min-w-0 [&>span]:truncate [&>span]:text-left',
        className,
      )}
      {...rest}
    >
      {children}
      {hideChevron ? null : (
        <SelectPrimitive.Icon asChild>
          <ChevronDown size={14} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden />
        </SelectPrimitive.Icon>
      )}
    </SelectPrimitive.Trigger>
  )
})

export const SelectContent = forwardRef<
  ElementRef<typeof SelectPrimitive.Content>,
  ComponentPropsWithoutRef<typeof SelectPrimitive.Content>
>(function SelectContent({ className, children, position = 'popper', ...rest }, ref) {
  const quiet = useQuietSurface()
  const mobile = useMobileLayout()
  const { t } = useTranslation('common')
  const { label } = useContext(selectContext)
  useAppViewport()
  if (mobile) return <MobileSelectPanel {...rest} ref={ref} label={rest['aria-label'] || label || t('aria.selectOption', { defaultValue: 'Select option' })} className={className}>{children}</MobileSelectPanel>
  return (
    <SelectPrimitive.Portal>
      <SelectPrimitive.Content
        ref={ref}
        position={position}
        sideOffset={6}
        className={cn(
          'z-[70] min-w-[min(var(--radix-select-trigger-width),calc(var(--surface-viewport-width)-1rem))] max-w-[min(var(--radix-select-content-available-width),calc(var(--surface-viewport-width)-1rem))] overflow-hidden',
          // Radix locks page scroll while the listbox is open, so a long model
          // list MUST scroll inside the dropdown — otherwise items below the
          // fold are unreachable and the page feels frozen. Cap to the space
          // Radix measured (popper) and let the viewport scroll.
          'max-h-[min(22rem,var(--radix-select-content-available-height))]',
          'rounded-popup bg-[var(--color-surface-raised)] border border-[var(--color-border)]',
          'shadow-[var(--shadow-popover)] p-1',
          'popup-motion',
          quiet && 'border-0 shadow-[var(--shadow-popover-quiet)]',
          className,
        )}
        {...rest}
      >
        <SelectPrimitive.Viewport className="p-1 max-h-[min(22rem,var(--radix-select-content-available-height))] overflow-y-auto scrollbar-thin">
          {children}
        </SelectPrimitive.Viewport>
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
        'relative flex min-w-0 items-center cursor-pointer select-none whitespace-normal [overflow-wrap:anywhere]',
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
