import * as ToastPrimitive from '@radix-ui/react-toast'
import { CheckCircle2, AlertTriangle, Info, XCircle, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useToastStore } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

const variantStyles = {
  info: {
    icon: Info,
    accent: 'text-[var(--color-info)] bg-[var(--color-info-soft)]',
  },
  success: {
    icon: CheckCircle2,
    accent: 'text-[var(--color-success)] bg-[var(--color-success-soft)]',
  },
  warning: {
    icon: AlertTriangle,
    accent: 'text-[var(--color-warning)] bg-[var(--color-warning-soft)]',
  },
  danger: {
    icon: XCircle,
    accent: 'text-[var(--color-danger)] bg-[var(--color-danger-soft)]',
  },
} as const

/**
 * Mount once at the root. Reads from the Zustand toast store.
 */
export function Toaster() {
  const toasts = useToastStore((s) => s.toasts)
  const dismiss = useToastStore((s) => s.dismiss)
  const { t } = useTranslation('common')

  return (
    // The store owns dismiss timing. We give Radix a very large finite duration
    // so its internal scheduler doesn't fight ours.
    <ToastPrimitive.Provider swipeDirection="right" duration={1000 * 60 * 60 * 24}>
      {toasts.map((toast) => {
        const { icon: Icon, accent } = variantStyles[toast.variant ?? 'info']
        return (
          <ToastPrimitive.Root
            key={toast.id}
            open={toast.open}
            duration={1000 * 60 * 60 * 24}
            onOpenChange={(o) => !o && dismiss(toast.id)}
            className={cn(
              'group/toast pointer-events-auto',
              'flex items-start gap-3 w-[min(360px,calc(100vw-2rem))]',
              'rounded-[14px] bg-[var(--color-surface-raised)] border border-[var(--color-border)]',
              'p-3.5 shadow-[var(--shadow-lg)]',
              'data-[state=open]:animate-[slide-up_var(--duration-base)_var(--ease-out)]',
              'data-[state=closed]:animate-[fade-out_var(--duration-fast)_var(--ease-in)]',
              'data-[swipe=move]:translate-x-[var(--radix-toast-swipe-move-x)]',
              'data-[swipe=cancel]:translate-x-0 data-[swipe=cancel]:transition-transform',
              'data-[swipe=end]:animate-[fade-out_var(--duration-fast)_var(--ease-in)]',
            )}
          >
            <span className={cn('inline-flex shrink-0 size-7 rounded-full items-center justify-center', accent)}>
              <Icon size={14} aria-hidden />
            </span>
            <div className="flex-1 min-w-0">
              {toast.title ? (
                <ToastPrimitive.Title className="text-sm font-medium text-[var(--color-fg)] leading-tight">
                  {toast.title}
                </ToastPrimitive.Title>
              ) : null}
              {toast.description ? (
                <ToastPrimitive.Description className="mt-1 text-xs text-[var(--color-fg-muted)] leading-relaxed">
                  {toast.description}
                </ToastPrimitive.Description>
              ) : null}
              {toast.action ? (
                <ToastPrimitive.Action
                  altText={toast.action.label}
                  onClick={toast.action.onClick}
                  className="mt-2 inline-flex items-center text-xs font-medium text-[var(--color-accent)] hover:text-[var(--color-accent-hover)]"
                >
                  {toast.action.label}
                </ToastPrimitive.Action>
              ) : null}
            </div>
            <ToastPrimitive.Close
              aria-label={t('aria.dismiss')}
              className="shrink-0 size-6 inline-flex items-center justify-center rounded-[6px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] -mr-1 -mt-1"
            >
              <X size={12} aria-hidden />
            </ToastPrimitive.Close>
          </ToastPrimitive.Root>
        )
      })}
      <ToastPrimitive.Viewport
        className={cn(
          'fixed z-[80] bottom-4 right-4 max-sm:bottom-2 max-sm:right-2 max-sm:left-2',
          'flex flex-col gap-2.5 outline-none',
        )}
      />
    </ToastPrimitive.Provider>
  )
}
