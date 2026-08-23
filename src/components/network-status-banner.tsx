import { WifiOff } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useNetworkOnlineStatus } from '@/hooks/use-network-status'

/** A persistent global signal for browser-detected network loss. */
export function NetworkStatusBanner() {
  const online = useNetworkOnlineStatus()
  const { t } = useTranslation('common')

  if (online) return null

  return (
    <div
      role="alert"
      aria-atomic="true"
      className="pointer-events-none fixed inset-x-0 top-0 z-[var(--z-toast)] animate-[slide-down_var(--duration-base)_var(--ease-out)]"
    >
      <div className="border-b border-[var(--color-warning)]/35 bg-[var(--color-warning-soft)] px-4 pb-2.5 pt-[max(var(--safe-top),0.625rem)] sm:px-6">
        <div className="mx-auto flex max-w-[var(--layout-content-max-w)] items-center gap-2.5">
          <span className="grid size-7 shrink-0 place-items-center text-[var(--color-warning)]" aria-hidden>
            <WifiOff size={17} strokeWidth={2.25} />
          </span>
          <div className="min-w-0">
            <p className="text-sm font-semibold leading-snug text-[var(--color-fg)]">
              {t('network.offlineTitle', { defaultValue: 'No network connection' })}
            </p>
            <p className="mt-0.5 text-xs leading-snug text-[var(--color-fg-muted)]">
              {t('network.offlineDescription', {
                defaultValue: 'Actions that need a network connection are temporarily unavailable. Check your connection and try again.',
              })}
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}
