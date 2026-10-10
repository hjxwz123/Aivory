import { useTranslation } from 'react-i18next'
import { createPortal } from 'react-dom'
import { cn } from '@/lib/utils'
import { TracedLogo } from '@/components/brand/logo'

/**
 * The canonical page-loading state.
 * Full-screen startup uses the brand lockup; smaller panels keep a compact
 * indicator. Both announce the same localized state and respect reduced motion.
 */
export function PanelFallback({ scope = 'panel' }: { scope?: 'panel' | 'screen' | 'fill' }) {
  const { t } = useTranslation('common')
  const fallback = (
    <div
      className={cn(
        'w-full flex flex-col items-center justify-center gap-3 text-[var(--color-fg-subtle)]',
        scope === 'panel' && 'min-h-48 flex-1 py-24',
        scope === 'fill' && 'h-full min-h-0 flex-1',
        scope === 'screen' && 'fixed inset-0 z-[var(--z-overlay)] min-h-svh bg-[var(--color-bg)]',
      )}
      role="status"
      aria-live="polite"
      aria-busy="true"
      aria-atomic="true"
    >
      {scope === 'screen' ? (
        <div className="flex flex-col items-center gap-6 px-6">
          <TracedLogo size="lg" className="gap-3 [&>svg]:size-10 [&>.aivory-wordmark]:h-[34px]" />
          <div className="flex items-center gap-2.5 text-[var(--color-fg-muted)]">
            <span aria-hidden className="flex items-center gap-1">
              {[0, 160, 320].map((delay) => (
                <span key={delay} className="size-1 rounded-full bg-current animate-[pulse_1400ms_ease-in-out_infinite] motion-reduce:animate-none" style={{ animationDelay: `${delay}ms` }} />
              ))}
            </span>
            <span className="text-[13px]">{t('common.loading', { defaultValue: 'Loading…' })}</span>
          </div>
        </div>
      ) : (
        <>
          <span
            aria-hidden
            className="inline-block size-5 rounded-full border-2 border-[var(--color-fg-faint)] border-r-transparent animate-[spin_900ms_linear_infinite] motion-reduce:animate-none"
          />
          <span className="text-[13px]">{t('common.loading', { defaultValue: 'Loading…' })}</span>
        </>
      )}
    </div>
  )
  return scope === 'screen' && typeof document !== 'undefined'
    ? createPortal(fallback, document.body)
    : fallback
}
