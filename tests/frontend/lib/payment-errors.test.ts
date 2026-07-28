import { describe, expect, it } from 'vitest'

import { adminPaymentChannelErrorKey, checkoutPaymentErrorKey } from '@/lib/payment-errors'

describe('payment error translation keys', () => {
  it('maps every supported checkout error code to a buyer-facing message', () => {
    expect(checkoutPaymentErrorKey('payment_method_unavailable')).toBe('payment.errors.methodUnavailable')
    expect(checkoutPaymentErrorKey('payment_product_unavailable')).toBe('payment.errors.productUnavailable')
    expect(checkoutPaymentErrorKey('payment_checkout_unavailable')).toBe('payment.errors.checkoutUnavailable')
    expect(checkoutPaymentErrorKey('payment_waffo_product_currency_unsupported')).toBe(
      'payment.errors.waffoProductCurrencyUnsupported',
    )
    expect(checkoutPaymentErrorKey('payment_user_group_already_permanent')).toBe(
      'payment.errors.userGroupAlreadyPermanent',
    )
  })

  it('maps payment-channel conflicts to administrator-facing messages', () => {
    expect(adminPaymentChannelErrorKey('payment_channel_id_exists')).toBe(
      'admin:paymentChannels.errors.idConflict',
    )
    expect(adminPaymentChannelErrorKey('payment_channel_has_pending_orders')).toBe(
      'admin:paymentChannels.errors.pendingOrders',
    )
  })

  it('leaves unknown provider and server errors to the existing fallback', () => {
    expect(checkoutPaymentErrorKey('unknown_error')).toBeUndefined()
    expect(adminPaymentChannelErrorKey('unknown_error')).toBeUndefined()
  })
})
