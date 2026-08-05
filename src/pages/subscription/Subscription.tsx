import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AlertTriangle,
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  Clock,
  Loader2,
  ReceiptText,
  RefreshCw,
  Sparkles,
  Ticket,
  Wallet,
} from 'lucide-react'
import { authApi, creditPackagesApi, groupsApi, paymentsApi, redeemApi, ApiError } from '@/api'
import type { ApiCreditPackage, ApiCredits, ApiUserGroup, ApiUserPaymentOrder } from '@/api/types'
import { useAuth } from '@/store/auth'
import { ContentHeader } from '@/components/layout/content-header'
import { PaymentMethodDialog } from '@/components/payment/PaymentMethodDialog'
import { UserGroupTierCard } from '@/components/subscription/user-group-tier-card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { SegmentedControl } from '@/components/ui/segmented-control'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { formatCurrencyMinor } from '@/lib/currency'
import {
  CHECKOUT_REQUEST_TIMEOUT_MS,
  PaymentCheckoutActionRunner,
  PaymentCheckoutActionError,
} from '@/lib/payment-checkout'
import { checkoutPaymentErrorKey } from '@/lib/payment-errors'
import {
  canResumePaymentOrder,
  PaymentOrderRecoveryCoordinator,
  paymentOrderResumeKind,
} from '@/lib/payment-order-state'
import { groupPriceAmount, type BillingCycle } from '@/lib/user-group-tier'
import { cn, formatAbsoluteDate, formatDateTime } from '@/lib/utils'

type TFn = (key: string, options?: Record<string, unknown>) => string
type CatalogTab = 'groups' | 'credit-packages'
type PurchaseTarget =
  | { type: 'credit_package'; id: string; name: string }
  | { type: 'user_group'; id: string; name: string; billingCycle: BillingCycle }

const PAYMENT_HISTORY_PAGE_SIZE = 10

