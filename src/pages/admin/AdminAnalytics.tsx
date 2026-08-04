import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import {
  AlertCircle,
  ArrowDownRight,
  ArrowUpRight,
  ExternalLink,
  Info,
  Minus,
  RefreshCw,
  Search,
  X,
} from 'lucide-react'
import { adminApi, ApiError, type AdminAnalyticsParams } from '@/api'
import type {
  ApiAnalytics,
  ApiAnalyticsDimension,
  ApiUsageBreakdownRow,
  ApiUsageTotals,
} from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'
import {
  alignedAnalyticsTrend,
  inputOutputTokens,
  periodChange,
  processedTokens,
  retainSelectedOption,
  safeRatio,
  type AnalyticsMetric,
} from '@/lib/admin-analytics'
import { cn } from '@/lib/utils'
import { useLanguage } from '@/store/language'
import { AdminModelFeedback } from './AdminModelFeedback'

const RANGE_IDS = ['1', '7', '30', '90', '365'] as const
const METRICS: AnalyticsMetric[] = ['turns', 'tokens', 'cost', 'credits', 'users']
const DIMENSIONS: ApiAnalyticsDimension[] = ['user', 'model', 'workspace', 'purpose', 'channel']
const PAGE_SIZE = 20
const ALL_FILTERS = '__all__'
const PERSONAL_WORKSPACE = '__personal__'
const UNATTRIBUTED_CHANNEL = '__unattributed__'

type AnalyticsView = 'usage' | 'feedback'
type BreakdownSort = 'cost' | 'turns' | 'calls' | 'tokens' | 'credits'
type FilterDimension = Exclude<ApiAnalyticsDimension, 'user'>
type SelectedFilterLabel = { value: string; label: string }

function clampRatio(value: number) {
  return Math.min(1, Math.max(0, value))
}

