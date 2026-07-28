import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { PaymentMethodIcon } from '@/components/payment/payment-method-icon'
import { resolvePaymentMethodIcon } from '@/lib/payment-method-icons'

describe('PaymentMethodIcon', () => {
  it('renders administrator-uploaded icon URLs as images', () => {
    const html = renderToStaticMarkup(
      createElement(PaymentMethodIcon, { icon: '/api/icons/0123456789abcdef01234567.png' }),
    )

    expect(html).toContain('<img')
    expect(html).toContain('/api/icons/0123456789abcdef01234567.png')
  })

  it('keeps existing Lucide icon values compatible', () => {
    const html = renderToStaticMarkup(createElement(PaymentMethodIcon, { icon: 'CreditCard' }))

    expect(html).toContain('lucide-credit-card')
  })

  it('rejects unsafe or unknown values and uses the payment fallback', () => {
    expect(resolvePaymentMethodIcon('javascript:alert(1)')).toEqual({ kind: 'fallback' })
    expect(resolvePaymentMethodIcon('/not-an-icon.png')).toEqual({ kind: 'fallback' })

    const html = renderToStaticMarkup(createElement(PaymentMethodIcon, { icon: 'not-a-real-icon' }))
    expect(html).toContain('lucide-credit-card')
  })
})
