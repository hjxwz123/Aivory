import { describe, expect, it } from 'vitest'
import {
  isSelectableMCPServer,
  materializeModelMCPServerIDs,
  replaceAvailableModelMCPServerIDs,
  resolveModelMCPServerIDs,
  toggleModelMCPServerID,
} from '@/lib/model-mcp-tools'

const AVAILABLE = ['rail', 'papers']

describe('model MCP default selection', () => {
  it('uses every currently available service for the default policy', () => {
    expect(resolveModelMCPServerIDs(null, AVAILABLE)).toEqual(AVAILABLE)
    expect(materializeModelMCPServerIDs(undefined, AVAILABLE)).toEqual(AVAILABLE)
  })

  it('keeps explicit unavailable and deleted IDs visible', () => {
    expect(resolveModelMCPServerIDs(['rail', 'disabled', 'deleted', 'rail'], AVAILABLE)).toEqual([
      'rail',
      'disabled',
      'deleted',
    ])
  })

  it('preserves unavailable selections when replacing the available subset', () => {
    expect(replaceAvailableModelMCPServerIDs(['rail', 'disabled'], AVAILABLE, ['papers'])).toEqual([
      'papers',
      'disabled',
    ])
  })

  it('can remove an unavailable saved service without losing other selections', () => {
    expect(toggleModelMCPServerID(['rail', 'disabled'], AVAILABLE, 'disabled')).toEqual(['rail'])
    expect(toggleModelMCPServerID(null, AVAILABLE, 'papers')).toEqual(['rail'])
  })

  it('requires global enablement and a non-empty discovery snapshot', () => {
    expect(
      isSelectableMCPServer({
        id: 'rail',
        enabled: true,
        discovered_tools: [{}],
      }),
    ).toBe(true)
    expect(
      isSelectableMCPServer({
        id: 'rail',
        enabled: false,
        discovered_tools: [{}],
      }),
    ).toBe(false)
    expect(
      isSelectableMCPServer({
        id: 'rail',
        enabled: true,
        discovered_tools: [],
      }),
    ).toBe(false)
    expect(isSelectableMCPServer({ id: 'rail', enabled: true })).toBe(false)
  })
})
