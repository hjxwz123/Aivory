/**
 * AdminUsage — per-record usage log from usage_logs (one row per API call).
 *
 * Each call is one row, newest first. Filter by time range, user (nickname/email/id), and
 * model; delete a single record or every record matching the current filter.
 * Purpose values (chat/image/embedding/task.*) are translated via i18n keys; a
 * row whose conversation was deleted shows "deleted" instead of a dangling id.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { LoaderCircle, Trash2, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUsageRecord } from '@/api/types'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import {
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
import { usageUserLabel } from '@/lib/admin-usage'
import { cn } from '@/lib/utils'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminTableFrame } from '@/components/admin/AdminTable'
import { Sheet, SheetBody, SheetClose, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'

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
  'task.rag_evidence_judge',
  'task.rag_map_reduce',
  'task.compact',
  'task.memory_extract',
  'task.memory_adjudicate',
  'task.downgrade',
  'task.image_prompt',
  'task.image_intent',
  'task.tool_route',
  'task.search_queries',
  'task.research_plan',
  'task.research_read',
  'task.research_verify',
  'task.research_validate',
  'task.moderation',
  'task.vision_caption',
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
  const [busyId, setBusyId] = useState<number | null>(null)
  const [selectedRecord, setSelectedRecord] = useState<ApiUsageRecord | null>(null)

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

  function purposeFilterLabel(value: string): string {
    if (value === 'all') return t('usage.filters.allPurposes')
    if (value === 'task') return t('usage.filters.taskAll')
    return purposeLabel(value)
  }

  /** § AI PPT: which AI PPT step produced this row. */
  function aipptEventLabel(event?: string): string {
    if (event === 'rewrite') return t('usage.aippt.events.rewrite', { defaultValue: 'AI rewrite' })
    if (event === 'template') return t('usage.aippt.events.template', { defaultValue: 'Template change' })
    return t('usage.aippt.events.generate', { defaultValue: 'Generation' })
  }

  function aipptStatusLabel(status?: string): string {
    if (!status) return '—'
    return t(`usage.aippt.deckStatus.${status}`, { defaultValue: status })
  }

  /** The deck title of an AI PPT row, falling back to its ids. */
  function aipptTitle(row: ApiUsageRecord | null): string {
    return (
      row?.aippt?.subject ||
      row?.aippt?.deck_id ||
      row?.aippt?.ppt_id ||
      t('usage.aippt.untitled', { defaultValue: 'Untitled PPT' })
    )
  }

  async function deleteOne(id: number) {
    if (busy || busyId !== null) return
    setBusyId(id)
    try {
      await adminApi.deleteUsageRecord(id)
      // Optimistically drop the row; reload to keep the page full + totals fresh.
      setRecords((rs) => rs.filter((r) => r.id !== id))
      setSelectedRecord((record) => record?.id === id ? null : record)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setBusyId(null)
    }
  }

  async function deleteFiltered() {
    if (busy || busyId !== null) return
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
      <AdminPageHeader
        title={t('logs.callLogs', { defaultValue: 'Call logs' })}
        description={t('usage.leadRecords', { defaultValue: 'Every API call, one row. Filter and prune the log below.' })}
        actions={(
          <Button
            size="sm"
            variant="secondary"
            leadingIcon={<Trash2 size={14} aria-hidden />}
            disabled={total === 0 || loading || busy || busyId !== null}
            onClick={() => setConfirmBulk(true)}
            className="text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)] max-sm:min-h-[var(--tap-min)] max-sm:flex-1"
          >
            {t('usage.deleteFiltered', { defaultValue: 'Delete filtered' })}
          </Button>
        )}
      />

      {/* Filters: time range · user · model */}
      <section className="mt-5 grid min-w-0 grid-cols-2 gap-3 rounded-[12px] bg-[var(--color-surface)] p-3 sm:grid-cols-3 lg:grid-cols-5">
        <div className="min-w-0">
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
        <div className="col-span-2 min-w-0 sm:col-span-1">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('usage.filters.user', { defaultValue: 'User' })}</label>
          <Input
            value={userQ}
            onChange={(e) => setUserQ(e.target.value)}
            placeholder={t('usage.filters.userPlaceholder', { defaultValue: 'Nickname, email, or ID' })}
          />
        </div>
        <div className="col-span-2 min-w-0 sm:col-span-1">
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
        <div className="min-w-0">
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
        <div className="col-span-2 min-w-0 sm:col-span-1">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('usage.filters.purpose', { defaultValue: 'Purpose' })}</label>
          <Select value={purpose} onValueChange={setPurpose}>
            <SelectTrigger title={purposeFilterLabel(purpose)}>
              <SelectValue className="min-w-0 flex-1 truncate text-left" />
            </SelectTrigger>
            <SelectContent className="max-w-[min(32rem,calc(100vw-2rem))]">
              <SelectItem value="all">{t('usage.filters.allPurposes', { defaultValue: 'All purposes' })}</SelectItem>
              <SelectItem value="chat">{purposeLabel('chat')}</SelectItem>
              <SelectItem value="image">{purposeLabel('image')}</SelectItem>
              <SelectItem value="embedding">{purposeLabel('embedding')}</SelectItem>
              {/* AI PPT calls: credit-only rows with no model or tokens, so they
                  would otherwise be hard to isolate in the list. */}
              <SelectItem value="ppt">{purposeLabel('ppt')}</SelectItem>
              {/* Server-side speech recognition, billed per second (§ voice). */}
              <SelectItem value="audio.transcription">{purposeLabel('audio.transcription')}</SelectItem>
              {/* "task" is the backend umbrella matching every task.* sub-purpose */}
              <SelectItem value="task">
                {t('usage.filters.taskAll', { defaultValue: 'All internal model tasks' })}
              </SelectItem>
              {TASK_PURPOSES.map((p) => (
                <SelectItem key={p} value={p}>
                  {purposeLabel(p)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </section>

      <section className="mt-4 grid grid-cols-2 gap-2 sm:mt-6 sm:gap-3">
        <Stat label={t('usage.stats.totalCost')} value={`$${totalCost.toFixed(4)}`} />
        <Stat label={t('usage.stats.rows')} value={String(total)} />
      </section>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : records.length === 0 ? (
          <div className="rounded-[12px] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('usage.empty')}
          </div>
        ) : (
          <>
          <AdminTableFrame label={t('usage.title')}>
            <table className="admin-data-table admin-usage-log-table tabular-nums">
              <colgroup>
                <col className="w-[150px]" />
                <col className="w-[190px]" />
                <col className="w-[220px]" />
                <col className="w-[210px]" />
                <col className="w-[175px]" />
                <col className="w-[165px]" />
                <col className="w-[110px]" />
                <col className="w-[110px]" />
                <col className="w-[110px]" />
                <col className="w-[82px]" />
                <col className="w-[82px]" />
                <col className="w-[105px]" />
                <col className="w-[92px]" />
                <col className="w-[56px]" />
              </colgroup>
              <thead className="whitespace-nowrap bg-[var(--color-bg-muted)] text-[12px] text-[var(--color-fg-subtle)]">
                <tr>
                  <th scope="col" data-column="id" className="text-left py-2.5 px-4 font-medium">{t('usage.table.id', { defaultValue: 'ID / time' })}</th>
                  <th scope="col" data-column="user" className="text-left py-2.5 px-4 font-medium">{t('usage.table.user')}</th>
                  <th scope="col" data-column="conversation" className="text-left py-2.5 px-4 font-medium">{t('usage.table.conversation', { defaultValue: 'Conversation' })}</th>
                  <th scope="col" data-column="model" className="text-left py-2.5 px-4 font-medium">{t('usage.table.model')}</th>
                  <th scope="col" data-column="channel" className="text-left py-2.5 px-4 font-medium">{t('usage.table.channel', { defaultValue: 'Channel' })}</th>
                  <th scope="col" data-column="purpose" className="text-left py-2.5 px-4 font-medium">{t('usage.table.purpose')}</th>
                  <th scope="col" data-column="status" className="text-left py-2.5 px-4 font-medium">{t('usage.detail.status', { defaultValue: 'Status' })}</th>
                  <th scope="col" data-column="first-byte" className="text-right py-2.5 px-4 font-medium">{t('usage.table.firstByte', { defaultValue: 'First byte' })}</th>
                  <th scope="col" data-column="duration" className="text-right py-2.5 px-4 font-medium">{t('usage.table.duration', { defaultValue: 'Duration' })}</th>
                  <th scope="col" data-column="input" className="text-right py-2.5 px-4 font-medium">{t('usage.table.in')}</th>
                  <th scope="col" data-column="output" className="text-right py-2.5 px-4 font-medium">{t('usage.table.out')}</th>
                  <th scope="col" data-column="cost" className="text-right py-2.5 px-4 font-medium">{t('usage.table.cost')}</th>
                  <th scope="col" data-column="credits" className="text-right py-2.5 px-4 font-medium">{t('usage.table.credits', { defaultValue: 'Credits' })}</th>
                  <th scope="col" data-column="actions" className="text-right py-2.5 px-4 font-medium">{t('usage.table.actions', { defaultValue: 'Actions' })}</th>
                </tr>
              </thead>
              <tbody>
                {records.map((r) => (
                  <tr
                    key={r.id}
                    role="button"
                    tabIndex={0}
                    aria-label={t('usage.openDetail', { defaultValue: 'Open call log {{id}}', id: r.id })}
                    onClick={() => setSelectedRecord(r)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault()
                        setSelectedRecord(r)
                      }
                    }}
                    className="cursor-pointer hover:bg-[var(--color-bg-muted)]/45 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
                  >
                    <td data-column="id" className="py-2 px-4">
                      <span className="block font-mono text-[12px] text-[var(--color-fg)]">#{r.id}</span>
                      <span className="block whitespace-nowrap text-[12px] text-[var(--color-fg-muted)]">{timeFmt.format(new Date(r.created_at * 1000))}</span>
                    </td>
                    <td data-column="user" className="truncate px-4 py-2" title={usageUserLabel(r)}>{usageUserLabel(r)}</td>
                    <td data-column="conversation" className="px-4 py-2">
                      <div className="flex min-w-0 items-center gap-1.5 whitespace-nowrap">
                        {r.aippt ? (
                          <button
                            type="button"
                            onClick={() => setSelectedRecord(r)}
                            title={t('usage.aippt.detail', { defaultValue: 'View AI PPT call detail' })}
                            className="flex min-w-0 flex-1 items-center gap-1.5 text-left interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                          >
                            <span className="min-w-0 flex-1 truncate text-[var(--color-accent)] hover:underline">{aipptTitle(r)}</span>
                            <span className="shrink-0 rounded-full px-1.5 text-[12px] text-[var(--color-fg-subtle)]">
                              {t('usage.aippt.tag', { defaultValue: 'PPT' })} · {aipptEventLabel(r.aippt.event)}
                            </span>
                          </button>
                        ) : r.conversation_deleted ? (
                          <span className="min-w-0 truncate text-[var(--color-fg-faint)] italic">
                            {t('usage.conversationDeleted', { defaultValue: 'Deleted' })}
                          </span>
                        ) : r.conversation_id ? (
                          <Link
                            to={`/admin/users/${encodeURIComponent(r.user_id)}/conversations/${encodeURIComponent(r.conversation_id)}`}
                            className="min-w-0 flex-1 truncate text-[var(--color-accent)] hover:underline"
                            title={r.conversation_title || r.conversation_id}
                          >
                            {r.conversation_title || r.conversation_id}
                          </Link>
                        ) : (
                          <span className="text-[var(--color-fg-muted)]">{r.conversation_title ? t('usage.anonymousConversation') : '—'}</span>
                        )}
                        {r.workspace_name || r.workspace_id ? (
                          <span className="max-w-[5.5rem] shrink-0 truncate rounded-full px-1.5 text-[12px] text-[var(--color-fg-subtle)]" title={r.workspace_name || r.workspace_id}>
                            {t('usage.workspaceTag', { defaultValue: 'WS' })} · {r.workspace_name || r.workspace_id}
                          </span>
                        ) : null}
                      </div>
                    </td>
                    <td data-column="model" className="px-4 py-2 text-[12px]">
                      <span className="flex min-w-0 items-center gap-1 whitespace-nowrap">
                        <span className="min-w-0 flex-1 truncate" title={modelLabel(r.model_id)}>
                          {modelLabel(r.model_id)}
                        </span>
                        {r.ttft_fallback_model ? (
                          <span
                            className="shrink-0 rounded-full border border-[var(--color-warning)] px-1.5 text-[12px] text-[var(--color-warning)]"
                            title={t('usage.ttftFallbackTitle', {
                              defaultValue: 'Primary model produced no output in time; this turn was served by the fallback model {{model}}',
                              model: r.ttft_fallback_model,
                            })}
                          >
                            {t('usage.ttftFallbackShort', { defaultValue: 'Timeout fallback' })}
                          </span>
                        ) : null}
                      </span>
                    </td>
                    <td data-column="channel" className="px-4 py-2 text-[12px]">
                      {r.channel_name || r.channel_id ? (
                        <span className="flex min-w-0 items-center gap-1 whitespace-nowrap">
                          <span className="min-w-0 flex-1 truncate text-[var(--color-fg-muted)]" title={r.channel_name || r.channel_id}>
                            {r.channel_name || r.channel_id}
                          </span>
                          {r.fallback ? (
                            <span className="shrink-0 rounded-full border border-[var(--color-warning)] px-1.5 text-[12px] text-[var(--color-warning)]" title={t('usage.fallbackTitle', { defaultValue: 'Served by the model’s fallback channel' })}>
                              {t('usage.fallbackTag', { defaultValue: 'Fallback' })}
                            </span>
                          ) : null}
                        </span>
                      ) : <span className="text-[var(--color-fg-faint)]">—</span>}
                    </td>
                    <td data-column="purpose" className="whitespace-nowrap px-4 py-2 text-[var(--color-fg-muted)]">
                      <span className="flex min-w-0 items-center gap-1.5 whitespace-nowrap">
                        <span className="min-w-0 flex-1 truncate" title={purposeLabel(r.purpose)}>{purposeLabel(r.purpose)}</span>
                      </span>
                    </td>
                    <td data-column="status" className="px-4 py-2">
                      <span className={cn(
                        'inline-flex h-5 items-center justify-center whitespace-nowrap rounded-full px-2 text-[12px] leading-none',
                        r.status === 'error'
                          ? 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]'
                          : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]',
                      )}>
                        {r.status === 'error'
                          ? t('usage.statusError', { defaultValue: 'Error' })
                          : t('usage.status.success', { defaultValue: 'Success' })}
                      </span>
                    </td>
                    <td data-column="first-byte" className="whitespace-nowrap py-2 px-4 text-right text-[12px] text-[var(--color-fg-muted)]">{formatDuration(r.first_byte_ms)}</td>
                    <td data-column="duration" className="whitespace-nowrap py-2 px-4 text-right text-[12px] text-[var(--color-fg-muted)]">{formatDuration(r.duration_ms)}</td>
                    <td data-column="input" className="py-2 px-4 text-right">{r.input_tokens}</td>
                    <td data-column="output" className="py-2 px-4 text-right">{r.output_tokens}</td>
                    <td data-column="cost" className="py-2 px-4 text-right">${r.cost.toFixed(4)}</td>
                    <td data-column="credits" className="py-2 px-4 text-right">
                      {r.credits > 0 ? (
                        <span className="text-[var(--color-fg)]">{formatCredits(r.credits)}</span>
                      ) : r.aippt ? (
                        <span className="text-[var(--color-fg-subtle)]">{t('usage.aippt.free', { defaultValue: 'Free' })}</span>
                      ) : <span className="text-[var(--color-fg-faint)]">—</span>}
                    </td>
                    <td data-column="actions" className="py-2 px-4 text-right">
                      <button
                        type="button"
                        onClick={() => void deleteOne(r.id)}
                        disabled={busy || busyId !== null}
                        aria-busy={busyId === r.id || undefined}
                        aria-label={t('usage.deleteRow', { defaultValue: 'Delete record' })}
                        className="inline-flex items-center justify-center size-7 rounded-[6px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-40"
                      >
                        {busyId === r.id ? <LoaderCircle size={13} className="animate-spin" aria-hidden /> : <Trash2 size={13} aria-hidden />}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </AdminTableFrame>
          </>
        )}
        {!loading && total > PAGE_SIZE ? <Pagination page={page} pageCount={pageCount} onPage={setPage} /> : null}
      </section>

      <Sheet open={!!selectedRecord} onOpenChange={(open) => !open && setSelectedRecord(null)}>
        <SheetContent
          side="right"
          size="lg"
          label={t('usage.detail.title', { defaultValue: 'Call log detail' })}
          className="max-w-[calc(100vw-var(--safe-left)-var(--safe-right))]"
        >
          <SheetHeader className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <SheetTitle>{t('usage.detail.title', { defaultValue: 'Call log detail' })}</SheetTitle>
              <p className="mt-1.5 truncate font-mono text-[12px] text-[var(--color-fg-muted)]">#{selectedRecord?.id}</p>
            </div>
            <SheetClose asChild>
              <button
                type="button"
                aria-label={t('common.close', { defaultValue: 'Close' })}
                className="inline-flex size-8 shrink-0 items-center justify-center rounded-[6px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <X size={16} aria-hidden />
              </button>
            </SheetClose>
          </SheetHeader>
          <SheetBody className="space-y-5 pb-6">
            <dl className="grid grid-cols-2 gap-x-4 gap-y-4 text-[12px]">
              <DetailItem label={t('usage.table.id', { defaultValue: 'ID' })} value={selectedRecord ? `#${selectedRecord.id}` : ''} mono />
              <DetailItem label={t('usage.table.time', { defaultValue: 'Time' })} value={selectedRecord ? timeFmt.format(new Date(selectedRecord.created_at * 1000)) : ''} />
              <DetailItem label={t('usage.table.user')} value={selectedRecord ? usageUserLabel(selectedRecord) : ''} />
              <DetailItem label={t('usage.detail.userId', { defaultValue: 'User ID' })} value={selectedRecord?.user_id || '—'} mono />
              <DetailItem label={t('usage.detail.email', { defaultValue: 'Email' })} value={selectedRecord?.user_email || '—'} />
              <DetailItem label={t('usage.detail.status', { defaultValue: 'Status' })} value={selectedRecord?.status === 'error' ? t('usage.statusError', { defaultValue: 'Error' }) : t('usage.status.success', { defaultValue: 'Success' })} />
              <DetailItem label={t('usage.table.firstByte', { defaultValue: 'First byte' })} value={selectedRecord ? formatDuration(selectedRecord.first_byte_ms) : ''} />
              <DetailItem label={t('usage.table.duration', { defaultValue: 'Duration' })} value={selectedRecord ? formatDuration(selectedRecord.duration_ms) : ''} />
              <DetailItem label={t('usage.table.model')} value={selectedRecord ? modelLabel(selectedRecord.model_id) : ''} />
              <DetailItem label={t('usage.table.channel', { defaultValue: 'Channel' })} value={selectedRecord?.channel_name || selectedRecord?.channel_id || '—'} />
              <DetailItem label={t('usage.table.purpose')} value={selectedRecord ? purposeLabel(selectedRecord.purpose) : ''} />
              <DetailItem label={t('usage.detail.workspace', { defaultValue: 'Workspace' })} value={selectedRecord?.workspace_name || selectedRecord?.workspace_id || '—'} />
              <DetailItem label={t('usage.table.in')} value={selectedRecord ? selectedRecord.input_tokens.toLocaleString() : ''} />
              <DetailItem label={t('usage.table.out')} value={selectedRecord ? selectedRecord.output_tokens.toLocaleString() : ''} />
              <DetailItem label={t('usage.table.cost')} value={selectedRecord ? `${selectedRecord.cost.toFixed(6)} ${selectedRecord.currency || 'USD'}` : ''} />
              <DetailItem label={t('usage.table.credits', { defaultValue: 'Credits' })} value={selectedRecord ? formatCredits(selectedRecord.credits) : ''} />
              <DetailItem label={t('usage.detail.fallback', { defaultValue: 'Fallback channel' })} value={selectedRecord?.fallback ? t('common.yes', { defaultValue: 'Yes' }) : t('common.no', { defaultValue: 'No' })} />
              {selectedRecord?.ttft_fallback_model ? <DetailItem label={t('usage.ttftFallbackShort', { defaultValue: 'Timeout fallback' })} value={selectedRecord.ttft_fallback_model} /> : null}
            </dl>
            {selectedRecord?.conversation_id ? (
              <div>
                <h3 className="mb-1 text-[12px] font-medium text-[var(--color-fg-subtle)]">{t('usage.table.conversation', { defaultValue: 'Conversation' })}</h3>
                {selectedRecord.conversation_deleted ? (
                  <p className="text-sm text-[var(--color-fg-muted)]">{t('usage.conversationDeleted', { defaultValue: 'Deleted' })} · {selectedRecord.conversation_id}</p>
                ) : (
                  <Link
                    to={`/admin/users/${encodeURIComponent(selectedRecord.user_id)}/conversations/${encodeURIComponent(selectedRecord.conversation_id)}`}
                    className="text-sm text-[var(--color-accent)] hover:underline"
                  >
                    {selectedRecord.conversation_title || selectedRecord.conversation_id}
                  </Link>
                )}
              </div>
            ) : null}
            {selectedRecord?.aippt ? (
              <section className="space-y-3 rounded-[8px] bg-[var(--color-bg-muted)] p-3">
                <div className="flex items-center justify-between gap-3">
                  <h3 className="text-sm font-medium text-[var(--color-fg)]">{t('usage.aippt.title', { defaultValue: 'AI PPT call detail' })}</h3>
                  <span className="text-[12px] text-[var(--color-fg-muted)]">{aipptEventLabel(selectedRecord.aippt.event)}</span>
                </div>
                <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-[12px]">
                  <DetailItem label={t('usage.aippt.deckStatusLabel', { defaultValue: 'Status' })} value={aipptStatusLabel(selectedRecord.aippt.status)} />
                  <DetailItem label={t('usage.aippt.subject', { defaultValue: 'Subject' })} value={selectedRecord.aippt.subject || aipptTitle(selectedRecord)} />
                  <DetailItem label={t('usage.aippt.template', { defaultValue: 'Template' })} value={selectedRecord.aippt.template_name || '—'} />
                  <DetailItem label={t('usage.aippt.pptId', { defaultValue: 'Docmee PPT id' })} value={selectedRecord.aippt.ppt_id || '—'} mono />
                  <DetailItem label={t('usage.aippt.deckId', { defaultValue: 'Deck record id' })} value={selectedRecord.aippt.deck_id || '—'} mono />
                </dl>
              </section>
            ) : null}
            {selectedRecord?.status === 'error' ? (
              <ErrorDetailBlock
                title={t('usage.errorDetail.error', { defaultValue: 'Error' })}
                content={selectedRecord.error || t('usage.errorDetail.none', { defaultValue: 'No error detail was recorded for this request.' })}
              />
            ) : null}
            {selectedRecord?.request_method || selectedRecord?.request_url ? (
              <div className="rounded-[8px] bg-[var(--color-bg-muted)] px-3 py-2 text-[12px] text-[var(--color-fg-muted)]">
                <span className="font-medium text-[var(--color-fg)]">{selectedRecord.request_method || 'REQUEST'}</span>
                {selectedRecord.request_url ? <span className="ml-2 break-all">{selectedRecord.request_url}</span> : null}
              </div>
            ) : null}
            {selectedRecord?.request_headers || selectedRecord?.request_body ? (
              <>
                <ErrorDetailBlock title={t('usage.errorDetail.headers', { defaultValue: 'Request headers' })} content={selectedRecord.request_headers || t('usage.errorDetail.noHeaders', { defaultValue: 'No request headers were recorded.' })} />
                <ErrorDetailBlock title={t('usage.errorDetail.body', { defaultValue: 'Request body' })} content={selectedRecord.request_body || t('usage.errorDetail.noBody', { defaultValue: 'No request body was recorded.' })} />
              </>
            ) : null}
          </SheetBody>
          <div className="flex justify-end px-5 py-4">
            <Button
              size="sm"
              variant="destructive"
              leadingIcon={busyId === selectedRecord?.id ? <LoaderCircle size={14} className="animate-spin" aria-hidden /> : <Trash2 size={14} aria-hidden />}
              disabled={!selectedRecord || busy || busyId !== null}
              onClick={() => selectedRecord && void deleteOne(selectedRecord.id)}
            >
              {t('usage.deleteRow', { defaultValue: 'Delete record' })}
            </Button>
          </div>
        </SheetContent>
      </Sheet>

      <Dialog open={confirmBulk} onOpenChange={(open) => !busy && setConfirmBulk(open)}>
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
      <pre className="max-h-[34vh] overflow-auto rounded-[8px] bg-[var(--color-bg-muted)] p-3 text-[12px] leading-relaxed text-[var(--color-fg-muted)] whitespace-pre-wrap break-words">
        {content}
      </pre>
    </section>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-[12px] bg-[var(--color-surface)] p-3 sm:p-4">
      <div className="text-[12px] text-[var(--color-fg-subtle)]">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums tracking-normal text-[var(--color-fg)] sm:text-2xl">{value}</div>
    </div>
  )
}

/** One label/value pair of the AI PPT call-detail dialog. */
function DetailItem({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-[12px] text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className={cn('mt-0.5 break-all text-[var(--color-fg)]', mono && 'font-mono text-[12px]')}>{value}</dd>
    </div>
  )
}

/** Credits are fractional for token-metered rows; keep the number honest. */
function formatCredits(value: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 6 }).format(value)
}

function formatDuration(ms?: number): string {
  if (!ms || ms < 0) return '—'
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
}
