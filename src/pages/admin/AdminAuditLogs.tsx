import { useEffect, useMemo, useRef, useState } from 'react'
import { Download, RotateCw, Search, SlidersHorizontal, Trash2, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiAdminAuditLog, ApiAuditFilters } from '@/api/types'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminTable, type AdminTableColumn } from '@/components/admin/AdminTable'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetBody, SheetClose, SheetContent, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'
import { formatRecordedClient } from '@/lib/client-device'
import { auditActionLabel, auditTargetLabel } from '@/lib/audit-labels'

const PAGE_SIZE = 50
const AUDIT_TYPES = ['authentication', 'workspace', 'users', 'models', 'channels', 'billing', 'settings', 'access', 'integrations', 'content', 'logs', 'system', 'other'] as const
const RESULTS = ['success', 'failure', 'denied', 'pending'] as const
const EMPTY_FILTERS = { search: '', type: 'all', result: 'all', actor: '', target: '', action: '', from: '', until: '' }
type DeleteRequest = { kind: 'one'; log: ApiAdminAuditLog } | { kind: 'filtered'; filters: ApiAuditFilters; count: number }

function apiFilters(filters: typeof EMPTY_FILTERS): ApiAuditFilters {
  return {
    search: filters.search.trim() || undefined,
    type: filters.type === 'all' ? undefined : filters.type,
    result: filters.result === 'all' ? undefined : filters.result,
    actor: filters.actor.trim() || undefined,
    target: filters.target.trim() || undefined,
    action: filters.action.trim() || undefined,
    from: filters.from ? new Date(filters.from).toISOString() : undefined,
    until: filters.until ? new Date(filters.until).toISOString() : undefined,
  }
}

