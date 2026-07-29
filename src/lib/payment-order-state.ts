import type { ApiPaymentOrder, ApiPaymentOrderStatus, ApiUserPaymentOrder } from '@/api/types'

type ResumableOrder = Pick<
  ApiUserPaymentOrder,
  'can_resume' | 'can_retry' | 'resume_mode' | 'resume_kind'
>

export type PaymentOrderRecoveryRequestResult =
  | 'ignored'
  | 'confirmation_required'
  | 'started'

export function canResumePaymentOrder(order: ResumableOrder): boolean {
  return order.can_resume || order.can_retry
}

export function paymentOrderResumeKind(order: ResumableOrder): 'continue' | 'retry' {
  if (order.resume_mode === 'retry_submission') return 'retry'
  if (order.resume_mode === 'original_session') return 'continue'
  return order.resume_kind === 'retry' ? 'retry' : 'continue'
}

/** Keeps retry confirmation separate from the API action so dismissing the
 * dialog can never submit a replacement checkout by accident. */
export class PaymentOrderRecoveryCoordinator<Order extends ResumableOrder> {
  private retryOrder: Order | null = null

  constructor(private readonly onRetryOrderChange: (order: Order | null) => void) {}

  get pendingRetryOrder(): Order | null {
    return this.retryOrder
  }

  request(order: Order, start: (order: Order) => void): PaymentOrderRecoveryRequestResult {
    if (!canResumePaymentOrder(order)) return 'ignored'
    if (paymentOrderResumeKind(order) === 'retry') {
      this.retryOrder = order
      this.onRetryOrderChange(order)
      return 'confirmation_required'
    }
    start(order)
    return 'started'
  }

  cancelRetry(): void {
    if (!this.retryOrder) return
    this.retryOrder = null
    this.onRetryOrderChange(null)
  }

  confirmRetry(start: (order: Order) => void): PaymentOrderRecoveryRequestResult {
    const order = this.retryOrder
    if (!order) return 'ignored'
    this.retryOrder = null
    this.onRetryOrderChange(null)
    start(order)
    return 'started'
  }
}

export function isTerminalPaymentOrderStatus(status: ApiPaymentOrderStatus): boolean {
  return status !== 'pending' && status !== 'processing'
}

export function canDeletePaymentOrder(
  order: Pick<ApiPaymentOrder, 'status' | 'can_delete'>,
): boolean {
  return order.can_delete ?? isTerminalPaymentOrderStatus(order.status)
}
