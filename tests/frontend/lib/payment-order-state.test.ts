import { afterEach, describe, expect, it, vi } from 'vitest'

import { adminApi } from '@/api'
import type { ApiPaymentOrderStatus } from '@/api/types'
import {
  canDeletePaymentOrder,
  canResumePaymentOrder,
  isTerminalPaymentOrderStatus,
  PaymentOrderRecoveryCoordinator,
  paymentOrderResumeKind,
} from '@/lib/payment-order-state'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('buyer payment-order recovery state', () => {
  it('uses the final resume mode and keeps the legacy kind only as a fallback', () => {
    expect(canResumePaymentOrder({
      can_resume: true,
      can_retry: false,
      resume_mode: 'original_session',
      resume_kind: 'retry',
    })).toBe(true)
    expect(paymentOrderResumeKind({
      can_resume: true,
      can_retry: false,
      resume_mode: 'original_session',
      resume_kind: 'retry',
    })).toBe('continue')
    expect(paymentOrderResumeKind({
      can_resume: false,
      can_retry: true,
      resume_mode: 'retry_submission',
      resume_kind: 'continue',
    })).toBe('retry')
    expect(paymentOrderResumeKind({
      can_resume: false,
      can_retry: true,
      resume_kind: 'retry',
    })).toBe('retry')
  })

  it('hides recovery when neither recovery capability is available', () => {
    expect(canResumePaymentOrder({
      can_resume: false,
      can_retry: false,
    })).toBe(false)
  })

  it('requires confirmation for a replacement checkout and cancellation never starts it', () => {
    const pendingChanges: Array<{ id: string } | null> = []
    const coordinator = new PaymentOrderRecoveryCoordinator<{
      id: string
      can_resume: boolean
      can_retry: boolean
      resume_mode: 'retry_submission'
      resume_kind?: 'continue' | 'retry'
    }>((order) => pendingChanges.push(order))
    const start = vi.fn()
    const order = {
      id: 'order-retry',
      can_resume: false,
      can_retry: true,
      resume_mode: 'retry_submission' as const,
    }

    expect(coordinator.request(order, start)).toBe('confirmation_required')
    expect(start).not.toHaveBeenCalled()
    expect(coordinator.pendingRetryOrder).toBe(order)

    coordinator.cancelRetry()

    expect(start).not.toHaveBeenCalled()
    expect(coordinator.pendingRetryOrder).toBeNull()
    expect(pendingChanges).toEqual([order, null])
  })

  it('starts a replacement checkout only after confirmation', () => {
    const coordinator = new PaymentOrderRecoveryCoordinator<{
      id: string
      can_resume: boolean
      can_retry: boolean
      resume_mode: 'retry_submission'
      resume_kind?: 'continue' | 'retry'
    }>(() => undefined)
    const start = vi.fn()
    const order = {
      id: 'order-retry',
      can_resume: false,
      can_retry: true,
      resume_mode: 'retry_submission' as const,
    }

    coordinator.request(order, start)
    expect(coordinator.confirmRetry(start)).toBe('started')

    expect(start).toHaveBeenCalledOnce()
    expect(start).toHaveBeenCalledWith(order)
    expect(coordinator.confirmRetry(start)).toBe('ignored')
    expect(start).toHaveBeenCalledOnce()
  })

  it('continues an original provider session without showing retry confirmation', () => {
    const onRetryOrderChange = vi.fn()
    const coordinator = new PaymentOrderRecoveryCoordinator<{
      id: string
      can_resume: boolean
      can_retry: boolean
      resume_mode: 'original_session'
      resume_kind?: 'continue' | 'retry'
    }>(onRetryOrderChange)
    const start = vi.fn()
    const order = {
      id: 'order-original',
      can_resume: true,
      can_retry: false,
      resume_mode: 'original_session' as const,
    }

    expect(coordinator.request(order, start)).toBe('started')
    expect(start).toHaveBeenCalledWith(order)
    expect(onRetryOrderChange).not.toHaveBeenCalled()
  })
})

describe('admin payment-order deletion', () => {
  it('enables permanent deletion only for terminal statuses', () => {
    const expected: Record<ApiPaymentOrderStatus, boolean> = {
      pending: false,
      processing: false,
      fulfilled: true,
      failed: true,
      expired: true,
      cancelled: true,
    }

    for (const [status, terminal] of Object.entries(expected)) {
      expect(isTerminalPaymentOrderStatus(status as ApiPaymentOrderStatus)).toBe(terminal)
    }
  })

  it('uses the backend deletion decision while remaining compatible with older responses', () => {
    expect(canDeletePaymentOrder({ status: 'cancelled', can_delete: true })).toBe(true)
    expect(canDeletePaymentOrder({ status: 'failed', can_delete: true })).toBe(true)
    expect(canDeletePaymentOrder({ status: 'fulfilled' })).toBe(true)
    expect(canDeletePaymentOrder({ status: 'processing' })).toBe(false)
  })

  it('sends a permanent-delete request for the exact encoded order id', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(adminApi.deletePaymentOrder('order / 42', true)).resolves.toEqual({ ok: true })
    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, options] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/admin/payment-orders/order%20%2F%2042?gateway_final_acknowledged=true')
    expect(options).toMatchObject({ method: 'DELETE', credentials: 'include' })
  })
})
