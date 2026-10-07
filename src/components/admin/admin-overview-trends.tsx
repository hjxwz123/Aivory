import { useMemo, useState, type KeyboardEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowRight, RefreshCw } from 'lucide-react'
import type { ApiAdminOverviewTrends } from '@/api/types'
import { Button } from '@/components/ui/button'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { useLanguage } from '@/store/language'

interface TrendSeries {
  label: string
  color: string
  values: number[]
}

export function AdminOverviewTrends({ data, days, onDaysChange, onRefresh, loading, failed }: {
  data: ApiAdminOverviewTrends | null
  days: string
  onDaysChange: (days: string) => void
  onRefresh: () => void
  loading: boolean
  failed: boolean
}) {
  const { t } = useTranslation(['admin', 'common'])
  const lang = useLanguage((state) => state.lang)
  const [consumption, setConsumption] = useState<'cost' | 'credits'>('cost')
  const [activity, setActivity] = useState<'users' | 'turns'>('users')
  const formats = useMemo(() => ({
    number: new Intl.NumberFormat(lang, { maximumFractionDigits: 2 }),
    compact: new Intl.NumberFormat(lang, { notation: 'compact', maximumFractionDigits: 1 }),
    cost: new Intl.NumberFormat(lang, { style: 'currency', currency: 'USD', maximumFractionDigits: 4 }),
    date: new Intl.DateTimeFormat(lang, { month: 'short', day: 'numeric', timeZone: 'UTC' }),
  }), [lang])
  const points = data?.points ?? []
  const dates = points.map((point) => formats.date.format(new Date(point.bucket_start * 1000)))
  const costFormat = consumption === 'cost' ? formats.cost : formats.number

  return (
    <section className="mt-7" aria-labelledby="overview-trends-title" aria-busy={loading || undefined}>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-3 pb-3">
        <div className="flex items-center gap-2">
          <h2 id="overview-trends-title" className="text-base font-semibold">{t('admin:overview.trends.title')}</h2>
          <span className="text-[12px] text-[var(--color-fg-muted)]">UTC</span>
        </div>
        <div className="flex max-w-full flex-wrap items-center gap-2">
          <SegmentedControl
            label={t('admin:overview.trends.range')}
            value={days}
            onChange={onDaysChange}
            options={['7', '30', '90'].map((value) => ({ value, label: t('admin:overview.trends.days', { count: Number(value) }) }))}
          />
          <Tooltip content={t('admin:overview.trends.refresh')}>
            <Button size="icon-sm" variant="ghost" aria-label={t('admin:overview.trends.refresh')} disabled={loading} onClick={onRefresh}>
              <RefreshCw size={15} aria-hidden />
            </Button>
          </Tooltip>
          <Button asChild size="sm" variant="ghost">
            <Link to="/admin/analytics">{t('admin:overview.openAnalytics')}<ArrowRight size={14} aria-hidden /></Link>
          </Button>
        </div>
      </div>
      {loading ? (
        <div className="grid gap-x-8 sm:grid-cols-2" role="status" aria-label={t('common:aria.loading')}>
          {[0, 1, 2, 3].map((key) => <div key={key} className="min-w-0 py-5"><Skeleton className="h-4 w-24" /><Skeleton className="mt-3 h-6 w-32" /><Skeleton className="mt-4 h-[168px] w-full" /><Skeleton className="mt-4 h-5 w-40" /></div>)}
        </div>
      ) : failed || !data ? (
        <div className="flex min-h-48 flex-col items-center justify-center gap-3 text-sm text-[var(--color-fg-muted)]" role="alert">
          <p>{t('admin:overview.trends.loadFailed')}</p>
          <Button size="sm" variant="secondary" onClick={onRefresh}>{t('common:actions.tryAgain')}</Button>
        </div>
      ) : (
        <div className="grid min-w-0 gap-x-8 sm:grid-cols-2">
          <TrendChart
            key={`tokens-${data.period_start}`}
            title={t('admin:overview.trends.tokens')}
            total={formats.compact.format(data.totals.input_tokens + data.totals.output_tokens)}
            totalLabel={t('admin:overview.trends.total')}
            dates={dates}
            series={[
              { label: t('admin:analytics.details.inputTokens'), color: 'var(--color-info)', values: points.map((point) => point.input_tokens) },
              { label: t('admin:analytics.details.outputTokens'), color: 'var(--color-success)', values: points.map((point) => point.output_tokens) },
            ]}
            format={(value) => formats.number.format(value)}
            axisFormat={(value) => formats.compact.format(value)}
            emptyLabel={t('admin:overview.trends.empty')}
          />
          <TrendChart
            key={`consumption-${data.period_start}-${consumption}`}
            title={t('admin:overview.trends.consumption')}
            total={costFormat.format(data.totals[consumption])}
            totalLabel={t('admin:overview.trends.total')}
            dates={dates}
            series={[{ label: t(`admin:analytics.metric.${consumption}`), color: 'var(--color-accent)', values: points.map((point) => point[consumption]) }]}
            format={(value) => costFormat.format(value)}
            axisFormat={(value) => consumption === 'cost' ? formats.cost.format(value) : formats.compact.format(value)}
            fractional
            emptyLabel={t('admin:overview.trends.empty')}
            controls={<SegmentedControl compact label={t('admin:overview.trends.consumption')} value={consumption} onChange={setConsumption} options={(['cost', 'credits'] as const).map((value) => ({ value, label: t(`admin:analytics.metric.${value}`) }))} />}
          />
          <TrendChart
            key={`registrations-${data.period_start}`}
            title={t('admin:overview.trends.registrations')}
            total={formats.number.format(data.registrations)}
            totalLabel={t('admin:overview.trends.total')}
            dates={dates}
            series={[{ label: t('admin:overview.trends.newUsers'), color: 'var(--color-info)', values: points.map((point) => point.registrations) }]}
            format={(value) => formats.number.format(value)}
            axisFormat={(value) => formats.compact.format(value)}
            bars
            emptyLabel={t('admin:overview.trends.empty')}
          />
          <TrendChart
            key={`activity-${data.period_start}-${activity}`}
            title={t('admin:overview.trends.activity')}
            total={formats.number.format(data.totals[activity])}
            totalLabel={t(activity === 'users' ? 'admin:overview.trends.distinctUsers' : 'admin:overview.trends.total')}
            dates={dates}
            series={[{ label: t(`admin:analytics.metric.${activity}`), color: 'var(--color-success)', values: points.map((point) => point[activity]) }]}
            format={(value) => formats.number.format(value)}
            axisFormat={(value) => formats.compact.format(value)}
            emptyLabel={t('admin:overview.trends.empty')}
            controls={<SegmentedControl compact label={t('admin:overview.trends.activity')} value={activity} onChange={setActivity} options={(['users', 'turns'] as const).map((value) => ({ value, label: t(`admin:analytics.metric.${value}`) }))} />}
          />
        </div>
      )}
    </section>
  )
}

