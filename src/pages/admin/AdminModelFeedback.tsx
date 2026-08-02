import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowUpRight, ChevronRight, ThumbsDown, ThumbsUp, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type {
  ApiAdminMessageFeedbackItem,
  ApiAdminMessageFeedbackModel,
  ApiAdminMessageFeedbackPage,
} from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  Sheet,
  SheetBody,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { cn } from '@/lib/utils'
import { FEEDBACK_REASON_VALUES, type FeedbackReason } from '@/types/chat'

const PAGE_SIZE = 50
const MIN_QUALITY_SAMPLE = 20
const ALL = 'all'
type RatingFilter = typeof ALL | 'like' | 'dislike'

interface AdminModelFeedbackProps {
  days: number
}

export function AdminModelFeedback({ days }: AdminModelFeedbackProps) {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [rating, setRating] = useState<RatingFilter>('dislike')
  const [modelId, setModelId] = useState(ALL)
  const [reason, setReason] = useState<typeof ALL | FeedbackReason>(ALL)
  const [page, setPage] = useState(1)
  const [data, setData] = useState<ApiAdminMessageFeedbackPage | null>(null)
  const [modelOptions, setModelOptions] = useState<Array<{ id: string; label: string }>>([])
  const [selected, setSelected] = useState<ApiAdminMessageFeedbackItem | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const requestRef = useRef(0)

  const load = useCallback(async () => {
    const request = ++requestRef.current
    setLoading(true)
    setError('')
    try {
      const result = await adminApi.messageFeedback({
        days,
        rating: rating === ALL ? '' : rating,
        modelId: modelId === ALL ? undefined : modelId,
        reason: reason === ALL ? undefined : reason,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      })
      if (request === requestRef.current) setData(result)
    } catch (cause) {
      if (request === requestRef.current) {
        setData(null)
        setError(cause instanceof ApiError ? cause.message : t('common:common.error'))
      }
    } finally {
      if (request === requestRef.current) setLoading(false)
    }
  }, [days, modelId, page, rating, reason, t])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(
    () => () => {
      requestRef.current += 1
    },
    [],
  )

  useEffect(() => {
    setPage(1)
  }, [days])

  useEffect(() => {
    let active = true
    void adminApi
      .models('chat')
      .then((models) => {
        if (active) setModelOptions(models.map((model) => ({ id: model.id, label: model.label })))
      })
      .catch(() => {
        // The feedback response still carries model labels, so this filter can
        // degrade to the models represented in the current result.
      })
    return () => {
      active = false
    }
  }, [])

  const availableModels = useMemo(() => {
    const merged = new Map(modelOptions.map((model) => [model.id, model.label]))
    for (const model of data?.by_model ?? []) {
      if (model.model_id) merged.set(model.model_id, model.model_label || model.model_id)
    }
    return Array.from(merged, ([id, label]) => ({ id, label }))
  }, [data?.by_model, modelOptions])

  const pageCount = Math.max(1, Math.ceil((data?.total ?? 0) / PAGE_SIZE))
  const dateTimeFormat = useMemo(
    () => new Intl.DateTimeFormat(i18n.language || undefined, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  )
  const numberFormat = useMemo(() => new Intl.NumberFormat(i18n.language || undefined), [i18n.language])

  function updateRating(value: string) {
    setRating(value as RatingFilter)
    setPage(1)
  }

  function updateModel(value: string) {
    setModelId(value)
    setPage(1)
  }

  function updateReason(value: string) {
    setReason(value as typeof ALL | FeedbackReason)
    setPage(1)
  }

  function reasonLabel(value: string): string {
    return t(`analytics.feedback.reasons.${value}`, { defaultValue: value.replaceAll('_', ' ') })
  }

  function formatDate(value: number): string {
    if (!value) return '—'
    return dateTimeFormat.format(new Date(value > 1_000_000_000_000 ? value : value * 1000))
  }

  function formatPercent(value: number): string {
    const percent = value <= 1 ? value * 100 : value
    return `${percent.toLocaleString(i18n.language || undefined, { maximumFractionDigits: 1 })}%`
  }

  function formatLatency(value: number): string {
    if (value <= 0) return '—'
    if (value >= 1000) {
      return `${(value / 1000).toLocaleString(i18n.language || undefined, { maximumFractionDigits: 1 })}s`
    }
    return `${numberFormat.format(value)}ms`
  }

  function itemMetrics(item: ApiAdminMessageFeedbackItem): string {
    return t('analytics.feedback.list.metrics', {
      latency: formatLatency(item.gen_ms),
      tokens: numberFormat.format(item.input_tokens + item.output_tokens),
    })
  }

  function modelLabel(item: ApiAdminMessageFeedbackItem): string {
    return item.model_label || availableModels.find((model) => model.id === item.model_id)?.label || item.model_id || '—'
  }

  const summary = data?.summary

  return (
    <div className="min-w-0">
      <div className="mt-5 w-full sm:w-64">
        <Filter label={t('analytics.feedback.filters.model')}>
          <Select value={modelId} onValueChange={updateModel}>
            <SelectTrigger aria-label={t('analytics.feedback.filters.model')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('analytics.feedback.filters.allModels')}</SelectItem>
              {availableModels.map((model) => (
                <SelectItem key={model.id} value={model.id}>
                  {model.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Filter>
      </div>

      {loading && !data ? (
        <PanelFallback />
      ) : error ? (
        <section
          role="alert"
          className="mt-6 flex flex-col items-start gap-3 rounded-[12px] bg-[var(--color-danger-soft)] px-4 py-4 text-sm text-[var(--color-danger)] sm:flex-row sm:items-center sm:justify-between"
        >
          <div className="min-w-0">
            <p className="font-medium">{t('analytics.feedback.error.title')}</p>
            <p className="mt-1 break-words text-[12px] leading-5">{error}</p>
          </div>
          <Button variant="secondary" size="sm" onClick={() => void load()} className="shrink-0">
            {t('common:actions.tryAgain')}
          </Button>
        </section>
      ) : data ? (
        <div
          aria-busy={loading || undefined}
          className={cn('transition-opacity', loading && 'pointer-events-none opacity-60')}
        >
          <section className="mt-6 grid grid-cols-2 gap-2.5 sm:grid-cols-3 sm:gap-3 xl:grid-cols-5">
            <FeedbackStat
              label={t('analytics.feedback.stats.evaluated')}
              value={numberFormat.format(summary?.total ?? 0)}
            />
            <FeedbackStat label={t('analytics.feedback.stats.likes')} value={numberFormat.format(summary?.likes ?? 0)} />
            <FeedbackStat label={t('analytics.feedback.stats.dislikes')} value={numberFormat.format(summary?.dislikes ?? 0)} />
            <FeedbackStat
              label={t('analytics.feedback.stats.positiveRate')}
              value={summary?.total ? formatPercent(summary.positive_rate) : '—'}
            />
            <FeedbackStat
              label={t('analytics.feedback.stats.coverage')}
              value={summary?.assistant_messages ? formatPercent(summary.coverage) : '—'}
              detail={t('analytics.feedback.stats.coverageDetail', {
                evaluated: numberFormat.format(summary?.rated_messages ?? summary?.total ?? 0),
                replies: numberFormat.format(summary?.assistant_messages ?? 0),
              })}
              className="col-span-2 sm:col-span-1"
            />
          </section>

          <ModelQualityTable
            rows={data.by_model}
            formatPercent={formatPercent}
            formatNumber={numberFormat.format}
            reasonLabel={reasonLabel}
          />

          <section className="mt-6 min-w-0 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            <div className="border-b border-[var(--color-divider)] px-4 py-4 sm:px-5">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <h2 className="text-sm font-medium text-[var(--color-fg)]">
                  {rating === 'dislike'
                    ? t('analytics.feedback.list.dislikesTitle')
                    : t('analytics.feedback.list.title')}
                </h2>
                <Badge size="xs" variant="neutral">
                  {t('analytics.feedback.list.count', { count: numberFormat.format(data.total) })}
                </Badge>
              </div>
              <p className="mt-1 text-[12px] leading-5 text-[var(--color-fg-muted)]">
                {t('analytics.feedback.list.lead')}
              </p>
              <div className="mt-3 grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
                <Filter label={t('analytics.feedback.filters.rating')}>
                  <Select value={rating} onValueChange={updateRating}>
                    <SelectTrigger aria-label={t('analytics.feedback.filters.rating')}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="dislike">{t('analytics.feedback.filters.dislikes')}</SelectItem>
                      <SelectItem value={ALL}>{t('analytics.feedback.filters.allRatings')}</SelectItem>
                      <SelectItem value="like">{t('analytics.feedback.filters.likes')}</SelectItem>
                    </SelectContent>
                  </Select>
                </Filter>
                <Filter label={t('analytics.feedback.filters.reason')}>
                  <Select value={reason} onValueChange={updateReason}>
                    <SelectTrigger aria-label={t('analytics.feedback.filters.reason')}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL}>{t('analytics.feedback.filters.allReasons')}</SelectItem>
                      {FEEDBACK_REASON_VALUES.map((value) => (
                        <SelectItem key={value} value={value}>
                          {reasonLabel(value)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Filter>
              </div>
            </div>

            {data.items.length === 0 ? (
              <div className="px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
                {rating === 'dislike'
                  ? t('analytics.feedback.list.emptyDislikes')
                  : t('analytics.feedback.list.empty')}
              </div>
            ) : (
              <ul className="divide-y divide-[var(--color-divider)]">
                {data.items.map((item) => (
                  <li key={item.id}>
                    <button
                      type="button"
                      onClick={() => setSelected(item)}
                      className="group grid w-full min-w-0 grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-2 px-4 py-3.5 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] sm:px-5 xl:grid-cols-[9rem_10rem_minmax(12rem,1fr)_minmax(8rem,auto)_8.5rem_1.5rem] xl:items-center xl:gap-3"
                    >
                      <span className="text-[11.5px] tabular-nums text-[var(--color-fg-subtle)] xl:text-[12px]">
                        {formatDate(item.updated_at)}
                      </span>
                      <span className="col-start-1 row-start-2 min-w-0 truncate text-[12px] font-medium text-[var(--color-fg)] xl:col-start-2 xl:row-start-1">
                        {modelLabel(item)}
                      </span>
                      <span className="col-span-2 col-start-1 row-start-3 min-w-0 xl:col-span-1 xl:col-start-3 xl:row-start-1">
                        <span className="block line-clamp-2 break-words text-[13px] leading-5 text-[var(--color-fg)] xl:line-clamp-1">
                          {item.question || t('analytics.feedback.list.noQuestion')}
                        </span>
                        <span className="mt-0.5 block line-clamp-1 break-words text-[11.5px] leading-4 text-[var(--color-fg-muted)]">
                          {item.response || t('analytics.feedback.list.noResponse')}
                        </span>
                      </span>
                      <span className="col-span-2 col-start-1 row-start-4 flex min-w-0 flex-wrap gap-1 xl:col-span-1 xl:col-start-4 xl:row-start-1 xl:flex-nowrap xl:overflow-hidden">
                        <RatingBadge rating={item.rating} />
                        {item.reasons.slice(0, 1).map((itemReason) => (
                          <Badge key={itemReason} size="xs" variant="neutral" className="max-w-full truncate">
                            {reasonLabel(itemReason)}
                          </Badge>
                        ))}
                        {item.reasons.length > 1 ? (
                          <Badge size="xs" variant="neutral">+{item.reasons.length - 1}</Badge>
                        ) : null}
                      </span>
                      <span
                        className="col-start-2 row-start-2 min-w-0 max-w-[10rem] truncate whitespace-nowrap text-right text-[11px] tabular-nums text-[var(--color-fg-subtle)] sm:max-w-[14rem] xl:col-start-5 xl:row-start-1 xl:max-w-none xl:text-[11.5px]"
                        title={itemMetrics(item)}
                        aria-label={itemMetrics(item)}
                      >
                        {itemMetrics(item)}
                      </span>
                      <ChevronRight
                        size={15}
                        aria-hidden
                        className="col-start-2 row-start-1 self-center text-[var(--color-fg-faint)] transition-transform group-hover:translate-x-0.5 group-hover:text-[var(--color-fg-muted)] xl:col-start-6"
                      />
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <Pagination page={page} pageCount={pageCount} onPage={setPage} />
        </div>
      ) : null}

      <FeedbackDetail
        item={selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        formatDate={formatDate}
        modelLabel={() => (selected ? modelLabel(selected) : '—')}
        reasonLabel={reasonLabel}
      />
    </div>
  )
}

function Filter({ label, className, children }: { label: string; className?: string; children: React.ReactNode }) {
  return (
    <div className={cn('min-w-0', className)}>
      <span className="mb-1 block text-[12px] text-[var(--color-fg-subtle)]">{label}</span>
      {children}
    </div>
  )
}

function FeedbackStat({
  label,
  value,
  detail,
  className,
}: {
  label: string
  value: string
  detail?: string
  className?: string
}) {
  return (
    <div className={cn('min-w-0 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 sm:p-4', className)}>
      <div className="break-words text-[11px] uppercase text-[var(--color-fg-subtle)] sm:text-[12px]">
        {label}
      </div>
      <div className="mt-1 break-all font-serif text-xl tabular-nums text-[var(--color-fg)] sm:text-2xl">{value}</div>
      {detail ? <p className="mt-1 truncate text-[10.5px] text-[var(--color-fg-subtle)]">{detail}</p> : null}
    </div>
  )
}

function ModelQualityTable({
  rows,
  formatPercent,
  formatNumber,
  reasonLabel,
}: {
  rows: ApiAdminMessageFeedbackModel[]
  formatPercent: (value: number) => string
  formatNumber: (value: number) => string
  reasonLabel: (value: string) => string
}) {
  const { t } = useTranslation('admin')
  return (
    <section className="mt-6 min-w-0 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
      <div className="border-b border-[var(--color-divider)] px-4 py-4 sm:px-5">
        <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('analytics.feedback.quality.title')}</h2>
        <p className="mt-1 text-[12px] leading-5 text-[var(--color-fg-muted)]">{t('analytics.feedback.quality.lead')}</p>
      </div>
      {rows.length === 0 ? (
        <div className="px-6 py-8 text-center text-sm text-[var(--color-fg-muted)]">
          {t('analytics.feedback.quality.empty')}
        </div>
      ) : (
        <>
          <div className="hidden lg:block">
            <table className="w-full table-fixed text-[12.5px] tabular-nums">
              <thead className="bg-[var(--color-bg-muted)] text-[11.5px] text-[var(--color-fg-subtle)]">
                <tr>
                  <th className="w-[27%] px-5 py-2.5 text-left font-medium">{t('analytics.feedback.quality.model')}</th>
                  <th className="w-[13%] px-3 py-2.5 text-right font-medium">{t('analytics.feedback.quality.evaluated')}</th>
                  <th className="w-[17%] px-3 py-2.5 text-right font-medium">{t('analytics.feedback.quality.sentiment')}</th>
                  <th className="w-[18%] px-3 py-2.5 text-right font-medium">{t('analytics.feedback.quality.positiveRate')}</th>
                  <th className="w-[25%] px-5 py-2.5 text-left font-medium">{t('analytics.feedback.quality.topReason')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--color-divider)]">
                {rows.map((row) => (
                  <tr key={row.model_id}>
                    <td className="truncate px-5 py-3 font-medium text-[var(--color-fg)]" title={row.model_label || row.model_id}>
                      {row.model_label || row.model_id}
                    </td>
                    <td className="px-3 py-3 text-right text-[var(--color-fg-muted)]">{formatNumber(row.total)}</td>
                    <td className="px-3 py-3 text-right">
                      <span className="text-[var(--color-success)]">{formatNumber(row.likes)}</span>
                      <span className="mx-1 text-[var(--color-fg-faint)]">/</span>
                      <span className="text-[var(--color-danger)]">{formatNumber(row.dislikes)}</span>
                    </td>
                    <td className="px-3 py-3 text-right">
                      {!(row.sample_sufficient ?? row.total >= MIN_QUALITY_SAMPLE) ? (
                        <span className="text-[11px] text-[var(--color-fg-subtle)]">
                          {t('analytics.feedback.quality.sampleInsufficient', { count: MIN_QUALITY_SAMPLE })}
                        </span>
                      ) : (
                        <span className="font-medium text-[var(--color-fg)]">{formatPercent(row.positive_rate)}</span>
                      )}
                    </td>
                    <td className="truncate px-5 py-3 text-[var(--color-fg-muted)]">
                      {row.top_reason ? reasonLabel(row.top_reason) : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <ul className="divide-y divide-[var(--color-divider)] lg:hidden">
            {rows.map((row) => (
              <li key={row.model_id} className="px-4 py-3.5 sm:px-5">
                <div className="flex min-w-0 items-start justify-between gap-3">
                  <span className="min-w-0 truncate text-[13px] font-medium text-[var(--color-fg)]">
                    {row.model_label || row.model_id}
                  </span>
                  {!(row.sample_sufficient ?? row.total >= MIN_QUALITY_SAMPLE) ? (
                    <Badge size="xs" variant="neutral">
                      {t('analytics.feedback.quality.sampleInsufficient', { count: MIN_QUALITY_SAMPLE })}
                    </Badge>
                  ) : (
                    <span className="shrink-0 text-sm font-medium tabular-nums text-[var(--color-fg)]">
                      {formatPercent(row.positive_rate)}
                    </span>
                  )}
                </div>
                <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-[var(--color-fg-subtle)]">
                  <span>{t('analytics.feedback.quality.evaluatedValue', { count: formatNumber(row.total) })}</span>
                  <span className="text-[var(--color-success)]">
                    {t('analytics.feedback.quality.likesValue', { count: formatNumber(row.likes) })}
                  </span>
                  <span className="text-[var(--color-danger)]">
                    {t('analytics.feedback.quality.dislikesValue', { count: formatNumber(row.dislikes) })}
                  </span>
                </div>
                {row.top_reason ? (
                  <p className="mt-1.5 truncate text-[11.5px] text-[var(--color-fg-muted)]">
                    {t('analytics.feedback.quality.topReasonValue', { reason: reasonLabel(row.top_reason) })}
                  </p>
                ) : null}
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  )
}

function RatingBadge({ rating }: { rating: 'like' | 'dislike' }) {
  const { t } = useTranslation('admin')
  return rating === 'like' ? (
    <Badge size="xs" variant="success" leadingIcon={<ThumbsUp size={10} aria-hidden />}>
      {t('analytics.feedback.rating.like')}
    </Badge>
  ) : (
    <Badge size="xs" variant="danger" leadingIcon={<ThumbsDown size={10} aria-hidden />}>
      {t('analytics.feedback.rating.dislike')}
    </Badge>
  )
}

function FeedbackDetail({
  item,
  onOpenChange,
  formatDate,
  modelLabel,
  reasonLabel,
}: {
  item: ApiAdminMessageFeedbackItem | null
  onOpenChange: (open: boolean) => void
  formatDate: (value: number) => string
  modelLabel: () => string
  reasonLabel: (value: string) => string
}) {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const numberFormat = useMemo(() => new Intl.NumberFormat(i18n.language || undefined), [i18n.language])
  if (!item) return null

  const totalTokens =
    item.input_tokens == null && item.output_tokens == null
      ? null
      : (item.input_tokens ?? 0) + (item.output_tokens ?? 0)
  const hasContextFlags = item.has_tools || item.has_files || item.has_rag || item.fallback
  const hasGenerationError = item.status === 'error' || Boolean(item.error)
  const recordedCost = item.cost
  const recordedCurrency = item.currency
  const conversationOwnerId = item.conversation_owner_id || item.user_id
  const toolNames = item.tool_names?.join(', ') || ''
  const fileNames = item.file_names?.join(', ') || ''
  const citationTitles = item.citation_titles?.join(', ') || ''

  function formatCost(): string {
    if (recordedCost == null) return '—'
    const currency = (recordedCurrency || 'USD').toUpperCase()
    try {
      return new Intl.NumberFormat(i18n.language || undefined, {
        style: 'currency',
        currency,
        minimumFractionDigits: recordedCost > 0 && recordedCost < 0.01 ? 4 : 2,
        maximumFractionDigits: 6,
      }).format(recordedCost)
    } catch {
      return `${numberFormat.format(recordedCost)} ${currency}`
    }
  }

  const metadata = [
    [t('analytics.feedback.detail.conversation'), item.conversation_title || item.conversation_id || '—'],
    [t('analytics.feedback.detail.model'), modelLabel()],
    [t('analytics.feedback.detail.channel'), item.channel_name || item.channel_id || '—'],
    [t('analytics.feedback.detail.provider'), item.provider || '—'],
    [t('analytics.feedback.detail.user'), item.user_name || item.user_email || item.user_id || '—'],
    [t('analytics.feedback.detail.workspace'), item.workspace_name || item.workspace_id || '—'],
    [t('analytics.feedback.detail.createdAt'), formatDate(item.created_at)],
    [t('analytics.feedback.detail.updatedAt'), formatDate(item.updated_at)],
    [
      t('analytics.feedback.detail.messageCreatedAt'),
      item.message_created_at == null ? '—' : formatDate(item.message_created_at),
    ],
    [t('analytics.feedback.detail.timing'), item.gen_ms <= 0 ? '—' : `${numberFormat.format(item.gen_ms)} ms`],
    [
      t('analytics.feedback.detail.tokens'),
      item.input_tokens == null && item.output_tokens == null
        ? '—'
        : `${numberFormat.format(item.input_tokens ?? 0)} / ${numberFormat.format(item.output_tokens ?? 0)}`,
    ],
    [t('analytics.feedback.detail.totalTokens'), totalTokens == null ? '—' : numberFormat.format(totalTokens)],
    [
      t('analytics.feedback.detail.cacheTokens'),
      item.cache_read_tokens == null && item.cache_write_tokens == null
        ? '—'
        : `${numberFormat.format(item.cache_read_tokens ?? 0)} / ${numberFormat.format(item.cache_write_tokens ?? 0)}`,
    ],
    [t('analytics.feedback.detail.cost'), formatCost()],
    [t('analytics.feedback.detail.credits'), item.credits == null ? '—' : numberFormat.format(item.credits)],
  ]

  return (
    <Sheet open onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-[min(32rem,100vw)]">
        <SheetHeader className="relative border-b border-[var(--color-divider)] pr-14">
          <SheetTitle>{t('analytics.feedback.detail.title')}</SheetTitle>
          <SheetDescription>{t('analytics.feedback.detail.lead')}</SheetDescription>
          <SheetClose asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t('common:actions.close')}
              className="absolute right-4 top-4"
            >
              <X size={17} aria-hidden />
            </Button>
          </SheetClose>
        </SheetHeader>
        <SheetBody className="px-4 py-0 sm:px-5">
          <section className="py-4">
            <div className="flex flex-wrap items-center gap-2">
              <RatingBadge rating={item.rating} />
              {item.status ? (
                <Badge size="xs" variant={hasGenerationError ? 'danger' : 'neutral'}>
                  {hasGenerationError
                    ? t('analytics.feedback.detail.statusError')
                    : item.status === 'complete'
                      ? t('analytics.feedback.detail.statusSuccess')
                      : item.status}
                </Badge>
              ) : null}
              {item.reasons.map((itemReason) => (
                <Badge key={itemReason} size="xs" variant="neutral">
                  {reasonLabel(itemReason)}
                </Badge>
              ))}
            </div>
            {item.comment ? (
              <div className="mt-3">
                <h3 className="text-[11.5px] font-medium text-[var(--color-fg-subtle)]">
                  {t('analytics.feedback.detail.comment')}
                </h3>
                <p className="mt-1 whitespace-pre-wrap break-words text-[13px] leading-5 text-[var(--color-fg)]">
                  {item.comment}
                </p>
              </div>
            ) : null}
            {hasContextFlags ? (
              <div className="mt-3 flex flex-wrap gap-1.5" aria-label={t('analytics.feedback.detail.context')}>
                {item.has_tools ? (
                  <Badge
                    size="xs"
                    variant="info"
                    title={toolNames || undefined}
                    aria-label={toolNames ? `${t('analytics.feedback.detail.hasTools')}: ${toolNames}` : undefined}
                  >
                    {t('analytics.feedback.detail.hasTools')}
                  </Badge>
                ) : null}
                {item.has_files ? (
                  <Badge
                    size="xs"
                    variant="neutral"
                    title={fileNames || undefined}
                    aria-label={fileNames ? `${t('analytics.feedback.detail.hasFiles')}: ${fileNames}` : undefined}
                  >
                    {t('analytics.feedback.detail.hasFiles')}
                  </Badge>
                ) : null}
                {item.has_rag ? (
                  <Badge
                    size="xs"
                    variant="sage"
                    title={citationTitles || undefined}
                    aria-label={citationTitles ? `${t('analytics.feedback.detail.hasRag')}: ${citationTitles}` : undefined}
                  >
                    {t('analytics.feedback.detail.hasRag')}
                  </Badge>
                ) : null}
                {item.fallback ? <Badge size="xs" variant="warning">{t('analytics.feedback.detail.fallback')}</Badge> : null}
              </div>
            ) : null}
            {item.error ? (
              <div className="mt-3 rounded-[8px] bg-[var(--color-danger-soft)] px-3 py-2.5 text-[12px] leading-5 text-[var(--color-danger)]" role="alert">
                <span className="font-medium">{t('analytics.feedback.detail.error')}</span>
                <p className="mt-1 whitespace-pre-wrap break-words [overflow-wrap:anywhere]">{item.error}</p>
              </div>
            ) : null}
          </section>

          <TextDetail title={t('analytics.feedback.detail.question')} content={item.question} />
          <TextDetail title={t('analytics.feedback.detail.response')} content={item.response} />

          <section className="border-t border-[var(--color-divider)] py-4">
            <h3 className="text-[12px] font-medium text-[var(--color-fg)]">
              {t('analytics.feedback.detail.metadata')}
            </h3>
            <dl className="mt-3 grid min-w-0 gap-x-5 gap-y-3 sm:grid-cols-2">
              {metadata.map(([label, value]) => (
                <div key={label} className="min-w-0">
                  <dt className="text-[10.5px] text-[var(--color-fg-subtle)]">{label}</dt>
                  <dd className="mt-0.5 break-words text-[12px] leading-5 text-[var(--color-fg)]">{value}</dd>
                </div>
              ))}
            </dl>
          </section>

          {conversationOwnerId && item.conversation_id ? (
            <div className="border-t border-[var(--color-divider)] py-4">
              <Button variant="secondary" size="sm" asChild trailingIcon={<ArrowUpRight size={13} aria-hidden />}>
                <Link
                  to={`/admin/users/${encodeURIComponent(conversationOwnerId)}/conversations/${encodeURIComponent(item.conversation_id)}`}
                  onClick={() => onOpenChange(false)}
                >
                  {t('analytics.feedback.detail.openConversation')}
                </Link>
              </Button>
            </div>
          ) : null}
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}

function TextDetail({ title, content }: { title: string; content: string }) {
  const { t } = useTranslation('admin')
  return (
    <section className="border-t border-[var(--color-divider)] py-4">
      <h3 className="text-[12px] font-medium text-[var(--color-fg)]">{title}</h3>
      <p className="mt-2 whitespace-pre-wrap break-words text-[13px] leading-6 text-[var(--color-fg-muted)] [overflow-wrap:anywhere]">
        {content || t('analytics.feedback.detail.notRecorded')}
      </p>
    </section>
  )
}