export default function AdminAnalytics() {
  const { t } = useTranslation(['admin', 'common'])
  const lang = useLanguage((state) => state.lang)
  const [view, setView] = useState<AnalyticsView>('usage')
  const [days, setDays] = useState('30')
  const [metric, setMetric] = useState<AnalyticsMetric>('turns')
  const [userQuery, setUserQuery] = useState('')
  const [debouncedUserQuery, setDebouncedUserQuery] = useState('')
  const [modelFilter, setModelFilter] = useState(ALL_FILTERS)
  const [workspaceFilter, setWorkspaceFilter] = useState(ALL_FILTERS)
  const [purposeFilter, setPurposeFilter] = useState(ALL_FILTERS)
  const [channelFilter, setChannelFilter] = useState(ALL_FILTERS)
  const [data, setData] = useState<ApiAnalytics | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [refreshVersion, setRefreshVersion] = useState(0)
  const requestRef = useRef(0)
  const selectedFilterLabelsRef = useRef<Partial<Record<FilterDimension, SelectedFilterLabel>>>({})

  useEffect(() => {
    const timeout = window.setTimeout(() => setDebouncedUserQuery(userQuery.trim()), 350)
    return () => window.clearTimeout(timeout)
  }, [userQuery])

  const analyticsParams = useMemo<AdminAnalyticsParams>(() => ({
    days: Number(days),
    user: debouncedUserQuery || undefined,
    model: modelFilter === ALL_FILTERS ? undefined : modelFilter,
    workspace: workspaceFilter === ALL_FILTERS ? undefined : workspaceFilter,
    purpose: purposeFilter === ALL_FILTERS ? undefined : purposeFilter,
    channel: channelFilter === ALL_FILTERS ? undefined : channelFilter,
  }), [channelFilter, days, debouncedUserQuery, modelFilter, purposeFilter, workspaceFilter])

  const load = useCallback(async () => {
    const request = ++requestRef.current
    setLoading(true)
    setLoadError('')
    try {
      const next = await adminApi.analytics(analyticsParams)
      if (request !== requestRef.current) return
      setData(next)
    } catch (error) {
      if (request !== requestRef.current) return
      const message = error instanceof ApiError ? error.message : t('admin:analytics.error.title')
      setLoadError(message)
      toast.error(message)
    } finally {
      if (request === requestRef.current) setLoading(false)
    }
  }, [analyticsParams, t])

  useEffect(() => {
    if (view !== 'usage') {
      requestRef.current += 1
      return
    }
    void load()
    return () => {
      requestRef.current += 1
    }
  }, [load, refreshVersion, view])

  const numberFormat = useMemo(() => new Intl.NumberFormat(lang, { maximumFractionDigits: 0 }), [lang])
  const compactFormat = useMemo(
    () => new Intl.NumberFormat(lang, { notation: 'compact', maximumFractionDigits: 1 }),
    [lang],
  )
  const decimalFormat = useMemo(
    () => new Intl.NumberFormat(lang, { maximumFractionDigits: 4 }),
    [lang],
  )
  const percentFormat = useMemo(
    () => new Intl.NumberFormat(lang, { style: 'percent', maximumFractionDigits: 1 }),
    [lang],
  )
  const deltaPercentFormat = useMemo(
    () => new Intl.NumberFormat(lang, { style: 'percent', maximumFractionDigits: 1, signDisplay: 'exceptZero' }),
    [lang],
  )
  const costFormat = useMemo(
    () => new Intl.NumberFormat(lang, { style: 'currency', currency: 'USD', maximumFractionDigits: 4 }),
    [lang],
  )
  const dateTimeFormat = useMemo(
    () => new Intl.DateTimeFormat(lang, { dateStyle: 'medium', timeStyle: 'short' }),
    [lang],
  )

  const formatMetric = useCallback(
    (selected: AnalyticsMetric, value: number) => {
      if (selected === 'cost') return costFormat.format(value)
      if (selected === 'credits') return decimalFormat.format(value)
      return compactFormat.format(value)
    },
    [compactFormat, costFormat, decimalFormat],
  )

  const availableFilterOptions = useMemo(() => {
    const build = (dimension: ApiAnalyticsDimension) => {
      const options: Array<{ value: string; label: string }> = []
      for (const row of data?.filter_options?.[dimension] ?? []) {
        let value = row.key
        if (!value && dimension === 'workspace') value = PERSONAL_WORKSPACE
        if (!value && dimension === 'channel') value = UNATTRIBUTED_CHANNEL
        if (!value) continue

        let label = row.label || row.key
        if (!row.key && dimension === 'workspace') label = t('admin:analytics.labels.personal')
        else if (!row.key && dimension === 'channel') label = t('admin:analytics.labels.unattributedChannel')
        else if (dimension === 'model' && !row.label) {
          label = t('admin:analytics.labels.deletedModel', { id: row.key })
        } else if (dimension === 'purpose') {
          label = t(`admin:usage.purposes.${row.key.replaceAll('.', '_')}`, {
            defaultValue: row.label || row.key,
          })
        }
        options.push({ value, label })
      }
      return options
    }
    return {
      model: build('model'),
      workspace: build('workspace'),
      purpose: build('purpose'),
      channel: build('channel'),
    }
  }, [data?.filter_options, t])

  useEffect(() => {
    const selections: Array<[FilterDimension, string]> = [
      ['model', modelFilter],
      ['workspace', workspaceFilter],
      ['purpose', purposeFilter],
      ['channel', channelFilter],
    ]
    for (const [dimension, selected] of selections) {
      const option = availableFilterOptions[dimension].find((item) => item.value === selected)
      if (option) selectedFilterLabelsRef.current[dimension] = option
    }
  }, [availableFilterOptions, channelFilter, modelFilter, purposeFilter, workspaceFilter])

  const filterOptions = useMemo(() => {
    const fallback = (dimension: FilterDimension, selected: string) => {
      if (dimension === 'workspace' && selected === PERSONAL_WORKSPACE) {
        return t('admin:analytics.labels.personal')
      }
      if (dimension === 'channel' && selected === UNATTRIBUTED_CHANNEL) {
        return t('admin:analytics.labels.unattributedChannel')
      }
      if (dimension === 'purpose') {
        return t(`admin:usage.purposes.${selected.replaceAll('.', '_')}`, { defaultValue: selected })
      }
      const cached = selectedFilterLabelsRef.current[dimension]
      return cached?.value === selected ? cached.label : selected
    }
    return {
      model: retainSelectedOption(
        availableFilterOptions.model,
        modelFilter,
        ALL_FILTERS,
        fallback('model', modelFilter),
      ),
      workspace: retainSelectedOption(
        availableFilterOptions.workspace,
        workspaceFilter,
        ALL_FILTERS,
        fallback('workspace', workspaceFilter),
      ),
      purpose: retainSelectedOption(
        availableFilterOptions.purpose,
        purposeFilter,
        ALL_FILTERS,
        fallback('purpose', purposeFilter),
      ),
      channel: retainSelectedOption(
        availableFilterOptions.channel,
        channelFilter,
        ALL_FILTERS,
        fallback('channel', channelFilter),
      ),
    }
  }, [availableFilterOptions, channelFilter, modelFilter, purposeFilter, t, workspaceFilter])

  const activeFilterCount = [
    userQuery.trim(),
    modelFilter !== ALL_FILTERS,
    workspaceFilter !== ALL_FILTERS,
    purposeFilter !== ALL_FILTERS,
    channelFilter !== ALL_FILTERS,
  ].filter(Boolean).length

  const clearFilters = useCallback(() => {
    setUserQuery('')
    setDebouncedUserQuery('')
    setModelFilter(ALL_FILTERS)
    setWorkspaceFilter(ALL_FILTERS)
    setPurposeFilter(ALL_FILTERS)
    setChannelFilter(ALL_FILTERS)
  }, [])

  return (
    <div>
      <header className="flex flex-col gap-4 xl:flex-row xl:items-end xl:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
            {t('admin:analytics.title')}
          </h1>
          <p className="mt-2 max-w-3xl text-sm text-[var(--color-fg-muted)]">
            {t(view === 'usage' ? 'admin:analytics.lead' : 'admin:analytics.feedback.lead')}
          </p>
        </div>

        <div className="flex w-full flex-wrap items-center gap-2 xl:w-auto xl:justify-end">
          {view === 'usage' ? (
            <Button asChild size="sm" variant="secondary" leadingIcon={<ExternalLink size={15} aria-hidden />}>
              <Link to="/admin/usage">{t('admin:analytics.actions.usageRecords')}</Link>
            </Button>
          ) : null}
          {view === 'usage' ? (
            <Tooltip content={t('admin:analytics.actions.refresh')}>
              <Button
                size="icon"
                variant="ghost"
                aria-label={t('admin:analytics.actions.refresh')}
                loading={loading && Boolean(data)}
                onClick={() => setRefreshVersion((value) => value + 1)}
              >
                <RefreshCw size={16} aria-hidden />
              </Button>
            </Tooltip>
          ) : null}
          <div className="min-w-[10rem] flex-1 sm:flex-none">
            <Select value={days} onValueChange={setDays}>
              <SelectTrigger aria-label={t('admin:usage.filters.range')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {RANGE_IDS.map((id) => (
                  <SelectItem key={id} value={id}>
                    {t(`admin:usage.range.${id}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
      </header>

      <div className="mt-5">
        <SegmentedControl
          label={t('admin:analytics.view.label')}
          value={view}
          onChange={setView}
          fullWidthOnMobile
          options={[
            { value: 'usage', label: t('admin:analytics.view.usage') },
            { value: 'feedback', label: t('admin:analytics.view.feedback') },
          ]}
        />
      </div>

      {view === 'usage' ? (
        <div className="mt-4 grid min-w-0 grid-cols-2 gap-2 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 sm:grid-cols-2 xl:grid-cols-[minmax(15rem,1fr)_repeat(4,minmax(8rem,11rem))_auto]">
          <Input
            value={userQuery}
            onChange={(event) => setUserQuery(event.target.value)}
            leadingIcon={<Search size={15} aria-hidden />}
            placeholder={t('admin:analytics.filters.userPlaceholder')}
            aria-label={t('admin:analytics.filters.user')}
            wrapperClassName="col-span-2 min-w-0 max-sm:h-11 xl:col-span-1"
          />
          <Select value={modelFilter} onValueChange={setModelFilter}>
            <SelectTrigger className="min-w-0 max-sm:h-11" aria-label={t('admin:analytics.filters.model')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_FILTERS}>{t('admin:analytics.filters.allModels')}</SelectItem>
              {filterOptions.model.map((option) => (
                <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={workspaceFilter} onValueChange={setWorkspaceFilter}>
            <SelectTrigger className="min-w-0 max-sm:h-11" aria-label={t('admin:analytics.filters.workspace')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_FILTERS}>{t('admin:analytics.filters.allWorkspaces')}</SelectItem>
              {filterOptions.workspace.map((option) => (
                <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={purposeFilter} onValueChange={setPurposeFilter}>
            <SelectTrigger className="min-w-0 max-sm:h-11" aria-label={t('admin:analytics.filters.purpose')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_FILTERS}>{t('admin:analytics.filters.allPurposes')}</SelectItem>
              {filterOptions.purpose.map((option) => (
                <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={channelFilter} onValueChange={setChannelFilter}>
            <SelectTrigger className="min-w-0 max-sm:h-11" aria-label={t('admin:analytics.filters.channel')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_FILTERS}>{t('admin:analytics.filters.allChannels')}</SelectItem>
              {filterOptions.channel.map((option) => (
                <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          {activeFilterCount > 0 ? (
            <div className="col-span-2 flex min-w-0 items-center justify-end xl:col-span-1">
              <span className="mr-auto text-[12px] text-[var(--color-fg-muted)] xl:sr-only">
                {t('admin:analytics.filters.active', { count: activeFilterCount })}
              </span>
              <Tooltip content={t('admin:analytics.filters.clear')}>
                <Button
                  size="icon"
                  variant="ghost"
                  className="shrink-0 max-sm:size-11"
                  aria-label={t('admin:analytics.filters.clear')}
                  onClick={clearFilters}
                >
                  <X size={16} aria-hidden />
                </Button>
              </Tooltip>
            </div>
          ) : null}
        </div>
      ) : null}

      {view === 'feedback' ? (
        <AdminModelFeedback days={Number(days)} />
      ) : loading && !data ? (
        <div className="mt-6"><PanelFallback /></div>
      ) : loadError && !data ? (
        <LoadError message={loadError} onRetry={() => setRefreshVersion((value) => value + 1)} />
      ) : data ? (
        <main className="mt-6" aria-busy={loading || undefined}>
          {loadError ? (
            <div className="mb-4 flex flex-wrap items-center justify-between gap-3 rounded-[10px] border border-[var(--color-danger)]/30 bg-[var(--color-danger-soft)] px-3 py-2 text-sm text-[var(--color-fg)]">
              <span>{t('admin:analytics.error.stale')}</span>
              <Button size="xs" variant="secondary" onClick={() => setRefreshVersion((value) => value + 1)}>
                {t('admin:analytics.error.retry')}
              </Button>
            </div>
          ) : null}

          <SummaryStrip
            formatPercent={deltaPercentFormat.format}
            items={[
              {
                key: 'turns',
                label: t('admin:analytics.stats.turns'),
                current: data.totals.turns,
                previous: data.previous_totals.turns,
                format: numberFormat.format,
              },
              {
                key: 'users',
                label: t('admin:analytics.stats.users'),
                current: data.totals.users,
                previous: data.previous_totals.users,
                format: numberFormat.format,
              },
              {
                key: 'tokens',
                label: t('admin:analytics.stats.tokens'),
                current: inputOutputTokens(data.totals),
                previous: inputOutputTokens(data.previous_totals),
                format: compactFormat.format,
              },
              {
                key: 'cost',
                label: t('admin:analytics.stats.cost'),
                current: data.totals.cost,
                previous: data.previous_totals.cost,
                format: costFormat.format,
              },
              {
                key: 'credits',
                label: t('admin:analytics.stats.credits'),
                current: data.totals.credits,
                previous: data.previous_totals.credits,
                format: decimalFormat.format,
              },
              {
                key: 'operations',
                label: t('admin:analytics.stats.operations'),
                current: data.totals.calls,
                previous: data.previous_totals.calls,
                format: numberFormat.format,
              },
            ]}
          />

          <section className="mt-6 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-5">
            <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
              <div>
                <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('admin:analytics.sections.trend')}</h2>
                <p className="mt-1 text-[12px] text-[var(--color-fg-muted)]">
                  {t('admin:analytics.comparison.previousPeriod')}
                </p>
              </div>
              <div className="sm:hidden">
                <Select value={metric} onValueChange={(value) => setMetric(value as AnalyticsMetric)}>
                  <SelectTrigger aria-label={t('admin:analytics.metric.label')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {METRICS.map((value) => (
                      <SelectItem key={value} value={value}>{t(`admin:analytics.metric.${value}`)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="hidden sm:block">
                <SegmentedControl
                  compact
                  label={t('admin:analytics.metric.label')}
                  value={metric}
                  onChange={setMetric}
                  options={METRICS.map((value) => ({ value, label: t(`admin:analytics.metric.${value}`) }))}
                />
              </div>
            </div>
            <div className="mt-5">
              <ComparisonChart
                data={data}
                metric={metric}
                lang={lang}
                format={(value) => formatMetric(metric, value)}
                metricLabel={t(`admin:analytics.metric.${metric}`)}
                currentLabel={t('admin:analytics.comparison.current')}
                previousLabel={t('admin:analytics.comparison.previous')}
                weeklyLabel={t('admin:analytics.sections.weekly')}
                emptyLabel={t('admin:analytics.trend.empty')}
              />
            </div>
          </section>

          <section className="mt-6 grid gap-4 xl:grid-cols-2 xl:gap-6">
            <BillingEconomics
              totals={data.totals}
              t={t}
              formatCost={costFormat.format}
              formatNumber={decimalFormat.format}
              formatPercent={percentFormat.format}
            />
            <TokenComposition
              totals={data.totals}
              t={t}
              formatNumber={compactFormat.format}
              footerValues={[
                [t('admin:analytics.details.images'), numberFormat.format(data.totals.images_count)],
                [t('admin:analytics.details.conversations'), numberFormat.format(data.totals.conversations)],
                [t('admin:analytics.details.workspaces'), numberFormat.format(data.totals.workspaces)],
              ]}
            />
          </section>

          <BreakdownSection
            data={data}
            lang={lang}
            formatCost={costFormat.format}
            formatNumber={numberFormat.format}
            formatDecimal={decimalFormat.format}
            formatCompact={compactFormat.format}
            formatPercent={percentFormat.format}
          />

          <p className="mt-4 text-right text-[11.5px] text-[var(--color-fg-muted)]">
            {t('admin:analytics.updatedAt', { value: dateTimeFormat.format(new Date(data.generated_at * 1000)) })}
          </p>
        </main>
      ) : null}
    </div>
  )
}

interface SummaryItemSpec {
  key: string
  label: string
  current: number
  previous: number
  format: (value: number) => string
}

function SummaryStrip({ items, formatPercent }: { items: SummaryItemSpec[]; formatPercent: (value: number) => string }) {
  const { t } = useTranslation('admin')
  return (
    <section className="grid overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] grid-cols-2 md:grid-cols-3 xl:grid-cols-6">
      {items.map((item, index) => {
        const change = periodChange(item.current, item.previous)
        return (
          <div
            key={item.key}
            className={cn(
              'min-w-0 px-3 py-3.5 sm:px-4',
              index % 2 !== 0 && 'border-l border-[var(--color-divider)] md:border-l-0',
              index >= 2 && 'border-t border-[var(--color-divider)] md:border-t-0',
              index % 3 !== 0 && 'md:border-l md:border-[var(--color-divider)]',
              index >= 3 && 'md:border-t md:border-[var(--color-divider)] xl:border-t-0',
              index !== 0 && 'xl:border-l xl:border-[var(--color-divider)]',
            )}
          >
            <div className="truncate text-[11.5px] font-medium text-[var(--color-fg-muted)]">{item.label}</div>
            <div className="mt-1.5 truncate text-xl font-medium tabular-nums text-[var(--color-fg)]">
              {item.format(item.current)}
            </div>
            <PeriodDelta value={change} newLabel={t('analytics.comparison.newActivity')} formatPercent={formatPercent} />
          </div>
        )
      })}
    </section>
  )
}

function PeriodDelta({
  value,
  newLabel,
  formatPercent,
}: {
  value: number | null
  newLabel: string
  formatPercent: (value: number) => string
}) {
  const { t } = useTranslation('admin')
  if (value === null) {
    return (
      <span className="mt-1 inline-flex items-center gap-1 text-[11px] text-[var(--color-fg-muted)]">
        <ArrowUpRight size={12} aria-hidden />{newLabel}
      </span>
    )
  }
  const Icon = value > 0 ? ArrowUpRight : value < 0 ? ArrowDownRight : Minus
  return (
    <span className="mt-1 inline-flex items-center gap-1 text-[11px] tabular-nums text-[var(--color-fg-muted)]">
      <Icon size={12} aria-hidden />
      {t('analytics.comparison.delta', {
        value: formatPercent(value),
      })}
    </span>
  )
}

function ComparisonChart({
  data,
  metric,
  lang,
  format,
  metricLabel,
  currentLabel,
  previousLabel,
  weeklyLabel,
  emptyLabel,
}: {
  data: ApiAnalytics
  metric: AnalyticsMetric
  lang: string
  format: (value: number) => string
  metricLabel: string
  currentLabel: string
  previousLabel: string
  weeklyLabel: string
  emptyLabel: string
}) {
  const points = useMemo(() => alignedAnalyticsTrend(data, metric), [data, metric])
  const [activeIndex, setActiveIndex] = useState(Math.max(0, points.length - 1))
  useEffect(() => setActiveIndex(Math.max(0, points.length - 1)), [metric, points.length])
  const selected = points[Math.min(activeIndex, Math.max(0, points.length - 1))]
  const max = Math.max(1, ...points.flatMap((point) => [point.current, point.previous]))
  const weekly = data.bucket >= 7 * 86400
  const hourly = data.bucket <= 3600
  const label = (timestamp: number) => {
    const date = new Date(timestamp * 1000)
    if (hourly) return date.toLocaleTimeString(lang, { month: 'short', day: 'numeric', hour: '2-digit' })
    if (weekly) return `${weeklyLabel} · ${date.toLocaleDateString(lang, { month: 'short', day: 'numeric' })}`
    return date.toLocaleDateString(lang, { month: 'short', day: 'numeric' })
  }
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (points.length === 0) return
    if (event.key === 'ArrowLeft') setActiveIndex((value) => Math.max(0, value - 1))
    else if (event.key === 'ArrowRight') setActiveIndex((value) => Math.min(points.length - 1, value + 1))
    else if (event.key === 'Home') setActiveIndex(0)
    else if (event.key === 'End') setActiveIndex(points.length - 1)
    else return
    event.preventDefault()
  }
  const selectAtPosition = (clientX: number, element: HTMLDivElement) => {
    const bounds = element.getBoundingClientRect()
    if (bounds.width <= 0) return
    const position = Math.min(1, Math.max(0, (clientX - bounds.left) / bounds.width))
    setActiveIndex(Math.min(points.length - 1, Math.floor(position * points.length)))
  }

  if (points.length === 0) {
    return <div className="py-12 text-center text-sm text-[var(--color-fg-muted)]">{emptyLabel}</div>
  }

  return (
    <div>
      <div className="mb-4 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <span className="text-[12px] text-[var(--color-fg-muted)]">{selected ? label(selected.bucketStart) : '—'}</span>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px] tabular-nums">
          <span className="inline-flex items-center gap-1.5 text-[var(--color-fg)]">
            <span className="size-2 rounded-[2px] bg-[var(--color-fg)]" aria-hidden />
            {currentLabel} <strong className="font-medium">{format(selected?.current ?? 0)}</strong>
          </span>
          <span className="inline-flex items-center gap-1.5 text-[var(--color-fg-muted)]">
            <span className="size-2 rounded-[2px] border border-[var(--color-border-strong)]" aria-hidden />
            {previousLabel} <strong className="font-medium">{format(selected?.previous ?? 0)}</strong>
          </span>
        </div>
      </div>
      <div className="overflow-x-auto pb-1">
        <div
          role="slider"
          tabIndex={0}
          aria-label={`${metricLabel}: ${currentLabel} / ${previousLabel}`}
          aria-orientation="horizontal"
          aria-valuemin={0}
          aria-valuemax={Math.max(0, points.length - 1)}
          aria-valuenow={Math.min(activeIndex, Math.max(0, points.length - 1))}
          aria-valuetext={`${selected ? label(selected.bucketStart) : ''}: ${metricLabel}; ${currentLabel} ${format(selected?.current ?? 0)}, ${previousLabel} ${format(selected?.previous ?? 0)}`}
          onKeyDown={onKeyDown}
          onPointerDown={(event) => selectAtPosition(event.clientX, event.currentTarget)}
          onPointerMove={(event) => {
            if (event.pointerType === 'mouse') selectAtPosition(event.clientX, event.currentTarget)
          }}
          className="flex h-48 min-w-full w-[var(--chart-width)] items-end gap-1 rounded-[6px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          style={{ '--chart-width': `${Math.max(100, points.length * 1.5)}%` } as CSSProperties}
        >
          {points.map((point, index) => (
            <div
              key={`${point.bucketStart}-${index}`}
              className="group flex h-full min-w-[7px] flex-1 cursor-crosshair items-end justify-center gap-px"
              aria-hidden
            >
              <span
                className="w-[42%] max-w-2 rounded-t-[2px] border border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] transition-colors group-hover:border-[var(--color-fg-muted)]"
                style={{ height: `${Math.max(point.previous > 0 ? 2 : 0, (point.previous / max) * 100)}%` }}
              />
              <span
                className={cn(
                  'w-[42%] max-w-2 rounded-t-[2px] bg-[var(--color-fg-muted)] transition-colors group-hover:bg-[var(--color-fg)]',
                  index === activeIndex && 'bg-[var(--color-fg)]',
                )}
                style={{ height: `${Math.max(point.current > 0 ? 2 : 0, (point.current / max) * 100)}%` }}
              />
            </div>
          ))}
        </div>
      </div>
      <div className="mt-2 flex justify-between text-[11px] text-[var(--color-fg-muted)]">
        <span>{label(points[0].bucketStart)}</span>
        <span>{label(points[points.length - 1].bucketStart)}</span>
      </div>
    </div>
  )
}

function BillingEconomics({
  totals,
  t,
  formatCost,
  formatNumber,
  formatPercent,
}: {
  totals: ApiUsageTotals
  t: TFunction
  formatCost: (value: number) => string
  formatNumber: (value: number) => string
  formatPercent: (value: number) => string
}) {
  const includedCost = Math.max(0, totals.turn_cost - totals.credit_charged_cost)
  const rows = [
    [t('admin:analytics.economics.chargedTurnRate'), formatPercent(safeRatio(totals.credit_charged_turns, totals.turns))],
    [t('admin:analytics.economics.costCoverage'), formatPercent(safeRatio(totals.credit_charged_cost, totals.turn_cost))],
    [t('admin:analytics.economics.includedCost'), formatCost(includedCost)],
    [t('admin:analytics.economics.costPerTurn'), formatCost(safeRatio(totals.turn_cost, totals.turns))],
    [t('admin:analytics.economics.costPerUser'), formatCost(safeRatio(totals.cost, totals.users))],
    [t('admin:analytics.economics.creditsPerChargedTurn'), formatNumber(safeRatio(totals.credits, totals.credit_charged_turns))],
    [t('admin:analytics.economics.operationsPerTurn'), formatNumber(safeRatio(totals.calls, totals.turns))],
    [t('admin:analytics.economics.chargedUsers'), formatNumber(totals.credit_charged_users)],
  ]
  return (
    <div className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-5">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('admin:analytics.sections.economics')}</h2>
        <DefinitionPopover content={t('admin:analytics.notes.billingDefinition')} />
      </div>
      <dl className="mt-4 grid grid-cols-1 divide-y divide-[var(--color-divider)] sm:grid-cols-2 sm:gap-x-6 sm:divide-y-0">
        {rows.map(([label, value]) => (
          <div key={label} className="flex items-center justify-between gap-4 border-b border-[var(--color-divider)] py-2.5 last:border-b-0 sm:last:border-b">
            <dt className="text-[12px] text-[var(--color-fg-muted)]">{label}</dt>
            <dd className="shrink-0 text-[13px] font-medium tabular-nums text-[var(--color-fg)]">{value}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

function TokenComposition({
  totals,
  t,
  formatNumber,
  footerValues,
}: {
  totals: ApiUsageTotals
  t: TFunction
  formatNumber: (value: number) => string
  footerValues: Array<[string, string]>
}) {
  const total = processedTokens(totals)
  const rows = [
    [t('admin:analytics.details.inputTokens'), totals.input_tokens, 'bg-[var(--color-fg)]'],
    [t('admin:analytics.details.outputTokens'), totals.output_tokens, 'bg-[var(--color-accent)]'],
    [t('admin:analytics.details.cacheReadTokens'), totals.cache_read_tokens, 'bg-[var(--color-secondary)]'],
    [t('admin:analytics.details.cacheWriteTokens'), totals.cache_write_tokens, 'bg-[var(--color-fg-subtle)]'],
  ] as const
  return (
    <div className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-5">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('admin:analytics.sections.tokenComposition')}</h2>
        <DefinitionPopover content={t('admin:analytics.notes.tokenDefinition')} />
      </div>
      <div className="mt-4 h-2.5 overflow-hidden rounded-[4px] bg-[var(--color-bg-muted)]" aria-hidden>
        <div className="flex h-full">
          {rows.map(([label, value, color]) => (
            <span key={label} className={color} style={{ width: `${safeRatio(value, total) * 100}%` }} />
          ))}
        </div>
      </div>
      <dl className="mt-3 grid gap-x-6 sm:grid-cols-2">
        {rows.map(([label, value, color]) => (
          <div key={label} className="flex items-center justify-between gap-4 border-b border-[var(--color-divider)] py-2.5">
            <dt className="inline-flex min-w-0 items-center gap-2 text-[12px] text-[var(--color-fg-muted)]">
              <span className={cn('size-2 shrink-0 rounded-[2px]', color)} aria-hidden />{label}
            </dt>
            <dd className="shrink-0 text-[13px] font-medium tabular-nums text-[var(--color-fg)]">{formatNumber(value)}</dd>
          </div>
        ))}
      </dl>
      <dl className="mt-4 grid grid-cols-3 divide-x divide-[var(--color-divider)] border-t border-[var(--color-divider)] pt-4">
        {footerValues.map(([label, value]) => (
          <div key={label} className="min-w-0 px-2 text-center first:pl-0 last:pr-0">
            <dt className="truncate text-[11px] text-[var(--color-fg-muted)]">{label}</dt>
            <dd className="mt-1 text-sm font-medium tabular-nums text-[var(--color-fg)]">{value}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

function DefinitionPopover({ content }: { content: string }) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="inline-flex size-7 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-11"
          aria-label={content}
        >
          <Info size={14} aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent
        side="top"
        align="start"
        collisionPadding={12}
        className="w-[min(20rem,calc(100vw-1.5rem))] min-w-0 p-3 text-xs leading-relaxed text-[var(--color-fg-muted)] [overflow-wrap:anywhere]"
      >
        {content}
      </PopoverContent>
    </Popover>
  )
}

function BreakdownSection({
  data,
  lang,
  formatCost,
  formatNumber,
  formatDecimal,
  formatCompact,
  formatPercent,
}: {
  data: ApiAnalytics
  lang: string
  formatCost: (value: number) => string
  formatNumber: (value: number) => string
  formatDecimal: (value: number) => string
  formatCompact: (value: number) => string
  formatPercent: (value: number) => string
}) {
  const { t } = useTranslation(['admin', 'common'])
  const [dimension, setDimension] = useState<ApiAnalyticsDimension>('user')
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<BreakdownSort>('cost')
  const [page, setPage] = useState(1)
  const rows = data.breakdowns[dimension]
  const billingSafe = dimension === 'user' || dimension === 'workspace'

  useEffect(() => {
    if (!billingSafe && sort === 'credits') setSort('cost')
  }, [billingSafe, sort])

  const labelFor = useCallback((row: ApiUsageBreakdownRow) => {
    if (dimension === 'purpose') {
      return t(`admin:usage.purposes.${row.key.replaceAll('.', '_')}`, { defaultValue: row.key || t('admin:analytics.labels.unknown') })
    }
    if (row.label) return row.label
    if (dimension === 'model' && row.key) {
      return t('admin:analytics.labels.deletedModel', { id: row.key })
    }
    if (!row.key) {
      if (dimension === 'user') return t('admin:analytics.labels.deletedUser')
      if (dimension === 'workspace') return t('admin:analytics.labels.personal')
      if (dimension === 'channel') return t('admin:analytics.labels.unattributedChannel')
      return t('admin:analytics.labels.unknown')
    }
    return row.key
  }, [dimension, t])

  const visibleRows = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase(lang)
    const filtered = normalized
      ? rows.filter((row) => `${labelFor(row)} ${row.key}`.toLocaleLowerCase(lang).includes(normalized))
      : rows
    const value = (row: ApiUsageBreakdownRow) => {
      if (sort === 'tokens') return row.input_tokens + row.output_tokens
      return row[sort]
    }
    return [...filtered].sort((left, right) => value(right) - value(left) || labelFor(left).localeCompare(labelFor(right), lang))
  }, [labelFor, lang, query, rows, sort])

  useEffect(() => setPage(1), [dimension, query, rows, sort])
  const pageCount = Math.max(1, Math.ceil(visibleRows.length / PAGE_SIZE))
  useEffect(() => setPage((value) => Math.min(value, pageCount)), [pageCount])
  const pagedRows = visibleRows.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE)

  return (
    <section className="mt-6 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
      <div className="border-b border-[var(--color-divider)] p-4 sm:p-5">
        <div className="flex flex-col gap-4 xl:flex-row xl:items-end xl:justify-between">
          <div>
            <h2 className="text-sm font-medium text-[var(--color-fg)]">{t('admin:analytics.sections.breakdown')}</h2>
            <p className="mt-1 text-[12px] text-[var(--color-fg-muted)]">
              {t('admin:analytics.breakdown.count', { count: visibleRows.length })}
            </p>
          </div>
          <div className="hidden xl:block">
            <SegmentedControl
              compact
              label={t('admin:analytics.dimension.label')}
              value={dimension}
              onChange={setDimension}
              options={DIMENSIONS.map((value) => ({ value, label: t(`admin:analytics.dimension.${value}`) }))}
            />
          </div>
        </div>
        <div className="mt-4 grid gap-2 sm:grid-cols-2 xl:grid-cols-[minmax(0,1fr)_12rem]">
          <div className="xl:hidden sm:col-span-2">
            <Select value={dimension} onValueChange={(value) => setDimension(value as ApiAnalyticsDimension)}>
              <SelectTrigger aria-label={t('admin:analytics.dimension.label')}><SelectValue /></SelectTrigger>
              <SelectContent>
                {DIMENSIONS.map((value) => <SelectItem key={value} value={value}>{t(`admin:analytics.dimension.${value}`)}</SelectItem>)}
              </SelectContent>
            </Select>
          </div>
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            leadingIcon={<Search size={15} aria-hidden />}
            placeholder={t('admin:analytics.breakdown.search')}
            aria-label={t('admin:analytics.breakdown.search')}
            wrapperClassName="w-full"
          />
          <Select value={sort} onValueChange={(value) => setSort(value as BreakdownSort)}>
            <SelectTrigger aria-label={t('admin:analytics.breakdown.sort')}><SelectValue /></SelectTrigger>
            <SelectContent>
              {(['cost', 'turns', 'calls', 'tokens'] as BreakdownSort[]).map((value) => (
                <SelectItem key={value} value={value}>{t(`admin:analytics.breakdown.sortOptions.${value}`)}</SelectItem>
              ))}
              {billingSafe ? <SelectItem value="credits">{t('admin:analytics.breakdown.sortOptions.credits')}</SelectItem> : null}
            </SelectContent>
          </Select>
        </div>
      </div>

      {pagedRows.length === 0 ? (
        <div className="px-6 py-12 text-center text-sm text-[var(--color-fg-muted)]">{t('admin:analytics.breakdown.empty')}</div>
      ) : (
        <>
          <div className="hidden overflow-x-auto lg:block">
            <table className="w-full min-w-[940px] text-[12.5px] tabular-nums">
              <thead className="bg-[var(--color-bg-muted)] text-[11.5px] text-[var(--color-fg-muted)]">
                <tr>
                  <th className="px-4 py-2.5 text-left font-medium">{t('admin:analytics.table.name')}</th>
                  <th className="px-3 py-2.5 text-right font-medium">{t('admin:analytics.table.operations')}</th>
                  <th className="px-3 py-2.5 text-right font-medium">{t('admin:analytics.table.turns')}</th>
                  <th className="px-3 py-2.5 text-right font-medium">{t('admin:analytics.table.tokens')}</th>
                  <th className="px-3 py-2.5 text-right font-medium">{t('admin:analytics.table.cost')}</th>
                  {billingSafe ? <th className="px-3 py-2.5 text-right font-medium">{t('admin:analytics.table.credits')}</th> : null}
                  {billingSafe ? <th className="px-3 py-2.5 text-right font-medium">{t('admin:analytics.table.chargedTurns')}</th> : null}
                  <th className="px-4 py-2.5 text-right font-medium">
                    {t(billingSafe ? 'admin:analytics.table.avgCostTurn' : 'admin:analytics.table.avgCostOperation')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {pagedRows.map((row) => (
                  <BreakdownDesktopRow
                    key={`${dimension}:${row.key}`}
                    row={row}
                    dimension={dimension}
                    label={labelFor(row)}
                    billingSafe={billingSafe}
                    totalCost={data.totals.cost}
                    formatCost={formatCost}
                    formatNumber={formatNumber}
                    formatDecimal={formatDecimal}
                    formatCompact={formatCompact}
                    formatPercent={formatPercent}
                  />
                ))}
              </tbody>
            </table>
          </div>

          <div className="divide-y divide-[var(--color-divider)] lg:hidden">
            {pagedRows.map((row) => (
              <BreakdownMobileRow
                key={`${dimension}:${row.key}`}
                row={row}
                dimension={dimension}
                label={labelFor(row)}
                billingSafe={billingSafe}
                totalCost={data.totals.cost}
                formatCost={formatCost}
                formatNumber={formatNumber}
                formatDecimal={formatDecimal}
                formatCompact={formatCompact}
                formatPercent={formatPercent}
              />
            ))}
          </div>
        </>
      )}
      <Pagination page={page} pageCount={pageCount} onPage={setPage} className="border-t border-[var(--color-divider)] pb-4" />
    </section>
  )
}

function EntityLabel({
  dimension,
  id,
  linkable,
  children,
}: {
  dimension: ApiAnalyticsDimension
  id: string
  linkable: boolean
  children: ReactNode
}) {
  if (!linkable) return children
  if (dimension === 'user' && id) {
    return (
      <Link
        className="rounded-[2px] hover:text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        to={`/admin/users/${encodeURIComponent(id)}/conversations`}
      >
        {children}
      </Link>
    )
  }
  if (dimension === 'model' && id) {
    return (
      <Link
        className="rounded-[2px] hover:text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        to={`/admin/models/${encodeURIComponent(id)}`}
      >
        {children}
      </Link>
    )
  }
  return children
}

interface BreakdownRowProps {
  row: ApiUsageBreakdownRow
  dimension: ApiAnalyticsDimension
  label: string
  billingSafe: boolean
  totalCost: number
  formatCost: (value: number) => string
  formatNumber: (value: number) => string
  formatDecimal: (value: number) => string
  formatCompact: (value: number) => string
  formatPercent: (value: number) => string
}

function BreakdownDesktopRow(props: BreakdownRowProps) {
  const {
    row,
    dimension,
    label,
    billingSafe,
    totalCost,
    formatCost,
    formatNumber,
    formatDecimal,
    formatCompact,
    formatPercent,
  } = props
  const share = clampRatio(safeRatio(row.cost, totalCost))
  return (
    <tr className="border-t border-[var(--color-divider)] hover:bg-[var(--color-bg-muted)]/60">
      <td className="min-w-[15rem] px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <span className="max-w-[18rem] truncate font-medium text-[var(--color-fg)]" title={label}>
            <EntityLabel dimension={dimension} id={row.key} linkable={Boolean(row.label)}>{label}</EntityLabel>
          </span>
          <span className="text-[11px] text-[var(--color-fg-muted)]">{formatPercent(share)}</span>
        </div>
        <div className="mt-1.5 h-1 overflow-hidden rounded-[2px] bg-[var(--color-bg-muted)]">
          <div className="h-full bg-[var(--color-fg-muted)]" style={{ width: `${share * 100}%` }} />
        </div>
      </td>
      <td className="px-3 py-3 text-right text-[var(--color-fg-muted)]">{formatNumber(row.calls)}</td>
      <td className="px-3 py-3 text-right text-[var(--color-fg)]">{formatNumber(row.turns)}</td>
      <td className="px-3 py-3 text-right text-[var(--color-fg-muted)]">{formatCompact(row.input_tokens + row.output_tokens)}</td>
      <td className="px-3 py-3 text-right font-medium text-[var(--color-fg)]">{formatCost(row.cost)}</td>
      {billingSafe ? <td className="px-3 py-3 text-right text-[var(--color-fg)]">{formatDecimal(row.credits)}</td> : null}
      {billingSafe ? <td className="px-3 py-3 text-right text-[var(--color-fg-muted)]">{formatNumber(row.credit_charged_turns)}</td> : null}
      <td className="px-4 py-3 text-right text-[var(--color-fg-muted)]">
        {formatCost(safeRatio(row.cost, billingSafe ? row.turns : row.calls))}
      </td>
    </tr>
  )
}

function BreakdownMobileRow(props: BreakdownRowProps) {
  const { t } = useTranslation('admin')
  const {
    row,
    dimension,
    label,
    billingSafe,
    totalCost,
    formatCost,
    formatNumber,
    formatDecimal,
    formatCompact,
    formatPercent,
  } = props
  const share = clampRatio(safeRatio(row.cost, totalCost))
  const pairs: Array<[string, string]> = [
    [t('analytics.table.operations'), formatNumber(row.calls)],
    [t('analytics.table.turns'), formatNumber(row.turns)],
    [t('analytics.table.tokens'), formatCompact(row.input_tokens + row.output_tokens)],
    [t('analytics.table.cost'), formatCost(row.cost)],
  ]
  if (billingSafe) {
    pairs.push([t('analytics.table.credits'), formatDecimal(row.credits)])
    pairs.push([t('analytics.table.chargedTurns'), formatNumber(row.credit_charged_turns)])
  }
  return (
    <article className="px-4 py-4">
      <div className="flex items-start justify-between gap-3">
        <span className="min-w-0 truncate text-sm font-medium text-[var(--color-fg)]">
          <EntityLabel dimension={dimension} id={row.key} linkable={Boolean(row.label)}>{label}</EntityLabel>
        </span>
        <span className="shrink-0 text-[12px] tabular-nums text-[var(--color-fg-muted)]">
          {formatPercent(share)}
        </span>
      </div>
      <dl className="mt-3 grid grid-cols-2 gap-x-5 gap-y-2.5">
        {pairs.map(([term, value]) => (
          <div key={term} className="flex items-baseline justify-between gap-2 border-b border-[var(--color-divider)] pb-1.5">
            <dt className="truncate text-[11.5px] text-[var(--color-fg-muted)]">{term}</dt>
            <dd className="shrink-0 text-[12.5px] font-medium tabular-nums text-[var(--color-fg)]">{value}</dd>
          </div>
        ))}
      </dl>
    </article>
  )
}

function LoadError({ message, onRetry }: { message: string; onRetry: () => void }) {
  const { t } = useTranslation('admin')
  return (
    <div className="mt-8 flex flex-col items-center border-y border-[var(--color-divider)] px-6 py-12 text-center">
      <AlertCircle size={22} className="text-[var(--color-danger)]" aria-hidden />
      <h2 className="mt-3 text-sm font-medium text-[var(--color-fg)]">{t('analytics.error.title')}</h2>
      <p className="mt-1 max-w-lg text-sm text-[var(--color-fg-muted)]">{message}</p>
      <Button size="sm" variant="secondary" className="mt-4" onClick={onRetry}>{t('analytics.error.retry')}</Button>
    </div>
  )
}
