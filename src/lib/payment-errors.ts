const CHECKOUT_ERROR_KEYS = {
  payment_method_unavailable: 'payment.errors.methodUnavailable',
  payment_product_unavailable: 'payment.errors.productUnavailable',
  payment_checkout_unavailable: 'payment.errors.checkoutUnavailable',
  payment_user_group_already_permanent: 'payment.errors.userGroupAlreadyPermanent',
} as const

const ADMIN_PAYMENT_CHANNEL_ERROR_KEYS = {
  payment_channel_id_exists: 'admin:paymentChannels.errors.idConflict',
  payment_channel_has_pending_orders: 'admin:paymentChannels.errors.pendingOrders',
} as const

export function checkoutPaymentErrorKey(
  code: string,
): (typeof CHECKOUT_ERROR_KEYS)[keyof typeof CHECKOUT_ERROR_KEYS] | undefined {
  return CHECKOUT_ERROR_KEYS[code as keyof typeof CHECKOUT_ERROR_KEYS]
}

export function adminPaymentChannelErrorKey(
  code: string,
): (typeof ADMIN_PAYMENT_CHANNEL_ERROR_KEYS)[keyof typeof ADMIN_PAYMENT_CHANNEL_ERROR_KEYS] | undefined {
  return ADMIN_PAYMENT_CHANNEL_ERROR_KEYS[code as keyof typeof ADMIN_PAYMENT_CHANNEL_ERROR_KEYS]
}
