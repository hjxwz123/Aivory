import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, Bell } from 'lucide-react'
import { notificationsApi, type SiteNotification } from '@/api/notifications'
import { useAuth } from '@/store/auth'
import { useNotificationCenter } from '@/store/notification-center'
import { acquireStartupDialog } from '@/lib/startup-dialog-queue'
import { sanitizeHtml } from '@/lib/markdown'
import { cn } from '@/lib/utils'
import { Dialog, DialogBody, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { PanelFallback } from '@/components/ui/panel-fallback'

export function NotificationCenter() {
  const user = useAuth((state) => state.user)
  const status = useAuth((state) => state.status)
  const eligible = status === 'authenticated' && user && user.has_password !== false && Boolean(user.settings?.onboarded)
  return eligible ? <UserNotificationCenter key={user.id} /> : null
}

function UserNotificationCenter() {
  const { t, i18n } = useTranslation('common')
  const request = useNotificationCenter((state) => state.request)
  const [open, setOpen] = useState(false)
  const [items, setItems] = useState<SiteNotification[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const [selected, setSelected] = useState<SiteNotification | null>(null)
  const [detail, setDetail] = useState<SiteNotification | null>(null)
  const [detailError, setDetailError] = useState(false)
  const [saving, setSaving] = useState(false)
  const [dismissError, setDismissError] = useState(false)
  const active = useRef(true)
  const openRef = useRef(false)
  const itemsRef = useRef<SiteNotification[]>([])
  const releaseRef = useRef<(() => void) | null>(null)
  const queued = useRef(false)
  const seen = useRef(new Set<string>())
  const lastRequest = useRef(request)
  const listSeq = useRef(0)
  const detailSeq = useRef(0)
  const busy = useRef(false)

  const select = useCallback((item: SiteNotification) => {
    setSelected(item)
    setDetail(null)
    setDetailError(false)
    const seq = ++detailSeq.current
    void notificationsApi.get(item.id).then(async (data) => {
      if (!active.current || seq !== detailSeq.current) return
      setDetail(data)
      // Only the content actually displayed is marked as read.
      await notificationsApi.read(data.id, data.version)
      if (!active.current || seq !== detailSeq.current) return
      setItems((current) => current.map((entry) => entry.id === data.id ? { ...entry, version: data.version, unread: false } : entry))
    }).catch(() => {
      if (active.current && seq === detailSeq.current) setDetailError(true)
    })
  }, [])

  const show = useCallback((manual: boolean, next: SiteNotification[]) => {
    const pending = next.filter((item) => item.should_popup && !seen.current.has(item.version))
    if (!manual && !pending.length) return
    if (openRef.current) {
      pending.forEach((item) => seen.current.add(item.version))
      return
    }
    if (queued.current) return
    queued.current = true
    void acquireStartupDialog().then((release) => {
      queued.current = false
      if (!active.current) { release(); return }
      releaseRef.current = release
      const current = itemsRef.current
      current.filter((item) => item.should_popup).forEach((item) => seen.current.add(item.version))
      openRef.current = true
      setDismissError(false)
      setOpen(true)
      const first = current.find((item) => item.unread) ?? current[0]
      if (first) select(first)
      else { setSelected(null); setDetail(null) }
    })
  }, [select])

  const refresh = useCallback(async (manual = false) => {
    const seq = ++listSeq.current
    setLoading(true)
    setError(false)
    try {
      const page = await notificationsApi.list()
      if (!active.current || seq !== listSeq.current) return
      itemsRef.current = page.notifications
      setItems(page.notifications)
      setTotal(page.total)
      show(manual, page.notifications)
    } catch {
      if (!active.current || seq !== listSeq.current) return
      setError(true)
      if (manual) show(true, itemsRef.current)
    } finally {
      if (active.current && seq === listSeq.current) setLoading(false)
    }
  }, [show])

  useEffect(() => {
    active.current = true
    void refresh()
    const focus = () => { if (!document.hidden) void refresh() }
    window.addEventListener('focus', focus)
    return () => {
      active.current = false
      // These counters invalidate async work, rather than refer to DOM nodes.
      // eslint-disable-next-line react-hooks/exhaustive-deps
      listSeq.current++
      // eslint-disable-next-line react-hooks/exhaustive-deps
      detailSeq.current++
      releaseRef.current?.()
      releaseRef.current = null
      window.removeEventListener('focus', focus)
    }
  }, [refresh])

  useEffect(() => {
    if (request === lastRequest.current) return
    lastRequest.current = request
    void refresh(true)
  }, [request, refresh])

  async function loadMore() {
    const seq = ++listSeq.current
    setLoading(true)
    try {
      const page = await notificationsApi.list(items.length)
      if (!active.current || seq !== listSeq.current) return
      const existing = new Set(items.map((item) => item.id))
      const next = [...items, ...page.notifications.filter((item) => !existing.has(item.id))]
      itemsRef.current = next
      setItems(next)
      setTotal(page.total)
    } catch { if (active.current) setError(true) }
    finally { if (active.current && seq === listSeq.current) setLoading(false) }
  }

  async function close() {
    if (busy.current) return
    busy.current = true
    setSaving(true)
    setDismissError(false)
    try {
      // Every dismissal path suppresses this publication, without marking
      // unviewed notifications as read. New publication versions still prompt.
      await Promise.all(itemsRef.current.filter((item) => item.should_popup).map((item) => notificationsApi.read(item.id, item.version, true, false)))
    } catch {
      if (active.current) { setDismissError(true); setSaving(false) }
      busy.current = false
      return
    }
    if (!active.current) return
    setSaving(false)
    setOpen(false)
    openRef.current = false
    detailSeq.current++
    const release = releaseRef.current
    releaseRef.current = null
    window.setTimeout(() => release?.(), 180)
    busy.current = false
  }

  const date = (value: number) => new Date(value * 1000).toLocaleDateString(i18n.language, { year: 'numeric', month: 'short', day: 'numeric' })
  return <Dialog open={open} onOpenChange={(value) => { if (!value) void close() }}>
    <DialogContent size="xl" className="border-0 p-0" aria-describedby={undefined} showClose={false} closeDisabled={saving} data-notification-center>
      <DialogHeader><DialogTitle className="flex items-center gap-2"><Bell size={17} aria-hidden />{t('notifications.title')}</DialogTitle></DialogHeader>
      <DialogBody className="flex h-[min(30rem,60dvh)] flex-none gap-5 p-0 pb-0">
        <nav aria-label={t('notifications.history')} className={cn('w-full shrink-0 overflow-y-auto px-3 pb-4 sm:w-60 sm:bg-[var(--color-bg-muted)] sm:pt-3', selected && 'hidden sm:block')}>
          {error ? <div role="alert" className="px-3 py-3 text-sm"><p>{t('notifications.failed')}</p><Button variant="ghost" size="sm" onClick={() => void refresh(true)}>{t('actions.tryAgain')}</Button></div> : null}
          {!items.length && loading ? <PanelFallback scope="fill" /> : null}
          {!items.length && !loading && !error ? <p className="px-3 py-6 text-sm text-[var(--color-fg-muted)]">{t('notifications.empty')}</p> : null}
          {items.map((item) => <button key={item.id} type="button" onClick={() => select(item)} aria-current={selected?.id === item.id ? 'true' : undefined} className={cn('mb-1 flex w-full items-start gap-2 rounded-[8px] px-3 py-3 text-left hover:bg-[var(--color-surface)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]', selected?.id === item.id && 'bg-[var(--color-surface)]')}>
            <span className="min-w-0 flex-1"><span className="block break-words text-sm font-medium">{item.title}</span><time dateTime={new Date(item.updated_at * 1000).toISOString()} className="mt-1 block text-xs text-[var(--color-fg-muted)]">{date(item.updated_at)}</time></span>
            {item.unread ? <span aria-label={t('notifications.unread')} className="mt-1.5 size-1.5 shrink-0 rounded-full bg-[var(--color-accent)]" /> : null}
          </button>)}
          {items.length < total ? <Button variant="ghost" size="sm" loading={loading} className="mt-2 w-full" onClick={() => void loadMore()}>{t('notifications.loadMore')}</Button> : null}
        </nav>
        <article className={cn('min-w-0 flex-1 overflow-y-auto px-5 pb-5 sm:pl-0 sm:pr-6', !selected && 'hidden sm:block')}>
          {selected ? <>
            <Button variant="ghost" size="sm" className="mb-3 sm:hidden" leadingIcon={<ArrowLeft size={14} aria-hidden />} onClick={() => { detailSeq.current++; setSelected(null) }}>{t('notifications.history')}</Button>
            {detailError ? <div role="alert" className="py-4 text-sm"><p>{t('notifications.unavailable')}</p><Button variant="secondary" size="sm" className="mt-3" onClick={() => select(selected)}>{t('actions.tryAgain')}</Button></div> : !detail ? <PanelFallback scope="fill" /> : <>
              <h2 className="break-words text-base font-semibold">{detail.title}</h2>
              <time className="mt-1 block text-xs text-[var(--color-fg-muted)]">{date(detail.updated_at)}</time>
              <div className="prose-announcement mt-5 max-w-[70ch] break-words text-sm leading-7 [&_a]:text-[var(--color-accent)] [&_a]:underline [&_img]:max-w-full [&_pre]:overflow-x-auto" dangerouslySetInnerHTML={{ __html: sanitizeHtml(detail.body ?? '') }} />
            </>}
          </> : <div className="flex h-full items-center justify-center text-sm text-[var(--color-fg-muted)]">{t('notifications.choose')}</div>}
        </article>
      </DialogBody>
      <DialogFooter className="flex-wrap justify-end gap-y-3 border-0">
        {dismissError ? <p role="alert" className="w-full text-xs text-[var(--color-danger)]">{t('notifications.dismissFailed')}</p> : null}
        <Button size="sm" loading={saving} title={t('notifications.dismissHint')} data-notification-dismiss onClick={() => void close()}>{t('notifications.dismiss')}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
}
