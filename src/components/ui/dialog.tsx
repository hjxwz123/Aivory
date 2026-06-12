import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { forwardRef, type ComponentPropsWithoutRef, type ElementRef, type HTMLAttributes } from 'react'
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
>(function DialogContent({ className, size = 'md', showClose = true, children, ...rest }, ref) {
  const { t } = useTranslation('common')
  return (
    <DialogPortal>
      <DialogOverlay />
      <DialogPrimitive.Content
        ref={ref}
        className={cn(
          'fixed left-1/2 top-1/2 z-[60] -translate-x-1/2 -translate-y-1/2 w-[min(96vw,calc(100vw-2rem))]',
          sizeMap[size],
          'rounded-[18px] bg-[var(--color-surface)] border border-[var(--color-border)]',
          'shadow-[var(--shadow-xl)]',
          'data-[state=open]:animate-[pop-in_220ms_var(--ease-out)]',
          'data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
          'focus:outline-none',
          className,
        )}
        {...rest}
      >
        {showClose && (
          <DialogPrimitive.Close
            aria-label={t('aria.close')}
            className={cn(
              'absolute right-3 top-3 inline-flex items-center justify-center size-8 rounded-[8px]',
              'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
              'transition-colors duration-150',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
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
  return <div className={cn('px-6 pt-6 pb-3', className)} {...rest} />
}

export function DialogBody({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('px-6 pb-4', className)} {...rest} />
}

export function DialogFooter({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        'px-6 py-4 border-t border-[var(--color-divider)] flex items-center justify-end gap-2',
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
      className={cn('font-serif text-2xl tracking-tight text-[var(--color-fg)]', className)}
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
