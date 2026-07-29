const CHECKOUT_ERROR_KEYS = {
  payment_method_unavailable: 'payment.errors.methodUnavailable',
  payment_product_unavailable: 'payment.errors.productUnavailable',
  payment_checkout_unavailable: 'payment.errors.checkoutUnavailable',
  payment_checkout_expired: 'payment.errors.checkoutExpired',
  payment_order_not_resumable: 'payment.errors.checkoutNotResumable',
  payment_order_already_paid: 'payment.errors.orderAlreadyPaid',
  payment_resume_unavailable: 'payment.errors.resumeUnavailable',
  payment_order_state_changed: 'payment.errors.orderStateChanged',
  payment_checkout_state_unknown: 'payment.errors.checkoutStateUnknown',
  payment_waffo_product_currency_unsupported: 'payment.errors.waffoProductCurrencyUnsupported',
  payment_user_group_already_permanent: 'payment.errors.userGroupAlreadyPermanent',
} as const

const ADMIN_PAYMENT_CHANNEL_ERROR_KEYS = {
  payment_channel_id_exists: 'admin:paymentChannels.errors.idConflict',
  payment_channel_has_pending_orders: 'admin:paymentChannels.errors.pendingOrders',
} as const

const ADMIN_PAYMENT_ORDER_ERROR_KEYS = {
  payment_order_not_deletable: 'admin:paymentOrders.actions.deleteUnavailable',
  payment_order_delete_requires_gateway_confirmation: 'admin:paymentOrders.actions.deleteGatewayConfirmationRequired',
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

export function adminPaymentOrderErrorKey(
  code: string,
): (typeof ADMIN_PAYMENT_ORDER_ERROR_KEYS)[keyof typeof ADMIN_PAYMENT_ORDER_ERROR_KEYS] | undefined {
  return ADMIN_PAYMENT_ORDER_ERROR_KEYS[code as keyof typeof ADMIN_PAYMENT_ORDER_ERROR_KEYS]
}
