import type { ApiModel } from '@/api/types'

export function showsDedicatedImageControls(kind: ApiModel['kind'] | undefined): boolean {
  return kind === 'image'
}
