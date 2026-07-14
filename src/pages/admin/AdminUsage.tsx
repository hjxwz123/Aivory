/**
 * AdminUsage — per-record usage log from usage_logs (one row per API call).
 *
 * Each call is one row, newest first. Filter by time range, user (email/id), and
 * model; delete a single record or every record matching the current filter.
 * Purpose values (chat/image/embedding/task.*) are translated via i18n keys; a
 * row whose conversation was deleted shows "deleted" instead of a dangling id.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUsageRecord } from '@/api/types'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import {
  DialogBody,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Pagination } from '@/components/ui/pagination'
import { toast } from '@/hooks/use-toast'
import { envNum } from '@/lib/env-config'
import { PanelFallback } from '@/components/ui/panel-fallback'

const RANGE_IDS = ['1', '7', '30', '90'] as const
const ALL_MODELS = 'all'
const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE', 50)
// Known task-model sub-purposes for the purpose filter dropdown. Labels come
// from the existing usage.purposes.* i18n keys (dots → underscores); an
// unknown/new purpose still displays raw in rows and matches via the "task"
// umbrella option even before it's added here.
const TASK_PURPOSES = [
  'task.title',
  'task.router',
  'task.compact',
  'task.memory_extract',
  'task.memory_adjudicate',
  'task.downgrade',
  'task.image_prompt',
  'task.research_plan',
  'task.research_verify',
  'task.research_validate',
  'task.moderation',
] as const

export default function AdminUsage() {
  const { t, i18n } = useTranslation('admin')
  const [days, setDays] = useState('30')
  const [userQ, setUserQ] = useState('')
  const [userQDebounced, setUserQDebounced] = useState('')
  const [modelId, setModelId] = useState(ALL_MODELS)
  const [status, setStatus] = useState('all')
  const [purpose, setPurpose] = useState('all')

  const [records, setRecords] = useState<ApiUsageRecord[]>([])
  const [total, setTotal] = useState(0)
  const [totalCost, setTotalCost] = useState(0)
  const [modelMap, setModelMap] = useState<Record<string, string>>({})
  const [modelOptions, setModelOptions] = useState<{ id: string; label: string }[]>([])
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState(1)
  const [confirmBulk, setConfirmBulk] = useState(false)
  const [busy, setBusy] = useState(false)
  // The failed-request record whose upstream error detail is being viewed (§usage errors).
  const [errorDetail, setErrorDetail] = useState<ApiUsageRecord | null>(null)

  // Debounce the free-text user filter so we don't refetch on every keystroke.
  useEffect(() => {
    const id = setTimeout(() => setUserQDebounced(userQ.trim()), 400)
    return () => clearTimeout(id)
  }, [userQ])

  // The filters the backend sees (model 'all' → no model constraint).
  const queryParams = useMemo(
    () => ({
      days: Number(days),
      user: userQDebounced || undefined,
      model: modelId === ALL_MODELS ? undefined : modelId,
      status: status === 'all' ? undefined : status,
      purpose: purpose === 'all' ? undefined : purpose,
    }),
    [days, userQDebounced, modelId, status, purpose],
  )

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const r = await adminApi.usage({ ...queryParams, page, pageSize: PAGE_SIZE })
      setRecords(r.records)
      setTotal(r.total)
      setTotalCost(r.total_cost)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setLoading(false)
    }
  }, [queryParams, page, t])

  // Models are fetched once for the id→label map + the filter dropdown.
  useEffect(() => {
    void (async () => {
      try {
        const models = await adminApi.models()
        const map: Record<string, string> = {}
        for (const m of models) map[m.id] = m.label
        setModelMap(map)
        setModelOptions(models.map((m) => ({ id: m.id, label: m.label })))
      } catch {
        /* non-fatal: ids just won't resolve to labels */
      }
    })()
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // Any filter change resets to the first page.
  useEffect(() => {
    setPage(1)
  }, [days, userQDebounced, modelId, status, purpose])

  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const timeFmt = useMemo(
    () => new Intl.DateTimeFormat(i18n.language || undefined, { dateStyle: 'short', timeStyle: 'medium' }),
    [i18n.language],
  )

  function modelLabel(id: string): string {
    return modelMap[id] || id
  }

  function purposeLabel(purpose: string): string {
    // Backend purposes like "task.title" contain dots; i18next treats "." as a
    // key separator, so normalise to "task_title" to match the flat keys.
    const key = `usage.purposes.${purpose.replace(/\./g, '_')}`
    return t(key, { defaultValue: '' }) || purpose
  }

  async function deleteOne(id: number) {
    setBusy(true)
    try {
      await adminApi.deleteUsageRecord(id)
      // Optimistically drop the row; reload to keep the page full + totals fresh.
      setRecords((rs) => rs.filter((r) => r.id !== id))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setBusy(false)
    }
  }

  async function deleteFiltered() {
    setBusy(true)
    try {
      const r = await adminApi.deleteUsageFiltered(queryParams)
      toast.success(t('usage.deleted', { defaultValue: 'Deleted {{count}} record(s)', count: r.deleted }))
      setConfirmBulk(false)
      setPage(1)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('usage.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">
            {t('usage.leadRecords', { defaultValue: 'Every API call, one row. Filter and prune the log below.' })}
          </p>
        </div>
        <Button
          variant="destructive"
          leadingIcon={<Trash2 size={13} aria-hidden />}
          disabled={total === 0 || loading}
          onClick={() => setConfirmBulk(true)}
        >
          {t('usage.deleteFiltered', { defaultValue: 'Delete filtered' })}
        </Button>
      </header>

      {/* Filters: time range · user · model */}
      <section className="mt-6 flex flex-wrap items-end gap-3">
        <div className="w-40">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('usage.filters.range', { defaultValue: 'Time range' })}</label>
          <Select value={days} onValueChange={setDays}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RANGE_IDS.map((id) => (
                <SelectItem key={id} value={id}>
                  {t(`usage.range.${id}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="w-56">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('usage.filters.user', { defaultValue: 'User' })}</label>
          <Input
            value={userQ}
            onChange={(e) => setUserQ(e.target.value)}
            placeholder={t('usage.filters.userPlaceholder', { defaultValue: 'Email or ID' })}
          />
        </div>
        <div className="w-56">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('usage.filters.model', { defaultValue: 'Model' })}</label>
          <Select value={modelId} onValueChange={setModelId}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_MODELS}>{t('usage.filters.allModels', { defaultValue: 'All models' })}</SelectItem>
              {modelOptions.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  {m.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="w-44">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('usage.filters.status', { defaultValue: 'Status' })}</label>
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('usage.status.all', { defaultValue: 'All' })}</SelectItem>
              <SelectItem value="error">{t('usage.status.errorsOnly', { defaultValue: 'Errors only' })}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="w-48">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('usage.filters.purpose', { defaultValue: 'Purpose' })}</label>
          <Select value={purpose} onValueChange={setPurpose}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('usage.filters.allPurposes', { defaultValue: 'All purposes' })}</SelectItem>
              <SelectItem value="chat">{purposeLabel('chat')}</SelectItem>
              <SelectItem value="image">{purposeLabel('image')}</SelectItem>
              <SelectItem value="embedding">{purposeLabel('embedding')}</SelectItem>
              {/* "task" is the backend umbrella matching every task.* sub-purpose */}
              <SelectItem value="task">{t('usage.filters.taskAll', { defaultValue: 'Task models (all)' })}</SelectItem>
              {TASK_PURPOSES.map((p) => (
                <SelectItem key={p} value={p}>
                  {purposeLabel(p)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </section>

      <section className="mt-6 grid grid-cols-2 gap-3">
        <Stat label={t('usage.stats.totalCost')} value={`$${totalCost.toFixed(4)}`} />
        <Stat label={t('usage.stats.rows')} value={String(total)} />
      </section>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : records.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('usage.empty')}
          </div>
        ) : (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] overflow-x-auto">
            <table className="min-w-[1120px] w-full text-sm tabular-nums">
              <thead className="bg-[var(--color-bg-muted)] text-[12px] text-[var(--color-fg-subtle)]">
                <tr>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.time', { defaultValue: 'Time' })}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.user')}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.conversation', { defaultValue: 'Conversation' })}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.model')}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.channel', { defaultValue: 'Channel' })}</th>
                  <th className="text-left py-2.5 px-4 font-medium">{t('usage.table.purpose')}</th>
                  <th className="text-right py-2.5 px-4 font-medium">{t('usage.table.in')}</th>
                  <th className="text-right py-2.5 px-4 font-medium">{t('usage.table.out')}</th>
                  <th className="text-right py-2.5 px-4 font-medium">{t('usage.table.cost')}</th>
                  <th className="text-right py-2.5 px-4 font-medium" aria-label={t('usage.table.actions', { defaultValue: 'Actions' })} />
                </tr>
              </thead>
              <tbody>
                {records.map((r) => (
                  <tr key={r.id} className="border-t border-[var(--color-divider)]">
                    <td className="py-2 px-4 text-[12px] text-[var(--color-fg-muted)] whitespace-nowrap">{timeFmt.format(new Date(r.created_at * 1000))}</td>
                    <td className="py-2 px-4 truncate max-w-[12rem]">{r.user_email || r.user_id}</td>
                    <td className="py-2 px-4 max-w-[14rem]">
                      {r.conversation_deleted ? (
                        <span className="text-[var(--color-fg-faint)] italic">
                          {t('usage.conversationDeleted', { defaultValue: 'Deleted' })}
                        </span>
                      ) : r.conversation_id ? (
                        <Link
                          to={`/admin/users/${encodeURIComponent(r.user_id)}/conversations/${encodeURIComponent(r.conversation_id)}`}
                          className="block truncate text-[var(--color-accent)] hover:underline"
                          title={r.conversation_title || r.conversation_id}
                        >
                          {r.conversation_title || r.conversation_id}
                        </Link>
                      ) : (
                        <span className="text-[var(--color-fg-faint)]">—</span>
                      )}
                      {r.workspace_name || r.workspace_id ? (
                        <span
                          className="mt-0.5 inline-block max-w-full truncate rounded-full border border-[var(--color-border)] px-1.5 text-[10px] text-[var(--color-fg-subtle)]"
                          title={r.workspace_name || r.workspace_id}
                        >
                          {t('usage.workspaceTag', { defaultValue: 'WS' })} · {r.workspace_name || r.workspace_id}
                        </span>
                      ) : null}
                    </td>
                    <td className="py-2 px-4 text-[12px]">
                      <span className="inline-flex flex-wrap items-center gap-1">
                        {modelLabel(r.model_id)}
                        {r.ttft_fallback_model ? (
                          <span
                            className="rounded-full border border-[var(--color-warning)] px-1.5 text-[10px] text-[var(--color-warning)]"
                            title={t('usage.ttftFallbackTitle', {
                              defaultValue:
                                'Primary model produced no output in time; this turn was served by the fallback model {{model}}',
                              model: r.ttft_fallback_model,
                            })}
                          >
                            {t('usage.ttftFallbackTag', {
                              defaultValue: 'Timeout fallback → {{model}}',
                              model: r.ttft_fallback_model,
                            })}
                          </span>
                        ) : null}
                      </span>
                    </td>
                    <td className="py-2 px-4 text-[12px]">
                      {r.channel_name || r.channel_id ? (
                        <span className="inline-flex items-center gap-1">
                          <span className="truncate max-w-[8rem] text-[var(--color-fg-muted)]" title={r.channel_name || r.channel_id}>
                            {r.channel_name || r.channel_id}
                          </span>
                          {r.fallback ? (
                            <span
                              className="rounded-full border border-[var(--color-warning)] px-1.5 text-[10px] text-[var(--color-warning)]"
                              title={t('usage.fallbackTitle', { defaultValue: 'Served by the model’s fallback channel' })}
                            >
                              {t('usage.fallbackTag', { defaultValue: 'Fallback' })}
                            </span>
                          ) : null}
                        </span>
                      ) : (
                        <span className="text-[var(--color-fg-faint)]">—</span>
                      )}
                    </td>
                    <td className="py-2 px-4 text-[var(--color-fg-muted)] whitespace-nowrap">
                      <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
                        {purposeLabel(r.purpose)}
                        {r.status === 'error' ? (
                          <button
                            type="button"
                            onClick={() => setErrorDetail(r)}
                            title={t('usage.errorDetail.view', { defaultValue: 'View error detail' })}
                            className="inline-flex h-5 shrink-0 items-center justify-center whitespace-nowrap rounded-full bg-[var(--color-danger-soft)] px-2 text-[10px] leading-none text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                          >
                            {t('usage.statusError', { defaultValue: 'Error' })}
                          </button>
                        ) : r.request_method || r.request_url || r.request_body ? (
                          /* §B5-request-logging: with full-request logging on,
                             SUCCESS rows also carry the sanitized snapshot —
                             give them the same viewer behind a neutral chip. */
                          <button
                            type="button"
                            onClick={() => setErrorDetail(r)}
                            title={t('usage.requestDetail.view', { defaultValue: 'View request detail' })}
                            className="inline-flex h-5 shrink-0 items-center justify-center whitespace-nowrap rounded-full bg-[var(--color-bg-muted)] px-2 text-[10px] leading-none text-[var(--color-fg-muted)] interactive hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                          >
                            {t('usage.requestDetail.tag', { defaultValue: 'Request' })}
                          </button>
                        ) : null}
                      </span>
                    </td>
                    <td className="py-2 px-4 text-right">{r.input_tokens}</td>
                    <td className="py-2 px-4 text-right">{r.output_tokens}</td>
                    <td className="py-2 px-4 text-right">${r.cost.toFixed(4)}</td>
                    <td className="py-2 px-4 text-right">
                      <button
                        type="button"
                        onClick={() => void deleteOne(r.id)}
                        disabled={busy}
                        aria-label={t('usage.deleteRow', { defaultValue: 'Delete record' })}
                        className="inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-40"
                      >
                        <Trash2 size={13} aria-hidden />
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {!loading && total > PAGE_SIZE ? <Pagination page={page} pageCount={pageCount} onPage={setPage} /> : null}
      </section>

      <Dialog open={!!errorDetail} onOpenChange={(o) => !o && setErrorDetail(null)}>
        <DialogContent size="full">
          <DialogHeader>
            <DialogTitle>
              {errorDetail?.status === 'error'
                ? t('usage.errorDetail.title', { defaultValue: 'Upstream error' })
                : t('usage.requestDetail.title', { defaultValue: 'Request detail' })}
            </DialogTitle>
            <DialogDescription>
              {errorDetail
                ? `${modelLabel(errorDetail.model_id)} · ${errorDetail.channel_name || errorDetail.channel_id || '—'}`
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            {errorDetail?.request_method || errorDetail?.request_url ? (
              <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2 text-[12px] text-[var(--color-fg-muted)]">
                <span className="font-medium text-[var(--color-fg)]">{errorDetail.request_method || 'REQUEST'}</span>
                {errorDetail.request_url ? <span className="ml-2 break-all">{errorDetail.request_url}</span> : null}
              </div>
            ) : null}
            {errorDetail?.status === 'error' ? (
              <ErrorDetailBlock
                title={t('usage.errorDetail.error', { defaultValue: 'Error' })}
                content={errorDetail?.error || t('usage.errorDetail.none', { defaultValue: 'No error detail was recorded for this request.' })}
              />
            ) : null}
            <ErrorDetailBlock
              title={t('usage.errorDetail.headers', { defaultValue: 'Request headers' })}
              content={errorDetail?.request_headers || t('usage.errorDetail.noHeaders', { defaultValue: 'No request headers were recorded.' })}
            />
            <ErrorDetailBlock
              title={t('usage.errorDetail.body', { defaultValue: 'Request body' })}
              content={errorDetail?.request_body || t('usage.errorDetail.noBody', { defaultValue: 'No request body was recorded.' })}
            />
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setErrorDetail(null)}>
              {t('common.close', { defaultValue: 'Close' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmBulk} onOpenChange={setConfirmBulk}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('usage.deleteConfirm.title', { defaultValue: 'Delete these usage records?' })}</DialogTitle>
            <DialogDescription>
              {t('usage.deleteConfirm.body', {
                defaultValue: 'This permanently deletes the {{count}} record(s) matching the current filter. This cannot be undone.',
                count: total,
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmBulk(false)} disabled={busy}>
              {t('common.cancel', { defaultValue: 'Cancel' })}
            </Button>
            <Button variant="destructive" loading={busy} onClick={() => void deleteFiltered()}>
              {t('usage.deleteConfirm.action', { defaultValue: 'Delete' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function ErrorDetailBlock({ title, content }: { title: string; content: string }) {
  return (
    <section>
      <h3 className="mb-1.5 text-[12px] font-medium text-[var(--color-fg-subtle)]">{title}</h3>
      <pre className="max-h-[34vh] overflow-auto rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-3 text-[12px] leading-relaxed text-[var(--color-fg-muted)] whitespace-pre-wrap break-words">
        {content}
      </pre>
    </section>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <div className="text-[12px] text-[var(--color-fg-subtle)] uppercase tracking-wide">{label}</div>
      <div className="mt-1 font-serif text-2xl text-[var(--color-fg)]">{value}</div>
    </div>
  )
}
