import type { ApiRedeemCode } from '@/api/types'

export type RedeemCodeStatus = 'unused' | 'partial' | 'used' | 'invalid'

type RedeemCodeStatusFields = Pick<ApiRedeemCode, 'enabled' | 'expires_at' | 'used_count' | 'max_uses'>

export function getRedeemCodeStatus(
  code: RedeemCodeStatusFields,
  now = Math.floor(Date.now() / 1000),
): RedeemCodeStatus {
  if (!code.enabled || (code.expires_at > 0 && code.expires_at < now)) return 'invalid'
  if (code.used_count >= code.max_uses) return 'used'
  if (code.used_count > 0) return 'partial'
  return 'unused'
}
