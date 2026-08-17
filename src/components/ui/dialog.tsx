import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { forwardRef, useLayoutEffect, useRef, type ComponentPropsWithoutRef, type ElementRef, type HTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

export const Dialog = DialogPrimitive.Root
export const DialogTrigger = DialogPrimitive.Trigger
export const DialogClose = DialogPrimitive.Close
export const DialogPortal = DialogPrimitive.Portal

export const DialogOverlay = forwardRef<
  ElementRef<typeof DialogPrimitive.Overlay>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Overlay>
>(function DialogOverlay({ className, ...rest }, ref) {
  return (
    <DialogPrimitive.Overlay
      ref={ref}
      className={cn(
        'fixed inset-0 z-[60] bg-[var(--color-overlay)] backdrop-blur-[2px]',
        'data-[state=open]:animate-[fade-in_220ms_var(--ease-out)]',
        'data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
        className,
      )}
      {...rest}
    />
  )
})

type DialogSize = 'sm' | 'md' | 'lg' | 'xl' | 'full'

export interface DialogContentProps
  extends ComponentPropsWithoutRef<typeof DialogPrimitive.Content> {
  size?: DialogSize
  showClose?: boolean
  closeDisabled?: boolean
  /** Exclude this dialog and its overlay from page-feedback screenshots. */
  captureIgnore?: boolean
}

const sizeMap: Record<DialogSize, string> = {
  sm: 'max-w-md',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
  xl: 'max-w-4xl',
  full: 'max-w-[min(96vw,72rem)]',
}

export const DialogContent = forwardRef<
  ElementRef<typeof DialogPrimitive.Content>,
  DialogContentProps
>(function DialogContent({ className, size = 'md', showClose = true, closeDisabled = false, captureIgnore = false, children, ...rest }, ref) {
  const { t } = useTranslation('common')
  const contentRef = useRef<ElementRef<typeof DialogPrimitive.Content>>(null)

  // Dialog bodies often grow after an async result, validation error, or a
  // conditional field appears. Scale the new box from its previous dimensions
  // so the size change reads as one continuous surface instead of a hard jump.
  useLayoutEffect(() => {
    const node = contentRef.current
    if (!node || typeof ResizeObserver === 'undefined') return
    let previous = node.getBoundingClientRect()
    let observed = false
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
    const observer = new ResizeObserver(() => {
      const next = node.getBoundingClientRect()
      const changed = Math.abs(previous.width - next.width) > 0.5 || Math.abs(previous.height - next.height) > 0.5
      const before = previous
      previous = next
      if (!observed) {
        observed = true
        return
      }
      if (!changed || reducedMotion.matches || before.width <= 0 || before.height <= 0 || next.width <= 0 || next.height <= 0) return
      node.animate(
        [
          { transform: `translate(-50%, -50%) scale(${before.width / next.width}, ${before.height / next.height})` },
          { transform: 'translate(-50%, -50%) scale(1)' },
        ],
        { duration: 200, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' },
      )
    })
    observer.observe(node)
    return () => observer.disconnect()
  }, [])

  function setContentRef(node: ElementRef<typeof DialogPrimitive.Content> | null) {
    contentRef.current = node
    if (typeof ref === 'function') ref(node)
    else if (ref) ref.current = node
  }

  return (
    <DialogPortal>
      <DialogOverlay data-feedback-capture-ignore={captureIgnore ? '' : undefined} />
      <DialogPrimitive.Content
        ref={setContentRef}
        data-feedback-capture-ignore={captureIgnore ? '' : undefined}
        className={cn(
          'fixed left-1/2 top-1/2 z-[60] -translate-x-1/2 -translate-y-1/2 w-[min(96vw,calc(100vw-2rem))]',
          sizeMap[size],
          // Never exceed the viewport: cap height and let the body scroll while
          // the header/footer stay pinned (see DialogBody/DialogHeader/Footer).
          'flex flex-col max-h-[calc(100dvh-2rem)]',
          'rounded-popup bg-[var(--color-surface)] border border-[var(--color-border)]',
          'shadow-[var(--shadow-xl)]',
          'data-[state=open]:animate-[pop-in_220ms_var(--ease-out)]',
          'data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
          'focus-visible:outline-none',
          className,
        )}
        {...rest}
      >
        {showClose && (
          <DialogPrimitive.Close
            aria-label={t('aria.close')}
            disabled={closeDisabled}
            className={cn(
              'absolute right-3 top-3 inline-flex items-center justify-center size-8 rounded-[8px]',
              'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
              'transition-colors duration-150',
              'focus-visible:outline-none focus-visible:text-[var(--color-fg)] focus-visible:bg-[var(--color-bg-muted)]',
              'disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-40',
            )}
          >
            <X size={16} aria-hidden />
          </DialogPrimitive.Close>
        )}
        {children}
      </DialogPrimitive.Content>
    </DialogPortal>
  )
})

export function DialogHeader({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('shrink-0 px-6 pt-4 pb-3', className)} {...rest} />
}

export function DialogBody({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  // The scroll region: takes the slack between header/footer and the capped
  // content height, scrolling its own overflow so tall forms stay reachable.
  return <div className={cn('min-h-0 flex-1 overflow-y-auto px-6 pb-4', className)} {...rest} />
}

export function DialogFooter({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        'shrink-0 border-t border-[var(--color-divider)] flex items-center justify-end gap-1.5 px-5 py-2.5 sm:px-6',
        '[&_button]:h-8 [&_button]:px-3 [&_button]:text-sm [&_a]:h-8 [&_a]:px-3 [&_a]:text-sm',
        'max-sm:[&_button]:h-10 max-sm:[&_a]:h-10',
        className,
      )}
      {...rest}
    />
  )
}

export const DialogTitle = forwardRef<
  ElementRef<typeof DialogPrimitive.Title>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(function DialogTitle({ className, ...rest }, ref) {
  return (
    <DialogPrimitive.Title
      ref={ref}
      className={cn('text-[1rem] font-semibold leading-6 tracking-normal text-[var(--color-fg)]', className)}
      {...rest}
    />
  )
})

export const DialogDescription = forwardRef<
  ElementRef<typeof DialogPrimitive.Description>,
  ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(function DialogDescription({ className, ...rest }, ref) {
  return (
    <DialogPrimitive.Description
      ref={ref}
      className={cn('text-sm text-[var(--color-fg-muted)] mt-2 leading-relaxed', className)}
      {...rest}
    />
  )
})
