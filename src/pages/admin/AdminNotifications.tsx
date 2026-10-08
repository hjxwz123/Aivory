import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, X } from 'lucide-react'
import { notificationsApi, type NotificationDraft, type SiteNotification } from '@/api/notifications'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminListToolbar } from '@/components/admin/admin-list-toolbar'
import { AdminTable } from '@/components/admin/AdminTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader } from '@/components/ui/sheet'
import { toast } from '@/hooks/use-toast'
import { sanitizeHtml } from '@/lib/markdown'

const emptyDraft: NotificationDraft = { title: '', body: '', enabled: true }

export default function AdminNotifications() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [items, setItems] = useState<SiteNotification[]>([])
  const [total, setTotal] = useState(0)
  const [search, setSearch] = useState('')
  const [request, setRequest] = useState({ search: '', page: 1, revision: 0 })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const [editor, setEditor] = useState<{ id?: string } | null>(null)
  const [draft, setDraft] = useState<NotificationDraft>(emptyDraft)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState(false)
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const editorSeq = useRef(0)
  const mutationBusy = useRef(false)
  const refresh = () => setRequest((current) => ({ ...current, revision: current.revision + 1 }))

  useEffect(() => {
    const timer = window.setTimeout(() => setRequest((current) => current.search === search.trim() ? current : { ...current, search: search.trim(), page: 1 }), 250)
    return () => window.clearTimeout(timer)
  }, [search])
  useEffect(() => {
    let current = true
    setLoading(true)
    setError(false)
    notificationsApi.adminList((request.page - 1) * 50, request.search).then((page) => {
      if (!current) return
      const last = Math.max(1, Math.ceil(page.total / 50))
      if (request.page > last) { setRequest((previous) => ({ ...previous, page: last })); return }
      setItems(page.notifications)
      setTotal(page.total)
    }).catch(() => { if (current) setError(true) }).finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [request])
  useEffect(() => () => { editorSeq.current++ }, [])

  function edit(item?: SiteNotification) {
    const seq = ++editorSeq.current
    setEditor(item ? { id: item.id } : {})
    setDraft(emptyDraft)
    setConfirmDelete(false)
    setDetailError(false)
    setDetailLoading(Boolean(item))
    if (item) void notificationsApi.adminGet(item.id).then((notification) => {
      if (seq === editorSeq.current) setDraft({ title: notification.title, body: notification.body ?? '', enabled: notification.enabled })
    }).catch(() => { if (seq === editorSeq.current) setDetailError(true) }).finally(() => { if (seq === editorSeq.current) setDetailLoading(false) })
  }
  function close() { if (!mutationBusy.current) { editorSeq.current++; setEditor(null) } }
  async function save(event: React.FormEvent) {
    event.preventDefault()
    if (mutationBusy.current || !editor) return
    mutationBusy.current = true
    setBusy(true)
    try {
      await notificationsApi.save({ ...draft, title: draft.title.trim(), body: draft.body.trim() }, editor.id)
      toast.success(t('common.saved'))
      setEditor(null)
      refresh()
    } catch { toast.error(t('common.failed')) }
    finally { mutationBusy.current = false; setBusy(false) }
  }
  async function remove() {
    if (!editor?.id || mutationBusy.current) return
    if (!confirmDelete) { setConfirmDelete(true); return }
    mutationBusy.current = true
    setBusy(true)
    try {
      await notificationsApi.delete(editor.id)
      setEditor(null)
      refresh()
    } catch { toast.error(t('common.failed')) }
    finally { mutationBusy.current = false; setBusy(false) }
  }
  const date = (value: number) => new Date(value * 1000).toLocaleString(i18n.language, { dateStyle: 'medium', timeStyle: 'short' })

  return <div>
    <AdminPageHeader title={t('notifications.title')} description={t('notifications.lead')} />
    <AdminListToolbar search={search} onSearchChange={setSearch} placeholder={t('notifications.search')} actions={<Button size="sm" leadingIcon={<Plus size={14} aria-hidden />} onClick={() => edit()}>{t('notifications.create')}</Button>} />
    {loading ? <PanelFallback /> : error ? <div role="alert" className="py-8 text-sm"><p>{t('common.failed')}</p><Button variant="secondary" size="sm" className="mt-3" onClick={refresh}>{t('common:actions.tryAgain')}</Button></div> : <>
      <AdminTable items={items} rowKey={(item) => item.id} label={t('notifications.title')} className="mt-4" emptyMessage={t('notifications.empty')} columns={[
        { id: 'title', header: t('notifications.subject'), width: 340, render: (item) => <Button variant="ghost" size="sm" className="h-auto max-w-full justify-start whitespace-normal py-1 text-left" onClick={() => edit(item)}>{item.title}</Button> },
        { id: 'id', header: 'ID', width: 180, render: (item) => <span className="font-mono text-xs text-[var(--color-fg-muted)]">{item.id}</span> },
        { id: 'status', header: t('notifications.status'), width: 100, render: (item) => <span className={item.enabled ? 'text-[var(--color-accent)]' : 'text-[var(--color-fg-muted)]'}>{t(item.enabled ? 'notifications.published' : 'notifications.draft')}</span> },
        { id: 'updated', header: t('notifications.updated'), width: 180, render: (item) => <time className="text-xs">{date(item.updated_at)}</time> },
        { id: 'actions', header: t('common.actions'), width: 100, align: 'right', render: (item) => <Button variant="ghost" size="sm" onClick={() => edit(item)}>{t('common:actions.edit')}</Button> },
      ]} />
      <Pagination page={request.page} pageCount={Math.ceil(total / 50)} onPage={(page) => setRequest((current) => ({ ...current, page }))} />
    </>}
    <Sheet open={Boolean(editor)} onOpenChange={(value) => { if (!value) close() }}>
      <SheetContent side="right" size="lg" className="w-[min(38rem,100vw)] border-0" label={t(editor?.id ? 'notifications.edit' : 'notifications.create')} onEscapeKeyDown={(event) => { if (busy) event.preventDefault() }} onInteractOutside={(event) => { if (busy) event.preventDefault() }}>
        <SheetHeader className="flex items-center justify-between gap-3"><h2 className="text-base font-semibold">{t(editor?.id ? 'notifications.edit' : 'notifications.create')}</h2><Button variant="ghost" size="icon-sm" disabled={busy} aria-label={t('common:actions.close')} onClick={close}><X size={16} aria-hidden /></Button></SheetHeader>
        <form onSubmit={(event) => void save(event)} className="flex min-h-0 flex-1 flex-col">
          <SheetBody className="min-h-0 space-y-5">
            {detailLoading ? <PanelFallback /> : detailError ? <div role="alert" className="text-sm"><p>{t('common.failed')}</p><Button variant="secondary" size="sm" className="mt-3" onClick={() => edit(items.find((item) => item.id === editor?.id))}>{t('common:actions.tryAgain')}</Button></div> : <>
              <Field label={t('notifications.subject')} htmlFor="notification-title"><Input id="notification-title" value={draft.title} maxLength={120} required disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, title: event.target.value }))} /></Field>
              <Field label={t('notifications.body')} htmlFor="notification-body" hint={t('notifications.bodyHint')}><Textarea id="notification-body" value={draft.body} rows={12} maxLength={256 * 1024} required disabled={busy} onChange={(event) => setDraft((current) => ({ ...current, body: event.target.value }))} /></Field>
              <div className="flex items-start justify-between gap-4"><label htmlFor="notification-published" className="text-sm"><span className="font-medium">{t('notifications.publish')}</span><span className="mt-1 block text-xs leading-5 text-[var(--color-fg-muted)]">{t('notifications.publishHint')}</span></label><Switch id="notification-published" checked={draft.enabled} disabled={busy} onCheckedChange={(enabled) => setDraft((current) => ({ ...current, enabled }))} /></div>
              {draft.body.trim() ? <details className="pb-3 text-sm"><summary className="cursor-pointer text-[var(--color-fg-muted)]">{t('notifications.preview')}</summary><div className="prose-announcement mt-3 break-words leading-7 [&_img]:max-w-full [&_a]:text-[var(--color-accent)] [&_a]:underline" dangerouslySetInnerHTML={{ __html: sanitizeHtml(draft.body) }} /></details> : null}
            </>}
          </SheetBody>
          <SheetFooter className="flex-wrap border-0">
            {confirmDelete ? <p role="alert" className="w-full text-xs text-[var(--color-danger)]">{t('notifications.deleteHint')}</p> : null}
            {editor?.id ? <Button type="button" variant="ghost" size="sm" className="mr-auto text-[var(--color-danger)]" disabled={busy} leadingIcon={<Trash2 size={14} aria-hidden />} onClick={() => void remove()}>{t(confirmDelete ? 'notifications.confirmDelete' : 'common:actions.delete')}</Button> : null}
            {confirmDelete ? <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={() => setConfirmDelete(false)}>{t('common:actions.cancel')}</Button> : <Button type="button" variant="ghost" size="sm" disabled={busy} onClick={close}>{t('common:actions.cancel')}</Button>}
            <Button type="submit" size="sm" loading={busy} disabled={detailLoading || detailError || !draft.title.trim() || !draft.body.trim()}>{t('common:actions.save')}</Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  </div>
}
