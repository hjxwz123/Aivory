import { describe, expect, it } from 'vitest'
import { committedToolSelection } from '@/lib/tool-selection'
import {
  countSelectedTools,
  toolSegmentOf,
  toolsInSegment,
} from '@/lib/tool-segments'

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

describe('tool selection official/mine segments', () => {
  const catalog = [
    { id: 'builtin:web_fetch' },
    { id: 'hosted:web_search' },
    { id: 'mcp:rail' },
    { id: 'mcp:usermcp' },
    { id: 'usermcp:umcp_notes' },
  ]
  // Mirrors the dialog: allowedIDs / defaults / Select all / count / commit
  // are built from the GLOBAL merged catalog, never per-segment.
  const allowedIDs = catalog.map((tool) => tool.id)
  const defaultIDs = ['builtin:web_fetch']

  it('assigns segments by the source parsed on the first colon', () => {
    expect(toolSegmentOf('usermcp:umcp_notes')).toBe('mine')
    expect(toolSegmentOf('mcp:usermcp')).toBe('official')
    expect(toolSegmentOf('usermcp')).toBe('official')
    expect(toolSegmentOf('builtin:web_fetch')).toBe('official')
  })

  it('shows usermcp ids only under the mine segment', () => {
    expect(toolsInSegment(catalog, 'mine').map((tool) => tool.id)).toEqual(['usermcp:umcp_notes'])
    expect(toolsInSegment(catalog, 'official').map((tool) => tool.id)).toEqual([
      'builtin:web_fetch',
      'hosted:web_search',
      'mcp:rail',
      'mcp:usermcp',
    ])
  })

  it('keeps a selection made under mine when switching to official and commits it', () => {
    const mineVisible = toolsInSegment(catalog, 'mine').map((tool) => tool.id)
    const draft = new Set([...defaultIDs, ...mineVisible])

    const officialVisible = toolsInSegment(catalog, 'official').map((tool) => tool.id)
    expect(officialVisible).not.toContain('usermcp:umcp_notes')
    // Segment switching only reshapes the visible list; the draft set survives.
    expect(countSelectedTools(draft, allowedIDs)).toBe(2)
    expect(committedToolSelection(allowedIDs, defaultIDs, draft)).toEqual([
      'builtin:web_fetch',
      'usermcp:umcp_notes',
    ])
  })

  it('spans both segments: Select all drafts and commits official plus usermcp ids', () => {
    const draft = new Set(allowedIDs)
    expect(committedToolSelection(allowedIDs, defaultIDs, draft)).toEqual(allowedIDs)
    expect(draft.has('usermcp:umcp_notes')).toBe(true)
    expect(draft.has('mcp:rail')).toBe(true)
  })

  it('reports the count on the global set, unaffected by the active segment', () => {
    const draft = new Set(['mcp:rail', 'usermcp:umcp_notes'])
    const officialVisible = toolsInSegment(catalog, 'official').map((tool) => tool.id)
    const mineVisible = toolsInSegment(catalog, 'mine').map((tool) => tool.id)
    // Each segment alone sees only one of the two selected ids, yet the
    // dialog counts against the merged allowed list.
    expect(officialVisible.filter((id) => draft.has(id))).toHaveLength(1)
    expect(mineVisible.filter((id) => draft.has(id))).toHaveLength(1)
    expect(countSelectedTools(draft, allowedIDs)).toBe(2)
  })
})
