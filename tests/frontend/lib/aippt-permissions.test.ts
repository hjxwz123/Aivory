import { afterEach, describe, expect, it, vi } from 'vitest'
import { aipptApi } from '@/api/endpoints'

describe('AI PPT request scope', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('keeps personal requests unscoped', () => {
    vi.stubGlobal('localStorage', { getItem: () => 'personal' })
    expect(aipptApi.scopedPath('/me/ppt/decks')).toBe('/me/ppt/decks')
  })

  it('adds the selected workspace to every request path', () => {
    vi.stubGlobal('localStorage', { getItem: () => 'ws & 1' })
    expect(aipptApi.scopedPath('/me/ppt/decks')).toBe('/me/ppt/decks?workspace_id=ws%20%26%201')
    expect(aipptApi.scopedPath('/me/ppt/resource?url=cover')).toBe('/me/ppt/resource?url=cover&workspace_id=ws%20%26%201')
  })
})
