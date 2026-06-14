/**
 * ImageLightbox — full-viewport image preview opened by clicking a thumbnail
 * (user bubble attachments, assistant artifacts). Uses the same Radix Dialog
 * primitives as the rest of the app so focus/ESC/scroll-lock all behave.
 *
 * Render style: dark backdrop, centred image up to 96vw × 90vh, "object-contain"
 * so portrait & landscape both fit, with a close button in the top-right and an
 * external-link affordance to open the original. Source URL + alt are the only
 * required inputs.
 */
import { X, ExternalLink } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Dialog, DialogClose, DialogOverlay, DialogPortal } from '@/components/ui/dialog'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import { cn } from '@/lib/utils'

interface ImageLightboxProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  src: string
  alt?: string
  /** Optional download/original URL (when src is a thumbnail). Defaults to src. */
  downloadUrl?: string
}

export function ImageLightbox({ open, onOpenChange, src, alt, downloadUrl }: ImageLightboxProps) {
  const { t } = useTranslation('common')
  const href = downloadUrl ?? src
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay className="bg-[color-mix(in_oklab,var(--color-fg)_85%,transparent)] backdrop-blur-[4px]" />
        <DialogPrimitive.Content
          className={cn(
            'fixed inset-0 z-[70] grid place-items-center p-4 sm:p-8',
            'data-[state=open]:animate-[fade-in_220ms_var(--ease-out)]',
            'data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
            'focus:outline-none',
          )}
          aria-label={alt ?? t('aria.themeGroup', { defaultValue: 'Image preview' })}
        >
          <DialogPrimitive.Title className="sr-only">{alt || 'Image'}</DialogPrimitive.Title>
          <DialogPrimitive.Description className="sr-only">{alt || ''}</DialogPrimitive.Description>
          <img
            src={src}
            alt={alt ?? ''}
            className="max-w-[96vw] max-h-[90vh] object-contain rounded-[10px] shadow-[var(--shadow-lg)]"
            draggable={false}
          />
          {/* Top-right control cluster */}
          <div className="absolute top-3 right-3 sm:top-5 sm:right-5 flex items-center gap-1.5">
            <a
              href={href}
              target="_blank"
              rel="noreferrer"
              aria-label={t('actions.openInNew', { defaultValue: 'Open in new tab' })}
              className="inline-flex items-center justify-center size-9 rounded-full bg-[var(--color-surface)]/90 text-[var(--color-fg)] hover:bg-[var(--color-surface)] interactive shadow-[var(--shadow-sm)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <ExternalLink size={15} aria-hidden />
            </a>
            <DialogClose asChild>
              <button
                type="button"
                aria-label={t('actions.close')}
                className="inline-flex items-center justify-center size-9 rounded-full bg-[var(--color-surface)]/90 text-[var(--color-fg)] hover:bg-[var(--color-surface)] interactive shadow-[var(--shadow-sm)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <X size={15} aria-hidden />
              </button>
            </DialogClose>
          </div>
        </DialogPrimitive.Content>
      </DialogPortal>
    </Dialog>
  )
}
