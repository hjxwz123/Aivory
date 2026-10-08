/**
 * AdminUserLoginHistory — read-only successful sign-in audit trail for one user.
 * Shared by the user-management drawer and the standalone history route.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router-dom'
import { AlertCircle, History, Monitor, RefreshCw, Smartphone } from 'lucide-react'

import { adminApi, ApiError } from '@/api'
import type { ApiAdminLoginHistoryEntry, ApiUser } from '@/api/types'
import { AdminDetailHeader } from '@/components/admin/admin-detail-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { AdminTable } from '@/components/admin/AdminTable'
import { EmptyState } from '@/components/ui/empty-state'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { formatDateTime } from '@/lib/utils'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { formatRecordedClient, parseClientDevice } from '@/lib/client-device'

const PAGE_SIZE = 50

function methodVariant(method: string): 'neutral' | 'accent' | 'sage' | 'info' {
  if (method === 'password_2fa') return 'sage'
  if (method === 'oauth') return 'accent'
  if (method === 'oauth_2fa') return 'info'
  return 'neutral'
}

export default function AdminUserLoginHistory({ userId, embedded = false }: { userId?: string; embedded?: boolean } = {}) {
  const { t } = useTranslation(['admin', 'common'])
  const { id: routeId = '' } = useParams<{ id: string }>()
  const id = userId ?? routeId
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
      embedded ? Promise.resolve(null) : adminApi.user(id),
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
    return () => {
      requestSequence.current += 1
    }
  }, [id, embedded, page, reloadKey, t])

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
    const parsed = parseClientDevice(entry.user_agent, t('common:desktopApp'))
    return {
      ...parsed,
      label: parsed.label || t('admin:users.loginHistory.unknownDevice'),
    }
  }

  return (
    <div>
      {!embedded ? (
        <>
          <AdminDetailHeader backTo="/admin/users" backLabel={t('admin:users.backToUsers')} />
          <AdminPageHeader
            title={firstLoad ? (
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
            titleBusy={firstLoad}
            description={t('admin:users.loginHistoryLead')}
          />
        </>
      ) : null}

      <section className={embedded ? undefined : 'mt-6 sm:mt-8'} aria-label={t('admin:users.viewLoginHistory')}>
        {loading ? (
          <PanelFallback />
        ) : error ? (
          <div
            className="flex flex-col items-start gap-3 rounded-[8px] border border-[var(--color-danger)]/25 bg-[var(--color-danger-soft)] px-4 py-4 sm:flex-row sm:items-center sm:justify-between"
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
          <div className="rounded-[8px] bg-[var(--color-surface)]">
            <EmptyState
              icon={<History size={20} aria-hidden />}
              title={t('admin:users.noLoginHistory')}
              description={t('admin:users.noLoginHistoryBody')}
              className="py-10"
            />
          </div>
        ) : (
          <>
            <AdminTable
              items={rows}
              rowKey={(entry) => entry.id}
              label={t('admin:users.viewLoginHistory')}
              columns={[
                { id: 'time', header: t('admin:users.loginHistory.time'), width: 170, render: (entry) => <span className="text-[12px] tabular-nums text-[var(--color-fg-muted)]">{loginTime(entry.login_at)}</span> },
                { id: 'ip', header: t('admin:users.loginHistory.ip'), width: 180, render: (entry) => <code className="block truncate font-mono text-[12px]" title={entry.ip}>{entry.ip || '—'}</code> },
                { id: 'location', header: t('admin:users.loginHistory.location'), width: 160, render: (entry) => <span className="block truncate text-[var(--color-fg-muted)]" title={entry.location}>{entry.location || t('admin:users.loginHistory.unknownLocation')}</span> },
                { id: 'method', header: t('admin:users.loginHistory.method'), width: 160, render: (entry) => <Badge size="xs" variant={methodVariant(entry.method)}>{methodLabel(entry.method)}</Badge> },
                { id: 'device', header: t('admin:users.loginHistory.device'), width: 300, render: (entry) => {
                  const parsedDevice = device(entry)
                  const DeviceIcon = parsedDevice.mobile ? Smartphone : Monitor
                  return <div className="flex min-w-0 items-center gap-2"><DeviceIcon size={14} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden /><span className="min-w-0"><span className="block truncate font-medium">{parsedDevice.label}</span>{entry.user_agent ? <span className="block truncate text-[12px] text-[var(--color-fg-muted)]" title={entry.user_agent}>{formatRecordedClient(entry.user_agent, t('common:desktopApp'))}</span> : null}</span></div>
                } },
              ]}
            />


            <Pagination page={page} pageCount={pageCount} onPage={setPage} />
          </>
        )}
      </section>
    </div>
  )
}
