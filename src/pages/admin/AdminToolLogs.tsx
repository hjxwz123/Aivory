import { useEffect, useMemo, useRef, useState } from 'react'
import { RotateCw, Search, SlidersHorizontal, Trash2, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiToolCallLog, ApiToolLogFilters } from '@/api/types'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminTable, type AdminTableColumn } from '@/components/admin/AdminTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetBody, SheetClose, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'

const PAGE_SIZE = 50
const STATUSES = ['success', 'error', 'partial', 'timeout', 'canceled'] as const
const EMPTY_FILTERS = { search: '', kind: 'all', status: 'all', user: '', tool: '', from: '', until: '' }
type DeleteRequest = { kind: 'one'; log: ApiToolCallLog } | { kind: 'filtered'; filters: ApiToolLogFilters; count: number }

function apiFilters(filters: typeof EMPTY_FILTERS): ApiToolLogFilters {
  return {
    search: filters.search.trim() || undefined,
    kind: filters.kind === 'all' ? undefined : filters.kind,
    status: filters.status === 'all' ? undefined : filters.status,
    user: filters.user.trim() || undefined,
    tool: filters.tool.trim() || undefined,
    from: filters.from ? new Date(filters.from).toISOString() : undefined,
    until: filters.until ? new Date(filters.until).toISOString() : undefined,
  }
}

