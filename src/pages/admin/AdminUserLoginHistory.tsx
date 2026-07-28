/**
 * AdminUserLoginHistory — read-only successful sign-in audit trail for one user.
 * The compact desktop table becomes field-labelled rows below xl so long user
 * agents and locations never force the admin shell to scroll horizontally.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useParams } from 'react-router-dom'
import { AlertCircle, ArrowLeft, History, Monitor, RefreshCw, Smartphone } from 'lucide-react'

import { adminApi, ApiError } from '@/api'
import type { ApiAdminLoginHistoryEntry, ApiUser } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { formatDateTime } from '@/lib/utils'

const PAGE_SIZE = 50

function parseDevice(userAgent: string): { label: string; mobile: boolean } {
  const mobile = /Mobile|Android|iPhone|iPad|iPod/i.test(userAgent)
  let os = ''
  if (/iPhone|iPad|iPod/i.test(userAgent)) os = 'iOS'
  else if (/Android/i.test(userAgent)) os = 'Android'
  else if (/Windows/i.test(userAgent)) os = 'Windows'
  else if (/Mac OS X|Macintosh/i.test(userAgent)) os = 'macOS'
  else if (/CrOS/i.test(userAgent)) os = 'ChromeOS'
  else if (/Linux/i.test(userAgent)) os = 'Linux'

  let browser = ''
  if (/Edg\//i.test(userAgent)) browser = 'Edge'
  else if (/OPR\/|Opera/i.test(userAgent)) browser = 'Opera'
  else if (/Firefox\//i.test(userAgent)) browser = 'Firefox'
  else if (/Chrome\//i.test(userAgent)) browser = 'Chrome'
  else if (/Safari\//i.test(userAgent)) browser = 'Safari'

  return { label: [browser, os].filter(Boolean).join(' · '), mobile }
}

function methodVariant(method: string): 'neutral' | 'accent' | 'sage' | 'info' {
  if (method === 'password_2fa') return 'sage'
  if (method === 'oauth') return 'accent'
  if (method === 'oauth_2fa') return 'info'
  return 'neutral'
}

export default function AdminUserLoginHistory() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const { id = '' } = useParams<{ id: string }>()
  const [user, setUser] = useState<ApiUser | null>(null)
  const [rows, setRows] = useState<ApiAdminLoginHistoryEntry[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [loadedId, setLoadedId] = useState('')
  const [error, setError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const requestSequence = useRef(0)

  useEffect(() => {
    setPage(1)
  }, [id])

  useEffect(() => {
    const sequence = ++requestSequence.current
    setLoading(true)
    setError('')
    setRows([])

    void Promise.all([
      adminApi.user(id),
      adminApi.userLoginHistory(id, PAGE_SIZE, (page - 1) * PAGE_SIZE),
    ]).then(([targetUser, result]) => {
      if (sequence !== requestSequence.current) return
      setUser(targetUser)
      setRows(result.items)
      setTotal(result.total)
    }).catch((loadError: unknown) => {
      if (sequence !== requestSequence.current) return
      setError(loadError instanceof ApiError ? loadError.message : t('admin:users.loginHistoryLoadFailed'))
    }).finally(() => {
      if (sequence !== requestSequence.current) return
      setLoadedId(id)
      setLoading(false)
    })
  }, [id, page, reloadKey, t])

  const currentUser = user?.id === id ? user : null
  const firstLoad = loading && loadedId !== id
  const headerName = currentUser?.name.trim() || currentUser?.email.trim() || ''
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  function loginTime(value: number): string {
    return value ? formatDateTime(value * 1000) : '—'
  }

  function methodLabel(method: string): string {
    return t(`admin:users.loginHistory.methods.${method}`, {
      defaultValue: method || t('admin:users.loginHistory.unknownMethod'),
    })
  }

  function device(entry: ApiAdminLoginHistoryEntry) {
    const parsed = parseDevice(entry.user_agent)
    return {
      ...parsed,
      label: parsed.label || t('admin:users.loginHistory.unknownDevice'),
    }
  }

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate('/admin/users')}
        className="mb-4 -ml-2 inline-flex items-center gap-1.5 rounded-[6px] px-2 py-1.5 text-[12.5px] text-[var(--color-fg-subtle)] interactive hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <ArrowLeft size={12} aria-hidden />
        {t('admin:users.backToUsers')}
      </button>

      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]" aria-busy={firstLoad}>
          {firstLoad ? (
            <span className="block" role="status" aria-live="polite">
              <span className="sr-only">{t('admin:common.loading')}</span>
              <span
                aria-hidden
                className="block h-9 w-[min(18rem,70vw)] animate-pulse rounded-[8px] bg-[var(--color-bg-muted)]"
              />
            </span>
          ) : headerName ? (
            t('admin:users.loginHistoryTitle', { name: headerName })
          ) : (
            t('admin:users.loginHistoryFallbackTitle')
          )}
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:users.loginHistoryLead')}
        </p>
      </header>

      <section className="mt-6 sm:mt-8" aria-label={t('admin:users.viewLoginHistory')}>
        {loading ? (
          <PanelFallback />
        ) : error ? (
          <div
            className="flex flex-col items-start gap-3 rounded-[10px] border border-[var(--color-danger)]/25 bg-[var(--color-danger-soft)] px-4 py-4 sm:flex-row sm:items-center sm:justify-between"
            role="alert"
          >
            <div className="flex min-w-0 items-start gap-2.5 text-sm text-[var(--color-danger)]">
              <AlertCircle size={16} aria-hidden className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{error}</span>
            </div>
            <Button
              variant="secondary"
              size="sm"
              className="shrink-0 max-sm:w-full"
              leadingIcon={<RefreshCw size={13} aria-hidden />}
              onClick={() => setReloadKey((key) => key + 1)}
            >
              {t('admin:users.loginHistoryRetry')}
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <div className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            <EmptyState
              icon={<History size={20} aria-hidden />}
              title={t('admin:users.noLoginHistory')}
              description={t('admin:users.noLoginHistoryBody')}
              className="py-10"
            />
          </div>
        ) : (
          <>
            <div
              role="table"
              aria-label={t('admin:users.viewLoginHistory')}
              className="hidden overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] xl:block"
            >
              <div
                role="row"
                className="grid grid-cols-[minmax(8rem,1.1fr)_minmax(6.5rem,.85fr)_minmax(7rem,1fr)_minmax(7rem,.8fr)_minmax(11rem,1.4fr)] gap-3 border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)] px-4 py-2.5 text-[11.5px] font-medium text-[var(--color-fg-muted)]"
              >
                <span role="columnheader">{t('admin:users.loginHistory.time')}</span>
                <span role="columnheader">{t('admin:users.loginHistory.ip')}</span>
                <span role="columnheader">{t('admin:users.loginHistory.location')}</span>
                <span role="columnheader">{t('admin:users.loginHistory.method')}</span>
                <span role="columnheader">{t('admin:users.loginHistory.device')}</span>
              </div>
              <div role="rowgroup" className="divide-y divide-[var(--color-divider)]">
                {rows.map((entry) => {
                  const parsedDevice = device(entry)
                  const DeviceIcon = parsedDevice.mobile ? Smartphone : Monitor
                  return (
                    <div
                      key={entry.id}
                      role="row"
                      className="grid grid-cols-[minmax(8rem,1.1fr)_minmax(6.5rem,.85fr)_minmax(7rem,1fr)_minmax(7rem,.8fr)_minmax(11rem,1.4fr)] items-center gap-3 px-4 py-3 text-[12.5px]"
                    >
                      <span role="cell" className="min-w-0 tabular-nums text-[var(--color-fg-muted)]">
                        {loginTime(entry.login_at)}
                      </span>
                      <code role="cell" className="min-w-0 truncate font-mono text-[12px] text-[var(--color-fg)]" title={entry.ip}>
                        {entry.ip || '—'}
                      </code>
                      <span role="cell" className="min-w-0 truncate text-[var(--color-fg-muted)]" title={entry.location}>
                        {entry.location || t('admin:users.loginHistory.unknownLocation')}
                      </span>
                      <span role="cell">
                        <Badge size="xs" variant={methodVariant(entry.method)}>
                          {methodLabel(entry.method)}
                        </Badge>
                      </span>
                      <span role="cell" className="flex min-w-0 items-center gap-2">
                        <DeviceIcon size={14} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
                        <span className="min-w-0">
                          <span className="block truncate font-medium text-[var(--color-fg)]">{parsedDevice.label}</span>
                          {entry.user_agent ? (
                            <span className="mt-0.5 block truncate text-[11px] text-[var(--color-fg-subtle)]" title={entry.user_agent}>
                              {entry.user_agent}
                            </span>
                          ) : null}
                        </span>
                      </span>
                    </div>
                  )
                })}
              </div>
            </div>

            <ul className="flex flex-col divide-y divide-[var(--color-divider)] overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] xl:hidden">
              {rows.map((entry) => {
                const parsedDevice = device(entry)
                const DeviceIcon = parsedDevice.mobile ? Smartphone : Monitor
                return (
                  <li key={entry.id} className="px-4 py-3 sm:px-5">
                    <div className="flex min-w-0 items-center justify-between gap-3">
                      <time className="min-w-0 text-[12.5px] tabular-nums text-[var(--color-fg)]">
                        {loginTime(entry.login_at)}
                      </time>
                      <Badge size="xs" variant={methodVariant(entry.method)}>
                        {methodLabel(entry.method)}
                      </Badge>
                    </div>
                    <dl className="mt-2.5 grid grid-cols-[4.5rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[12px] sm:grid-cols-[4.5rem_minmax(0,1fr)_4.5rem_minmax(0,1fr)]">
                      <dt className="text-[var(--color-fg-subtle)]">{t('admin:users.loginHistory.ip')}</dt>
                      <dd className="min-w-0 break-all font-mono text-[var(--color-fg-muted)]">{entry.ip || '—'}</dd>
                      <dt className="text-[var(--color-fg-subtle)]">{t('admin:users.loginHistory.location')}</dt>
                      <dd className="min-w-0 break-words text-[var(--color-fg-muted)]">
                        {entry.location || t('admin:users.loginHistory.unknownLocation')}
                      </dd>
                      <dt className="text-[var(--color-fg-subtle)] sm:col-start-1">{t('admin:users.loginHistory.device')}</dt>
                      <dd className="flex min-w-0 items-start gap-2 text-[var(--color-fg-muted)] sm:col-span-3">
                        <DeviceIcon size={14} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)]" />
                        <span className="min-w-0">
                          <span className="block font-medium text-[var(--color-fg)]">{parsedDevice.label}</span>
                          {entry.user_agent ? (
                            <span className="mt-0.5 line-clamp-2 break-all text-[11px] leading-4" title={entry.user_agent}>
                              {entry.user_agent}
                            </span>
                          ) : null}
                        </span>
                      </dd>
                    </dl>
                  </li>
                )
              })}
            </ul>

            <Pagination page={page} pageCount={pageCount} onPage={setPage} />
          </>
        )}
      </section>
    </div>
  )
}
