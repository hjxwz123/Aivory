import { describe, expect, it } from 'vitest'
import { committedToolSelection } from '@/lib/tool-selection'

describe('tool selection commit semantics', () => {
  it('uses the model default when the committed selection matches it', () => {
    expect(committedToolSelection([], [], new Set())).toBeUndefined()
    expect(
      committedToolSelection(
        ['builtin:web_fetch', 'mcp:rail'],
        ['builtin:web_fetch'],
        new Set(['builtin:web_fetch']),
      ),
    ).toBeUndefined()
  })

  it('keeps a complete manual selection when the model default is narrower', () => {
    expect(
      committedToolSelection(
        ['builtin:web_fetch', 'mcp:rail'],
        ['builtin:web_fetch'],
        new Set(['builtin:web_fetch', 'mcp:rail']),
      ),
    ).toEqual(['builtin:web_fetch', 'mcp:rail'])
  })

  it('keeps a selected subset in catalog order and drops unavailable ids', () => {
    expect(
      committedToolSelection(
        ['builtin:web_fetch', 'hosted:web_search', 'mcp:rail'],
        ['hosted:web_search'],
        new Set(['mcp:rail', 'missing:old', 'builtin:web_fetch']),
      ),
    ).toEqual(['builtin:web_fetch', 'mcp:rail'])
  })
})
