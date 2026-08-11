import { describe, expect, it } from 'vitest'
import { committedToolSelection } from '@/lib/tool-selection'

describe('tool selection commit semantics', () => {
  it('keeps default-all distinct from an explicit empty selection', () => {
    expect(committedToolSelection([], new Set(), true)).toBeUndefined()
    expect(committedToolSelection([], new Set(), false)).toEqual([])
  })

  it('canonicalizes a complete non-empty selection to default-all', () => {
    expect(
      committedToolSelection(
        ['builtin:web_fetch', 'mcp:rail'],
        new Set(['builtin:web_fetch', 'mcp:rail']),
        false,
      ),
    ).toBeUndefined()
  })

  it('keeps a selected subset in catalog order and drops unavailable ids', () => {
    expect(
      committedToolSelection(
        ['builtin:web_fetch', 'hosted:web_search', 'mcp:rail'],
        new Set(['mcp:rail', 'missing:old', 'builtin:web_fetch']),
        false,
      ),
    ).toEqual(['builtin:web_fetch', 'mcp:rail'])
  })
})
