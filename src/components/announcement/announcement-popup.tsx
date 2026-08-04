/**
 * AnnouncementPopup — the global notice (§ announcement) shown to users on load.
 *
 * Config comes from GET /api/announcement. An image makes it an image
 * announcement (image left, text right); without one it's a clean text card with
 * a thin accent rule. A configured plain-text title is shown above the sanitized
 * HTML body; legacy announcements keep an a11y-only title. Dismissal is remembered
 * per-version (the notice's updated_at) in localStorage when remember_dismiss is
 * set; otherwise it re-appears every visit.
 *
 * Held back until the mandatory flows (set-password, onboarding wizard) are done
 * so dialogs never stack.
 */
import { useEffect, useRef, useState } from 'react'
import { authApi } from '@/api'
import { useAuth } from '@/store/auth'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { sanitizeHtml } from '@/lib/markdown'
import { cn } from '@/lib/utils'

interface AnnouncementData {
  title: string
  body: string
  image_url: string
  remember_dismiss: boolean
  updated_at: number
}

const DISMISS_KEY = 'aivory.announcement.dismissed'

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
        if (!a.title?.trim() && !a.body?.trim() && !a.image_url?.trim()) return
        if (a.remember_dismiss) {
          const dismissed = localStorage.getItem(DISMISS_KEY)
          if (dismissed && Number(dismissed) === a.updated_at) return
        }
        setData({
          title: a.title ?? '',
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
  const title = data.title.trim()

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) close() }}>
      <DialogContent size={hasImage ? 'xl' : 'md'} className="overflow-hidden p-0">
        {!title ? (
          /* Legacy announcements without a configured title retain an a11y-only heading. */
          <DialogTitle className="sr-only">Announcement</DialogTitle>
        ) : null}
        <div className="flex flex-col sm:flex-row max-h-[85vh]">
          {hasImage ? (
            <div className="sm:w-[42%] shrink-0 bg-[var(--color-bg-muted)]">
              <img src={data.image_url} alt="" className="h-48 w-full object-cover sm:h-full" draggable={false} />
            </div>
          ) : null}
          <div className="flex-1 min-w-0">
            <div className="flex-1 overflow-y-auto px-7 py-7">
              {title ? <DialogTitle className="break-words pr-7">{title}</DialogTitle> : null}
              {data.body.trim() ? (
                <div
                  className={cn(
                    'text-[14.5px] leading-relaxed text-[var(--color-fg)]',
                    title && 'mt-3',
                    // Themed styling for the admin's HTML body.
                    'prose-announcement break-words',
                    '[&_h1]:font-serif [&_h1]:text-xl [&_h1]:tracking-tight [&_h1]:mb-2',
                    '[&_h2]:font-serif [&_h2]:text-lg [&_h2]:tracking-tight [&_h2]:mt-4 [&_h2]:mb-1.5',
                    '[&_p]:my-2 [&_a]:text-[var(--color-accent)] [&_a]:underline [&_a]:underline-offset-2',
                    '[&_strong]:font-semibold [&_em]:italic',
                    '[&_ul]:list-disc [&_ul]:pl-5 [&_ul]:my-2 [&_ol]:list-decimal [&_ol]:pl-5 [&_ol]:my-2 [&_li]:my-1',
                    '[&_img]:rounded-[10px] [&_img]:my-2 [&_hr]:my-4 [&_hr]:border-[var(--color-divider)]',
                    '[&_code]:font-mono [&_code]:text-[13px] [&_code]:bg-[var(--color-bg-muted)] [&_code]:px-1 [&_code]:py-0.5 [&_code]:rounded-[5px]',
                  )}
                  dangerouslySetInnerHTML={{ __html: sanitizeHtml(data.body) }}
                />
              ) : null}
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
