import type { ApiUsageRecord } from '@/api/types'

export function usageUserLabel(record: Pick<ApiUsageRecord, 'user_name' | 'user_id'>): string {
  return record.user_name?.trim() || record.user_id.trim() || '—'
}
