import { useEffect, useRef, useState, type CSSProperties } from 'react'
import { Clock, RefreshCw, Wallet } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { ApiError, authApi } from '@/api'
import type { ApiDailyUsage, ApiPersonalUsage } from '@/api/types'
import { ContentHeader } from '@/components/layout/content-header'
import { Button } from '@/components/ui/button'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { SegmentedControl } from '@/components/ui/segmented-control'
import { useAuth } from '@/store/auth'
import { CheckinCalendar } from './CheckinCalendar'
import './personal-usage.css'

const surface = 'min-w-0 rounded-popup border border-[var(--color-border-subtle)] bg-[var(--color-surface)] p-4 sm:p-6'
const calendarSteps: Record<string, number> = { ArrowLeft: -7, ArrowRight: 7, ArrowUp: -1, ArrowDown: 1 }
const levels = ['var(--color-surface-sunken)', ...[24, 44, 68, 100].map(
  (weight) => `color-mix(in oklch, var(--color-accent) ${weight}%, var(--color-surface-sunken))`,
)]

// Calendar labels represent dates already bucketed by the server's system zone.
const civilDate = (date: string) => new Date(`${date}T00:00:00Z`)

function UsageCalendar({ days, showCredits }: { days: ApiDailyUsage[]; showCredits: boolean }) {
  const { t, i18n } = useTranslation('usage')
  const [preferCredits, setPreferCredits] = useState(showCredits)
  const metric = preferCredits && showCredits ? 'credits' : 'tokens'
  const [selection, setSelection] = useState('')
  const scroller = useRef<HTMLDivElement>(null)
  const today = days.at(-1)?.date

  useEffect(() => {
    const node = scroller.current
    if (!node) return
    const observer = new ResizeObserver(() => { node.scrollLeft = node.scrollWidth })
    observer.observe(node)
    return () => observer.disconnect()
  }, [today])

  if (!days.length) return null
  const active = days.find((d) => d.date === selection) ?? days[days.length - 1]
  const locale = i18n.resolvedLanguage
  const number = new Intl.NumberFormat(locale, { maximumFractionDigits: metric === 'credits' ? 6 : 0 })
  const date = new Intl.DateTimeFormat(locale, { dateStyle: 'full', timeZone: 'UTC' })
  const month = new Intl.DateTimeFormat(locale, { month: 'short', timeZone: 'UTC' })
  const weekday = new Intl.DateTimeFormat(locale, { weekday: 'short', timeZone: 'UTC' })
  const offset = (civilDate(days[0].date).getUTCDay() + 6) % 7
  const columns = Math.ceil((offset + days.length) / 7)
  const maximum = Math.max(...days.map((d) => d[metric]), 0)
  const total = days.reduce((sum, d) => sum + d[metric], 0)
  const monthLabels = new Map<number, string>()
  days.forEach((day, index) => {
    if (day.date.endsWith('-01')) monthLabels.set(Math.floor((offset + index) / 7), month.format(civilDate(day.date)))
  })
  const amountLabel = (value: number) => t(metric === 'credits' ? 'creditValue' : 'tokenValue', { value: number.format(value) })

  return (
    <section className={surface} aria-labelledby="daily-usage-heading">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="daily-usage-heading" className="text-base font-semibold">{t('activity')}</h2>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{t('total', { amount: amountLabel(total) })}</p>
        </div>
        {showCredits && (
          <SegmentedControl<'credits' | 'tokens'>
            label={t('metric')}
            value={metric}
            onChange={(value) => setPreferCredits(value === 'credits')}
            options={[{ value: 'credits', label: t('creditsUsed') }, { value: 'tokens', label: 'Token' }]}
          />
        )}
      </div>

      <div ref={scroller} className="usage-calendar mt-5 overflow-x-auto pb-2">
        <div className="usage-calendar-grid" style={{ '--usage-columns': columns } as CSSProperties}>
          {[0, 2, 4].map((day) => (
            <span key={day} aria-hidden style={{ gridRow: day + 2 }} className="usage-calendar-weekday">
              {weekday.format(new Date(Date.UTC(2024, 0, day + 1)))}
            </span>
          ))}
          {[...monthLabels].map(([column, name]) => (
            <span key={column} aria-hidden className="usage-calendar-month" style={{ gridColumn: column + 2 }}>{name}</span>
          ))}
          {days.map((day, index) => {
            const position = offset + index
            const value = day[metric]
            const level = value > 0 && maximum > 0 ? Math.ceil(Math.sqrt(value / maximum) * 4) : 0
            const label = `${date.format(civilDate(day.date))} · ${amountLabel(value)}`
            return (
              <button
                key={day.date}
                type="button"
                className="usage-calendar-day"
                data-usage-day={index}
                style={{ gridColumn: Math.floor(position / 7) + 2, gridRow: position % 7 + 2, background: levels[level] }}
                title={label}
                aria-label={label}
                aria-pressed={active.date === day.date}
                tabIndex={active.date === day.date ? 0 : -1}
                onClick={() => setSelection(day.date)}
                onKeyDown={(event) => {
                  const step = calendarSteps[event.key]
                  if (!step) return
                  event.preventDefault()
                  const next = Math.max(0, Math.min(days.length - 1, index + step))
                  setSelection(days[next].date)
                  scroller.current?.querySelector<HTMLButtonElement>(`[data-usage-day="${next}"]`)?.focus()
                }}
              />
            )
          })}
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-start justify-between gap-x-4 gap-y-2 border-t border-[var(--color-border-subtle)] pt-3 text-xs">
        <div>
          <p aria-live="polite" className="font-medium tabular-nums">{date.format(civilDate(active.date))} · {amountLabel(active[metric])}</p>
        </div>
        <div className="flex items-center gap-1.5 text-[var(--color-fg-muted)]" aria-hidden>
          {t('less')}
          {levels.map((background) => <span key={background} className="size-2.5 rounded-[2px]" style={{ background }} />)}
          {t('more')}
        </div>
      </div>
    </section>
  )
}

