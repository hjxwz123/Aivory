import { useTranslation } from 'react-i18next'

/**
 * PanelFallback — a CONTENT-AREA loading state (spinner + "loading…"), sized to
 * sit inside a page panel rather than taking the whole screen.
 *
 * Use it as the Suspense fallback around a layout's <Outlet> so navigating to a
 * not-yet-loaded lazy route keeps the surrounding shell (sidebar, tabs, header)
 * on screen and only the content area shows a loader — the clicked nav item
 * highlights instantly instead of the whole app blanking to a full-screen
 * spinner. (§ instant navigation feedback)
 */
export function PanelFallback() {
  const { t } = useTranslation('common')
  return (
    <div
      className="w-full flex flex-col items-center justify-center gap-3 py-24 text-[var(--color-fg-subtle)]"
      role="status"
      aria-live="polite"
      aria-busy="true"
      aria-atomic="true"
    >
      <span
        aria-hidden
        className="inline-block size-5 rounded-full border-2 border-[var(--color-fg-faint)] border-r-transparent animate-[spin_900ms_linear_infinite]"
      />
      <span className="text-[13px]">{t('common.loading', { defaultValue: 'Loading…' })}</span>
    </div>
  )
}
