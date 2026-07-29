import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ApiPaymentCheckoutAction } from '@/api/types'
import {
  executePaymentCheckoutAction,
  PaymentCheckoutActionError,
  PaymentCheckoutActionRunner,
} from '@/lib/payment-checkout'

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubWindow(assign = vi.fn()) {
  vi.stubGlobal('window', {
    location: {
      origin: 'https://app.example.test',
      assign,
    },
  })
  return assign
}

describe('executePaymentCheckoutAction', () => {
  it('navigates to a validated redirect checkout', () => {
    const assign = stubWindow()

    executePaymentCheckoutAction({
      type: 'redirect',
      url: 'https://pay.example.test/checkout/session-1',
    })

    expect(assign).toHaveBeenCalledOnce()
    expect(assign).toHaveBeenCalledWith('https://pay.example.test/checkout/session-1')
  })

  it('submits every field for a validated form-post checkout', () => {
    stubWindow()
    const inputs: Array<{ type: string; name: string; value: string }> = []
    const form = {
      action: '',
      method: '',
      hidden: false,
      appendChild: vi.fn((input: { type: string; name: string; value: string }) => inputs.push(input)),
      submit: vi.fn(),
      remove: vi.fn(),
    }
    const appendChild = vi.fn()
    vi.stubGlobal('document', {
      createElement: vi.fn((tag: string) => {
        if (tag === 'form') return form
        if (tag === 'input') return { type: '', name: '', value: '' }
        throw new Error(`unexpected element: ${tag}`)
      }),
      body: { appendChild },
    })

    executePaymentCheckoutAction({
      type: 'form_post',
      url: 'https://gateway.example.test/pay',
      fields: { merchant: 'merchant-1', order_no: 'order-1' },
    })

    expect(form).toMatchObject({
      action: 'https://gateway.example.test/pay',
      method: 'POST',
      hidden: true,
    })
    expect(inputs).toEqual([
      { type: 'hidden', name: 'merchant', value: 'merchant-1' },
      { type: 'hidden', name: 'order_no', value: 'order-1' },
    ])
    expect(appendChild).toHaveBeenCalledWith(form)
    expect(form.submit).toHaveBeenCalledOnce()
    expect(form.remove).toHaveBeenCalledOnce()
  })

  it('rejects malformed action types and unsafe checkout URLs', () => {
    stubWindow()
    const invalidActions = [
      null,
      { type: 'popup', url: 'https://pay.example.test' },
      { type: 'redirect', url: 'javascript:alert(1)' },
      { type: 'redirect', url: 'mailto:billing@example.test' },
      { type: 'form_post', url: '//attacker.example.test/pay' },
    ]

    for (const action of invalidActions) {
      expect(() => executePaymentCheckoutAction(action as ApiPaymentCheckoutAction)).toThrow(
        PaymentCheckoutActionError,
      )
    }
  })
})

describe('PaymentCheckoutActionRunner', () => {
  it('keeps only the active order busy and ignores a concurrent retry click', async () => {
    const busyChanges: string[] = []
    const runner = new PaymentCheckoutActionRunner((orderId) => busyChanges.push(orderId))
    const execute = vi.fn()
    let resolveAction!: (action: ApiPaymentCheckoutAction) => void
    const first = runner.run(
      'order-1',
      () => new Promise<ApiPaymentCheckoutAction>((resolve) => { resolveAction = resolve }),
      execute,
    )

    expect(runner.busyOrderId).toBe('order-1')
    expect(busyChanges).toEqual(['order-1'])

    const secondLoader = vi.fn(async () => ({ type: 'redirect', url: '/second' }) as const)
    await expect(runner.run('order-2', secondLoader, execute)).resolves.toEqual({ status: 'ignored' })
    expect(secondLoader).not.toHaveBeenCalled()

    resolveAction({ type: 'redirect', url: '/first' })
    await expect(first).resolves.toEqual({ status: 'completed' })
    expect(execute).toHaveBeenCalledWith({ type: 'redirect', url: '/first' })
    expect(runner.busyOrderId).toBe('')
    expect(busyChanges).toEqual(['order-1', ''])
  })

  it('returns the resume error and always clears the row busy state', async () => {
    const busyChanges: string[] = []
    const runner = new PaymentCheckoutActionRunner((orderId) => busyChanges.push(orderId))
    const failure = new Error('checkout state changed')

    const result = await runner.run('order-3', async () => { throw failure }, vi.fn())

    expect(result).toEqual({ status: 'error', error: failure })
    expect(runner.busyOrderId).toBe('')
    expect(busyChanges).toEqual(['order-3', ''])
  })
})
