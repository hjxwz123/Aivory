import * as DialogPrimitive from '@radix-ui/react-dialog'
import { X } from 'lucide-react'
import { forwardRef, useEffect, useLayoutEffect, useRef, type ComponentPropsWithoutRef, type ElementRef, type HTMLAttributes } from 'react'
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

interface DialogDimensions {
  width: number
  height: number
}

function readDialogDimensions(node: HTMLElement): DialogDimensions {
  // offsetWidth/offsetHeight describe the layout box and are not distorted by
  // the transform used for the resize transition itself.
  return { width: node.offsetWidth, height: node.offsetHeight }
}

function dialogDimensionsChanged(before: DialogDimensions, next: DialogDimensions): boolean {
  return Math.abs(before.width - next.width) > 0.5 || Math.abs(before.height - next.height) > 0.5
}

function animateDialogResize(
  node: HTMLElement,
  before: DialogDimensions,
  next: DialogDimensions,
  reducedMotion: MediaQueryList,
): Animation | null {
  if (!dialogDimensionsChanged(before, next) || reducedMotion.matches || before.width <= 0 || before.height <= 0 || next.width <= 0 || next.height <= 0) return null
  return node.animate(
    [
      { transform: `scale(${before.width / next.width}, ${before.height / next.height})` },
      { transform: 'scale(1)' },
    ],
    { duration: 220, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' },
  )
}

export const DialogContent = forwardRef<
  ElementRef<typeof DialogPrimitive.Content>,
  DialogContentProps
>(function DialogContent({ className, size = 'md', showClose = true, closeDisabled = false, captureIgnore = false, children, ...rest }, ref) {
  const { t } = useTranslation('common')
  const contentRef = useRef<ElementRef<typeof DialogPrimitive.Content>>(null)
  const previousDimensionsRef = useRef<DialogDimensions | null>(null)
  const reducedMotionRef = useRef<MediaQueryList | null>(null)
  const resizeAnimationRef = useRef<Animation | null>(null)

  function transitionFromPreviousSize(node: HTMLElement, next: DialogDimensions) {
    let before = previousDimensionsRef.current
    previousDimensionsRef.current = next
    // ResizeObserver also runs after React's layout effects. Ignore its
    // duplicate same-size notification so it cannot cancel the tab animation
    // that was just started above.
    if (!before || !dialogDimensionsChanged(before, next)) return
    const runningAnimation = resizeAnimationRef.current
    if (runningAnimation) {
      // Continue from the currently painted dimensions when tabs or async
      // content resize repeatedly before the previous transition has ended.
      // This avoids snapping back to the previous transition's start size.
      const currentRect = node.getBoundingClientRect()
      before = { width: currentRect.width, height: currentRect.height }
      runningAnimation.cancel()
    }
    const animation = animateDialogResize(
      node,
      before,
      next,
      reducedMotionRef.current ?? window.matchMedia('(prefers-reduced-motion: reduce)'),
    )
    resizeAnimationRef.current = animation
    if (animation) {
      const clear = () => {
        if (resizeAnimationRef.current === animation) resizeAnimationRef.current = null
      }
      animation.addEventListener('finish', clear, { once: true })
      animation.addEventListener('cancel', clear, { once: true })
    }
  }

  // Capture React-driven layout changes before the browser paints. This is
  // important for tabs: the old geometry exists in the previous commit, while
  // ResizeObserver alone only reports after the new geometry is already live.
  useLayoutEffect(() => {
    const node = contentRef.current
    if (!node) return
    reducedMotionRef.current ??= window.matchMedia('(prefers-reduced-motion: reduce)')
    transitionFromPreviousSize(node, readDialogDimensions(node))
  })

  // Async content (validation text, loading results, images) can resize a
  // dialog without a React render in this component. Keep the observer as a
  // second path for those changes, while the layout effect handles tab swaps.
  useLayoutEffect(() => {
    const node = contentRef.current
    if (!node || typeof ResizeObserver === 'undefined') return
    reducedMotionRef.current ??= window.matchMedia('(prefers-reduced-motion: reduce)')
    const observer = new ResizeObserver(() => {
      transitionFromPreviousSize(node, readDialogDimensions(node))
    })
    observer.observe(node)
    return () => {
      observer.disconnect()
      resizeAnimationRef.current?.cancel()
      resizeAnimationRef.current = null
    }
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
          // Positioning uses the independent `translate` property. Entrance
          // and resize animations can then scale the surface without ever
          // replacing the transform responsible for keeping it centered.
          'fixed left-1/2 top-1/2 z-[60] [translate:-50%_-50%] w-[min(96vw,calc(100vw-2rem))]',
          sizeMap[size],
          // Never exceed the viewport: cap height and let the body scroll while
          // the header/footer stay pinned (see DialogBody/DialogHeader/Footer).
          'flex min-w-0 flex-col max-h-[calc(100dvh-2rem)] overflow-x-hidden',
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
  const bodyRef = useRef<HTMLDivElement>(null)
  const contentAnimationRef = useRef<Animation | null>(null)

  // A loading state can be replaced by real content without changing the
  // dialog's outer dimensions (for example, when both fit the same scroll
  // region). ResizeObserver cannot see that replacement, so fade the body in
  // once after a structural update. RAF coalescing prevents a burst of async
  // updates from repeatedly restarting the animation.
  useEffect(() => {
    const node = bodyRef.current
    if (!node || typeof MutationObserver === 'undefined' || typeof requestAnimationFrame === 'undefined') return
    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
    let frame = 0

    const animateContentUpdate = () => {
      frame = 0
      if (reducedMotion.matches) {
        node.style.removeProperty('opacity')
        return
      }
      const current = contentAnimationRef.current
      if (current?.playState === 'running') return
      const animation = node.animate(
        [{ opacity: 0.86 }, { opacity: 1 }],
        { duration: 160, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' },
      )
      contentAnimationRef.current = animation
      const clear = () => {
        if (contentAnimationRef.current === animation) contentAnimationRef.current = null
        node.style.removeProperty('opacity')
      }
      animation.addEventListener('finish', clear, { once: true })
      animation.addEventListener('cancel', clear, { once: true })
    }

    const observer = new MutationObserver((records) => {
      if (!records.some((record) => record.type === 'childList')) return
      if (reducedMotion.matches || contentAnimationRef.current?.playState === 'running') return
      if (frame === 0) {
        // MutationObserver runs before the next paint. Set the first frame
        // immediately so the replacement cannot flash at full opacity before
        // the RAF-driven animation starts.
        node.style.opacity = '0.86'
        frame = requestAnimationFrame(animateContentUpdate)
      }
    })
    observer.observe(node, { childList: true, subtree: true })
    return () => {
      observer.disconnect()
      if (frame !== 0) cancelAnimationFrame(frame)
      contentAnimationRef.current?.cancel()
      contentAnimationRef.current = null
      node.style.removeProperty('opacity')
    }
  }, [])

  return <div ref={bodyRef} className={cn('min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto px-6 pb-4', className)} {...rest} />
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