function UsageContent() {
  const { t, i18n } = useTranslation(['usage', 'common'])
  const [data, setData] = useState<ApiPersonalUsage>()
  const [error, setError] = useState(false)
  const [reload, setReload] = useState(0)
  const [submitting, setSubmitting] = useState(false)
  const [claimError, setClaimError] = useState(false)
  const [celebrate, setCelebrate] = useState(false)
  const claimRequest = useRef<AbortController | null>(null)
  const claimRevision = useRef(0)

  useEffect(() => {
    const controller = new AbortController()
    const revision = claimRevision.current
    setError(false)
    void authApi.dailyUsage(controller.signal).then((result) => {
      if (!controller.signal.aborted && revision === claimRevision.current) setData(result)
    }).catch(() => {
      if (!controller.signal.aborted) setError(true)
    })
    return () => controller.abort()
  }, [reload])

  useEffect(() => () => claimRequest.current?.abort(), [])

  useEffect(() => {
    if (!celebrate) return
    const timer = window.setTimeout(() => setCelebrate(false), 800)
    return () => window.clearTimeout(timer)
  }, [celebrate])

  const nextDay = data?.next_day_at
  const nextRefresh = Math.min(...[nextDay, data?.credits.timed?.resets_at, data?.credits.timed?.checkin_expires_at]
    .filter((value): value is number => Boolean(value && value * 1000 > Date.now())))
  useEffect(() => {
    if (!Number.isFinite(nextRefresh)) return
    const timer = window.setTimeout(() => {
      if (nextRefresh === nextDay) setCelebrate(false)
      setReload((value) => value + 1)
    }, Math.max(1500, nextRefresh * 1000 - Date.now() + 1000))
    return () => window.clearTimeout(timer)
  }, [nextDay, nextRefresh])

  async function claim() {
    const today = data?.days.at(-1)?.date
    if (!today || !data?.checkin_enabled || data.checkin || claimRequest.current) return
    const controller = new AbortController()
    claimRequest.current = controller
    setSubmitting(true)
    setClaimError(false)
    try {
      const result = await authApi.dailyCheckin(today, controller.signal)
      if (controller.signal.aborted) return
      claimRevision.current++
      setData((previous) => previous && ({ ...previous, checkin: result.checkin, credits: result.credits, first_checkin_date: previous.first_checkin_date || result.checkin.date }))
      setCelebrate(result.awarded)
      if (result.checkin.date !== today) setReload((value) => value + 1)
    } catch (err) {
      if (controller.signal.aborted) return
      setClaimError(true)
      if (err instanceof ApiError && err.status === 409) {
        if (err.message === 'daily_checkin_day_changed') setClaimError(false)
        setReload((value) => value + 1)
      }
    } finally {
      if (!controller.signal.aborted) {
        claimRequest.current = null
        setSubmitting(false)
      }
    }
  }

  const locale = i18n.resolvedLanguage
  const number = new Intl.NumberFormat(locale, { maximumFractionDigits: 6 })
  const formatTime = (seconds: number) => new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium', timeStyle: 'short', timeZone: data?.time_zone,
  }).format(seconds * 1000)
  const today = data?.days.at(-1)?.date
  const timed = data?.credits.timed
  const checkinRemaining = timed?.checkin_remaining ?? 0
  const membershipRemaining = Math.max(0, (timed?.remaining ?? 0) - checkinRemaining)
  const resetsAt = timed?.resets_at ?? 0
  const expiresAt = timed?.checkin_expires_at ?? 0
  const progress = timed?.allowance
    ? Math.max(0, Math.min(100, membershipRemaining / timed.allowance * 100))
    : 0

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader title={t('usage:title')} backTo="/" backLabel={t('usage:back')} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <main className="mx-auto grid w-full max-w-[var(--layout-content-max-w)] gap-4 px-4 py-5 pb-16 sm:gap-5 sm:px-8 sm:py-8">
          {error && (
            <div className={surface} role="alert">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <p className="text-sm text-[var(--color-fg-muted)]">{t('usage:loadError')}</p>
                <Button variant="secondary" size="sm" leadingIcon={<RefreshCw size={14} />} onClick={() => setReload((value) => value + 1)}>{t('common:actions.tryAgain')}</Button>
              </div>
            </div>
          )}
          {!data && !error && <PanelFallback />}
          {data && (
            <>
              <div className="grid gap-3 sm:grid-cols-2 sm:gap-5">
                <section className={surface} aria-labelledby="timed-credit-heading">
                  <h2 id="timed-credit-heading" className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg-muted)]"><Clock size={16} aria-hidden />{t('usage:timedBalance')}</h2>
                  <p className="my-3 break-words text-3xl font-semibold tracking-tight tabular-nums">{number.format(timed?.remaining ?? 0)}</p>
                  {resetsAt > 0 && (
                    <>
                      <div className="mb-2 h-1 w-full overflow-hidden rounded-full bg-[var(--color-accent-soft)]" aria-hidden><div className="h-full rounded-full bg-[var(--color-accent)]" style={{ width: `${progress}%` }} /></div>
                      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs text-[var(--color-fg-muted)]">
                        <span className="font-medium">{t('usage:groupCreditDetail', { value: number.format(membershipRemaining), allowance: number.format(timed?.allowance ?? 0) })}</span>
                        <span>{t('usage:refreshes', { time: formatTime(resetsAt) })}</span>
                      </div>
                    </>
                  )}
                  {expiresAt > 0 && <p className="mt-1 text-xs text-[var(--color-fg-muted)]">{t('usage:timedRewardExpiry', { value: number.format(checkinRemaining), time: formatTime(expiresAt) })}</p>}
                  {!resetsAt && !expiresAt && <p className="text-xs text-[var(--color-fg-muted)]">{t('usage:timedEmpty')}</p>}
                </section>
                <section className={surface} aria-labelledby="permanent-credit-heading">
                  <h2 id="permanent-credit-heading" className="flex items-center gap-2 text-sm font-medium text-[var(--color-fg-muted)]"><Wallet size={16} aria-hidden />{t('usage:permanentBalance')}</h2>
                  <p className="my-3 break-words text-3xl font-semibold tracking-tight tabular-nums">{number.format(data.credits.permanent)}</p>
                  <p className="text-xs text-[var(--color-fg-muted)]">{t('usage:permanentHint')}</p>
                </section>
              </div>
              {today && (data.checkin_enabled || data.first_checkin_date || data.checkin) && (
                <CheckinCalendar
                  today={today}
                  firstDate={data.first_checkin_date}
                  enabled={data.checkin_enabled}
                  claim={data.checkin}
                  pending={submitting}
                  celebrate={celebrate}
                  claimError={claimError}
                  onClaim={() => void claim()}
                  className={surface}
                />
              )}
              <UsageCalendar days={data.days} showCredits={data.credits.enabled || data.days.some((day) => day.credits > 0)} />
            </>
          )}
        </main>
      </div>
    </div>
  )
}

export default function PersonalUsage() {
  const userId = useAuth((state) => state.user?.id)
  return userId ? <UsageContent key={userId} /> : null
}
