/**
 * AdminAnalytics — visual usage trends from usage_logs (§ admin → analytics).
 *
 * Complements the AdminUsage table: KPI cards, an overall time trend, and
 * per-model / per-user breakdowns each with an inline sparkline so trends are
 * legible at a glance. One metric toggle (calls / tokens / cost) drives every
 * chart. Charts are token-driven monochrome SVG/divs — no chart library, no
 * accent overuse.
 */
import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiAnalytics, ApiUsageBreakdownRow, ApiUsageSeriesPoint } from '@/api/types'
import { useLanguage } from '@/store/language'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'
import { PanelFallback } from '@/components/ui/panel-fallback'

const RANGE_IDS = ['1', '7', '30', '90'] as const
type Metric = 'calls' | 'tokens' | 'cost'
const METRICS: Metric[] = ['calls', 'tokens', 'cost']

interface MetricCarrier {
  calls: number
  input_tokens: number
  output_tokens: number
  cost: number
}
function metricVal(p: MetricCarrier, m: Metric): number {
  if (m === 'calls') return p.calls
  if (m === 'cost') return p.cost
  return p.input_tokens + p.output_tokens
}
function compactNum(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(1).replace(/\.0$/, '') + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  return String(Math.round(n))
}
function fmtMetric(m: Metric, v: number): string {
  if (m === 'cost') return '$' + (v < 1 ? v.toFixed(4) : v.toFixed(2))
  return compactNum(v)
}

export default function AdminAnalytics() {
  const { t } = useTranslation(['admin', 'common'])
  const lang = useLanguage((s) => s.lang)
  const [days, setDays] = useState('30')
  const [metric, setMetric] = useState<Metric>('calls')
  const [data, setData] = useState<ApiAnalytics | null>(null)
  const [modelMap, setModelMap] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)

  async function load() {
    setLoading(true)
    try {
      const [a, models] = await Promise.all([adminApi.analytics(Number(days)), adminApi.models()])
      setData(a)
      const map: Record<string, string> = {}
      for (const m of models) map[m.id] = m.label
      setModelMap(map)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [days])

  const buckets = useMemo(() => (data ? data.trend.map((p) => p.bucket_start) : []), [data])
  const hourly = (data?.bucket ?? 86400) <= 3600

  function bucketLabel(unix: number): string {
    const d = new Date(unix * 1000)
    return hourly
      ? d.toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit' })
      : d.toLocaleDateString(lang, { month: 'short', day: 'numeric' })
  }

  // Pivot a flat series into a value-per-bucket array aligned to the trend axis.
  function seriesFor(points: ApiUsageSeriesPoint[], key: string): number[] {
    const byBucket = new Map<number, number>()
    for (const p of points) if (p.key === key) byBucket.set(p.bucket_start, metricVal(p, metric))
    return buckets.map((b) => byBucket.get(b) ?? 0)
  }

  const totals = data?.totals
  const trendValues = data ? data.trend.map((p) => metricVal(p, metric)) : []

  return (
    <div>
      <header className="flex flex-col gap-4 sm:flex-row sm:flex-wrap sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
            {t('analytics.title')}
          </h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('analytics.lead')}</p>
        </div>
        <div className="w-full sm:w-40">
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
      </header>

      {/* KPI cards */}
      <section className="mt-6 grid grid-cols-2 gap-2.5 sm:gap-3 lg:grid-cols-4">
        <Stat label={t('analytics.stats.calls')} value={totals ? compactNum(totals.calls) : '—'} />
        <Stat
          label={t('analytics.stats.tokens')}
          value={totals ? compactNum(totals.input_tokens + totals.output_tokens) : '—'}
        />
        <Stat label={t('analytics.stats.cost')} value={totals ? '$' + totals.cost.toFixed(2) : '—'} />
        <Stat label={t('analytics.stats.users')} value={totals ? String(totals.users) : '—'} />
      </section>

      {/* Metric toggle */}
      <div className="mt-6 grid w-full grid-cols-3 items-stretch gap-1 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-1 sm:inline-grid sm:w-auto sm:p-0.5">
        {METRICS.map((m) => (
          <button
            key={m}
            type="button"
            onClick={() => setMetric(m)}
            aria-pressed={metric === m}
            className={cn(
              'min-w-0 min-h-11 rounded-[8px] px-1.5 text-xs font-medium leading-tight transition-colors interactive sm:min-h-8 sm:px-3.5 sm:text-sm',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              metric === m
                ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
                : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
            )}
          >
            {t(`analytics.metric.${m}`)}
          </button>
        ))}
      </div>

      {loading ? (
        <PanelFallback />
      ) : !data || data.trend.length === 0 ? (
        <div className="mt-8 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
          {t('analytics.empty')}
        </div>
      ) : (
        <>
          {/* Overall trend */}
          <section className="mt-6 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-5">
            <h2 className="text-sm font-medium text-[var(--color-fg)]">
              {hourly ? t('analytics.sections.hourly') : t('analytics.sections.daily')}
            </h2>
            <div className="mt-4">
              <TrendBars
                values={trendValues}
                labels={buckets.map(bucketLabel)}
                format={(v) => fmtMetric(metric, v)}
              />
            </div>
          </section>

          {/* Breakdowns */}
          <section className="mt-6 grid gap-4 lg:grid-cols-2 lg:gap-6">
            <Breakdown
              title={t('analytics.sections.byModel')}
              rows={data.by_model}
              labelFor={(r) => modelMap[r.key] || r.key}
              series={(key) => seriesFor(data.model_series, key)}
              metric={metric}
            />
            <Breakdown
              title={t('analytics.sections.byUser')}
              rows={data.by_user}
              labelFor={(r) => r.label || r.key || t('analytics.unknownUser')}
              series={(key) => seriesFor(data.user_series, key)}
              metric={metric}
            />
          </section>
        </>
      )}
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 sm:p-4">
      <div className="break-words text-[11px] uppercase tracking-wide text-[var(--color-fg-subtle)] sm:text-[12px]">
        {label}
      </div>
      <div className="mt-1 break-all font-serif text-xl tabular-nums text-[var(--color-fg)] sm:text-2xl">{value}</div>
    </div>
  )
}