export default function AdminAuditLogs() {
  const { t, i18n } = useTranslation('admin')
  const [filters, setFilters] = useState(EMPTY_FILTERS)
  const [request, setRequest] = useState({ filters: EMPTY_FILTERS, page: 1, revision: 0 })
  const [advanced, setAdvanced] = useState(false)
  const [logs, setLogs] = useState<ApiAdminAuditLog[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [selectedLog, setSelectedLog] = useState<ApiAdminAuditLog | null>(null)
  const [deleteRequest, setDeleteRequest] = useState<DeleteRequest | null>(null)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const invalidRange = !!(filters.from && filters.until && new Date(filters.from) > new Date(filters.until))

  useEffect(() => {
    if (invalidRange) return
    const id = window.setTimeout(() => {
      setRequest((current) => JSON.stringify(current.filters) === JSON.stringify(filters) ? current : { ...current, filters, page: 1 })
    }, 300)
    return () => window.clearTimeout(id)
  }, [filters, invalidRange])

  useEffect(() => {
    let current = true
    setLoading(true)
    setLoadError(false)
    adminApi.auditLogs({ ...apiFilters(request.filters), page: request.page, pageSize: PAGE_SIZE }).then((result) => {
      if (!current) return
      const lastPage = Math.max(1, Math.ceil(result.total / PAGE_SIZE))
      if (request.page > lastPage) {
        setRequest((previous) => ({ ...previous, page: lastPage }))
        return
      }
      setLogs(result.logs)
      setTotal(result.total)
    }).catch((error: unknown) => {
      if (!current) return
      setLoadError(true)
      toast.error(error instanceof ApiError ? error.message : t('common.failed'))
    }).finally(() => { if (current) setLoading(false) })
    return () => { current = false }
  }, [request, t])

  const timeFmt = useMemo(() => new Intl.DateTimeFormat(i18n.language || undefined, { dateStyle: 'medium', timeStyle: 'medium' }), [i18n.language])
  const formatTime = (log: ApiAdminAuditLog) => timeFmt.format(new Date(log.occurred_at_ms || log.created_at * 1000))
  const typeLabel = (log: ApiAdminAuditLog) => t(`logs.types.${log.type || 'workspace'}`, { defaultValue: t('logs.types.other') })
  const actionLabel = (log: ApiAdminAuditLog) => auditActionLabel(log.action, t)
  const changeValue = (value: unknown) => value === null || value === undefined ? t('logs.unset') : typeof value === 'object' ? JSON.stringify(value) : String(value)
  const updateFilter = (key: keyof typeof EMPTY_FILTERS, value: string) => setFilters((current) => ({ ...current, [key]: value }))
  const refresh = () => setRequest((current) => ({ ...current, revision: current.revision + 1 }))
  const hasFilters = JSON.stringify(filters) !== JSON.stringify(EMPTY_FILTERS)
  const filtersPending = JSON.stringify(filters) !== JSON.stringify(request.filters)

  function confirmFilteredDelete() {
    const scope = apiFilters(request.filters)
    const cutoff = new Date().toISOString()
    setDeleteRequest({
      kind: 'filtered', count: total,
      filters: { ...scope, until: scope.until && scope.until < cutoff ? scope.until : cutoff },
    })
  }

  async function deleteLogs() {
    if (!deleteRequest || deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      let deleted = 1
      if (deleteRequest.kind === 'one') await adminApi.deleteAuditLog(deleteRequest.log.id)
      else deleted = (await adminApi.deleteAuditLogsFiltered(deleteRequest.filters)).deleted
      if (deleteRequest.kind === 'filtered' || selectedLog?.id === deleteRequest.log.id) setSelectedLog(null)
      setDeleteRequest(null)
      toast.success(t('logs.deleted', { count: deleted }))
      refresh()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  async function exportLogs() {
    setExporting(true)
    try {
      const result = await adminApi.exportAuditLogs(apiFilters(filters))
      const url = URL.createObjectURL(new Blob([JSON.stringify(result, null, 2)], { type: 'application/json' }))
      const link = document.createElement('a')
      link.href = url
      link.download = `audit-logs-${new Date().toISOString().slice(0, 10)}.json`
      document.body.appendChild(link)
      link.click()
      link.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 1000)
      toast.success(t(result.truncated ? 'logs.exportTruncated' : 'logs.exportDone', { count: result.exported, total: result.total }))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('common.failed'))
    } finally { setExporting(false) }
  }

  const columns: AdminTableColumn<ApiAdminAuditLog>[] = [
    { id: 'time', header: t('logs.table.time'), width: 195, render: (log) => <span className="whitespace-nowrap text-[12px] text-[var(--color-fg-muted)]">{formatTime(log)}</span> },
    { id: 'type', header: t('logs.table.type'), width: 135, render: (log) => <span className="text-[12px] text-[var(--color-fg-muted)]">{typeLabel(log)}</span> },
    { id: 'result', header: t('logs.result'), width: 100, render: (log) => <AuditResult result={log.result || 'success'} label={t(`logs.results.${log.result || 'success'}`)} /> },
    { id: 'actor', header: t('logs.table.actor'), width: 200, render: (log) => <AuditIdentity primary={log.actor_name || log.actor_user_id || t('logs.anonymous')} secondary={log.actor_name ? log.actor_user_id : ''} /> },
    { id: 'action', header: t('logs.table.action'), width: 220, render: (log) => <AuditIdentity primary={actionLabel(log)} /> },
    { id: 'target', header: t('logs.table.target'), width: 215, render: (log) => <AuditIdentity primary={log.target_name || auditTargetLabel(log.target_type, t)} secondary={log.target_id} /> },
    { id: 'workspace', header: t('logs.table.workspace'), width: 200, render: (log) => <AuditIdentity primary={log.workspace_name || log.workspace_id || '-'} secondary={log.workspace_name ? log.workspace_id : ''} /> },
    { id: 'id', header: t('logs.table.id'), width: 210, render: (log) => <span className="font-mono text-[12px]">{log.id}</span> },
    { id: 'actions', header: t('common.actions'), width: 80, align: 'right', render: (log) => (
      <Tooltip content={t('logs.deleteRow')}>
        <Button variant="ghost" size="icon-sm" aria-label={t('logs.deleteEntry', { id: log.id })} disabled={deleting}
          onClick={(event) => { event.stopPropagation(); setDeleteRequest({ kind: 'one', log }) }}>
          <Trash2 size={14} aria-hidden />
        </Button>
      </Tooltip>
    ) },
  ]

  return (
    <div>
      <AdminPageHeader title={t('logs.auditLogs')} actions={
        <div className="flex items-center gap-2">
          <span className="mr-1 text-[12px] tabular-nums text-[var(--color-fg-muted)]">{t('logs.total', { count: total })}</span>
          <Tooltip content={t('logs.refresh')}><Button variant="ghost" size="icon" aria-label={t('logs.refresh')} onClick={refresh} disabled={loading}><RotateCw size={16} aria-hidden /></Button></Tooltip>
          <Tooltip content={t('logs.export')}><Button variant="ghost" size="icon" aria-label={t('logs.export')} onClick={() => void exportLogs()} disabled={invalidRange} loading={exporting}><Download size={16} aria-hidden /></Button></Tooltip>
          <Tooltip content={t('logs.deleteFiltered')}><Button variant="ghost" size="icon" aria-label={t('logs.deleteFiltered')} onClick={confirmFilteredDelete} disabled={loading || loadError || total === 0 || invalidRange || filtersPending || deleting}><Trash2 size={16} aria-hidden /></Button></Tooltip>
        </div>
      } />

      <div className="mt-5 flex flex-wrap items-center gap-2">
        <div className="relative min-w-48 flex-1 sm:max-w-[30rem]">
          <Search size={15} aria-hidden className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
          <Input value={filters.search} onChange={(event) => updateFilter('search', event.target.value)} placeholder={t('logs.searchPlaceholder')} aria-label={t('logs.searchPlaceholder')} className="pl-9 pr-9" />
          {filters.search && <Button variant="ghost" size="icon" onClick={() => updateFilter('search', '')} aria-label={t('logs.clearSearch')} className="absolute right-1 top-1/2 size-7 -translate-y-1/2"><X size={14} aria-hidden /></Button>}
        </div>
        <Select value={filters.type} onValueChange={(value) => updateFilter('type', value)}>
          <SelectTrigger aria-label={t('logs.typeFilter')} className="w-40"><SelectValue /></SelectTrigger>
          <SelectContent><SelectItem value="all">{t('logs.types.all')}</SelectItem>{AUDIT_TYPES.map((item) => <SelectItem key={item} value={item}>{t(`logs.types.${item}`)}</SelectItem>)}</SelectContent>
        </Select>
        <Select value={filters.result} onValueChange={(value) => updateFilter('result', value)}>
          <SelectTrigger aria-label={t('logs.resultFilter')} className="w-32"><SelectValue /></SelectTrigger>
          <SelectContent><SelectItem value="all">{t('logs.results.all')}</SelectItem>{RESULTS.map((item) => <SelectItem key={item} value={item}>{t(`logs.results.${item}`)}</SelectItem>)}</SelectContent>
        </Select>
        <Tooltip content={t('logs.moreFilters')}><Button variant={advanced ? 'secondary' : 'ghost'} size="icon" aria-label={t('logs.moreFilters')} aria-expanded={advanced} aria-controls="audit-advanced-filters" onClick={() => setAdvanced(!advanced)}><SlidersHorizontal size={16} aria-hidden /></Button></Tooltip>
        {hasFilters && <Tooltip content={t('logs.resetFilters')}><Button variant="ghost" size="icon" aria-label={t('logs.resetFilters')} onClick={() => setFilters(EMPTY_FILTERS)}><X size={16} aria-hidden /></Button></Tooltip>}
      </div>
      {advanced && <div id="audit-advanced-filters" className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {(['from', 'until', 'actor', 'target', 'action'] as const).map((key) => <label key={key} className="flex min-w-0 flex-col gap-1 text-[12px] text-[var(--color-fg-muted)]">
          {t(`logs.filters.${key}`)}
          <Input type={key === 'from' || key === 'until' ? 'datetime-local' : 'text'} value={filters[key]} maxLength={160} aria-invalid={(key === 'from' || key === 'until') && invalidRange} onChange={(event) => updateFilter(key, event.target.value)} />
        </label>)}
      </div>}
      {invalidRange && <p role="alert" className="mt-2 text-[12px] text-[var(--color-danger)]">{t('logs.invalidRange')}</p>}

      <section className="mt-5" aria-busy={loading}>
        {loading ? <PanelFallback /> : loadError ? <div role="alert" className="py-10 text-center text-sm text-[var(--color-fg-muted)]"><p>{t('common.failed')}</p><Button variant="ghost" size="sm" onClick={refresh} className="mt-2"><RotateCw size={14} />{t('logs.refresh')}</Button></div> : logs.length === 0 ? <div className="px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">{t('logs.empty')}</div> : <AdminTable
          items={logs} columns={columns} rowKey={(log) => log.id} label={t('logs.auditLogs')}
          renderRow={(log, _index, cells) => <tr key={log.id} role="button" tabIndex={0} aria-label={t('logs.openDetail', { id: log.id })}
            onClick={() => setSelectedLog(log)} onKeyDown={(event) => { if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) { event.preventDefault(); setSelectedLog(log) } }}
            className="cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]">{cells}</tr>}
        />}
        {!loading && !loadError && total > PAGE_SIZE && <Pagination page={request.page} pageCount={Math.max(1, Math.ceil(total / PAGE_SIZE))} onPage={(page) => setRequest((current) => ({ ...current, page }))} />}
      </section>

      <Sheet open={!!selectedLog} onOpenChange={(open) => !open && setSelectedLog(null)}>
        <SheetContent side="right" size="lg" label={t('logs.detailTitle')} className="max-w-[calc(100vw-var(--safe-left)-var(--safe-right))]">
          <SheetHeader className="flex items-start justify-between gap-4">
            <div className="min-w-0"><SheetTitle>{t('logs.detailTitle')}</SheetTitle><p className="mt-1.5 break-all font-mono text-[12px] text-[var(--color-fg-muted)]">{selectedLog?.id}</p></div>
            <SheetClose asChild><Button variant="ghost" size="icon" aria-label={t('common.close')}><X size={16} aria-hidden /></Button></SheetClose>
          </SheetHeader>
          {selectedLog && <SheetBody className="space-y-6 pb-6">
            <div className="flex items-center gap-3"><AuditResult result={selectedLog.result || 'success'} label={t(`logs.results.${selectedLog.result || 'success'}`)} /><span className="text-sm font-medium">{actionLabel(selectedLog)}</span></div>
            <dl className="grid grid-cols-1 gap-x-4 gap-y-4 text-[12px] min-[380px]:grid-cols-2">
              <DetailItem label={t('logs.table.time')} value={formatTime(selectedLog)} />
              <DetailItem label={t('logs.table.type')} value={typeLabel(selectedLog)} />
              <DetailItem label={t('logs.table.actor')} value={selectedLog.actor_name || t('logs.anonymous')} />
              <DetailItem label={t('logs.detail.actorId')} value={selectedLog.actor_user_id || '-'} mono />
              <DetailItem label={t('logs.actorRole')} value={selectedLog.actor_role ? t(`logs.roles.${selectedLog.actor_role}`, { defaultValue: t('logs.roles.other') }) : '-'} />
              <DetailItem label={t('logs.source')} value={selectedLog.source ? t(`logs.sources.${selectedLog.source}`, { defaultValue: selectedLog.source }) : '-'} />
              <DetailItem label={t('logs.table.action')} value={actionLabel(selectedLog)} />
              <DetailItem label={t('logs.filters.action')} value={selectedLog.action} mono />
              <DetailItem label={t('logs.detail.targetType')} value={auditTargetLabel(selectedLog.target_type, t)} />
              <DetailItem label={t('logs.table.target')} value={selectedLog.target_name || '-'} />
              <DetailItem label={t('logs.detail.targetId')} value={selectedLog.target_id || '-'} mono />
              {selectedLog.workspace_id && <><DetailItem label={t('logs.table.workspace')} value={selectedLog.workspace_name || selectedLog.workspace_id} /><DetailItem label={t('logs.detail.workspaceId')} value={selectedLog.workspace_id} mono /></>}
              <DetailItem label={t('logs.clientIp')} value={selectedLog.client_ip || '-'} mono />
              <DetailItem label={t('logs.httpStatus')} value={selectedLog.http_status ? String(selectedLog.http_status) : '-'} />
              <DetailItem label={t('logs.requestId')} value={selectedLog.request_id || '-'} mono />
              <DetailItem label={t('logs.duration')} value={selectedLog.duration_ms === undefined ? '-' : `${selectedLog.duration_ms} ms`} />
              <DetailItem label={t('logs.route')} value={selectedLog.route ? `${selectedLog.method || ''} ${selectedLog.route}` : '-'} mono />
              <DetailItem label={t('logs.reason')} value={selectedLog.reason ? t(`logs.reasons.${selectedLog.reason}`, { defaultValue: selectedLog.reason }) : '-'} />
              <div className="col-span-full"><DetailItem label={t('logs.userAgent')} value={formatRecordedClient(selectedLog.user_agent || '', t('common:desktopApp')) || '-'} /></div>
            </dl>
            {!!Object.keys(selectedLog.changes || {}).length && <section>
              <h3 className="mb-3 text-sm font-medium">{t('logs.changes')}</h3>
              <div className="space-y-4">{Object.entries(selectedLog.changes || {}).map(([field, change]) => <div key={field}>
                <p className="mb-1.5 font-mono text-[12px] text-[var(--color-fg-muted)]">{t(`logs.fields.${field}`, { defaultValue: field })}</p>
                {change.redacted ? <p className="text-[12px] text-[var(--color-fg-subtle)]">{t('logs.redacted')}</p> : <dl className="grid grid-cols-2 gap-3 rounded-[6px] bg-[var(--color-bg-muted)] p-3 text-[12px]"><DetailItem label={t('logs.before')} value={changeValue(change.before)} /><DetailItem label={t('logs.after')} value={changeValue(change.after)} /></dl>}
              </div>)}</div>
            </section>}
            <section><h3 className="mb-1.5 text-[12px] font-medium text-[var(--color-fg-subtle)]">{t('logs.metadata')}</h3><pre className="max-h-[40vh] overflow-auto whitespace-pre-wrap break-words rounded-[6px] bg-[var(--color-bg-muted)] p-3 font-mono text-[12px] leading-relaxed text-[var(--color-fg-muted)]">{JSON.stringify(selectedLog.metadata || {}, null, 2)}</pre></section>
          </SheetBody>}
          {selectedLog && <SheetFooter>
            <Button variant="ghost" size="sm" leadingIcon={<Trash2 size={14} aria-hidden />} disabled={deleting}
              className="text-[var(--color-danger)] hover:text-[var(--color-danger)]"
              onClick={() => setDeleteRequest({ kind: 'one', log: selectedLog })}>
              {t('logs.deleteRow')}
            </Button>
          </SheetFooter>}
        </SheetContent>
      </Sheet>

      <Dialog open={!!deleteRequest} onOpenChange={(open) => { if (!open && !deletingRef.current) setDeleteRequest(null) }}>
        <DialogContent size="sm" closeDisabled={deleting}>
          <DialogHeader>
            <DialogTitle>{t('logs.deleteConfirm.title')}</DialogTitle>
            <DialogDescription className="break-words">{deleteRequest?.kind === 'one'
              ? t('logs.deleteConfirm.one', { id: deleteRequest.log.id })
              : t('logs.deleteConfirm.filtered', { count: deleteRequest?.count ?? 0 })}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => setDeleteRequest(null)}>{t('common.cancel')}</Button>
            <Button variant="destructive" loading={deleting} onClick={() => void deleteLogs()}>{t('common.delete')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function AuditResult({ result, label }: { result: string; label: string }) {
  const color = result === 'success' ? 'text-[var(--color-success)]' : result === 'failure' ? 'text-[var(--color-danger)]' : result === 'denied' ? 'text-[var(--color-warning)]' : 'text-[var(--color-fg-muted)]'
  return <span className={`inline-flex items-center gap-1.5 whitespace-nowrap text-[12px] ${color}`}><span aria-hidden className="size-1.5 shrink-0 rounded-full bg-current" />{label}</span>
}

function AuditIdentity({ primary, secondary }: { primary: string; secondary?: string }) {
  return <span className="block min-w-0"><span className="block truncate">{primary}</span>{secondary && <span className="block truncate font-mono text-xs text-[var(--color-fg-subtle)]">{secondary}</span>}</span>
}

function DetailItem({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return <div className="min-w-0"><dt className="text-[12px] text-[var(--color-fg-subtle)]">{label}</dt><dd className={`mt-0.5 break-words text-[var(--color-fg)] ${mono ? 'break-all font-mono text-[12px]' : ''}`}>{value}</dd></div>
}