export default function AdminToolLogs() {
  const { t, i18n } = useTranslation('admin')
  const [filters, setFilters] = useState(EMPTY_FILTERS)
  const [request, setRequest] = useState({ filters: EMPTY_FILTERS, page: 1, revision: 0 })
  const [advanced, setAdvanced] = useState(false)
  const [logs, setLogs] = useState<ApiToolCallLog[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [detail, setDetail] = useState<ApiToolCallLog | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState(false)
  const [detailRevision, setDetailRevision] = useState(0)
  const [deleteRequest, setDeleteRequest] = useState<DeleteRequest | null>(null)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const invalidRange = !!(filters.from && filters.until && new Date(filters.from) > new Date(filters.until))
  const filtersPending = JSON.stringify(filters) !== JSON.stringify(request.filters)
  const hasFilters = JSON.stringify(filters) !== JSON.stringify(EMPTY_FILTERS)
  const timeFmt = useMemo(() => new Intl.DateTimeFormat(i18n.language || undefined, { dateStyle: 'medium', timeStyle: 'medium' }), [i18n.language])
  const formatTime = (log: ApiToolCallLog) => timeFmt.format(new Date(log.created_at_ms))
  const updateFilter = (key: keyof typeof EMPTY_FILTERS, value: string) => setFilters((current) => ({ ...current, [key]: value }))
  const refresh = () => setRequest((current) => ({ ...current, revision: current.revision + 1 }))

  useEffect(() => {
    if (invalidRange) return
    const timer = window.setTimeout(() => {
      setRequest((current) => JSON.stringify(current.filters) === JSON.stringify(filters) ? current : { ...current, filters, page: 1 })
    }, 300)
    return () => window.clearTimeout(timer)
  }, [filters, invalidRange])

  useEffect(() => {
    let current = true
    setLoading(true)
    setLoadError(false)
    adminApi.toolLogs({ ...apiFilters(request.filters), page: request.page, pageSize: PAGE_SIZE }).then((result) => {
      if (!current) return
      const lastPage = Math.max(1, Math.ceil(result.total / PAGE_SIZE))
      if (request.page > lastPage) { setRequest((previous) => ({ ...previous, page: lastPage })); return }
      setLogs(result.logs)
      setTotal(result.total)
    }).catch((error: unknown) => {
      if (!current) return
      setLoadError(true)
      toast.error(error instanceof ApiError ? error.message : t('common.failed'))
    }).finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [request, t])

  useEffect(() => {
    let current = true
    setDetail(null)
    setDetailError(false)
    if (!selectedId) { setDetailLoading(false); return }
    setDetailLoading(true)
    adminApi.toolLogDetail(selectedId).then((log) => { if (current) setDetail(log) })
      .catch(() => { if (current) setDetailError(true) })
      .finally(() => { if (current) setDetailLoading(false) })
    return () => { current = false }
  }, [selectedId, detailRevision])

  function confirmFilteredDelete() {
    const scope = apiFilters(request.filters)
    const cutoff = new Date().toISOString()
    setDeleteRequest({ kind: 'filtered', count: total, filters: { ...scope, until: scope.until && scope.until < cutoff ? scope.until : cutoff } })
  }

  async function deleteLogs() {
    if (!deleteRequest || deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      let deleted = 1
      if (deleteRequest.kind === 'one') await adminApi.deleteToolLog(deleteRequest.log.id)
      else deleted = (await adminApi.deleteToolLogsFiltered(deleteRequest.filters)).deleted
      if (deleteRequest.kind === 'filtered' || selectedId === deleteRequest.log.id) setSelectedId(null)
      setDeleteRequest(null)
      toast.success(t('logs.deleted', { count: deleted }))
      refresh()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('common.failed'))
    } finally { deletingRef.current = false; setDeleting(false) }
  }

  const statusBadge = (status: ApiToolCallLog['status']) => {
    const color = status === 'success' ? 'text-[var(--color-success)]' : status === 'partial' ? 'text-[var(--color-warning)]' : status === 'canceled' ? 'text-[var(--color-fg-muted)]' : 'text-[var(--color-danger)]'
    return <span className={`inline-flex items-center gap-1.5 whitespace-nowrap text-[12px] ${color}`}><span aria-hidden className="size-1.5 shrink-0 rounded-full bg-current" />{t(`toolLogs.statuses.${status}`)}</span>
  }
  const columns: AdminTableColumn<ApiToolCallLog>[] = [
    { id: 'time', header: t('logs.table.time'), width: 195, render: (log) => <span className="whitespace-nowrap text-[12px] text-[var(--color-fg-muted)]">{formatTime(log)}</span> },
    { id: 'tool', header: t('toolLogs.tool'), width: 220, render: (log) => <Identity primary={log.remote_name || log.tool_name} secondary={log.remote_name ? log.tool_name : ''} /> },
    { id: 'kind', header: t('logs.table.type'), width: 110, render: (log) => <span className="text-[12px]">{t(`toolLogs.kinds.${log.tool_kind}`)}</span> },
    { id: 'status', header: t('toolLogs.status'), width: 115, render: (log) => statusBadge(log.status) },
    { id: 'summary', header: t('toolLogs.summary'), width: 260, render: (log) => <span className="line-clamp-2 text-[12px] text-[var(--color-fg-muted)]" title={log.summary}>{log.summary || '-'}</span> },
    { id: 'user', header: t('toolLogs.user'), width: 180, render: (log) => <Identity primary={log.user_name || log.user_id} secondary={log.user_name ? log.user_id : ''} /> },
    { id: 'model', header: t('usage.table.model'), width: 180, render: (log) => <Identity primary={log.model_label || log.model_id || '-'} /> },
    { id: 'server', header: t('toolLogs.server'), width: 170, render: (log) => <Identity primary={log.server_name || log.server_id || '-'} /> },
    { id: 'duration', header: t('logs.duration'), width: 100, render: (log) => <span className="whitespace-nowrap text-[12px] tabular-nums">{log.duration_ms} ms</span> },
    { id: 'id', header: t('logs.table.id'), width: 340, render: (log) => <span className="font-mono text-[11px]">{log.id}</span> },
    { id: 'actions', header: t('common.actions'), width: 65, align: 'right', render: (log) => <Tooltip content={t('logs.deleteRow')}><Button variant="ghost" size="icon-sm" disabled={deleting} aria-label={t('logs.deleteEntry', { id: log.id })} onClick={(event) => { event.stopPropagation(); setDeleteRequest({ kind: 'one', log }) }}><Trash2 size={14} aria-hidden /></Button></Tooltip> },
  ]

  return <div>
    <AdminPageHeader title={t('toolLogs.title')} actions={<div className="flex items-center gap-2">
      <span className="mr-1 text-[12px] tabular-nums text-[var(--color-fg-muted)]">{t('logs.total', { count: total })}</span>
      <Tooltip content={t('logs.refresh')}><Button variant="ghost" size="icon" disabled={loading} aria-label={t('logs.refresh')} onClick={refresh}><RotateCw size={16} aria-hidden /></Button></Tooltip>
      <Tooltip content={t('logs.deleteFiltered')}><Button variant="ghost" size="icon" disabled={loading || loadError || total === 0 || invalidRange || filtersPending || deleting} aria-label={t('logs.deleteFiltered')} onClick={confirmFilteredDelete}><Trash2 size={16} aria-hidden /></Button></Tooltip>
    </div>} />
    <div className="mt-5 flex flex-wrap items-center gap-2">
      <div className="relative min-w-48 flex-1 sm:max-w-[30rem]">
        <Search size={15} aria-hidden className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
        <Input value={filters.search} maxLength={512} onChange={(event) => updateFilter('search', event.target.value)} placeholder={t('toolLogs.search')} aria-label={t('toolLogs.search')} className="pl-9" />
      </div>
      <Select value={filters.kind} onValueChange={(value) => updateFilter('kind', value)}><SelectTrigger className="w-36" aria-label={t('logs.typeFilter')}><SelectValue /></SelectTrigger><SelectContent>{['all', 'builtin', 'mcp'].map((kind) => <SelectItem key={kind} value={kind}>{t(`toolLogs.kinds.${kind}`)}</SelectItem>)}</SelectContent></Select>
      <Select value={filters.status} onValueChange={(value) => updateFilter('status', value)}><SelectTrigger className="w-36" aria-label={t('logs.resultFilter')}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">{t('toolLogs.statuses.all')}</SelectItem>{STATUSES.map((status) => <SelectItem key={status} value={status}>{t(`toolLogs.statuses.${status}`)}</SelectItem>)}</SelectContent></Select>
      <Tooltip content={t('logs.moreFilters')}><Button variant={advanced ? 'secondary' : 'ghost'} size="icon" aria-label={t('logs.moreFilters')} aria-expanded={advanced} aria-controls="tool-log-filters" onClick={() => setAdvanced(!advanced)}><SlidersHorizontal size={16} aria-hidden /></Button></Tooltip>
      {hasFilters && <Tooltip content={t('logs.resetFilters')}><Button variant="ghost" size="icon" aria-label={t('logs.resetFilters')} onClick={() => setFilters(EMPTY_FILTERS)}><X size={16} aria-hidden /></Button></Tooltip>}
    </div>
    {advanced && <div id="tool-log-filters" className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
      {(['from', 'until', 'user', 'tool'] as const).map((key) => <label key={key} className="flex min-w-0 flex-col gap-1 text-[12px] text-[var(--color-fg-muted)]">{t(key === 'from' || key === 'until' ? `logs.filters.${key}` : `toolLogs.filters.${key}`)}<Input type={key === 'from' || key === 'until' ? 'datetime-local' : 'text'} maxLength={160} value={filters[key]} aria-invalid={(key === 'from' || key === 'until') && invalidRange} onChange={(event) => updateFilter(key, event.target.value)} /></label>)}
    </div>}
    {invalidRange && <p role="alert" className="mt-2 text-[12px] text-[var(--color-danger)]">{t('logs.invalidRange')}</p>}
    <section className="mt-5" aria-busy={loading}>
      {loading ? <PanelFallback /> : loadError ? <Failure onRetry={refresh} /> : <AdminTable items={logs} columns={columns} rowKey={(log) => log.id} label={t('toolLogs.title')} emptyMessage={t('logs.empty')}
        renderRow={(log, _index, cells) => <tr key={log.id} role="button" tabIndex={0} aria-label={t('logs.openDetail', { id: log.id })} onClick={() => setSelectedId(log.id)} onKeyDown={(event) => { if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); setSelectedId(log.id) } }} className="cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]">{cells}</tr>} />}
      {!loading && !loadError && total > PAGE_SIZE && <Pagination page={request.page} pageCount={Math.max(1, Math.ceil(total / PAGE_SIZE))} onPage={(page) => setRequest((current) => ({ ...current, page }))} />}
    </section>

    <Sheet open={!!selectedId} onOpenChange={(open) => !open && setSelectedId(null)}>
      <SheetContent side="right" size="lg" label={t('toolLogs.detailTitle')} className="max-w-[calc(100vw-var(--safe-left)-var(--safe-right))]">
        <SheetHeader className="flex items-start justify-between gap-4"><div className="min-w-0"><SheetTitle>{t('toolLogs.detailTitle')}</SheetTitle><p className="mt-1.5 break-all font-mono text-[12px] text-[var(--color-fg-muted)]">{selectedId}</p></div><SheetClose asChild><Button variant="ghost" size="icon" aria-label={t('common.close')}><X size={16} aria-hidden /></Button></SheetClose></SheetHeader>
        <SheetBody className="space-y-6 pb-6">
          {detailLoading ? <PanelFallback /> : detailError ? <Failure onRetry={() => setDetailRevision((value) => value + 1)} /> : detail && detail.id === selectedId && <>
            <div className="flex flex-wrap items-center gap-3">{statusBadge(detail.status)}<span className="break-all text-sm font-medium">{detail.remote_name || detail.tool_name}</span></div>
            <dl className="grid grid-cols-1 gap-x-4 gap-y-4 text-[12px] min-[380px]:grid-cols-2">
              <DetailItem label={t('logs.table.time')} value={formatTime(detail)} />
              <DetailItem label={t('logs.duration')} value={`${detail.duration_ms} ms`} />
              <DetailItem label={t('toolLogs.callId')} value={detail.call_id} mono />
              <DetailItem label={t('toolLogs.tool')} value={detail.tool_name} mono />
              <DetailItem label={t('toolLogs.user')} value={detail.user_name} />
              <DetailItem label={t('toolLogs.filters.user')} value={detail.user_id} mono />
              <DetailItem label={t('usage.table.model')} value={detail.model_label || detail.model_id} />
              <DetailItem label={t('toolLogs.modelId')} value={detail.model_id} mono />
              <DetailItem label={t('toolLogs.conversationId')} value={detail.conversation_id} mono />
              <DetailItem label={t('toolLogs.messageId')} value={detail.message_id} mono />
              <DetailItem label={t('logs.detail.workspaceId')} value={detail.workspace_id} mono />
              <DetailItem label={t('toolLogs.server')} value={detail.server_name} />
              {detail.server_id && <DetailItem label={t('toolLogs.serverId')} value={detail.server_id} mono />}
            </dl>
            {detail.error && <Evidence label={t('toolLogs.error')} value={detail.error} danger />}
            {!!detail.issues?.length && <section className="space-y-3"><h3 className="text-sm font-medium">{t('toolLogs.issues')}</h3>{detail.issues.map((issue, index) => <Evidence key={index} label={issue.scope || t('toolLogs.error')} value={issue.error} danger />)}</section>}
            {!detail.bodies_recorded && <p className="text-[12px] text-[var(--color-fg-muted)]">{t('toolLogs.bodiesDisabled')}</p>}
            {detail.bodies_recorded && <Evidence label={t('toolLogs.input')} value={detail.input || '{}'} truncated={detail.input_truncated} />}
            {!!detail.requests?.length && <section className="space-y-5"><h3 className="text-sm font-medium">{t('toolLogs.requests')}</h3>{detail.requests.map((http, index) => <div key={index} className="space-y-3">
              <div className="flex flex-wrap items-center gap-2 text-[12px]"><span className="font-mono font-medium">#{index + 1} {http.method}</span>{http.status_code && <span className="rounded bg-[var(--color-bg-muted)] px-2 py-1 font-mono">HTTP {http.status_code}</span>}<span className="text-[var(--color-fg-muted)]">{http.duration_ms} ms</span></div>
              <Evidence label={t('toolLogs.requestUrl')} value={http.url} />
              {http.error && <Evidence label={t('toolLogs.error')} value={http.error} danger />}
              {http.request_body && <Evidence label={t('toolLogs.requestBody')} value={http.request_body} truncated={http.request_truncated} />}
              {http.response_body && <Evidence label={t('toolLogs.responseBody')} value={http.response_body} truncated={http.response_truncated} />}
            </div>)}</section>}
            {detail.output && <Evidence label={t('toolLogs.output')} value={detail.output} truncated={detail.output_truncated} />}
            <Button variant="ghost" size="sm" leadingIcon={<Trash2 size={14} aria-hidden />} disabled={deleting} className="text-[var(--color-danger)] hover:text-[var(--color-danger)]" onClick={() => setDeleteRequest({ kind: 'one', log: detail })}>{t('logs.deleteRow')}</Button>
          </>}
        </SheetBody>
      </SheetContent>
    </Sheet>
    <Dialog open={!!deleteRequest} onOpenChange={(open) => { if (!open && !deletingRef.current) setDeleteRequest(null) }}>
      <DialogContent size="sm" closeDisabled={deleting}><DialogHeader><DialogTitle>{t('logs.deleteConfirm.title')}</DialogTitle><DialogDescription className="break-words">{deleteRequest?.kind === 'one' ? t('logs.deleteConfirm.one', { id: deleteRequest.log.id }) : t('logs.deleteConfirm.filtered', { count: deleteRequest?.count ?? 0 })}</DialogDescription></DialogHeader><DialogFooter><Button variant="ghost" disabled={deleting} onClick={() => setDeleteRequest(null)}>{t('common.cancel')}</Button><Button variant="destructive" loading={deleting} onClick={() => void deleteLogs()}>{t('common.delete')}</Button></DialogFooter></DialogContent>
    </Dialog>
  </div>
}

