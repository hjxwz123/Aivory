/**
 * AnnouncementBar — a thin notice strip pinned to the very top of the app shell
 * (§ announcement bar), spanning the FULL width above both the sidebar and the
 * content column. Driven by the same GET /api/announcement config as the popup
 * but independent: it renders when `bar_enabled` is set, shows the admin's HTML
 * (links allowed, sanitized) centered, and is dismissible per-version (editing
 * the bar bumps `bar_updated_at` so it re-appears for everyone who closed the
 * old one). Closing collapses the bar with a short animation rather than
 * vanishing instantly. Returns null when there's no active bar.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Megaphone, X } from 'lucide-react'
import { authApi } from '@/api'
import { useAuth } from '@/store/auth'
import { sanitizeHtml } from '@/lib/markdown'
import { cn } from '@/lib/utils'

const BAR_DISMISS_KEY = 'aurelia.announcement.bar.dismissed'

export function AnnouncementBar() {
  const { t } = useTranslation('common')
  const status = useAuth((s) => s.status)
  const [data, setData] = useState<{ html: string; version: number } | null>(null)
  const [closing, setClosing] = useState(false)
  const fetchedRef = useRef(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  useEffect(() => {
    if (status !== 'authenticated' || fetchedRef.current) return
    fetchedRef.current = true
    let cancelled = false
    authApi
      .announcement()
      .then((a) => {
        if (cancelled || !a.bar_enabled || !a.bar_html?.trim()) return
        const version = a.bar_updated_at ?? 0
        if (localStorage.getItem(BAR_DISMISS_KEY) === String(version)) return
        setData({ html: a.bar_html, version })
      })
      .catch(() => {
        /* missing/blocked announcement → no bar */
      })
    return () => {
      cancelled = true
    }
  }, [status])

  // Clear a pending close timer if the component unmounts mid-animation.
  useEffect(() => () => clearTimeout(timerRef.current), [])

  if (!data) return null

  function dismiss() {
    if (closing) return
    // Persist the dismissal immediately (so a fast reload won't re-show it),
    // then collapse with the animation before unmounting. The global
    // reduced-motion rule zeroes the transition, so the brief delay is invisible.
    try {
      localStorage.setItem(BAR_DISMISS_KEY, String(data!.version))
    } catch {
      /* ignore quota / privacy-mode errors */
    }
    setClosing(true)
    timerRef.current = setTimeout(() => setData(null), 320)
  }

  return (
    // grid 0fr→1fr collapses height without measuring; opacity fades in tandem.
    <div
      className={cn(
        'grid shrink-0 w-full transition-[grid-template-rows,opacity] duration-300 ease-out',
        closing ? 'grid-rows-[0fr] opacity-0' : 'grid-rows-[1fr] opacity-100',
      )}
    >
      <div className="overflow-hidden">
        <div className="relative flex w-full items-center justify-center border-b border-[var(--color-border)] bg-[var(--color-accent-soft)] px-11 py-2 text-[var(--color-fg)]">
          {/* Centered announcement — icon + sanitized HTML, centered across the
              whole bar (not just the content column). */}
          <div className="flex min-w-0 items-center justify-center gap-2 text-center text-[13px] leading-snug [&_a]:font-medium [&_a]:text-[var(--color-accent)] [&_a]:underline [&_a]:underline-offset-2">
            <Megaphone size={14} strokeWidth={1.5} aria-hidden className="shrink-0 text-[var(--color-accent)]" />
            <span className="min-w-0 break-words" dangerouslySetInnerHTML={{ __html: sanitizeHtml(data.html) }} />
          </div>
          {/* Close pinned to the far-right edge of the full-width bar. */}
          <button
            type="button"
            onClick={dismiss}
            aria-label={t('common.close', { defaultValue: 'Close' })}
            className="absolute right-2 top-1/2 inline-flex size-7 -translate-y-1/2 items-center justify-center rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)]/50 hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <X size={14} aria-hidden />
          </button>
        </div>
      </div>
    </div>
  )
}
