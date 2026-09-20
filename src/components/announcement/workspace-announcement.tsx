import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Clock3, Megaphone, X } from 'lucide-react'
import { workspacesApi } from '@/api'
import { useAuth } from '@/store/auth'
import { useWorkspaces } from '@/store/workspaces'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { sanitizeHtml } from '@/lib/markdown'
import { cn } from '@/lib/utils'
import { acquireStartupDialog } from '@/lib/startup-dialog-queue'
import { Dialog, DialogBody, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'

const REQUIRED_READ_SECONDS = 5
const DIALOG_EXIT_MS = 180

export function WorkspaceAnnouncementPopup() {
  const { t } = useTranslation('common')
  const status = useAuth((s) => s.status)
  const user = useAuth((s) => s.user)
  const workspaceID = useWorkspaces((s) => s.activeId)
  const [data, setData] = useState<{ title: string; body: string; image_url: string; remember_dismiss: boolean; require_read: boolean; updated_at: number } | null>(null)
  const [open, setOpen] = useState(false)
  const [unlockAt, setUnlockAt] = useState<number | null>(null)
  const [secondsRemaining, setSecondsRemaining] = useState(0)
  const [reload, setReload] = useState(0)
  const releaseRef = useRef<(() => void) | null>(null)
  const onboarded = Boolean((user?.settings as Record<string, unknown> | undefined)?.onboarded)
  const eligible = status === 'authenticated' && Boolean(user) && user?.has_password !== false && onboarded && Boolean(workspaceID)

  useEffect(() => subscribeAccessInvalidation((event) => {
    if (event.kind === 'workspace') setReload((value) => value + 1)
  }), [])

  useEffect(() => {
    if (!eligible || !workspaceID) return
    let cancelled = false
    setData(null)
    setOpen(false)
    releaseRef.current?.()
    releaseRef.current = null
    workspacesApi.announcement(workspaceID).then((a) => {
      if (cancelled || !a.enabled || (!a.title?.trim() && !a.body?.trim() && !a.image_url?.trim())) return
      const key = `aivory.workspace.${workspaceID}.announcement.dismissed`
      if (a.remember_dismiss && localStorage.getItem(key) === String(a.updated_at)) return
      void acquireStartupDialog().then((release) => {
        if (cancelled) { release(); return }
        releaseRef.current = release
        setData({ title: a.title ?? '', body: a.body ?? '', image_url: a.image_url ?? '', remember_dismiss: a.remember_dismiss, require_read: Boolean(a.require_read), updated_at: a.updated_at })
        setSecondsRemaining(a.require_read ? REQUIRED_READ_SECONDS : 0)
        setUnlockAt(a.require_read ? Date.now() + REQUIRED_READ_SECONDS * 1000 : null)
        setOpen(true)
      })
    }).catch(() => undefined)
    return () => { cancelled = true }
  }, [eligible, workspaceID, reload])

  useEffect(() => () => { releaseRef.current?.(); releaseRef.current = null }, [])
  useEffect(() => { if (!eligible) { setOpen(false); setData(null); releaseRef.current?.(); releaseRef.current = null } }, [eligible])
  useEffect(() => {
    if (!open || unlockAt === null) return
    const timer = window.setInterval(() => {
      const remaining = Math.max(0, Math.ceil((unlockAt - Date.now()) / 1000))
      setSecondsRemaining(remaining)
      if (remaining === 0) window.clearInterval(timer)
    }, 250)
    return () => window.clearInterval(timer)
  }, [open, unlockAt])

  function close() {
    setOpen(false)
    const release = releaseRef.current
    releaseRef.current = null
    window.setTimeout(() => release?.(), DIALOG_EXIT_MS)
  }
  function dismissVersion() {
    if (data?.remember_dismiss && workspaceID) localStorage.setItem(`aivory.workspace.${workspaceID}.announcement.dismissed`, String(data.updated_at))
    close()
  }
  if (!data) return null
  const title = data.title.trim()
  const closeLocked = data.require_read && secondsRemaining > 0
  const hasImage = Boolean(data.image_url.trim())
  const hasBody = Boolean(data.body.trim())
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next && !closeLocked) close() }}>
      <DialogContent
        size={hasImage && hasBody ? 'xl' : 'md'}
        aria-describedby={undefined}
        closeDisabled={closeLocked}
        onEscapeKeyDown={(event) => { if (closeLocked) event.preventDefault() }}
        onInteractOutside={(event) => { if (closeLocked) event.preventDefault() }}
      >
        <DialogHeader>
          <DialogTitle className="break-words pr-10 [overflow-wrap:anywhere]">
            {title || t('announcement.title', { defaultValue: 'Announcement' })}
          </DialogTitle>
        </DialogHeader>
        <DialogBody className="min-w-0 overscroll-contain">
          <div className={cn('min-w-0', hasImage && hasBody && 'grid items-start gap-5 md:grid-cols-[minmax(0,0.85fr)_minmax(0,1.15fr)] md:gap-6')}>
            {hasImage ? (
              <div className="min-w-0 overflow-hidden rounded-lg bg-[var(--color-bg-muted)]">
                <img
                  src={data.image_url}
                  alt=""
                  className="mx-auto block max-h-[min(40dvh,16rem)] w-full object-contain md:max-h-[min(50dvh,24rem)]"
                  draggable={false}
                />
              </div>
            ) : null}
            {hasBody ? (
              <div
                className={cn(
                  'prose-announcement min-w-0 text-sm leading-7 text-[var(--color-fg)] [overflow-wrap:anywhere]',
                  '[&>:first-child]:mt-0 [&>:last-child]:mb-0',
                  '[&_h1]:mb-3 [&_h1]:text-xl [&_h1]:font-semibold [&_h1]:leading-snug',
                  '[&_h2]:mb-2 [&_h2]:mt-5 [&_h2]:text-base [&_h2]:font-semibold [&_h2]:leading-snug',
                  '[&_h3]:mb-2 [&_h3]:mt-4 [&_h3]:font-semibold',
                  '[&_p]:my-3 [&_a]:text-[var(--color-accent)] [&_a]:underline [&_a]:underline-offset-2',
                  '[&_strong]:font-semibold [&_em]:italic [&_li]:my-1',
                  '[&_ol]:my-3 [&_ol]:list-decimal [&_ol]:pl-5 [&_ul]:my-3 [&_ul]:list-disc [&_ul]:pl-5',
                  '[&_img]:my-3 [&_img]:h-auto [&_img]:max-w-full [&_img]:rounded-lg',
                  '[&_pre]:my-3 [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_pre]:rounded-lg [&_pre]:bg-[var(--color-bg-muted)] [&_pre]:p-3',
                  '[&_code]:font-mono [&_code]:text-[0.9em] [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto',
                  '[&_td]:border [&_td]:border-[var(--color-divider)] [&_td]:px-3 [&_td]:py-2',
                  '[&_th]:border [&_th]:border-[var(--color-divider)] [&_th]:px-3 [&_th]:py-2 [&_th]:text-left',
                  '[&_hr]:my-4 [&_hr]:border-[var(--color-divider)]',
                )}
                dangerouslySetInnerHTML={{ __html: sanitizeHtml(data.body) }}
              />
            ) : null}
          </div>
        </DialogBody>
        <DialogFooter className="flex-wrap">
          {closeLocked ? (
            <span className="flex w-full items-center gap-1.5 text-xs leading-5 text-[var(--color-fg-muted)] sm:mr-auto sm:w-auto" role="status" aria-live="polite">
              <Clock3 size={14} aria-hidden className="shrink-0" />
              {t('announcement.requiredCountdown', { count: secondsRemaining })}
            </span>
          ) : null}
          <Button variant={data.remember_dismiss ? 'secondary' : 'primary'} disabled={closeLocked} onClick={close}>
            {t('actions.close')}
          </Button>
          {data.remember_dismiss ? (
            <Button disabled={closeLocked} onClick={dismissVersion}>
              {t('announcement.dontShowAgain', { defaultValue: "Don't show this again" })}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function WorkspaceAnnouncementBar() {
  const { t } = useTranslation('common')
  const status = useAuth((s) => s.status)
  const workspaceID = useWorkspaces((s) => s.activeId)
  const [data, setData] = useState<{ html: string; version: number } | null>(null)
  const [closing, setClosing] = useState(false)
  const [reload, setReload] = useState(0)
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => subscribeAccessInvalidation((event) => { if (event.kind === 'workspace') setReload((value) => value + 1) }), [])
  useEffect(() => {
    if (status !== 'authenticated' || !workspaceID) { setData(null); return }
    let cancelled = false
    setData(null); setClosing(false)
    workspacesApi.announcement(workspaceID).then((a) => {
      if (cancelled || !a.bar_enabled || !a.bar_html?.trim()) return
      const key = `aivory.workspace.${workspaceID}.announcement.bar.dismissed`
      if (localStorage.getItem(key) === String(a.bar_updated_at ?? 0)) return
      setData({ html: a.bar_html, version: a.bar_updated_at ?? 0 })
    }).catch(() => undefined)
    return () => { cancelled = true }
  }, [status, workspaceID, reload])
  useEffect(() => () => clearTimeout(timerRef.current), [])
  if (!data) return null
  function dismiss() {
    if (closing || !workspaceID || !data) return
    const current = data
    localStorage.setItem(`aivory.workspace.${workspaceID}.announcement.bar.dismissed`, String(current.version))
    setClosing(true); timerRef.current = setTimeout(() => setData(null), 320)
  }
  return <div className={cn('grid w-full shrink-0 transition-[grid-template-rows,opacity] duration-300 ease-out', closing ? 'grid-rows-[0fr] opacity-0' : 'grid-rows-[1fr] opacity-100')}><div className="overflow-hidden"><div className="relative flex w-full items-center justify-center border-b border-[var(--color-border)] bg-[var(--color-accent-soft)] px-11 py-2 text-[var(--color-fg)]"><div className="flex min-w-0 items-center justify-center gap-2 text-center text-[13px] leading-snug [&_a]:font-medium [&_a]:text-[var(--color-accent)] [&_a]:underline"><Megaphone size={14} strokeWidth={1.5} aria-hidden className="shrink-0 text-[var(--color-accent)]" /><span className="min-w-0 break-words" dangerouslySetInnerHTML={{ __html: sanitizeHtml(data.html) }} /></div><button type="button" onClick={dismiss} aria-label={t('common.close', { defaultValue: 'Close' })} className="absolute right-2 top-1/2 inline-flex size-7 -translate-y-1/2 items-center justify-center rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)]/50 hover:text-[var(--color-fg)]"><X size={14} aria-hidden /></button></div></div></div>
}
