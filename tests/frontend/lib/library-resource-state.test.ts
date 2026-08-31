import { describe, expect, it } from 'vitest'
import { revokedLibraryResourceKinds } from '@/lib/library-resource-state'

describe('library resource capability transitions', () => {
  it('returns only resource families that changed from enabled to disabled', () => {
    expect(
      revokedLibraryResourceKinds(
        { skill: true, prompt: true, mcp: true },
        { skill: false, prompt: true, mcp: false },
      ),
    ).toEqual(['skill', 'mcp'])
  })

  it('does not treat newly enabled or unchanged families as revoked', () => {
    expect(
      revokedLibraryResourceKinds(
        { skill: false, prompt: true, mcp: false },
        { skill: true, prompt: true, mcp: true },
      ),
    ).toEqual([])
  })
})
