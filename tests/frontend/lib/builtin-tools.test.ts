import { describe, expect, it } from 'vitest'
import {
  modelHasBuiltinTools,
  modelSupportsBuiltinTool,
  replaceVisibleBuiltinToolNames,
  resolveBuiltinToolNames,
  toggleBuiltinToolName,
} from '@/lib/builtin-tools'

const AVAILABLE = ['aivory_web_search', 'image_generate', 'python_execute']

describe('built-in tool selection', () => {
  it('treats null and omitted configuration as all registered tools', () => {
    expect(resolveBuiltinToolNames(null, AVAILABLE)).toEqual(AVAILABLE)
    expect(resolveBuiltinToolNames(undefined, AVAILABLE)).toEqual(AVAILABLE)
  })

  it('distinguishes an explicit empty allowlist from the default', () => {
    expect(resolveBuiltinToolNames([], AVAILABLE)).toEqual([])
  })

  it('keeps registry order and drops unavailable saved names', () => {
    expect(resolveBuiltinToolNames(['aivory_web_search', 'removed', 'image_generate'], AVAILABLE)).toEqual([
      'aivory_web_search',
      'image_generate',
    ])
  })

  it('expands the default before toggling and keeps custom all-selected explicit', () => {
    expect(toggleBuiltinToolName(null, AVAILABLE, 'python_execute')).toEqual([
      'aivory_web_search',
      'image_generate',
    ])
    expect(toggleBuiltinToolName(['aivory_web_search', 'image_generate'], AVAILABLE, 'python_execute')).toEqual(AVAILABLE)
  })

  it('changes visible tools without losing hidden selections', () => {
    const visible = ['aivory_web_search', 'image_generate']

    expect(
      replaceVisibleBuiltinToolNames(
        ['image_generate', 'python_execute'],
        AVAILABLE,
        visible,
        ['aivory_web_search'],
      ),
    ).toEqual(['aivory_web_search', 'python_execute'])
    expect(replaceVisibleBuiltinToolNames(['image_generate'], AVAILABLE, visible, [])).toEqual([])
  })

  it('materializes default-all while retaining globally hidden tools', () => {
    expect(replaceVisibleBuiltinToolNames(null, AVAILABLE, ['aivory_web_search', 'image_generate'], [])).toEqual([
      'python_execute',
    ])
  })

  it('reads resolved public capabilities and keeps legacy default-all compatibility', () => {
    expect(modelHasBuiltinTools(undefined)).toBe(false)
    expect(modelHasBuiltinTools({ tool_mode: 'native', builtin_tools: [] })).toBe(false)
    expect(modelHasBuiltinTools({ tool_mode: 'none', builtin_tools: AVAILABLE })).toBe(false)
    expect(modelHasBuiltinTools({ tool_mode: 'native', builtin_tools: null })).toBe(true)
    expect(modelHasBuiltinTools({ tool_mode: 'native' })).toBe(true)
    expect(
      modelSupportsBuiltinTool({ tool_mode: 'native', builtin_tools: ['aivory_web_search'] }, 'aivory_web_search'),
    ).toBe(true)
    expect(modelSupportsBuiltinTool({ tool_mode: 'native', builtin_tools: ['aivory_web_search'] }, 'python_execute')).toBe(
      false,
    )
    expect(modelSupportsBuiltinTool({ tool_mode: 'native' }, 'python_execute')).toBe(true)
  })
})
