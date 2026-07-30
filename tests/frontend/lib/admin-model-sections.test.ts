import { describe, expect, it } from 'vitest'
import type { ApiModel } from '@/api/types'
import { showsDedicatedImageControls } from '@/lib/admin-model-sections'

describe('admin model sections', () => {
  it.each<[ApiModel['kind'], boolean]>([
    ['chat', false],
    ['image', true],
    ['embedding', false],
  ])('shows dedicated image controls for %s models: %s', (kind, expected) => {
    expect(showsDedicatedImageControls(kind)).toBe(expected)
  })
})
