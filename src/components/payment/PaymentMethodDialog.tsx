import { useEffect, useRef, useState } from 'react'
import { AlertTriangle, ArrowRight, CreditCard, KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { paymentsApi, ApiError } from '@/api'
import type { ApiPublicPaymentMethod } from '@/api/types'
import { PaymentMethodIcon } from '@/components/payment/payment-method-icon'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  CHECKOUT_REQUEST_TIMEOUT_MS,
  executePaymentCheckoutAction,
  paymentCheckoutHref,
  PaymentCheckoutActionError,
} from '@/lib/payment-checkout'
import { checkoutPaymentErrorKey } from '@/lib/payment-errors'

type PaymentTargetType = 'credit_package' | 'user_group'
type BillingCycle = 'monthly' | 'yearly'
type MethodLoadState = 'loading' | 'ready' | 'error'

interface PaymentMethodDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  targetType: PaymentTargetType
  targetId: string
  targetName: string
  billingCycle?: BillingCycle
}

export function PaymentMethodDialog({
  open,
  onOpenChange,
  targetType,
  targetId,
  targetName,
  billingCycle,
}: PaymentMethodDialogProps) {
  const { t } = useTranslation('subscription')
  const [methods, setMethods] = useState<ApiPublicPaymentMethod[]>([])
  const [cardPurchaseUrl, setCardPurchaseUrl] = useState('')
  const [loadState, setLoadState] = useState<MethodLoadState>(open ? 'loading' : 'ready')
  const [checkoutError, setCheckoutError] = useState('')
  const [submittingId, setSubmittingId] = useState<string | null>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const checkoutAbortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!open) return

    let active = true
    const controller = new AbortController()
    setLoadState('loading')
    setCheckoutError('')
    setMethods([])
    setCardPurchaseUrl('')

    paymentsApi
      .methods(targetType, controller.signal)
      .then((result) => {
        if (!active) return
        setMethods(result.methods)
        setCardPurchaseUrl(result.card_purchase_url || '')
        setLoadState('ready')
      })
      .catch((error) => {
        if (active && !(error instanceof DOMException && error.name === 'AbortError')) {
          setLoadState('error')
        }
      })

    return () => {
      active = false
      controller.abort()
    }
  }, [open, reloadKey, targetType])

  useEffect(() => () => checkoutAbortRef.current?.abort('dismissed'), [])

  function checkoutErrorMessage(error: unknown): string {
    if (error instanceof PaymentCheckoutActionError) return t('payment.invalidUrl')
    if (!(error instanceof ApiError)) return t('payment.checkoutError')
    const key = checkoutPaymentErrorKey(error.message)
    return key ? t(key) : t('payment.checkoutError')
  }

  async function startCheckout(method: ApiPublicPaymentMethod) {
    if (submittingId) return
    const controller = new AbortController()
    checkoutAbortRef.current?.abort('replaced')
    checkoutAbortRef.current = controller
    let timedOut = false
    const timeoutId = window.setTimeout(() => {
      timedOut = true
      controller.abort('timeout')
    }, CHECKOUT_REQUEST_TIMEOUT_MS)
    setSubmittingId(method.id)
    setCheckoutError('')
    try {
      const result = await paymentsApi.checkout({
        payment_method_id: method.id,
        target_type: targetType,
        target_id: targetId,
        ...(targetType === 'user_group' && billingCycle ? { billing_cycle: billingCycle } : {}),
      }, controller.signal)
      executePaymentCheckoutAction(result.action)
    } catch (error) {
      if (controller.signal.aborted && !timedOut) return
      setCheckoutError(timedOut ? t('payment.checkoutTimeout') : checkoutErrorMessage(error))
    } finally {
      window.clearTimeout(timeoutId)
      if (checkoutAbortRef.current === controller) {
        checkoutAbortRef.current = null
        setSubmittingId(null)
      }
    }
  }

  function openCardPurchase() {
    if (submittingId) return
    const href = paymentCheckoutHref(cardPurchaseUrl)
    if (!href) {
      setCheckoutError(t('payment.invalidUrl'))
      return
    }
    setSubmittingId('card-purchase')
    setCheckoutError('')
    window.location.assign(href)
  }

  const hasCardPurchase = Boolean(cardPurchaseUrl.trim())
  const hasMethods = methods.length > 0
  const busy = submittingId !== null
  const loading = loadState === 'loading'

  function handleOpenChange(next: boolean) {
    if (!next) checkoutAbortRef.current?.abort('dismissed')
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent
        size="sm"
        className="font-sans max-sm:[&>button]:size-11"
        aria-busy={busy || loading || undefined}
      >
        <DialogHeader className="px-4 pb-2 pt-4 pr-14 sm:px-5 sm:pt-5 sm:pr-14">
          <DialogTitle className="text-[1rem] leading-6">{t('payment.title')}</DialogTitle>
          <DialogDescription className="mt-1 break-words text-[12.5px] leading-relaxed [overflow-wrap:anywhere]">
            {t('payment.description', { name: targetName })}
          </DialogDescription>
        </DialogHeader>

        <DialogBody className="px-4 pb-4 sm:px-5">
          <div aria-live="polite" className="space-y-2">
            {loading ? (
              <div className="space-y-2" role="status">
                <span className="sr-only">{t('payment.loading')}</span>
                {Array.from({ length: 3 }).map((_, index) => (
                  <div
                    key={index}
                    className="flex min-h-11 animate-pulse items-center gap-3 rounded-[8px] bg-[var(--color-bg-muted)] px-3"
                  >
                    <span className="size-7 shrink-0 rounded-[6px] bg-[var(--color-surface-sunken)]" />
                    <span className="h-3.5 w-32 max-w-[60%] rounded bg-[var(--color-surface-sunken)]" />
                  </div>
                ))}
              </div>
            ) : loadState === 'error' ? (
              <div className="rounded-[8px] bg-[var(--color-danger-soft)] px-3 py-3 text-[var(--color-danger)]">
                <div className="flex items-start gap-2 text-[12.5px] leading-relaxed">
                  <AlertTriangle size={16} className="mt-0.5 shrink-0" aria-hidden />
                  <span>{t('payment.loadError')}</span>
                </div>
                <Button
                  size="sm"
                  variant="secondary"
                  className="mt-2 min-h-11 sm:min-h-8"
                  onClick={() => {
                    setLoadState('loading')
                    setReloadKey((value) => value + 1)
                  }}
                >
                  {t('payment.retry')}
                </Button>
              </div>
            ) : (
              <>
                {hasMethods ? (
                  <div className="grid grid-cols-1 gap-2">
                    {methods.map((method) => {
                      const submitting = submittingId === method.id
                      return (
                        <button
                          key={method.id}
                          type="button"
                          disabled={busy}
                          aria-busy={submitting || undefined}
                          onClick={() => void startCheckout(method)}
                          className="flex min-h-11 w-full items-center gap-3 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-left transition-colors hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:cursor-not-allowed disabled:opacity-55"
                        >
                          <span className="inline-flex size-7 shrink-0 items-center justify-center rounded-[6px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                            <PaymentMethodIcon icon={method.icon} size={18} />
                          </span>
                          <span className="min-w-0 flex-1 break-words text-[13px] font-medium leading-5 text-[var(--color-fg)] [overflow-wrap:anywhere]">
                            {method.name}
                          </span>
                          {submitting ? (
                            <span
                              className="size-4 shrink-0 animate-[spin_700ms_linear_infinite] rounded-full border-2 border-[var(--color-accent)] border-r-transparent"
                              aria-hidden
                            />
                          ) : (
                            <ArrowRight size={15} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                          )}
                        </button>
                      )
                    })}
                  </div>
                ) : null}

                {hasCardPurchase ? (
                  <>
                    {hasMethods ? (
                      <div className="flex items-center gap-3 py-0.5" role="separator">
                        <span className="h-px flex-1 bg-[var(--color-divider)]" />
                        <span className="text-[11px] text-[var(--color-fg-subtle)]">{t('payment.or')}</span>
                        <span className="h-px flex-1 bg-[var(--color-divider)]" />
                      </div>
                    ) : null}
                    <button
                      type="button"
                      disabled={busy}
                      aria-busy={submittingId === 'card-purchase' || undefined}
                      onClick={openCardPurchase}
                      className="flex min-h-11 w-full items-center gap-3 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-left transition-colors hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:cursor-not-allowed disabled:opacity-55"
                    >
                      <span className="inline-flex size-7 shrink-0 items-center justify-center rounded-[6px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                        <KeyRound size={16} aria-hidden />
                      </span>
                      <span className="min-w-0 flex-1 text-[13px] font-medium text-[var(--color-fg)]">
                        {t('payment.cardPurchase')}
                      </span>
                      {submittingId === 'card-purchase' ? (
                        <span
                          className="size-4 shrink-0 animate-[spin_700ms_linear_infinite] rounded-full border-2 border-[var(--color-accent)] border-r-transparent"
                          aria-hidden
                        />
                      ) : (
                        <ArrowRight size={15} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                      )}
                    </button>
                  </>
                ) : null}

                {!hasMethods && !hasCardPurchase ? (
                  <div className="rounded-[8px] border border-dashed border-[var(--color-border)] px-4 py-5 text-center">
                    <CreditCard size={20} className="mx-auto text-[var(--color-fg-subtle)]" aria-hidden />
                    <p className="mt-2 text-[13px] font-medium text-[var(--color-fg)]">{t('payment.empty')}</p>
                    <p className="mt-1 text-[12px] leading-relaxed text-[var(--color-fg-muted)]">
                      {t('payment.emptyHint')}
                    </p>
                  </div>
                ) : null}
              </>
            )}

            {checkoutError ? (
              <div
                role="alert"
                className="flex items-start gap-2 rounded-[8px] bg-[var(--color-danger-soft)] px-3 py-2.5 text-[12px] leading-relaxed text-[var(--color-danger)]"
              >
                <AlertTriangle size={15} className="mt-0.5 shrink-0" aria-hidden />
                <span className="min-w-0 break-words">{checkoutError}</span>
              </div>
            ) : null}
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
