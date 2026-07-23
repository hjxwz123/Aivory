import { describe, expect, it } from 'vitest'
import {
  modelHasBuiltinTools,
  modelSupportsBuiltinTool,
  resolveBuiltinToolNames,
  toggleBuiltinToolName,
} from './builtin-tools'

const AVAILABLE = ['image_generate', 'python_execute', 'web_search']

describe('built-in tool selection', () => {
  it('treats null and omitted configuration as all registered tools', () => {
    expect(resolveBuiltinToolNames(null, AVAILABLE)).toEqual(AVAILABLE)
    expect(resolveBuiltinToolNames(undefined, AVAILABLE)).toEqual(AVAILABLE)
  })

  it('distinguishes an explicit empty allowlist from the default', () => {
    expect(resolveBuiltinToolNames([], AVAILABLE)).toEqual([])
  })

  it('keeps registry order and drops unavailable saved names', () => {
    expect(resolveBuiltinToolNames(['web_search', 'removed', 'image_generate'], AVAILABLE)).toEqual([
      'image_generate',
      'web_search',
    ])
  })

  it('expands the default before toggling and keeps custom all-selected explicit', () => {
    expect(toggleBuiltinToolName(null, AVAILABLE, 'python_execute')).toEqual([
      'image_generate',
      'web_search',
    ])
    expect(toggleBuiltinToolName(['image_generate', 'web_search'], AVAILABLE, 'python_execute')).toEqual(AVAILABLE)
  })

  it('reads resolved public capabilities and keeps legacy default-all compatibility', () => {
    expect(modelHasBuiltinTools(undefined)).toBe(false)
    expect(modelHasBuiltinTools({ tool_mode: 'native', builtin_tools: [] })).toBe(false)
    expect(modelHasBuiltinTools({ tool_mode: 'none', builtin_tools: AVAILABLE })).toBe(false)
    expect(modelHasBuiltinTools({ tool_mode: 'native', builtin_tools: null })).toBe(true)
    expect(modelHasBuiltinTools({ tool_mode: 'native' })).toBe(true)
    expect(modelSupportsBuiltinTool({ tool_mode: 'native', builtin_tools: ['web_search'] }, 'web_search')).toBe(true)
    expect(modelSupportsBuiltinTool({ tool_mode: 'native', builtin_tools: ['web_search'] }, 'python_execute')).toBe(false)
    expect(modelSupportsBuiltinTool({ tool_mode: 'native' }, 'python_execute')).toBe(true)
  })
})