/** Vertical bar chart over the time axis. Monochrome ink-on-paper. */
function TrendBars({ values, labels, format }: { values: number[]; labels: string[]; format: (v: number) => string }) {
  const max = Math.max(1, ...values)
  const [activeIndex, setActiveIndex] = useState(Math.max(0, values.length - 1))
  useEffect(() => {
    setActiveIndex(Math.max(0, values.length - 1))
  }, [values.length])
  const selectedIndex = Math.min(activeIndex, Math.max(0, values.length - 1))

  return (
    <div>
      {values.length > 0 ? (
        <div className="mb-3 flex min-h-5 flex-wrap items-baseline justify-between gap-x-3 gap-y-1 text-xs">
          <span className="text-[var(--color-fg-muted)]">{labels[selectedIndex]}</span>
          <span className="font-medium tabular-nums text-[var(--color-fg)]">{format(values[selectedIndex])}</span>
        </div>
      ) : null}
      <div className="overflow-x-auto pb-1">
        <div
          className="flex h-44 min-w-full w-[var(--mobile-chart-width)] items-end gap-[2px] sm:w-full"
          style={{ '--mobile-chart-width': values.length > 14 ? `${values.length * 1.5}rem` : '100%' } as CSSProperties}
        >
          {values.map((v, i) => (
            <button
              key={i}
              type="button"
              onClick={() => setActiveIndex(i)}
              onFocus={() => setActiveIndex(i)}
              aria-label={`${labels[i]} · ${format(v)}`}
              aria-pressed={selectedIndex === i}
              className="group relative flex h-full min-w-0 flex-1 flex-col justify-end rounded-t-[2px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <span
                aria-hidden
                className={cn(
                  'block w-full rounded-t-[2px] bg-[var(--color-fg-muted)] transition-colors group-hover:bg-[var(--color-fg)]',
                  selectedIndex === i && 'bg-[var(--color-fg)]',
                )}
                style={{ height: `${Math.max(v > 0 ? 2 : 0, Math.round((v / max) * 100))}%` }}
              />
              <span className="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 hidden -translate-x-1/2 whitespace-nowrap rounded-[6px] bg-[var(--color-fg)] px-2 py-1 text-[11px] text-[var(--color-fg-inverted)] shadow-[var(--shadow-md)] group-hover:block">
                {labels[i]} · {format(v)}
              </span>
            </button>
          ))}
        </div>
      </div>
      {labels.length > 0 ? (
        <div className="mt-2 flex justify-between text-[11px] text-[var(--color-fg-subtle)]">
          <span>{labels[0]}</span>
          <span>{labels[labels.length - 1]}</span>
        </div>
      ) : null}
    </div>
  )
}

/** A small inline sparkline (polyline) for one series. */
function Spark({ values }: { values: number[] }) {
  const w = 96
  const h = 26
  if (values.length === 0) return <svg width={w} height={h} aria-hidden />
  const max = Math.max(1, ...values)
  const n = values.length
  const pts = values
    .map((v, i) => {
      const x = n === 1 ? w : (i / (n - 1)) * w
      const y = h - 2 - (v / max) * (h - 4)
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
  return (
    <svg width={w} height={h} aria-hidden className="overflow-visible">
      <polyline
        points={pts}
        fill="none"
        stroke="var(--color-fg-muted)"
        strokeWidth={1.5}
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  )
}

function Breakdown({
  title,
  rows,
  labelFor,
  series,
  metric,
}: {
  title: string
  rows: ApiUsageBreakdownRow[]
  labelFor: (r: ApiUsageBreakdownRow) => string
  series: (key: string) => number[]
  metric: Metric
}) {
  const { t } = useTranslation('admin')
  const max = Math.max(1, ...rows.map((r) => metricVal(r, metric)))
  return (
    <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4 sm:p-5">
      <h2 className="text-sm font-medium text-[var(--color-fg)]">{title}</h2>
      {rows.length === 0 ? (
        <div className="mt-4 text-sm text-[var(--color-fg-subtle)]">{t('analytics.empty')}</div>
      ) : (
        <ul className="mt-4 flex flex-col divide-y divide-[var(--color-divider)]">
          {rows.map((r) => {
            const v = metricVal(r, metric)
            return (
              <li key={r.key} className="flex flex-col gap-2 py-3 sm:flex-row sm:items-center sm:gap-3 sm:py-2.5">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-[13px] text-[var(--color-fg)] truncate">{labelFor(r)}</span>
                    <span className="text-[12px] tabular-nums text-[var(--color-fg-muted)] shrink-0">
                      {fmtMetric(metric, v)}
                    </span>
                  </div>
                  <div className="mt-1.5 h-1.5 rounded-full bg-[var(--color-bg-muted)] overflow-hidden">
                    <div
                      className="h-full rounded-full bg-[var(--color-fg)]/70"
                      style={{ width: `${Math.round((v / max) * 100)}%` }}
                    />
                  </div>
                </div>
                <div className="flex w-full shrink-0 justify-end sm:w-auto">
                  <Spark values={series(r.key)} />
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