export default function Subscription() {
  const { t, i18n } = useTranslation(['subscription', 'common'])
  const user = useAuth((state) => state.user)
  const setUser = useAuth((state) => state.setUser)
  const [groups, setGroups] = useState<ApiUserGroup[]>([])
  const [creditPackages, setCreditPackages] = useState<ApiCreditPackage[]>([])
  const [credits, setCredits] = useState<ApiCredits | null>(null)
  const [groupsLoading, setGroupsLoading] = useState(true)
  const [packagesLoading, setPackagesLoading] = useState(true)
  const [creditsLoading, setCreditsLoading] = useState(true)
  const [groupsLoadError, setGroupsLoadError] = useState(false)
  const [packagesLoadError, setPackagesLoadError] = useState(false)
  const [creditsLoadError, setCreditsLoadError] = useState(false)
  const [groupsReloadKey, setGroupsReloadKey] = useState(0)
  const [packagesReloadKey, setPackagesReloadKey] = useState(0)
  const [creditsReloadKey, setCreditsReloadKey] = useState(0)
  const [paymentOrders, setPaymentOrders] = useState<ApiUserPaymentOrder[]>([])
  const [paymentHistoryTotal, setPaymentHistoryTotal] = useState(0)
  const [paymentHistoryPage, setPaymentHistoryPage] = useState(0)
  const [paymentHistoryLoading, setPaymentHistoryLoading] = useState(true)
  const [paymentHistoryError, setPaymentHistoryError] = useState(false)
  const [paymentHistoryReloadKey, setPaymentHistoryReloadKey] = useState(0)
  const [resumingPaymentOrderId, setResumingPaymentOrderId] = useState('')
  const [retryPaymentOrder, setRetryPaymentOrder] = useState<ApiUserPaymentOrder | null>(null)
  const [selectedPaymentOrder, setSelectedPaymentOrder] = useState<ApiUserPaymentOrder | null>(null)
  const [paymentOrderDetail, setPaymentOrderDetail] = useState<ApiUserPaymentOrder | null>(null)
  const [paymentOrderDetailLoading, setPaymentOrderDetailLoading] = useState(false)
  const [paymentOrderDetailError, setPaymentOrderDetailError] = useState(false)
  const [catalogTab, setCatalogTab] = useState<CatalogTab>('groups')
  const [billingCycle, setBillingCycle] = useState<BillingCycle>('monthly')
  const [purchaseTarget, setPurchaseTarget] = useState<PurchaseTarget | null>(null)
  const [upgrade, setUpgrade] = useState<ApiUserGroup | null>(null)
  const [redeemCode, setRedeemCode] = useState('')
  const [redeeming, setRedeeming] = useState(false)
  const [redeemSuccess, setRedeemSuccess] = useState<
    { kind: 'group'; group: string; date: string } | { kind: 'credits'; credits: number } | null
  >(null)
  const [confirmOverride, setConfirmOverride] = useState<{
    code: string
    current: string
    granted: string
    date: string
  } | null>(null)
  const paymentResumeAbortRef = useRef<AbortController | null>(null)
  const paymentOrderDetailAbortRef = useRef<AbortController | null>(null)
  const paymentResumeRunnerRef = useRef<PaymentCheckoutActionRunner | null>(null)
  const paymentRecoveryCoordinatorRef = useRef<PaymentOrderRecoveryCoordinator<ApiUserPaymentOrder> | null>(null)
  if (!paymentResumeRunnerRef.current) {
    paymentResumeRunnerRef.current = new PaymentCheckoutActionRunner(setResumingPaymentOrderId)
  }
  if (!paymentRecoveryCoordinatorRef.current) {
    paymentRecoveryCoordinatorRef.current = new PaymentOrderRecoveryCoordinator<ApiUserPaymentOrder>(
      setRetryPaymentOrder,
    )
  }

  useEffect(() => {
    let active = true

    setGroupsLoading(true)
    setGroupsLoadError(false)
    groupsApi
      .list()
      .then((items) => active && setGroups(items))
      .catch(() => {
        if (active) setGroupsLoadError(true)
      })
      .finally(() => active && setGroupsLoading(false))

    return () => {
      active = false
    }
  }, [groupsReloadKey])

  useEffect(() => {
    let active = true

    setPackagesLoading(true)
    setPackagesLoadError(false)
    creditPackagesApi
      .list()
      .then((items) => active && setCreditPackages(items))
      .catch(() => {
        if (active) setPackagesLoadError(true)
      })
      .finally(() => active && setPackagesLoading(false))

    return () => {
      active = false
    }
  }, [packagesReloadKey])

  useEffect(() => {
    let active = true

    setCreditsLoading(true)
    setCreditsLoadError(false)
    authApi
      .credits()
      .then((value) => active && setCredits(value))
      .catch(() => {
        if (active) setCreditsLoadError(true)
      })
      .finally(() => active && setCreditsLoading(false))

    return () => {
      active = false
    }
  }, [creditsReloadKey])

  useEffect(() => {
    let active = true
    const offset = paymentHistoryPage * PAYMENT_HISTORY_PAGE_SIZE

    setPaymentHistoryLoading(true)
    setPaymentHistoryError(false)
    setPaymentOrders([])
    paymentsApi
      .orders(PAYMENT_HISTORY_PAGE_SIZE, offset)
      .then((response) => {
        if (!active) return
        if (response.orders.length === 0 && paymentHistoryPage > 0 && offset >= response.total) {
          setPaymentHistoryPage((page) => Math.max(0, page - 1))
          return
        }
        setPaymentOrders(response.orders)
        setPaymentHistoryTotal(response.total)
      })
      .catch(() => {
        if (active) setPaymentHistoryError(true)
      })
      .finally(() => {
        if (active) setPaymentHistoryLoading(false)
      })

    return () => {
      active = false
    }
  }, [paymentHistoryPage, paymentHistoryReloadKey])

  useEffect(
    () => () => {
      paymentResumeAbortRef.current?.abort('unmounted')
      paymentOrderDetailAbortRef.current?.abort('unmounted')
      paymentOrderDetailAbortRef.current = null
    },
    [],
  )

  useEffect(() => {
    const url = new URL(window.location.href)
    const paymentReturn = url.searchParams.get('payment')
    const orderId = url.searchParams.get('order')?.trim() || ''
    if ((paymentReturn !== 'return' && paymentReturn !== 'cancel') || !orderId || orderId.length > 128) return

    let cancelled = false
    let timer: number | undefined
    let attempts = 0
    let consecutiveErrors = 0

    function clearReturnParams() {
      const next = new URL(window.location.href)
      next.searchParams.delete('payment')
      next.searchParams.delete('order')
      window.history.replaceState(window.history.state, '', `${next.pathname}${next.search}${next.hash}`)
    }

    async function refreshEntitlements() {
      const [meResult, creditsResult, groupsResult] = await Promise.allSettled([
        authApi.me(),
        authApi.credits(),
        groupsApi.list(),
      ])
      if (cancelled) return
      if (meResult.status === 'fulfilled') setUser(meResult.value)
      if (creditsResult.status === 'fulfilled') {
        setCredits(creditsResult.value)
        setCreditsLoadError(false)
      }
      if (groupsResult.status === 'fulfilled') {
        setGroups(groupsResult.value)
        setGroupsLoadError(false)
      }
    }

    async function handleCancelledReturn() {
      try {
        const order = await paymentsApi.order(orderId)
        if (cancelled) return
        if (order.status === 'paid') {
          await refreshEntitlements()
          if (cancelled) return
          setPaymentHistoryReloadKey((value) => value + 1)
          toast.success(t('subscription:payment.return.paid'), t('subscription:payment.return.paidHint'))
        } else if (order.status === 'failed') {
          setPaymentHistoryReloadKey((value) => value + 1)
          toast.error(t('subscription:payment.return.failed'))
        } else if (order.status === 'expired') {
          setPaymentHistoryReloadKey((value) => value + 1)
          toast.error(t('subscription:payment.return.expired'))
        } else {
          setPaymentHistoryReloadKey((value) => value + 1)
          toast.info(t('subscription:payment.return.cancelled'))
        }
      } catch {
        if (!cancelled) toast.info(t('subscription:payment.return.cancelled'))
      } finally {
        if (!cancelled) clearReturnParams()
      }
    }

    async function pollOrder() {
      attempts += 1
      try {
        const order = await paymentsApi.order(orderId)
        if (cancelled) return
        consecutiveErrors = 0

        if (order.status === 'paid') {
          await refreshEntitlements()
          if (cancelled) return
          clearReturnParams()
          setPaymentHistoryReloadKey((value) => value + 1)
          toast.success(t('subscription:payment.return.paid'), t('subscription:payment.return.paidHint'))
          return
        }
        if (order.status === 'failed') {
          clearReturnParams()
          setPaymentHistoryReloadKey((value) => value + 1)
          toast.error(t('subscription:payment.return.failed'))
          return
        }
        if (order.status === 'expired') {
          clearReturnParams()
          setPaymentHistoryReloadKey((value) => value + 1)
          toast.error(t('subscription:payment.return.expired'))
          return
        }
      } catch {
        if (cancelled) return
        consecutiveErrors += 1
        if (consecutiveErrors >= 5) {
          toast.error(t('subscription:payment.return.statusError'))
          return
        }
      }

      if (attempts >= 45) {
        setPaymentHistoryReloadKey((value) => value + 1)
        toast.warning(t('subscription:payment.return.pending'))
        return
      }
      timer = window.setTimeout(() => void pollOrder(), 2000)
    }

    if (paymentReturn === 'cancel') {
      void handleCancelledReturn()
    } else {
      toast.info(t('subscription:payment.return.checking'))
      void pollOrder()
    }

    return () => {
      cancelled = true
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [setUser, t])

  const currentId = user?.group_id || groups.find((group) => group.is_default)?.id || ''
  const sortedGroups = useMemo(
    () => groups.slice().sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name)),
    [groups],
  )
  const sortedPackages = useMemo(
    () =>
      creditPackages
        .slice()
        .sort((a, b) => a.sort_order - b.sort_order || a.price_amount_minor - b.price_amount_minor),
    [creditPackages],
  )
  const current = sortedGroups.find((group) => group.id === currentId)
  const currentGroupName = user?.group_name?.trim() || current?.name || ''
  const expiresAt = user?.group_expires_at ?? 0
  const permanentBaselineId = expiresAt > 0 ? user?.previous_group_id || '' : ''
  const recommendedId = useMemo(() => {
    const paid = sortedGroups.filter(
      (group) =>
        !group.is_default &&
        group.is_purchasable !== false &&
        group.id !== currentId &&
        group.id !== permanentBaselineId &&
        groupPriceAmount(group, billingCycle) > 0,
    )
    if (paid.length === 0) return null
    return paid.reduce((best, group) =>
      groupPriceAmount(group, billingCycle) > groupPriceAmount(best, billingCycle) ? group : best,
    ).id
  }, [billingCycle, currentId, permanentBaselineId, sortedGroups])
  const expiresLabel = expiresAt > 0 ? t('subscription:expiresOn', { date: formatAbsoluteDate(expiresAt * 1000) }) : null

  function redeemErrorMessage(error: unknown): string {
    if (error instanceof ApiError) {
      switch (error.message) {
        case 'code_invalid':
          return t('subscription:redeem.errors.invalid')
        case 'code_expired':
          return t('subscription:redeem.errors.expired')
        case 'code_used':
          return t('subscription:redeem.errors.alreadyUsed')
        case 'code_disabled':
          return t('subscription:redeem.errors.disabled')
        case 'code_already_owned':
          return t('subscription:redeem.errors.alreadyOwned')
      }
      return error.message || t('subscription:redeem.errors.generic')
    }
    return t('subscription:redeem.errors.generic')
  }

  async function applyRedeem(code: string, confirm: boolean) {
    setRedeeming(true)
    try {
      const response = await redeemApi.redeem(code, confirm)
      const date =
        (response.expires_at ?? 0) > 0 ? formatAbsoluteDate((response.expires_at ?? 0) * 1000) : ''
      if (response.requires_confirmation) {
        setConfirmOverride({
          code,
          current: response.current_group_name || '',
          granted: response.group_name || '',
          date,
        })
        return
      }
      if (response.user) setUser(response.user)
      groupsApi.list().then(setGroups).catch(() => undefined)
      authApi.credits().then(setCredits).catch(() => undefined)
      setRedeemCode('')
      setConfirmOverride(null)
      setRedeemSuccess(
        response.kind === 'credits'
          ? { kind: 'credits', credits: response.credits ?? 0 }
          : { kind: 'group', group: response.group_name || '', date },
      )
    } catch (error) {
      toast.error(redeemErrorMessage(error))
    } finally {
      setRedeeming(false)
    }
  }

  function submitRedeem() {
    const code = redeemCode.trim()
    if (!code) {
      toast.error(t('subscription:redeem.errors.empty'))
      return
    }
    void applyRedeem(code, false)
  }

  function resumePaymentErrorMessage(error: unknown): string {
    if (error instanceof PaymentCheckoutActionError) return t('subscription:payment.invalidUrl')
    if (!(error instanceof ApiError)) return t('subscription:history.resumeError')
    const key = checkoutPaymentErrorKey(error.message)
    return key ? t(`subscription:${key}`) : t('subscription:history.resumeError')
  }

  async function resumePaymentOrder(order: ApiUserPaymentOrder) {
    if (!canResumePaymentOrder(order)) return

    const attempt: {
      controller: AbortController | null
      timedOut: boolean
      timeoutId?: number
    } = { controller: null, timedOut: false }
    const result = await paymentResumeRunnerRef.current!.run(order.id, async () => {
      attempt.controller = new AbortController()
      paymentResumeAbortRef.current = attempt.controller
      attempt.timeoutId = window.setTimeout(() => {
        attempt.timedOut = true
        attempt.controller?.abort('timeout')
      }, CHECKOUT_REQUEST_TIMEOUT_MS)
      const response = await paymentsApi.resumeOrder(order.id, attempt.controller.signal)
      return response.action
    })

    if (attempt.timeoutId !== undefined) window.clearTimeout(attempt.timeoutId)
    if (attempt.controller && paymentResumeAbortRef.current === attempt.controller) {
      paymentResumeAbortRef.current = null
    }

    if (result.status === 'error') {
      if (attempt.controller?.signal.aborted && !attempt.timedOut) return
      toast.error(
        attempt.timedOut
          ? t('subscription:payment.checkoutTimeout')
          : resumePaymentErrorMessage(result.error),
      )
      setPaymentHistoryReloadKey((value) => value + 1)
    }
  }

  function requestPaymentOrderResume(order: ApiUserPaymentOrder) {
    paymentRecoveryCoordinatorRef.current!.request(order, (selectedOrder) => {
      void resumePaymentOrder(selectedOrder)
    })
  }

  function confirmPaymentOrderRetry() {
    paymentRecoveryCoordinatorRef.current!.confirmRetry((order) => {
      void resumePaymentOrder(order)
    })
  }

  async function loadPaymentOrderDetail(order: ApiUserPaymentOrder) {
    paymentOrderDetailAbortRef.current?.abort('superseded')
    const controller = new AbortController()
    paymentOrderDetailAbortRef.current = controller
    setSelectedPaymentOrder(order)
    setPaymentOrderDetail(null)
    setPaymentOrderDetailLoading(true)
    setPaymentOrderDetailError(false)

    try {
      const detail = await paymentsApi.order(order.id, controller.signal)
      if (!controller.signal.aborted) setPaymentOrderDetail(detail)
    } catch {
      if (!controller.signal.aborted) setPaymentOrderDetailError(true)
    } finally {
      if (paymentOrderDetailAbortRef.current === controller) {
        paymentOrderDetailAbortRef.current = null
        setPaymentOrderDetailLoading(false)
      }
    }
  }

  function closePaymentOrderDetail() {
    paymentOrderDetailAbortRef.current?.abort('closed')
    paymentOrderDetailAbortRef.current = null
    setSelectedPaymentOrder(null)
    setPaymentOrderDetail(null)
    setPaymentOrderDetailLoading(false)
    setPaymentOrderDetailError(false)
  }

  function resumeFromPaymentOrderDetail(order: ApiUserPaymentOrder) {
    closePaymentOrderDetail()
    requestPaymentOrderResume(order)
  }

  const creditsOn = Boolean(credits?.enabled)
  const showCreditsPanel = creditsLoading || creditsLoadError || creditsOn
  const hasCurrentGroup = Boolean(currentGroupName)
  const showAccount = hasCurrentGroup || showCreditsPanel
  const showingGroups = catalogTab === 'groups'
  const catalogCount = showingGroups ? sortedGroups.length : sortedPackages.length

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-[var(--color-bg)] font-sans text-[var(--color-fg)]">
      <ContentHeader title={t('subscription:title')} backTo="/" backLabel={t('subscription:back')} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <main className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-4 py-4 pb-16 sm:px-8 sm:py-6 sm:pb-20">
          {groupsLoading ? (
            <AccountSkeleton t={t} />
          ) : showAccount ? (
            <section className="overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)]">
              <div
                className={cn(
                  hasCurrentGroup && showCreditsPanel &&
                    'md:grid md:grid-cols-[minmax(0,0.8fr)_minmax(0,1.7fr)]',
                )}
              >
                {hasCurrentGroup ? (
                  <div className="min-w-0 p-3.5 sm:p-4">
                    <span className="inline-flex items-center gap-1.5 text-[11px] font-medium text-[var(--color-fg-muted)]">
                      <span className="size-1.5 rounded-full bg-[var(--color-secondary)]" aria-hidden />
                      {t('subscription:currentPlan')}
                    </span>
                    <div className="mt-1 flex flex-wrap items-center gap-1.5">
                      <h2 className="min-w-0 break-words text-[1.125rem] font-semibold leading-tight text-[var(--color-fg)] [overflow-wrap:anywhere]">{currentGroupName}</h2>
                      {current?.is_default ? (
                        <Badge size="sm" variant="neutral">
                          {t('subscription:free')}
                        </Badge>
                      ) : null}
                      {expiresLabel ? (
                        <Badge size="sm" variant="sage">
                          {expiresLabel}
                        </Badge>
                      ) : null}
                    </div>
                    {current?.description ? (
                      <p className="mt-1 max-w-prose break-words text-[11.5px] leading-snug text-[var(--color-fg-muted)] [overflow-wrap:anywhere]">
                        {current.description}
                      </p>
                    ) : null}
                  </div>
                ) : null}

                {creditsLoading ? (
                  <BalanceState hasPlanHeader={hasCurrentGroup} loading t={t} />
                ) : creditsLoadError ? (
                  <BalanceState
                    hasPlanHeader={hasCurrentGroup}
                    loading={false}
                    onRetry={() => setCreditsReloadKey((value) => value + 1)}
                    t={t}
                  />
                ) : creditsOn && credits ? (
                  <Balance credits={credits} hasPlanHeader={hasCurrentGroup} locale={i18n.resolvedLanguage} t={t} />
                ) : null}
              </div>
            </section>
          ) : null}

          <section className="mt-5 sm:mt-6" aria-labelledby="subscription-catalog-heading">
            <div className="flex items-center">
              <SegmentedControl<CatalogTab>
                label={t('subscription:catalog.label')}
                value={catalogTab}
                onChange={setCatalogTab}
                fullWidthOnMobile
                options={[
                  { value: 'groups', label: t('subscription:catalog.userGroups') },
                  { value: 'credit-packages', label: t('subscription:catalog.creditPackages') },
                ]}
              />
            </div>

            <div className="mt-3 sm:mt-4">
              <div className="flex min-w-0 items-center justify-between gap-3">
                <h2
                  id="subscription-catalog-heading"
                  className="min-w-0 text-[1.25rem] font-semibold leading-7 text-[var(--color-fg)]"
                >
                  {showingGroups ? t('subscription:allPlans') : t('subscription:packages.title')}
                </h2>
                {showingGroups ? (
                  <SegmentedControl<BillingCycle>
                    compact
                    label={t('subscription:billing.label')}
                    value={billingCycle}
                    onChange={setBillingCycle}
                    options={[
                      { value: 'monthly', label: t('subscription:billing.monthly') },
                      { value: 'yearly', label: t('subscription:billing.yearly') },
                    ]}
                  />
                ) : null}
              </div>
              <div className="mt-1 flex min-w-0 items-start justify-between gap-4">
                <p className="max-w-[60ch] text-[13px] leading-relaxed text-[var(--color-fg-muted)]">
                  {showingGroups ? t('subscription:subtitle') : t('subscription:packages.subtitle')}
                </p>
                {catalogCount > 0 ? (
                  <span className="hidden shrink-0 pt-1 text-[12px] tabular-nums text-[var(--color-fg-subtle)] sm:inline">
                    {showingGroups
                      ? t('subscription:planCount', { count: catalogCount })
                      : t('subscription:packages.count', { count: catalogCount })}
                  </span>
                ) : null}
              </div>
            </div>

            <div id="subscription-catalog" className="mt-3 sm:mt-4">
              {showingGroups ? (
                groupsLoading ? (
                  <CardsSkeleton t={t} />
                ) : groupsLoadError ? (
                  <CatalogLoadError
                    message={t('subscription:loadFailed')}
                    onRetry={() => setGroupsReloadKey((value) => value + 1)}
                    t={t}
                  />
                ) : sortedGroups.length > 0 ? (
                  <div className="grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    {sortedGroups.map((group) => (
                      <UserGroupTierCard
                        key={group.id}
                        group={group}
                        billingCycle={billingCycle}
                        isCurrent={group.id === currentId}
                        canRenew={group.id === currentId && !group.is_default && expiresAt > 0}
                        isPermanentlyOwned={group.id === permanentBaselineId}
                        isRecommended={group.id === recommendedId}
                        onSwitch={() => setUpgrade(group)}
                        onPurchase={() => {
                          if (group.is_purchasable === false) return
                          setPurchaseTarget({
                            type: 'user_group',
                            id: group.id,
                            name: group.name,
                            billingCycle,
                          })
                        }}
                        locale={i18n.resolvedLanguage}
                      />
                    ))}
                  </div>
                ) : (
                  <EmptyCatalog>{t('subscription:noGroups')}</EmptyCatalog>
                )
              ) : packagesLoading ? (
                <CardsSkeleton t={t} />
              ) : packagesLoadError ? (
                <CatalogLoadError
                  message={t('subscription:loadPackagesFailed')}
                  onRetry={() => setPackagesReloadKey((value) => value + 1)}
                  t={t}
                />
              ) : sortedPackages.length > 0 ? (
                <div className="grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  {sortedPackages.map((creditPackage) => (
                    <CreditPackageCard
                      key={creditPackage.id}
                      creditPackage={creditPackage}
                      onPurchase={() =>
                        setPurchaseTarget({
                          type: 'credit_package',
                          id: creditPackage.id,
                          name: creditPackage.name,
                        })
                      }
                      locale={i18n.resolvedLanguage}
                      t={t}
                    />
                  ))}
                </div>
              ) : (
                <EmptyCatalog>{t('subscription:packages.empty')}</EmptyCatalog>
              )}
            </div>
          </section>

          <section className="mt-6" aria-labelledby="redeem-heading">
            <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between xl:gap-6">
              <div className="flex min-w-0 items-start gap-2.5">
                <span className="mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                  <Ticket size={16} aria-hidden />
                </span>
                <div className="min-w-0">
                  <h2 id="redeem-heading" className="text-[1rem] font-semibold text-[var(--color-fg)]">
                    {t('subscription:redeem.title')}
                  </h2>
                  <p className="mt-0.5 max-w-[60ch] text-[12px] leading-snug text-[var(--color-fg-muted)]">
                    {t('subscription:redeem.subtitle')}
                  </p>
                </div>
              </div>
              <form
                className="flex min-w-0 items-center gap-2 sm:max-w-[30rem] xl:w-[26rem] xl:shrink-0"
                onSubmit={(event) => {
                  event.preventDefault()
                  submitRedeem()
                }}
              >
                <label className="sr-only" htmlFor="redeem-code">
                  {t('subscription:redeem.inputLabel')}
                </label>
                <Input
                  id="redeem-code"
                  value={redeemCode}
                  onChange={(event) => setRedeemCode(event.target.value.toUpperCase())}
                  placeholder={t('subscription:redeem.inputPlaceholder')}
                  autoComplete="off"
                  spellCheck={false}
                  wrapperClassName="h-11 min-w-0 flex-1 sm:h-9"
                  className="min-w-0 font-sans tracking-normal"
                />
                <Button
                  className="min-h-11 shrink-0 px-4 sm:min-h-9"
                  type="submit"
                  size="sm"
                  loading={redeeming}
                  disabled={!redeemCode.trim() || redeeming}
                >
                  {redeeming ? t('subscription:redeem.redeeming') : t('subscription:redeem.submit')}
                </Button>
              </form>
            </div>
          </section>

          <PaymentHistory
            orders={paymentOrders}
            total={paymentHistoryTotal}
            page={paymentHistoryPage}
            loading={paymentHistoryLoading}
            error={paymentHistoryError}
            locale={i18n.resolvedLanguage}
            resumingOrderId={resumingPaymentOrderId}
            onRetry={() => setPaymentHistoryReloadKey((value) => value + 1)}
            onResume={requestPaymentOrderResume}
            onViewDetails={(order) => void loadPaymentOrderDetail(order)}
            onPageChange={setPaymentHistoryPage}
            t={t}
          />
        </main>
      </div>

      {purchaseTarget ? (
        <PaymentMethodDialog
          open
          onOpenChange={(open) => !open && setPurchaseTarget(null)}
          targetType={purchaseTarget.type}
          targetId={purchaseTarget.id}
          targetName={purchaseTarget.name}
          billingCycle={purchaseTarget.type === 'user_group' ? purchaseTarget.billingCycle : undefined}
        />
      ) : null}

      <PaymentOrderDetailsDialog
        open={Boolean(selectedPaymentOrder)}
        order={paymentOrderDetail}
        selectedOrder={selectedPaymentOrder}
        loading={paymentOrderDetailLoading}
        error={paymentOrderDetailError}
        locale={i18n.resolvedLanguage}
        resuming={Boolean(paymentOrderDetail && resumingPaymentOrderId === paymentOrderDetail.id)}
        onOpenChange={(open) => {
          if (!open) closePaymentOrderDetail()
        }}
        onRetry={() => {
          if (selectedPaymentOrder) void loadPaymentOrderDetail(selectedPaymentOrder)
        }}
        onResume={resumeFromPaymentOrderDetail}
        t={t}
      />

      <Dialog
        open={Boolean(retryPaymentOrder)}
        onOpenChange={(open) => {
          if (!open) paymentRecoveryCoordinatorRef.current!.cancelRetry()
        }}
      >
        <DialogContent size="sm" className="rounded-[8px] font-sans max-sm:[&>button]:size-11">
          <DialogHeader className="px-5 pt-5 pb-3 max-sm:pr-16">
            <DialogTitle>{t('subscription:history.retryConfirm.title')}</DialogTitle>
            <DialogDescription className="mt-1 break-words text-[13px] [overflow-wrap:anywhere]">
              {retryPaymentOrder
                ? t('subscription:history.retryConfirm.description', { name: retryPaymentOrder.target_name })
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="px-5 pb-5">
            <div
              className="flex gap-2.5 rounded-[8px] border border-[var(--color-warning)]/25 bg-[var(--color-warning-soft)] px-3 py-2.5 text-[12px] leading-5 text-[var(--color-fg-muted)]"
              role="note"
            >
              <AlertTriangle className="mt-0.5 size-4 shrink-0 text-[var(--color-warning)]" aria-hidden />
              <p>{t('subscription:history.retryConfirm.warning')}</p>
            </div>
          </DialogBody>
          <DialogFooter className="max-sm:[&_button]:!h-11">
            <Button
              className="rounded-[8px]"
              variant="ghost"
              onClick={() => paymentRecoveryCoordinatorRef.current!.cancelRetry()}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button className="rounded-[8px]" onClick={confirmPaymentOrderRetry}>
              {t('subscription:history.retryConfirm.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(redeemSuccess)} onOpenChange={(open) => !open && setRedeemSuccess(null)}>
        <DialogContent size="sm" className="font-sans">
          <DialogHeader>
            <div className="mx-auto mb-1 inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]">
              <Sparkles size={22} aria-hidden />
            </div>
            <DialogTitle className="text-center">
              {redeemSuccess?.kind === 'credits'
                ? t('subscription:redeem.successCredits')
                : t('subscription:redeem.success')}
            </DialogTitle>
            <DialogDescription className="text-center">
              {redeemSuccess
                ? redeemSuccess.kind === 'credits'
                  ? t('subscription:redeem.successBodyCredits', { count: redeemSuccess.credits })
                  : redeemSuccess.date
                    ? t('subscription:redeem.successBodyUntil', {
                        group: redeemSuccess.group,
                        date: redeemSuccess.date,
                      })
                    : t('subscription:redeem.successBody', { group: redeemSuccess.group })
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button className="w-full" onClick={() => setRedeemSuccess(null)}>
              {t('common:actions.gotIt')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(confirmOverride)}
        onOpenChange={(open) => !open && !redeeming && setConfirmOverride(null)}
      >
        <DialogContent size="sm" className="font-sans">
          <DialogHeader>
            <div className="mx-auto mb-1 inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-warning-soft)] text-[var(--color-warning)]">
              <AlertTriangle size={22} aria-hidden />
            </div>
            <DialogTitle className="text-center">{t('subscription:redeem.override.title')}</DialogTitle>
            <DialogDescription className="text-center">
              {confirmOverride
                ? confirmOverride.date
                  ? t('subscription:redeem.override.bodyUntil', {
                      current: confirmOverride.current,
                      granted: confirmOverride.granted,
                      date: confirmOverride.date,
                    })
                  : t('subscription:redeem.override.body', {
                      current: confirmOverride.current,
                      granted: confirmOverride.granted,
                    })
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={redeeming} onClick={() => setConfirmOverride(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={redeeming} onClick={() => confirmOverride && void applyRedeem(confirmOverride.code, true)}>
              {t('subscription:redeem.override.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(upgrade)} onOpenChange={(open) => !open && setUpgrade(null)}>
        <DialogContent size="sm" className="font-sans">
          <DialogHeader>
            <DialogTitle>{upgrade ? t('subscription:upgradeTitle', { name: upgrade.name }) : ''}</DialogTitle>
            <DialogDescription>{t('subscription:upgradeBody')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button onClick={() => setUpgrade(null)}>{t('common:actions.gotIt')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function Balance({
  credits,
  hasPlanHeader,
  locale,
  t,
}: {
  credits: ApiCredits
  hasPlanHeader: boolean
  locale?: string
  t: TFn
}) {
  const timed = credits.timed
  const showTimed = Boolean(timed && timed.allowance > 0 && timed.period_seconds > 0)
  const percentage =
    timed && timed.allowance > 0 ? Math.max(0, Math.min(100, (timed.remaining / timed.allowance) * 100)) : 0
  const timedLabel = t('subscription:credits.timedTitle')
  const permanentLabel = t('subscription:credits.permanentTitle')
  const timedValue = timed ? formatCredits(timed.remaining, locale) : ''
  const permanentValue = formatCredits(credits.permanent, locale)

  return (
    <div
      className={cn(
        'min-w-0 bg-[var(--color-bg-muted)] p-3 sm:p-3.5',
        hasPlanHeader && 'border-t border-[var(--color-divider)] md:border-l md:border-t-0',
      )}
    >
      <div className={cn('grid gap-3', showTimed ? 'xl:grid-cols-2 xl:gap-4' : 'grid-cols-1')}>
        {showTimed && timed ? (
          <div className="min-w-0">
            <div className="flex min-w-0 items-center justify-between gap-2">
              <span
                className={cn(
                  'inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap font-medium text-[var(--color-accent)]',
                  balanceLabelSize(timedLabel),
                )}
              >
                <Clock className="shrink-0" size={13} aria-hidden />
                {timedLabel}
              </span>
              <span
                className={cn(
                  'min-w-0 whitespace-nowrap font-semibold leading-none tabular-nums text-[var(--color-fg)]',
                  balanceValueSize(timedValue),
                )}
              >
                {timedValue}
              </span>
            </div>
            <div
              className="mt-2 h-1 w-full overflow-hidden rounded-full bg-[var(--color-accent-soft)]"
              role="progressbar"
              aria-label={timedLabel}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(percentage)}
              aria-valuetext={`${timedValue} / ${formatCredits(timed.allowance, locale)}`}
            >
              <div
                className="h-full rounded-full bg-[var(--color-accent)]"
                style={{ width: `${percentage}%` }}
              />
            </div>
            {timed.resets_at > 0 ? (
              <p className="mt-1.5 inline-flex items-center gap-1.5 text-[11px] text-[var(--color-fg-subtle)]">
                <RefreshCw size={11} aria-hidden />
                {t('subscription:credits.resetsOn', { date: formatDateTime(timed.resets_at * 1000) })}
              </p>
            ) : null}
          </div>
        ) : null}

        <div
          className={cn(
            'min-w-0',
            showTimed &&
              'border-t border-[var(--color-divider)] pt-3 xl:border-l xl:border-t-0 xl:pl-4 xl:pt-0',
          )}
        >
          <div className="flex min-w-0 items-center justify-between gap-2">
            <span
              className={cn(
                'inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap font-medium text-[var(--color-secondary)]',
                balanceLabelSize(permanentLabel),
              )}
            >
              <Wallet className="shrink-0" size={13} aria-hidden />
              {permanentLabel}
            </span>
            <span
              className={cn(
                'min-w-0 whitespace-nowrap font-semibold leading-none tabular-nums text-[var(--color-fg)]',
                balanceValueSize(permanentValue),
              )}
            >
              {permanentValue}
            </span>
          </div>
          <p className="mt-1.5 max-w-[44ch] text-[11px] leading-snug text-[var(--color-fg-muted)]">
            {t('subscription:credits.permanentHint')}
          </p>
        </div>
      </div>
    </div>
  )
}

function BalanceState({
  hasPlanHeader,
  loading,
  onRetry,
  t,
}: {
  hasPlanHeader: boolean
  loading: boolean
  onRetry?: () => void
  t: TFn
}) {
  return (
    <div
      className={cn(
        'min-w-0 bg-[var(--color-bg-muted)] p-3.5 sm:p-4',
        hasPlanHeader && 'border-t border-[var(--color-divider)] md:border-l md:border-t-0',
      )}
      role={loading ? 'status' : 'alert'}
    >
      <span className="inline-flex items-center gap-1.5 text-[11px] font-medium text-[var(--color-fg-muted)]">
        <span className="size-1.5 rounded-full bg-[var(--color-accent)]" aria-hidden />
        {t('subscription:credits.sectionTitle')}
      </span>
      {loading ? (
        <div className="mt-2 animate-pulse" aria-label={t('common:aria.loading')}>
          <span className="sr-only">{t('common:aria.loading')}</span>
          <div className="h-3 w-24 rounded bg-[var(--color-surface-sunken)]" />
          <div className="mt-2 h-6 w-32 rounded bg-[var(--color-surface-sunken)]" />
        </div>
      ) : (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-2 text-[12.5px] text-[var(--color-danger)]">
          <span>{t('subscription:credits.loadFailed')}</span>
          <Button className="min-h-11 sm:min-h-8" size="sm" variant="secondary" onClick={onRetry}>
            {t('common:actions.tryAgain')}
          </Button>
        </div>
      )}
    </div>
  )
}

function CreditPackageCard({
  creditPackage,
  onPurchase,
  locale,
  t,
}: {
  creditPackage: ApiCreditPackage
  onPurchase: () => void
  locale?: string
  t: TFn
}) {
  return (
    <article className="flex min-w-0 flex-col rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
      <div className="flex min-h-6 items-start justify-between gap-2">
        <h3 className="min-w-0 break-words text-[1rem] font-semibold leading-snug text-[var(--color-fg)] [overflow-wrap:anywhere]">{creditPackage.name}</h3>
        <Badge size="sm" variant="neutral" className="shrink-0">
          {t('subscription:permanent')}
        </Badge>
      </div>
      {creditPackage.description ? (
        <p className="mt-2 break-words text-[12.5px] leading-relaxed text-[var(--color-fg-muted)] [overflow-wrap:anywhere]">
          {creditPackage.description}
        </p>
      ) : null}

      <div className="mt-4">
        <div className="text-[1.75rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
          {formatCredits(creditPackage.credits, locale)}
        </div>
        <div className="mt-1 text-[12px] text-[var(--color-fg-muted)]">{t('subscription:packages.credits')}</div>
      </div>
      <div className="mt-4 border-t border-[var(--color-divider)] pt-4 text-[1.25rem] font-semibold leading-none tabular-nums text-[var(--color-fg)]">
        {formatCurrencyMinor(creditPackage.price_amount_minor, creditPackage.settlement_currency, locale)}
      </div>

      <div className="mt-5 flex grow items-end">
        <Button
          size="sm"
          variant="primary"
          className="h-auto min-h-11 w-full whitespace-normal py-1.5 text-center leading-snug sm:min-h-8"
          onClick={onPurchase}
        >
          {t('subscription:packages.buy')}
        </Button>
      </div>
    </article>
  )
}

function PaymentHistory({
  orders,
  total,
  page,
  loading,
  error,
  locale,
  resumingOrderId,
  onRetry,
  onResume,
  onViewDetails,
  onPageChange,
  t,
}: {
  orders: ApiUserPaymentOrder[]
  total: number
  page: number
  loading: boolean
  error: boolean
  locale?: string
  resumingOrderId: string
  onRetry: () => void
  onResume: (order: ApiUserPaymentOrder) => void
  onViewDetails: (order: ApiUserPaymentOrder) => void
  onPageChange: (page: number) => void
  t: TFn
}) {
  const totalPages = Math.max(1, Math.ceil(total / PAYMENT_HISTORY_PAGE_SIZE))
  const headingRef = useRef<HTMLHeadingElement>(null)
  const restoreFocusRef = useRef(false)

  useEffect(() => {
    if (loading || !restoreFocusRef.current) return
    restoreFocusRef.current = false
    headingRef.current?.focus()
  }, [loading])

  function changePage(nextPage: number) {
    restoreFocusRef.current = true
    onPageChange(nextPage)
  }

  return (
    <section className="mt-6" aria-labelledby="payment-history-heading">
      <div className="flex items-start gap-2.5">
        <span className="mt-0.5 inline-flex size-8 shrink-0 items-center justify-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
          <ReceiptText size={16} aria-hidden />
        </span>
        <div className="min-w-0">
          <h2
            ref={headingRef}
            id="payment-history-heading"
            tabIndex={-1}
            className="text-[1rem] font-semibold text-[var(--color-fg)]"
          >
            {t('subscription:history.title')}
          </h2>
          <p className="mt-0.5 text-[12px] leading-snug text-[var(--color-fg-muted)]">
            {t('subscription:history.description')}
          </p>
        </div>
      </div>

      <div
        className="mt-3 overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)]"
        aria-busy={loading}
      >
        {loading ? (
          <PaymentHistorySkeleton t={t} />
        ) : error ? (
          <div
            role="alert"
            className="flex min-h-28 flex-col items-center justify-center gap-3 px-4 py-6 text-center"
          >
            <p className="text-[13px] text-[var(--color-danger)]">{t('subscription:history.loadError')}</p>
            <Button
              className="min-h-11 sm:min-h-8"
              size="sm"
              variant="secondary"
              leadingIcon={<RefreshCw size={13} aria-hidden />}
              onClick={onRetry}
            >
              {t('subscription:history.retry')}
            </Button>
          </div>
        ) : orders.length === 0 ? (
          <div className="flex min-h-28 flex-col items-center justify-center gap-2 px-4 py-6 text-center">
            <ReceiptText size={20} className="text-[var(--color-fg-faint)]" aria-hidden />
            <p className="text-[13px] text-[var(--color-fg-muted)]">{t('subscription:history.empty')}</p>
          </div>
        ) : (
          <>
            <table className="hidden w-full table-fixed border-collapse text-left text-[12px] xl:table">
              <caption className="sr-only">{t('subscription:history.title')}</caption>
              <thead className="border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)] text-[11px] text-[var(--color-fg-muted)]">
                <tr>
                  <th className="w-[22%] px-3 py-2 font-medium">{t('subscription:history.columns.name')}</th>
                  <th className="w-[16%] px-3 py-2 font-medium">{t('subscription:history.columns.time')}</th>
                  <th className="w-[13%] px-3 py-2 font-medium">{t('subscription:history.columns.amount')}</th>
                  <th className="w-[18%] px-3 py-2 font-medium">{t('subscription:history.columns.method')}</th>
                  <th className="w-[22%] px-3 py-2 text-right font-medium">{t('subscription:history.columns.status')}</th>
                  <th className="w-[9%] px-3 py-2 text-right font-medium">{t('subscription:history.columns.details')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--color-divider)]">
                {orders.map((order) => (
                  <tr key={order.id}>
                    <td className="min-w-0 px-3 py-2.5 align-top">
                      <span className="block break-words font-medium leading-snug text-[var(--color-fg)] [overflow-wrap:anywhere]">
                        {order.target_name}
                      </span>
                      <span className="mt-0.5 block text-[11px] leading-snug text-[var(--color-fg-subtle)]">
                        {paymentHistoryTargetLabel(order, t)}
                      </span>
                    </td>
                    <td className="px-3 py-2.5 align-top tabular-nums text-[var(--color-fg-muted)]">
                      {formatPaymentHistoryDate(order, locale)}
                    </td>
                    <td className="px-3 py-2.5 align-top font-medium tabular-nums text-[var(--color-fg)]">
                      {formatCurrencyMinor(order.amount_minor, order.currency, locale)}
                    </td>
                    <td className="min-w-0 px-3 py-2.5 align-top">
                      <span className="block break-words text-[var(--color-fg)] [overflow-wrap:anywhere]">
                        {order.method_name}
                      </span>
                      <span className="mt-0.5 block text-[11px] text-[var(--color-fg-subtle)]">
                        {paymentProviderLabel(order.provider)}
                      </span>
                    </td>
                    <td className="px-3 py-2.5 text-right align-top">
                      <PaymentHistoryStatus
                        order={order}
                        resuming={resumingOrderId === order.id}
                        onResume={onResume}
                        t={t}
                      />
                    </td>
                    <td className="px-3 py-2.5 text-right align-top">
                      <Button
                        className="h-7 rounded-[8px] px-2 text-[11px]"
                        size="xs"
                        variant="ghost"
                        trailingIcon={<ChevronRight size={12} aria-hidden />}
                        onClick={() => onViewDetails(order)}
                      >
                        {t('subscription:history.details.action')}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>

            <ul className="divide-y divide-[var(--color-divider)] xl:hidden">
              {orders.map((order) => (
                <li key={order.id} className="px-3.5 py-3">
                  <div className="flex min-w-0 items-start justify-between gap-3">
                    <div className="min-w-0">
                      <h3 className="break-words text-[13px] font-semibold leading-snug text-[var(--color-fg)] [overflow-wrap:anywhere]">
                        {order.target_name}
                      </h3>
                      <p className="mt-0.5 text-[11px] leading-snug text-[var(--color-fg-subtle)]">
                        {paymentHistoryTargetLabel(order, t)}
                      </p>
                    </div>
                    <PaymentHistoryStatus
                      order={order}
                      resuming={resumingOrderId === order.id}
                      onResume={onResume}
                      t={t}
                    />
                  </div>
                  <dl className="mt-2.5 grid grid-cols-2 gap-x-4 gap-y-2">
                    <div className="min-w-0">
                      <dt className="text-[10.5px] text-[var(--color-fg-subtle)]">
                        {t('subscription:history.columns.time')}
                      </dt>
                      <dd className="mt-0.5 text-[11.5px] tabular-nums text-[var(--color-fg)]">
                        {formatPaymentHistoryDate(order, locale)}
                      </dd>
                    </div>
                    <div className="min-w-0 text-right">
                      <dt className="text-[10.5px] text-[var(--color-fg-subtle)]">
                        {t('subscription:history.columns.amount')}
                      </dt>
                      <dd className="mt-0.5 text-[12px] font-semibold tabular-nums text-[var(--color-fg)]">
                        {formatCurrencyMinor(order.amount_minor, order.currency, locale)}
                      </dd>
                    </div>
                    <div className="col-span-2 min-w-0">
                      <dt className="text-[10.5px] text-[var(--color-fg-subtle)]">
                        {t('subscription:history.columns.method')}
                      </dt>
                      <dd className="mt-0.5 flex min-w-0 flex-wrap items-baseline gap-x-1.5 gap-y-0.5 text-[11.5px] text-[var(--color-fg)]">
                        <span className="break-words [overflow-wrap:anywhere]">{order.method_name}</span>
                        <span className="text-[10.5px] text-[var(--color-fg-subtle)]">
                          {paymentProviderLabel(order.provider)}
                        </span>
                      </dd>
                    </div>
                  </dl>
                  <Button
                    className="mt-2 min-h-11 w-full justify-between rounded-[8px] px-2 text-[12px] sm:min-h-10"
                    size="sm"
                    variant="ghost"
                    trailingIcon={<ChevronRight size={14} aria-hidden />}
                    onClick={() => onViewDetails(order)}
                  >
                    {t('subscription:history.details.action')}
                  </Button>
                </li>
              ))}
            </ul>

            {totalPages > 1 ? (
              <div className="flex min-h-12 items-center justify-between gap-3 border-t border-[var(--color-divider)] bg-[var(--color-bg-muted)] px-2 py-1.5 sm:px-3">
                <Button
                  className="size-11 shrink-0 p-0 sm:size-8"
                  size="sm"
                  variant="ghost"
                  disabled={page <= 0}
                  aria-label={t('subscription:history.previous')}
                  title={t('subscription:history.previous')}
                  onClick={() => changePage(page - 1)}
                >
                  <ChevronLeft size={16} aria-hidden />
                </Button>
                <span className="text-[11.5px] tabular-nums text-[var(--color-fg-muted)]">
                  {t('subscription:history.page', { current: page + 1, total: totalPages })}
                </span>
                <Button
                  className="size-11 shrink-0 p-0 sm:size-8"
                  size="sm"
                  variant="ghost"
                  disabled={page + 1 >= totalPages}
                  aria-label={t('subscription:history.next')}
                  title={t('subscription:history.next')}
                  onClick={() => changePage(page + 1)}
                >
                  <ChevronRight size={16} aria-hidden />
                </Button>
              </div>
            ) : null}
          </>
        )}
      </div>
    </section>
  )
}

function PaymentOrderDetailsDialog({
  open,
  order,
  selectedOrder,
  loading,
  error,
  locale,
  resuming,
  onOpenChange,
  onRetry,
  onResume,
  t,
}: {
  open: boolean
  order: ApiUserPaymentOrder | null
  selectedOrder: ApiUserPaymentOrder | null
  loading: boolean
  error: boolean
  locale?: string
  resuming: boolean
  onOpenChange: (open: boolean) => void
  onRetry: () => void
  onResume: (order: ApiUserPaymentOrder) => void
  t: TFn
}) {
  const displayOrder = order ?? selectedOrder

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg" className="rounded-[8px] font-sans max-sm:[&>button]:size-11">
        <DialogHeader className="px-5 pt-5 pb-3 max-sm:pr-16 sm:px-6">
          <DialogTitle>{t('subscription:history.details.title')}</DialogTitle>
          <DialogDescription className="mt-1 break-all font-mono text-[12px] leading-5 tracking-normal">
            {displayOrder
              ? t('subscription:history.details.description', { id: displayOrder.id })
              : t('subscription:history.details.title')}
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="px-5 pb-5 sm:px-6">
          {loading ? (
            <div
              className="flex min-h-48 flex-col items-center justify-center gap-2 text-[13px] text-[var(--color-fg-muted)]"
              role="status"
            >
              <Loader2 className="size-5 animate-spin" aria-hidden />
              <span>{t('subscription:history.details.loading')}</span>
            </div>
          ) : error ? (
            <div
              className="flex min-h-48 flex-col items-center justify-center gap-3 px-3 text-center"
              role="alert"
            >
              <p className="text-[13px] text-[var(--color-danger)]">
                {t('subscription:history.details.loadError')}
              </p>
              <Button
                className="min-h-11 rounded-[8px] sm:min-h-8"
                size="sm"
                variant="secondary"
                leadingIcon={<RefreshCw size={13} aria-hidden />}
                onClick={onRetry}
              >
                {t('subscription:history.details.retry')}
              </Button>
            </div>
          ) : order ? (
            <PaymentOrderDetailFields order={order} locale={locale} t={t} />
          ) : null}
        </DialogBody>
        <DialogFooter className="max-sm:[&_button]:!h-11">
          <Button className="rounded-[8px]" variant="ghost" onClick={() => onOpenChange(false)}>
            {t('common:actions.close')}
          </Button>
          {order && canResumePaymentOrder(order) ? (
            <Button
              className="rounded-[8px]"
              variant="primary"
              trailingIcon={<ArrowRight size={13} aria-hidden />}
              loading={resuming}
              disabled={resuming}
              onClick={() => onResume(order)}
            >
              {paymentOrderResumeKind(order) === 'retry'
                ? t('subscription:history.actions.retry')
                : t('subscription:history.actions.continue')}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function PaymentOrderDetailFields({
  order,
  locale,
  t,
}: {
  order: ApiUserPaymentOrder
  locale?: string
  t: TFn
}) {
  const fields: Array<{ label: string; value: string; wide?: boolean; mono?: boolean; danger?: boolean }> = [
    {
      label: t('subscription:history.details.fields.orderId'),
      value: order.id,
      wide: true,
      mono: true,
    },
    { label: t('subscription:history.details.fields.product'), value: order.target_name },
    {
      label: t('subscription:history.details.fields.targetType'),
      value: t(`subscription:history.target.${order.target_type}`),
    },
    ...(order.target_type === 'user_group' && order.billing_cycle
      ? [{
          label: t('subscription:history.details.fields.billingCycle'),
          value: t(`subscription:billing.${order.billing_cycle}`),
        }]
      : []),
    {
      label: t('subscription:history.details.fields.status'),
      value: t(`subscription:history.status.${order.status}`),
    },
    {
      label: t('subscription:history.details.fields.amount'),
      value: formatCurrencyMinor(order.amount_minor, order.currency, locale),
    },
    ...(typeof order.tax_amount_minor === 'number' && order.tax_amount_minor > 0
      ? [{
          label: t('subscription:history.details.fields.tax'),
          value: formatCurrencyMinor(order.tax_amount_minor, order.currency, locale),
        }]
      : []),
    { label: t('subscription:history.details.fields.currency'), value: order.currency.toUpperCase() },
    { label: t('subscription:history.details.fields.paymentMethod'), value: order.method_name },
    { label: t('subscription:history.details.fields.provider'), value: paymentProviderLabel(order.provider) },
    {
      label: t('subscription:history.details.fields.methodType'),
      value: order.method_type || t('subscription:history.details.notAvailable'),
    },
    {
      label: t('subscription:history.details.fields.createdAt'),
      value: formatPaymentOrderTimestamp(order.created_at, locale),
    },
    ...(order.paid_at
      ? [{
          label: t('subscription:history.details.fields.paidAt'),
          value: formatPaymentOrderTimestamp(order.paid_at, locale),
        }]
      : []),
    ...(order.fulfilled_at
      ? [{
          label: t('subscription:history.details.fields.fulfilledAt'),
          value: formatPaymentOrderTimestamp(order.fulfilled_at, locale),
        }]
      : []),
    ...(order.checkout_expires_at
      ? [{
          label: t('subscription:history.details.fields.checkoutExpiresAt'),
          value: formatPaymentOrderTimestamp(order.checkout_expires_at, locale),
        }]
      : []),
    ...(order.failure_reason
      ? [{
          label: t('subscription:history.details.fields.failureReason'),
          value: order.failure_reason,
          wide: true,
          danger: true,
        }]
      : []),
  ]

  return (
    <dl className="grid grid-cols-1 border-y border-[var(--color-divider)] sm:grid-cols-2">
      {fields.map((field) => (
        <div
          key={field.label}
          className={cn(
            'min-w-0 border-b border-[var(--color-divider)] py-3 last:border-b-0 sm:px-3 sm:first:pl-0',
            field.wide && 'sm:col-span-2',
          )}
        >
          <dt className="text-[11px] leading-4 text-[var(--color-fg-subtle)]">{field.label}</dt>
          <dd
            className={cn(
              'mt-1 break-words text-[13px] leading-5 text-[var(--color-fg)] [overflow-wrap:anywhere]',
              field.mono && 'font-mono text-[12px] tracking-normal',
              field.danger && 'text-[var(--color-danger)]',
            )}
          >
            {field.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function PaymentHistoryStatus({
  order,
  resuming,
  onResume,
  t,
}: {
  order: ApiUserPaymentOrder
  resuming: boolean
  onResume: (order: ApiUserPaymentOrder) => void
  t: TFn
}) {
  const { status } = order
  const variant =
    status === 'paid'
      ? 'success'
      : status === 'failed'
        ? 'danger'
        : status === 'expired'
          ? 'neutral'
          : status === 'processing'
            ? 'info'
            : 'warning'
  const resumeLabel = paymentOrderResumeKind(order) === 'retry'
    ? t('subscription:history.actions.retry')
    : t('subscription:history.actions.continue')

  return (
    <div className="flex min-w-0 shrink-0 flex-wrap items-center justify-end gap-1.5">
      <Badge className="shrink-0" size="xs" variant={variant}>
        {t(`subscription:history.status.${status}`)}
      </Badge>
      {canResumePaymentOrder(order) ? (
        <Button
          className="h-7 shrink-0 rounded-[8px] px-2 text-[11px]"
          size="xs"
          variant="secondary"
          trailingIcon={<ArrowRight size={11} aria-hidden />}
          loading={resuming}
          disabled={resuming}
          onClick={() => onResume(order)}
        >
          {resumeLabel}
        </Button>
      ) : null}
    </div>
  )
}

function PaymentHistorySkeleton({ t }: { t: TFn }) {
  return (
    <div className="animate-pulse px-3.5 py-2" role="status" aria-label={t('common:aria.loading')}>
      <span className="sr-only">{t('common:aria.loading')}</span>
      {Array.from({ length: 3 }).map((_, index) => (
        <div
          key={index}
          className="grid grid-cols-[minmax(0,1fr)_5rem] gap-4 border-b border-[var(--color-divider)] py-3 last:border-b-0 xl:grid-cols-[minmax(0,1.2fr)_minmax(0,0.9fr)_minmax(0,0.7fr)_minmax(0,1fr)_minmax(0,1.1fr)_4rem]"
        >
          <div className="h-3.5 w-32 max-w-full rounded bg-[var(--color-bg-muted)]" />
          <div className="h-3.5 w-full rounded bg-[var(--color-bg-muted)]" />
          <div className="hidden h-3.5 w-full rounded bg-[var(--color-bg-muted)] xl:block" />
          <div className="hidden h-3.5 w-full rounded bg-[var(--color-bg-muted)] xl:block" />
          <div className="hidden h-3.5 w-full rounded bg-[var(--color-bg-muted)] xl:block" />
          <div className="hidden h-3.5 w-full rounded bg-[var(--color-bg-muted)] xl:block" />
        </div>
      ))}
    </div>
  )
}

function paymentHistoryTargetLabel(order: ApiUserPaymentOrder, t: TFn): string {
  const target = t(`subscription:history.target.${order.target_type}`)
  if (order.target_type !== 'user_group' || !order.billing_cycle) return target
  return `${target} · ${t(`subscription:billing.${order.billing_cycle}`)}`
}

function formatPaymentHistoryDate(order: ApiUserPaymentOrder, locale?: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'short' }).format(
    (order.paid_at || order.created_at) * 1000,
  )
}

function formatPaymentOrderTimestamp(timestamp: number, locale?: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'medium' }).format(timestamp * 1000)
}

function paymentProviderLabel(provider: ApiUserPaymentOrder['provider']): string {
  switch (provider) {
    case 'stripe':
      return 'Stripe'
    case 'epay':
      return 'EPay'
    case 'waffo':
      return 'Waffo'
  }
}

function formatCredits(value: number, locale?: string): string {
  return new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value)
}

function balanceLabelSize(label: string): string {
  return label.length > 22 ? 'text-[10.5px]' : label.length > 14 ? 'text-[11px]' : 'text-[12px]'
}

function balanceValueSize(value: string): string {
  return value.length > 14 ? 'text-[13px]' : value.length > 10 ? 'text-[15px]' : 'text-[1.25rem]'
}

function EmptyCatalog({ children }: { children: string }) {
  return (
    <div className="rounded-[10px] border border-dashed border-[var(--color-border)] px-4 py-8 text-center text-[13px] text-[var(--color-fg-muted)]">
      {children}
    </div>
  )
}

function CatalogLoadError({ message, onRetry, t }: { message: string; onRetry: () => void; t: TFn }) {
  return (
    <div
      role="alert"
      className="flex flex-col items-start gap-3 rounded-[10px] border border-[var(--color-danger)]/25 bg-[var(--color-danger-soft)] px-4 py-3 text-[13px] text-[var(--color-danger)] sm:flex-row sm:items-center sm:justify-between"
    >
      <span>{message}</span>
      <Button
        className="min-h-11 shrink-0 sm:min-h-8"
        size="sm"
        variant="secondary"
        leadingIcon={<RefreshCw size={13} aria-hidden />}
        onClick={onRetry}
      >
        {t('common:actions.tryAgain')}
      </Button>
    </div>
  )
}

function AccountSkeleton({ t }: { t: TFn }) {
  return (
    <div
      className="animate-pulse overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)]"
      role="status"
      aria-label={t('common:aria.loading')}
    >
      <span className="sr-only">{t('common:aria.loading')}</span>
      <div className="md:grid md:grid-cols-[minmax(0,0.8fr)_minmax(0,1.7fr)]">
        <div className="p-3.5 sm:p-4">
          <div className="h-3 w-20 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-2 h-5 w-36 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-2 h-3 w-52 max-w-full rounded bg-[var(--color-bg-muted)]" />
        </div>
        <div className="border-t border-[var(--color-divider)] bg-[var(--color-bg-muted)] p-3 sm:p-3.5 md:border-l md:border-t-0">
          <div className="grid gap-3 sm:grid-cols-2 sm:gap-4">
            {Array.from({ length: 2 }).map((_, index) => (
              <div key={index} className="min-w-0">
                <div className="flex items-center justify-between gap-3">
                  <div className="h-3 w-24 rounded bg-[var(--color-surface-sunken)]" />
                  <div className="h-5 w-16 rounded bg-[var(--color-surface-sunken)]" />
                </div>
                <div className="mt-2 h-1 w-full rounded-full bg-[var(--color-surface-sunken)]" />
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

function CardsSkeleton({ t }: { t: TFn }) {
  return (
    <div
      className="grid items-stretch gap-3 sm:grid-cols-2 lg:grid-cols-3"
      role="status"
      aria-label={t('common:aria.loading')}
    >
      <span className="sr-only">{t('common:aria.loading')}</span>
      {Array.from({ length: 3 }).map((_, index) => (
        <div
          key={index}
          className="animate-pulse rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-5"
        >
          <div className="h-5 w-24 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-3 h-3.5 w-full rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-4 h-7 w-28 rounded bg-[var(--color-bg-muted)]" />
          <div className="mt-4 flex flex-col gap-2 border-t border-[var(--color-divider)] pt-4">
            <div className="h-3 w-full rounded bg-[var(--color-bg-muted)]" />
            <div className="h-3 w-5/6 rounded bg-[var(--color-bg-muted)]" />
          </div>
          <div className="mt-5 h-8 w-full rounded-[10px] bg-[var(--color-bg-muted)]" />
        </div>
      ))}
    </div>
  )
}