function Identity({ primary, secondary }: { primary: string; secondary?: string }) {
  return <div className="min-w-0"><p className="truncate text-[12px] font-medium" title={primary}>{primary}</p>{secondary && <p className="mt-1 truncate font-mono text-[10px] text-[var(--color-fg-subtle)]" title={secondary}>{secondary}</p>}</div>
}

function DetailItem({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return <div className="min-w-0"><dt className="mb-1 text-[var(--color-fg-subtle)]">{label}</dt><dd className={`break-all ${mono ? 'font-mono' : ''}`}>{value || '-'}</dd></div>
}

function Evidence({ label, value, truncated, danger }: { label: string; value: string; truncated?: boolean; danger?: boolean }) {
  const { t } = useTranslation('admin')
  let formatted = value
  try { formatted = JSON.stringify(JSON.parse(value), null, 2) } catch { /* Plain text and excerpts retain their original format. */ }
  return <section className="min-w-0"><h3 className="mb-1.5 text-[12px] font-medium text-[var(--color-fg-subtle)]">{label}</h3><pre className={`max-h-[40vh] overflow-auto whitespace-pre-wrap break-words rounded-[6px] bg-[var(--color-bg-muted)] p-3 font-mono text-[12px] leading-relaxed ${danger ? 'text-[var(--color-danger)]' : 'text-[var(--color-fg-muted)]'}`}>{formatted}</pre>{truncated && <p className="mt-1 text-[11px] text-[var(--color-fg-subtle)]">{t('toolLogs.truncated')}</p>}</section>
}

function Failure({ onRetry }: { onRetry: () => void }) {
  const { t } = useTranslation('admin')
  return <div role="alert" className="py-10 text-center text-sm text-[var(--color-fg-muted)]"><p>{t('common.failed')}</p><Button variant="ghost" size="sm" onClick={onRetry} className="mt-2"><RotateCw size={14} />{t('logs.refresh')}</Button></div>
}
