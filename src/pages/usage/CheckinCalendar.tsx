import { useEffect, useState, type CSSProperties } from 'react'
import { CalendarDays, ChevronLeft, ChevronRight, Sparkles } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { authApi } from '@/api'
import type { ApiDailyCheckin } from '@/api/types'
import { Button } from '@/components/ui/button'

const isoDate = (date: Date) => date.toISOString().slice(0, 10)
const civilDate = (date: string) => new Date(`${date}T00:00:00Z`)

export function CheckinCalendar({ today, firstDate, enabled, claim, pending, celebrate, claimError, onClaim, className }: {
  today: string
  firstDate?: string
  enabled: boolean
  claim: ApiDailyCheckin | null
  pending: boolean
  celebrate: boolean
  claimError: boolean
  onClaim: () => void
  className: string
}) {
  const { t, i18n } = useTranslation(['usage', 'common'])
  const [viewDate, setViewDate] = useState('')
  const [history, setHistory] = useState<{ month: string; claims: ApiDailyCheckin[] }>()
  const [failedMonth, setFailedMonth] = useState('')
  const [reload, setReload] = useState(0)
  const selected = viewDate && viewDate <= today ? viewDate : today
  const month = selected.slice(0, 7)
  const currentMonth = today.slice(0, 7)
  const firstMonth = firstDate?.slice(0, 7) || currentMonth

  useEffect(() => {
    const abort = new AbortController()
    void authApi.checkinHistory(month, abort.signal).then((claims) => {
      if (!abort.signal.aborted) {
        setHistory({ month, claims })
        setFailedMonth('')
      }
    }).catch(() => {
      if (!abort.signal.aborted) setFailedMonth(month)
    })
    return () => abort.abort()
  }, [month, reload])

  const locale = i18n.resolvedLanguage
  const formatMonth = new Intl.DateTimeFormat(locale, { month: 'long', year: 'numeric', timeZone: 'UTC' })
  const formatDate = new Intl.DateTimeFormat(locale, { dateStyle: 'full', timeZone: 'UTC' })
  const formatWeekday = new Intl.DateTimeFormat(locale, { weekday: 'short', timeZone: 'UTC' })
  const formatNumber = new Intl.NumberFormat(locale, { maximumFractionDigits: 2 })
  const start = civilDate(`${month}-01`)
  const year = start.getUTCFullYear(), monthIndex = start.getUTCMonth()
  const offset = (start.getUTCDay() + 6) % 7
  const lastDay = new Date(Date.UTC(year, monthIndex + 1, 0)).getUTCDate()
  const claims = new Map(history?.month === month ? history.claims.map((entry) => [entry.date, entry]) : [])
  if (claim) claims.set(claim.date, claim)
  const selectedClaim = claims.get(selected) ?? (selected === today ? claim : null)
  const isLoading = history?.month !== month && failedMonth !== month
  const changeMonth = (offset: number) => {
    const next = isoDate(new Date(Date.UTC(year, monthIndex + offset, 1)))
    setViewDate(next.slice(0, 7) === currentMonth ? '' : next)
  }
  const rewardText = (entry: ApiDailyCheckin) => t('usage:reward', {
    value: formatNumber.format(entry.credits), pool: t(entry.expires_at ? 'usage:timedPool' : 'usage:permanentPool'),
  })

  return (
    <section className={`${className} usage-checkin`} aria-labelledby="daily-checkin-heading">
      <div className="grid gap-5 md:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)] md:gap-8">
        <div className="min-w-0">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
            <h2 id="daily-checkin-heading" className="text-base font-semibold">{t('usage:checkin')}</h2>
            <div className="flex items-center gap-1.5">
              <Button variant="ghost" size="icon" aria-label={t('usage:previousMonth')} disabled={month <= firstMonth} onClick={() => changeMonth(-1)}><ChevronLeft size={16} /></Button>
              <span className="min-w-22 text-center text-sm font-medium" aria-live="polite">{formatMonth.format(start)}</span>
              <Button variant="ghost" size="icon" aria-label={t('usage:nextMonth')} disabled={month >= currentMonth} onClick={() => changeMonth(1)}><ChevronRight size={16} /></Button>
            </div>
          </div>
          <div className="grid grid-cols-7 gap-1.5" aria-busy={isLoading}>
            {Array.from({ length: 7 }, (_, i) => <span key={i} className="pb-1 text-center text-xs text-[var(--color-fg-muted)]">{formatWeekday.format(new Date(Date.UTC(2024, 0, i + 1)))}</span>)}
            {Array.from({ length: offset }, (_, i) => <span key={`blank-${i}`} />)}
            {Array.from({ length: lastDay }, (_, i) => {
              const date = `${month}-${String(i + 1).padStart(2, '0')}`
              const receipt = claims.get(date)
              const isToday = date === today
              const canClaim = isToday && enabled && !claim
              const title = [formatDate.format(civilDate(date)), receipt ? rewardText(receipt) : canClaim ? t('usage:claim') : ''].filter(Boolean).join(' · ')
              return <button
                key={date}
                type="button"
                className="usage-checkin-day"
                data-checkin-date={date}
                data-claimed={Boolean(receipt)}
                data-claimable={canClaim}
                aria-label={title}
                title={title}
                aria-current={isToday ? 'date' : undefined}
                aria-pressed={selected === date}
                disabled={date > today || (isToday && pending)}
                onClick={() => {
                  setViewDate(isToday ? '' : date)
                  if (canClaim) onClaim()
                }}
              ><span>{i + 1}</span>{(receipt || canClaim) && <span className="usage-checkin-dot" aria-hidden />}</button>
            })}
          </div>
        </div>
        <div className="flex min-w-0 items-center gap-3 rounded-xl bg-[var(--color-surface)] p-4 md:self-center">
          <div className={`usage-checkin-mark${celebrate && selected === today ? ' usage-checkin-celebrate' : ''}`} aria-hidden>
            {selectedClaim ? <Sparkles size={21} /> : <CalendarDays size={21} />}
            {celebrate && selected === today && Array.from({ length: 8 }, (_, i) => <i key={i} style={{ '--angle': `${i * 45}deg` } as CSSProperties} />)}
          </div>
          <div className="min-w-0" aria-live="polite">
            <p className="text-sm font-semibold">{formatDate.format(civilDate(selected))}</p>
            {failedMonth === month && !selectedClaim && selected !== today ? (
              <Button variant="ghost" size="sm" onClick={() => { setFailedMonth(''); setReload((value) => value + 1) }}>{t('common:actions.tryAgain')}</Button>
            ) : (
              <p className={`mt-1 text-sm text-[var(--color-fg-muted)]${celebrate && selected === today ? ' usage-checkin-reward' : ''}`}>
                {selectedClaim ? rewardText(selectedClaim) : selected === today && enabled ? t(pending ? 'usage:claiming' : 'usage:checkinHint') : isLoading ? t('common:aria.loading') : t('usage:missedCheckin')}
              </p>
            )}
            {claimError && selected === today && !claim && <p role="alert" className="mt-1 text-sm text-[var(--color-danger)]">{t('usage:claimError')}</p>}
          </div>
        </div>
      </div>
    </section>
  )
}
