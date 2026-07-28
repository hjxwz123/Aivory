import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, CircleX, Copy, RefreshCw, Search, ShieldAlert, X } from 'lucide-react'

import { adminApi, ApiError } from '@/api'
import type {
  ApiPaymentOrder,
  ApiPaymentOrderStatus,
  ApiPaymentProvider,
} from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'
import { formatCurrencyMinor } from '@/lib/currency'
import { copyText, formatDateTime } from '@/lib/utils'

type StatusFilter = 'all' | ApiPaymentOrderStatus
type ProviderFilter = 'all' | ApiPaymentProvider
type PaymentOrderAction = 'reconcile' | 'close'

const STATUSES: StatusFilter[] = ['all', 'pending', 'processing', 'fulfilled', 'failed', 'expired', 'cancelled']
const PROVIDERS: ProviderFilter[] = ['all', 'stripe', 'epay', 'waffo']
const PAGE_SIZE = 20

function orderDate(value?: number | null): Date | null {
  if (!value) return null
  return new Date(value < 1_000_000_000_000 ? value * 1000 : value)
}

function statusVariant(status: ApiPaymentOrderStatus): 'warning' | 'success' | 'danger' | 'neutral' | 'info' {
  if (status === 'pending') return 'warning'
  if (status === 'processing') return 'info'
  if (status === 'fulfilled') return 'success'
  if (status === 'failed') return 'danger'
  return 'neutral'
}

