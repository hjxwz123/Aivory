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
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next && !closeLocked) close() }}>
      <DialogContent size={data.image_url.trim() ? 'xl' : 'md'} aria-describedby={undefined} closeDisabled={closeLocked} onEscapeKeyDown={(event) => { if (closeLocked) event.preventDefault() }} onInteractOutside={(event) => { if (closeLocked) event.preventDefault() }} className="max-h-[min(88dvh,42rem)] overflow-hidden p-0">
        <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
          {data.image_url.trim() ? <div className="shrink-0 bg-[var(--color-bg-muted)] sm:w-[42%]"><img src={data.image_url} alt="" className="h-40 w-full object-cover sm:h-full" draggable={false} /></div> : null}
          <div className="flex min-h-0 min-w-0 flex-1 flex-col"><DialogHeader className={cn(!title && 'sr-only')}><DialogTitle className="break-words pr-10">{title || t('announcement.title', { defaultValue: 'Announcement' })}</DialogTitle></DialogHeader><DialogBody className="min-w-0 overflow-x-hidden overflow-y-auto"><div className="prose-announcement break-words text-[14.5px] leading-relaxed text-[var(--color-fg)] [&_a]:text-[var(--color-accent)] [&_a]:underline [&_h1]:font-serif [&_h1]:text-xl [&_h2]:font-serif [&_h2]:text-lg [&_li]:my-1 [&_ol]:list-decimal [&_ol]:pl-5 [&_p]:my-2 [&_strong]:font-semibold [&_ul]:list-disc [&_ul]:pl-5" dangerouslySetInnerHTML={{ __html: sanitizeHtml(data.body) }} /></DialogBody><DialogFooter className="max-sm:flex-col max-sm:items-stretch">{closeLocked ? <span className="flex min-h-8 items-center gap-1.5 text-[12.5px] text-[var(--color-fg-muted)] sm:mr-auto" role="status"><Clock3 size={14} aria-hidden />{t('announcement.requiredCountdown', { count: secondsRemaining })}</span> : null}<Button variant={data.remember_dismiss ? 'secondary' : 'primary'} disabled={closeLocked} onClick={close}>{t('actions.close')}</Button>{data.remember_dismiss ? <Button disabled={closeLocked} onClick={dismissVersion}>{t('announcement.dontShowAgain', { defaultValue: "Don't show this again" })}</Button> : null}</DialogFooter></div>
        </div>
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
