import type { ApiPaymentCheckoutAction } from '@/api/types'
import { safeHref } from '@/lib/utils'

export const CHECKOUT_REQUEST_TIMEOUT_MS = 55_000

export class PaymentCheckoutActionError extends Error {
  constructor() {
    super('invalid_payment_checkout_action')
    this.name = 'PaymentCheckoutActionError'
  }
}

export type PaymentCheckoutRunResult =
  | { status: 'completed' }
  | { status: 'ignored' }
  | { status: 'error'; error: unknown }

type CheckoutActionLoader = () => Promise<ApiPaymentCheckoutAction>
type CheckoutActionExecutor = (action: ApiPaymentCheckoutAction) => void

/** Serializes checkout recovery clicks immediately, without waiting for a
 * React state update to disable the clicked row. */
export class PaymentCheckoutActionRunner {
  private activeOrderId = ''

  constructor(private readonly onBusyChange: (orderId: string) => void) {}

  get busyOrderId(): string {
    return this.activeOrderId
  }

  async run(
    orderId: string,
    loadAction: CheckoutActionLoader,
    executeAction: CheckoutActionExecutor = executePaymentCheckoutAction,
  ): Promise<PaymentCheckoutRunResult> {
    if (!orderId || this.activeOrderId) return { status: 'ignored' }

    this.activeOrderId = orderId
    this.onBusyChange(orderId)
    try {
      const action = await loadAction()
      executeAction(action)
      return { status: 'completed' }
    } catch (error) {
      return { status: 'error', error }
    } finally {
      if (this.activeOrderId === orderId) {
        this.activeOrderId = ''
        this.onBusyChange('')
      }
    }
  }
}

export function paymentCheckoutHref(value?: string): string | undefined {
  const href = safeHref(value)
  if (!href || href.toLowerCase().startsWith('mailto:')) return undefined

  try {
    const parsed = new URL(href, window.location.origin)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return undefined
    return href
  } catch {
    return undefined
  }
}

export function executePaymentCheckoutAction(action: ApiPaymentCheckoutAction): void {
  if (!action || (action.type !== 'redirect' && action.type !== 'form_post')) {
    throw new PaymentCheckoutActionError()
  }

  const href = paymentCheckoutHref(action.url)
  if (!href) throw new PaymentCheckoutActionError()

  if (action.type === 'redirect') {
    window.location.assign(href)
    return
  }

  const form = document.createElement('form')
  form.action = href
  form.method = 'POST'
  form.hidden = true

  Object.entries(action.fields ?? {}).forEach(([name, value]) => {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = name
    input.value = String(value)
    form.appendChild(input)
  })

  document.body.appendChild(form)
  form.submit()
  form.remove()
}