export default function AdminPaymentOrders() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [orders, setOrders] = useState<ApiPaymentOrder[]>([])
  const [total, setTotal] = useState(0)
  const [status, setStatus] = useState<StatusFilter>('all')
  const [provider, setProvider] = useState<ProviderFilter>('all')
  const [searchDraft, setSearchDraft] = useState('')
  const [searchTerm, setSearchTerm] = useState('')
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [copiedOrderId, setCopiedOrderId] = useState('')
  const [copyAnnouncement, setCopyAnnouncement] = useState('')
  const [busyOrders, setBusyOrders] = useState<Record<string, PaymentOrderAction>>({})
  const [closeTarget, setCloseTarget] = useState<ApiPaymentOrder | null>(null)
  const [closeReason, setCloseReason] = useState('')
  const [closeAcknowledged, setCloseAcknowledged] = useState(false)
  const requestSequence = useRef(0)
  const copyResetTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const sequence = ++requestSequence.current
    setLoading(true)
    setLoadError('')
    void adminApi.paymentOrders({
      status: status === 'all' ? undefined : status,
      provider: provider === 'all' ? undefined : provider,
      search: searchTerm || undefined,
      limit: PAGE_SIZE,
      offset: (page - 1) * PAGE_SIZE,
    }).then((result) => {
      if (sequence !== requestSequence.current) return
      setOrders(result.orders)
      setTotal(result.total)
    }).catch((error: unknown) => {
      if (sequence !== requestSequence.current) return
      const message = error instanceof ApiError ? error.message : t('admin:paymentOrders.loadFailed')
      setLoadError(message)
      toast.error(message)
    }).finally(() => {
      if (sequence === requestSequence.current) setLoading(false)
    })
  }, [page, provider, reloadKey, searchTerm, status, t])

  useEffect(() => () => {
    if (copyResetTimer.current) clearTimeout(copyResetTimer.current)
  }, [])

  function applySearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setPage(1)
    setSearchTerm(searchDraft.trim())
  }

  function clearFilters() {
    setStatus('all')
    setProvider('all')
    setSearchDraft('')
    setSearchTerm('')
    setPage(1)
  }

  async function copyOrderId(orderId: string) {
    const copied = await copyText(orderId)
    if (!copied) {
      setCopiedOrderId('')
      setCopyAnnouncement(t('admin:paymentOrders.copyFailed'))
      toast.error(t('admin:paymentOrders.copyFailed'))
      return
    }

    setCopiedOrderId(orderId)
    setCopyAnnouncement(t('admin:paymentOrders.copied'))
    if (copyResetTimer.current) clearTimeout(copyResetTimer.current)
    copyResetTimer.current = setTimeout(() => {
      setCopiedOrderId((current) => current === orderId ? '' : current)
      copyResetTimer.current = null
    }, 2000)
  }

  function setOrderBusy(orderId: string, action?: PaymentOrderAction) {
    setBusyOrders((current) => {
      if (action) return { ...current, [orderId]: action }
      const next = { ...current }
      delete next[orderId]
      return next
    })
  }

  function updateOrder(updated: ApiPaymentOrder) {
    setOrders((current) => current.map((order) => order.id === updated.id ? updated : order))
  }

  async function reconcileOrder(order: ApiPaymentOrder) {
    setOrderBusy(order.id, 'reconcile')
    try {
      const updated = await adminApi.reconcilePaymentOrder(order.id, { action: 'reconcile' })
      updateOrder(updated)
      toast.success(t('admin:paymentOrders.actions.reconciled'))
      setReloadKey((value) => value + 1)
    } catch (error: unknown) {
      const message = error instanceof ApiError ? error.message : t('admin:paymentOrders.actions.reconcileFailed')
      toast.error(message)
      setReloadKey((value) => value + 1)
    } finally {
      setOrderBusy(order.id)
    }
  }

  function openCloseDialog(order: ApiPaymentOrder) {
    setCloseReason('')
    setCloseAcknowledged(false)
    setCloseTarget(order)
  }

  function dismissCloseDialog() {
    setCloseTarget(null)
    setCloseReason('')
    setCloseAcknowledged(false)
  }

  async function closeOrder(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!closeTarget || !closeAcknowledged || closeReason.trim().length < 5) return

    const order = closeTarget
    setOrderBusy(order.id, 'close')
    try {
      const updated = await adminApi.reconcilePaymentOrder(order.id, {
        action: 'close',
        confirm: true,
        reason: closeReason.trim(),
      })
      updateOrder(updated)
      dismissCloseDialog()
      toast.success(t('admin:paymentOrders.actions.closed'))
      setReloadKey((value) => value + 1)
    } catch (error: unknown) {
      const message = error instanceof ApiError ? error.message : t('admin:paymentOrders.actions.closeFailed')
      toast.error(message)
      setReloadKey((value) => value + 1)
    } finally {
      setOrderBusy(order.id)
    }
  }

  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const filtersActive = status !== 'all' || provider !== 'all' || Boolean(searchTerm)

  function cycleLabel(order: ApiPaymentOrder): string {
    if (!order.billing_cycle) return ''
    return t(`admin:paymentOrders.cycles.${order.billing_cycle}`)
  }

  function targetLabel(order: ApiPaymentOrder): string {
    const cycle = cycleLabel(order)
    return cycle ? `${order.target_name} · ${cycle}` : order.target_name
  }

  function channelLabel(order: ApiPaymentOrder): string {
    return [order.method_name, order.channel_name].filter(Boolean).join(' · ')
  }

  function environmentLabel(order: ApiPaymentOrder): string {
    return t(`admin:paymentOrders.environment.${order.environment}`)
  }

  function renderOrderTimes(order: ApiPaymentOrder) {
    const created = orderDate(order.created_at)
    const paid = orderDate(order.paid_at)
    const fulfilled = orderDate(order.fulfilled_at)
    const checkoutExpires = orderDate(order.checkout_expires_at)
    const lastReconciled = orderDate(order.last_reconciled_at)
    return (
      <>
        <span className="block">{created ? formatDateTime(created) : '—'}</span>
        {checkoutExpires ? <span className="mt-0.5 block text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.checkoutExpiresAt', { date: formatDateTime(checkoutExpires) })}</span> : null}
        {paid ? <span className="mt-0.5 block text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.paidAt', { date: formatDateTime(paid) })}</span> : null}
        {fulfilled ? <span className="mt-0.5 block text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.fulfilledAt', { date: formatDateTime(fulfilled) })}</span> : null}
        <span className="mt-0.5 block text-[var(--color-fg-subtle)]">
          {lastReconciled
            ? t('admin:paymentOrders.lastReconciledAt', { date: formatDateTime(lastReconciled) })
            : t('admin:paymentOrders.notReconciled')}
        </span>
      </>
    )
  }

  function renderOrderActions(order: ApiPaymentOrder) {
    if (order.status !== 'pending' && order.status !== 'processing') return null
    const busyAction = busyOrders[order.id]
    return (
      <div className="mt-1.5 flex flex-wrap gap-1">
        {order.provider !== 'epay' ? (
          <Button
            className="rounded-[8px]"
            size="xs"
            variant="secondary"
            leadingIcon={<RefreshCw size={11} aria-hidden />}
            loading={busyAction === 'reconcile'}
            disabled={Boolean(busyAction)}
            onClick={() => void reconcileOrder(order)}
          >
            {t('admin:paymentOrders.actions.reconcile')}
          </Button>
        ) : null}
        <Button
          className="rounded-[8px] text-[var(--color-danger)] hover:text-[var(--color-danger)]"
          size="xs"
          variant="ghost"
          leadingIcon={<CircleX size={11} aria-hidden />}
          loading={busyAction === 'close'}
          disabled={Boolean(busyAction)}
          onClick={() => openCloseDialog(order)}
        >
          {order.provider === 'epay'
            ? t('admin:paymentOrders.actions.manualClose')
            : t('admin:paymentOrders.actions.close')}
        </Button>
      </div>
    )
  }

  return (
    <div className="min-w-0 font-sans">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:paymentOrders.title')}</h1>
        <p className="mt-1 max-w-2xl text-[13px] leading-5 text-[var(--color-fg-muted)]">{t('admin:paymentOrders.lead')}</p>
      </header>

      <p className="sr-only" role="status" aria-live="polite" aria-atomic="true">{copyAnnouncement}</p>

      <form className="mt-5 grid min-w-0 grid-cols-1 gap-2 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 sm:grid-cols-2 lg:grid-cols-[minmax(14rem,1fr)_minmax(9rem,10rem)_minmax(9rem,10rem)_auto]" onSubmit={applySearch}>
        <Input
          wrapperClassName="min-w-0 rounded-[8px] max-sm:h-11 sm:col-span-2 lg:col-span-1"
          leadingIcon={<Search size={14} aria-hidden />}
          value={searchDraft}
          onChange={(event) => setSearchDraft(event.target.value)}
          placeholder={t('admin:paymentOrders.filters.searchPlaceholder')}
          aria-label={t('admin:paymentOrders.filters.search')}
        />
        <Select value={status} onValueChange={(value) => { setStatus(value as StatusFilter); setPage(1) }}>
          <SelectTrigger className="w-full min-w-0 rounded-[8px] max-sm:h-11" aria-label={t('admin:paymentOrders.filters.status')}><SelectValue /></SelectTrigger>
          <SelectContent>
            {STATUSES.map((value) => (
              <SelectItem key={value} value={value}>{t(`admin:paymentOrders.status.${value}`)}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={provider} onValueChange={(value) => { setProvider(value as ProviderFilter); setPage(1) }}>
          <SelectTrigger className="w-full min-w-0 rounded-[8px] max-sm:h-11" aria-label={t('admin:paymentOrders.filters.provider')}><SelectValue /></SelectTrigger>
          <SelectContent>
            {PROVIDERS.map((value) => (
              <SelectItem key={value} value={value}>
                {value === 'all' ? t('admin:paymentOrders.providers.all') : t(`admin:paymentProviders.${value}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="flex min-w-0 gap-1.5 sm:col-span-2 lg:col-span-1">
          <Button className="min-w-0 flex-1 rounded-[8px] max-sm:h-11 lg:flex-none" size="sm" type="submit" leadingIcon={<Search size={13} aria-hidden />}>
            {t('admin:paymentOrders.filters.apply')}
          </Button>
          {filtersActive || searchDraft ? (
            <Button className="shrink-0 rounded-[8px] max-sm:size-11" size="icon" variant="ghost" title={t('admin:paymentOrders.filters.clear')} aria-label={t('admin:paymentOrders.filters.clear')} onClick={clearFilters}>
              <X size={14} aria-hidden />
            </Button>
          ) : null}
        </div>
      </form>

      <div className="mt-3 flex min-h-5 items-center justify-between gap-3 text-[12px] text-[var(--color-fg-subtle)]">
        <span>{t('admin:paymentOrders.total', { count: total })}</span>
        {loading && orders.length > 0 ? <span role="status">{t('admin:common.loading')}</span> : null}
      </div>

      <section className="mt-2">
        {loading && orders.length === 0 ? (
          <PanelFallback />
        ) : loadError ? (
          <div className="flex flex-col items-start gap-3 rounded-[8px] border border-[var(--color-danger)]/25 bg-[var(--color-danger-soft)] px-4 py-3 text-[13px] text-[var(--color-danger)] sm:flex-row sm:items-center sm:justify-between">
            <span>{loadError}</span>
            <Button className="rounded-[8px] max-sm:h-11" variant="secondary" size="sm" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => setReloadKey((value) => value + 1)}>{t('admin:paymentOrders.retry')}</Button>
          </div>
        ) : orders.length === 0 ? (
          <div className="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center">
            <p className="text-sm font-medium text-[var(--color-fg)]">{filtersActive ? t('admin:paymentOrders.emptyFilteredTitle') : t('admin:paymentOrders.emptyTitle')}</p>
            <p className="mx-auto mt-1 max-w-lg text-[13px] text-[var(--color-fg-muted)]">{filtersActive ? t('admin:paymentOrders.emptyFiltered') : t('admin:paymentOrders.empty')}</p>
            {filtersActive ? <Button className="mt-4 rounded-[8px] max-sm:h-11" variant="secondary" size="sm" onClick={clearFilters}>{t('admin:paymentOrders.filters.clear')}</Button> : null}
          </div>
        ) : (
          <>
            <div className="hidden max-w-full overflow-x-auto rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] xl:block">
              <table className="w-full min-w-[72rem] table-fixed border-collapse text-left text-[12px]">
                <colgroup>
                  <col className="w-[12%]" />
                  <col className="w-[12%]" />
                  <col className="w-[13%]" />
                  <col className="w-[9%]" />
                  <col className="w-[14%]" />
                  <col className="w-[22%]" />
                  <col className="w-[18%]" />
                </colgroup>
                <thead className="bg-[var(--color-bg-muted)] text-[var(--color-fg-subtle)]">
                  <tr>
                    <th className="px-3 py-2 font-medium">{t('admin:paymentOrders.table.order')}</th>
                    <th className="px-3 py-2 font-medium">{t('admin:paymentOrders.table.user')}</th>
                    <th className="px-3 py-2 font-medium">{t('admin:paymentOrders.table.product')}</th>
                    <th className="px-3 py-2 font-medium">{t('admin:paymentOrders.table.amount')}</th>
                    <th className="px-3 py-2 font-medium">{t('admin:paymentOrders.table.method')}</th>
                    <th className="px-3 py-2 font-medium">{t('admin:paymentOrders.table.status')}</th>
                    <th className="px-3 py-2 font-medium">{t('admin:paymentOrders.table.time')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-[var(--color-divider)]">
                  {orders.map((order) => {
                    return (
                      <tr key={order.id} className="align-top hover:bg-[var(--color-bg-muted)]/45">
                        <td className="px-3 py-2.5 text-[var(--color-fg-muted)]">
                          <div className="flex min-w-0 items-start gap-1">
                            <code className="min-w-0 flex-1 break-all font-mono text-[11px] leading-4">{order.id}</code>
                            <Button
                              className="shrink-0 rounded-[8px]"
                              size="icon-sm"
                              variant="ghost"
                              title={copiedOrderId === order.id ? t('admin:paymentOrders.copied') : t('admin:paymentOrders.copyOrder')}
                              aria-label={copiedOrderId === order.id ? t('admin:paymentOrders.copied') : t('admin:paymentOrders.copyOrder')}
                              onClick={() => void copyOrderId(order.id)}
                            >
                              {copiedOrderId === order.id ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
                            </Button>
                          </div>
                        </td>
                        <td className="px-3 py-2.5 text-[var(--color-fg)]"><span className="block break-all">{order.user_email}</span></td>
                        <td className="px-3 py-2.5">
                          <span className="block break-words font-medium text-[var(--color-fg)] [overflow-wrap:anywhere]">{targetLabel(order)}</span>
                          <span className="mt-0.5 block text-[11px] text-[var(--color-fg-subtle)]">{t(`admin:paymentOrders.targets.${order.target_type}`)}</span>
                        </td>
                        <td className="whitespace-nowrap px-3 py-2.5 font-medium tabular-nums text-[var(--color-fg)]">{formatCurrencyMinor(order.amount_minor, order.currency, i18n.resolvedLanguage)}</td>
                        <td className="px-3 py-2.5">
                          <span className="block break-words text-[var(--color-fg)] [overflow-wrap:anywhere]">{channelLabel(order) || '—'}</span>
                          <span className="mt-0.5 flex flex-wrap items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
                            {t(`admin:paymentProviders.${order.provider}`)}
                            <Badge size="xs" variant={order.environment === 'test' ? 'warning' : 'neutral'}>{environmentLabel(order)}</Badge>
                          </span>
                        </td>
                        <td className="px-3 py-2.5">
                          <Badge size="xs" variant={statusVariant(order.status)}>{t(`admin:paymentOrders.status.${order.status}`)}</Badge>
                          {order.failure_reason ? <span className="mt-1 block whitespace-pre-wrap break-words text-[11px] leading-4 text-[var(--color-danger)] [overflow-wrap:anywhere]">{order.failure_reason}</span> : null}
                          {order.reconcile_error ? (
                            <span className="mt-1 block whitespace-pre-wrap break-words text-[11px] leading-4 text-[var(--color-danger)] [overflow-wrap:anywhere]">
                              {t('admin:paymentOrders.reconcileError', { error: order.reconcile_error })}
                            </span>
                          ) : null}
                          {renderOrderActions(order)}
                        </td>
                        <td className="whitespace-nowrap px-3 py-2.5 text-[11px] text-[var(--color-fg-muted)]">
                          {renderOrderTimes(order)}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>

            <ul className="divide-y divide-[var(--color-divider)] overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] xl:hidden">
              {orders.map((order) => {
                return (
                  <li key={order.id} className="px-3 py-3">
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <p className="break-words text-[13px] font-medium text-[var(--color-fg)] [overflow-wrap:anywhere]">{targetLabel(order)}</p>
                        <p className="mt-0.5 break-all text-[12px] text-[var(--color-fg-muted)]">{order.user_email}</p>
                      </div>
                      <Badge className="shrink-0" size="xs" variant={statusVariant(order.status)}>{t(`admin:paymentOrders.status.${order.status}`)}</Badge>
                    </div>

                    <dl className="mt-3 grid min-w-0 grid-cols-1 gap-x-4 gap-y-2 sm:grid-cols-2">
                      <div className="min-w-0">
                        <dt className="text-[11px] text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.table.order')}</dt>
                        <dd className="mt-0.5 flex min-w-0 items-start gap-1">
                          <code className="min-w-0 flex-1 break-all font-mono text-[11px] leading-5 text-[var(--color-fg-muted)]">{order.id}</code>
                          <Button
                            className="shrink-0 rounded-[8px] max-sm:size-11"
                            size="icon-sm"
                            variant="ghost"
                            title={copiedOrderId === order.id ? t('admin:paymentOrders.copied') : t('admin:paymentOrders.copyOrder')}
                            aria-label={copiedOrderId === order.id ? t('admin:paymentOrders.copied') : t('admin:paymentOrders.copyOrder')}
                            onClick={() => void copyOrderId(order.id)}
                          >
                            {copiedOrderId === order.id ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
                          </Button>
                        </dd>
                      </div>
                      <div className="min-w-0">
                        <dt className="text-[11px] text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.table.method')}</dt>
                        <dd className="mt-0.5 break-words text-[12px] leading-5 text-[var(--color-fg)] [overflow-wrap:anywhere]">{channelLabel(order) || '—'}</dd>
                        <dd className="flex flex-wrap items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
                          {t(`admin:paymentProviders.${order.provider}`)}
                          <Badge size="xs" variant={order.environment === 'test' ? 'warning' : 'neutral'}>{environmentLabel(order)}</Badge>
                        </dd>
                      </div>
                      <div>
                        <dt className="text-[11px] text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.table.amount')}</dt>
                        <dd className="mt-0.5 text-[13px] font-semibold tabular-nums text-[var(--color-fg)]">{formatCurrencyMinor(order.amount_minor, order.currency, i18n.resolvedLanguage)}</dd>
                      </div>
                      <div>
                        <dt className="text-[11px] text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.table.time')}</dt>
                        <dd className="mt-0.5 text-[11px] leading-5 text-[var(--color-fg-muted)]">
                          {renderOrderTimes(order)}
                        </dd>
                      </div>
                      {order.failure_reason ? (
                        <div className="min-w-0 sm:col-span-2">
                          <dt className="text-[11px] text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.failureReason')}</dt>
                          <dd className="mt-0.5 whitespace-pre-wrap break-words text-[11px] leading-5 text-[var(--color-danger)] [overflow-wrap:anywhere]">{order.failure_reason}</dd>
                        </div>
                      ) : null}
                      {order.reconcile_error ? (
                        <div className="min-w-0 sm:col-span-2">
                          <dt className="text-[11px] text-[var(--color-fg-subtle)]">{t('admin:paymentOrders.reconciliation')}</dt>
                          <dd className="mt-0.5 whitespace-pre-wrap break-words text-[11px] leading-5 text-[var(--color-danger)] [overflow-wrap:anywhere]">{order.reconcile_error}</dd>
                        </div>
                      ) : null}
                    </dl>
                    {renderOrderActions(order)}
                  </li>
                )
              })}
            </ul>
            <Pagination className="max-sm:[&_button]:size-11" page={page} pageCount={pageCount} onPage={setPage} />
          </>
        )}
      </section>

      <Dialog
        open={Boolean(closeTarget)}
        onOpenChange={(open) => {
          if (!open && (!closeTarget || busyOrders[closeTarget.id] !== 'close')) dismissCloseDialog()
        }}
      >
        <DialogContent size="sm" className="rounded-[8px] font-sans max-sm:[&>button]:size-11">
          <form className="flex min-h-0 flex-1 flex-col" onSubmit={(event) => void closeOrder(event)}>
            <DialogHeader className="px-5 pt-5 pb-3 max-sm:pr-16">
              <DialogTitle>{t('admin:paymentOrders.closeDialog.title')}</DialogTitle>
              <DialogDescription className="mt-1 break-words text-[13px] [overflow-wrap:anywhere]">
                {closeTarget ? t('admin:paymentOrders.closeDialog.description', { id: closeTarget.id }) : ''}
              </DialogDescription>
            </DialogHeader>
            <DialogBody className="px-5 pb-5">
              <div className="flex gap-2.5 rounded-[8px] border border-[var(--color-warning)]/25 bg-[var(--color-warning-soft)] px-3 py-2.5 text-[12px] leading-5 text-[var(--color-fg-muted)]" role="note">
                <ShieldAlert className="mt-0.5 size-4 shrink-0 text-[var(--color-warning)]" aria-hidden />
                <p>
                  {closeTarget?.provider === 'epay'
                    ? t('admin:paymentOrders.closeDialog.epayWarning')
                    : t('admin:paymentOrders.closeDialog.providerWarning')}
                </p>
              </div>

              <label className="mt-4 block text-[13px] font-medium text-[var(--color-fg)]" htmlFor="payment-order-close-reason">
                {t('admin:paymentOrders.closeDialog.reason')}
              </label>
              <Textarea
                id="payment-order-close-reason"
                className="mt-1.5 min-h-20 rounded-[8px] text-[13px]"
                value={closeReason}
                onChange={(event) => setCloseReason(event.target.value)}
                placeholder={t('admin:paymentOrders.closeDialog.reasonPlaceholder')}
                minLength={5}
                maxLength={500}
                required
                disabled={Boolean(closeTarget && busyOrders[closeTarget.id] === 'close')}
                aria-describedby="payment-order-close-reason-hint"
              />
              <p id="payment-order-close-reason-hint" className="mt-1 text-[11px] text-[var(--color-fg-subtle)]">
                {t('admin:paymentOrders.closeDialog.reasonHint')}
              </p>

              <label className="mt-4 flex cursor-pointer items-start gap-2.5 rounded-[8px] border border-[var(--color-border)] px-3 py-2.5 text-[12px] leading-5 text-[var(--color-fg-muted)]">
                <input
                  type="checkbox"
                  className="mt-1 size-4 shrink-0 cursor-pointer accent-[var(--color-danger)]"
                  checked={closeAcknowledged}
                  onChange={(event) => setCloseAcknowledged(event.target.checked)}
                  required
                  disabled={Boolean(closeTarget && busyOrders[closeTarget.id] === 'close')}
                />
                <span>
                  {closeTarget?.provider === 'epay'
                    ? t('admin:paymentOrders.closeDialog.epayAcknowledge')
                    : t('admin:paymentOrders.closeDialog.acknowledge')}
                </span>
              </label>
            </DialogBody>
            <DialogFooter className="max-sm:[&_button]:!h-11">
              <Button
                className="rounded-[8px]"
                variant="ghost"
                disabled={Boolean(closeTarget && busyOrders[closeTarget.id] === 'close')}
                onClick={dismissCloseDialog}
              >
                {t('common:actions.cancel')}
              </Button>
              <Button
                className="rounded-[8px]"
                type="submit"
                variant="destructive"
                loading={Boolean(closeTarget && busyOrders[closeTarget.id] === 'close')}
                disabled={!closeAcknowledged || closeReason.trim().length < 5}
              >
                {closeTarget?.provider === 'epay'
                  ? t('admin:paymentOrders.actions.manualClose')
                  : t('admin:paymentOrders.actions.close')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