function TrendChart({ title, total, totalLabel, dates, series, format, axisFormat, controls, bars = false, fractional = false, emptyLabel }: {
  title: string
  total: string
  totalLabel: string
  dates: string[]
  series: TrendSeries[]
  format: (value: number) => string
  axisFormat: (value: number) => string
  controls?: ReactNode
  bars?: boolean
  fractional?: boolean
  emptyLabel: string
}) {
  const [active, setActive] = useState(Math.max(0, dates.length - 1))
  const peak = Math.max(fractional ? 0.0001 : 2, ...series.flatMap((item) => item.values))
  const power = 10 ** Math.floor(Math.log10(peak))
  const maximum = ([1, 2, 5, 10].find((step) => step * power >= peak) ?? 10) * power
  const width = 500
  const top = 8
  const bottom = 160
  const x = (index: number) => (index + 0.5) / Math.max(1, dates.length) * width
  const y = (value: number) => bottom - value / maximum * (bottom - top)
  const empty = series.every((item) => item.values.every((value) => value === 0))
  const selectedText = `${dates[active] ?? ''}: ${series.map((item) => `${item.label} ${format(item.values[active] ?? 0)}`).join(', ')}`
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    let next = active
    if (event.key === 'ArrowLeft') next -= 1
    else if (event.key === 'ArrowRight') next += 1
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = dates.length - 1
    else return
    event.preventDefault()
    setActive(Math.min(dates.length - 1, Math.max(0, next)))
  }

  return (
    <figure className="min-w-0 py-5">
      <div className="flex min-h-7 flex-wrap items-center justify-between gap-2">
        <h3 className="text-[13px] font-medium">{title}</h3>
        {controls}
      </div>
      <div className="mt-1 flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <strong className="text-lg font-semibold tabular-nums">{total}</strong>
        <span className="text-[12px] text-[var(--color-fg-muted)]">{totalLabel}</span>
      </div>
      <div className="mt-4 grid grid-cols-[3.5rem_minmax(0,1fr)] gap-x-2">
        <div className="relative h-[168px] text-right text-[12px] tabular-nums text-[var(--color-fg-muted)]" aria-hidden>
          <span className="absolute right-0 top-0">{axisFormat(maximum)}</span>
          <span className="absolute right-0 top-1/2 -translate-y-1/2">{axisFormat(maximum / 2)}</span>
          <span className="absolute bottom-0 right-0">{axisFormat(0)}</span>
        </div>
        <div className="min-w-0">
          <div
            role="slider" tabIndex={0} aria-label={title} aria-orientation="horizontal"
            aria-valuemin={0} aria-valuemax={Math.max(0, dates.length - 1)} aria-valuenow={active} aria-valuetext={selectedText}
            onKeyDown={onKeyDown}
            onPointerMove={(event) => {
              if (event.pointerType !== 'mouse') return
              const bounds = event.currentTarget.getBoundingClientRect()
              setActive(Math.max(0, Math.min(dates.length - 1, Math.floor((event.clientX - bounds.left) / bounds.width * dates.length))))
            }}
            onPointerDown={(event) => {
              const bounds = event.currentTarget.getBoundingClientRect()
              setActive(Math.max(0, Math.min(dates.length - 1, Math.floor((event.clientX - bounds.left) / bounds.width * dates.length))))
            }}
            className="relative h-[168px] cursor-crosshair rounded-[6px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <svg className="block h-full w-full overflow-visible" viewBox={`0 0 ${width} 168`} preserveAspectRatio="none" aria-hidden>
              {[top, (top + bottom) / 2, bottom].map((position) => <line key={position} x1={0} x2={width} y1={position} y2={position} stroke="var(--color-divider)" strokeDasharray={position === bottom ? undefined : '3 4'} vectorEffect="non-scaling-stroke" />)}
              {!empty && <line x1={x(active)} x2={x(active)} y1={top} y2={bottom} stroke="var(--color-border-strong)" strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />}
              {series.map((item) => bars ? (
                <g key={item.label} fill={item.color}>
                  {item.values.map((value, index) => <rect key={index} x={x(index) - Math.min(18, width / dates.length * 0.65) / 2} y={y(value)} width={Math.min(18, width / dates.length * 0.65)} height={bottom - y(value)} rx={2} opacity={index === active ? 1 : 0.65} />)}
                </g>
              ) : (
                <g key={item.label}>
                  <polyline points={item.values.map((value, index) => `${x(index)},${y(value)}`).join(' ')} fill="none" stroke={item.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
                  {!empty && <circle cx={x(active)} cy={y(item.values[active] ?? 0)} r={3} fill={item.color} />}
                </g>
              ))}
            </svg>
            {empty && <span className="pointer-events-none absolute inset-0 flex items-center justify-center text-[12px] text-[var(--color-fg-muted)]">{emptyLabel}</span>}
          </div>
          <div className="mt-2 flex justify-between gap-2 text-[12px] text-[var(--color-fg-muted)]" aria-hidden><span>{dates[0]}</span><span>{dates[dates.length - 1]}</span></div>
        </div>
      </div>
      <figcaption className="mt-3 flex min-h-10 flex-wrap items-start gap-x-3 gap-y-1 text-[12px] tabular-nums">
        <span className="text-[var(--color-fg-muted)]">{dates[active]}</span>
        {series.map((item) => <span key={item.label} className="inline-flex items-center gap-1.5"><span aria-hidden className="size-2 shrink-0 rounded-full" style={{ backgroundColor: item.color }} /><span className="text-[var(--color-fg-muted)]">{item.label}</span><strong className="font-medium">{format(item.values[active] ?? 0)}</strong></span>)}
      </figcaption>
    </figure>
  )
}
