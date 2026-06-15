/**
 * AnnouncementPopup — the global notice (§ announcement) shown to users on load.
 *
 * Config comes from GET /api/announcement. An image makes it an image
 * announcement (image left, text right); without one it's a clean text card with
 * a thin accent rule. No title — editorial and minimal, on theme. Dismissal is
 * remembered per-version (the notice's updated_at) in localStorage when
 * remember_dismiss is set; otherwise it re-appears every visit.
 *
 * Held back until the mandatory flows (set-password, onboarding wizard) are done
 * so dialogs never stack.
 */
import { useEffect, useRef, useState } from 'react'
import { authApi } from '@/api'
import { useAuth } from '@/store/auth'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'

interface AnnouncementData {
  body: string
  image_url: string
  remember_dismiss: boolean
  updated_at: number
}

const DISMISS_KEY = 'aurelia.announcement.dismissed'

export function AnnouncementPopup() {
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const onboarded = Boolean((user?.settings as Record<string, unknown> | undefined)?.onboarded)
  // Don't compete with the set-password gate or the onboarding wizard.
  const eligible = status === 'authenticated' && Boolean(user) && user?.has_password !== false && onboarded

  const [data, setData] = useState<AnnouncementData | null>(null)
  const [open, setOpen] = useState(false)
  const fetchedRef = useRef(false)

  useEffect(() => {
    if (!eligible || fetchedRef.current) return
    fetchedRef.current = true
    let cancelled = false
    authApi
      .announcement()
      .then((a) => {
        if (cancelled || !a.enabled) return
        if (!a.body?.trim() && !a.image_url?.trim()) return
        if (a.remember_dismiss) {
          const dismissed = localStorage.getItem(DISMISS_KEY)
          if (dismissed && Number(dismissed) === a.updated_at) return
        }
        setData({
          body: a.body ?? '',
          image_url: a.image_url ?? '',
          remember_dismiss: a.remember_dismiss,
          updated_at: a.updated_at,
        })
        setOpen(true)
      })
      .catch(() => {
        /* a missing/blocked announcement just shows nothing */
      })
    return () => {
      cancelled = true
    }
  }, [eligible])

  function close() {
    // Remember the dismissal (by version) only when the admin asked us to.
    if (data?.remember_dismiss) {
      try {
        localStorage.setItem(DISMISS_KEY, String(data.updated_at))
      } catch {
        /* ignore quota / privacy-mode errors */
      }
    }
    setOpen(false)
  }

  if (!data) return null
  const hasImage = Boolean(data.image_url.trim())

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) close() }}>
      <DialogContent size={hasImage ? 'xl' : 'md'} className="overflow-hidden p-0">
        {/* a11y only — the design intentionally has no visible heading. */}
        <DialogTitle className="sr-only">Announcement</DialogTitle>
        <div className="flex flex-col sm:flex-row">
          {hasImage ? (
            <div className="sm:w-2/5 shrink-0 bg-[var(--color-bg-muted)]">
              <img src={data.image_url} alt="" className="h-44 w-full object-cover sm:h-full" draggable={false} />
            </div>
          ) : (
            <span aria-hidden className="hidden sm:block w-1 shrink-0 self-stretch bg-[var(--color-accent)]" />
          )}
          <div className="flex-1 min-w-0 px-6 py-6 sm:px-7 sm:py-7">
            <p className="whitespace-pre-wrap break-words text-[14.5px] leading-relaxed text-[var(--color-fg)]">
              {data.body}
            </p>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
