import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { parsePersistedComposerPrefs, resetComposerToolModeToDefault, useComposerPrefs } from '@/store/composer-prefs'
import {
  modelAllowsToolModeSelection,
  normalizeToolModeForCapabilities,
  resolveDefaultToolMode,
  resolveModelToolModeCapabilities,
  TOOL_MODE_MENU_ORDER,
  toolModeAvailable,
  visibleToolModes,
} from '@/lib/tool-mode'
import {
  resolveArmedTurnFlags,
  resolveToolRequestFlags,
  resolveTurnToolMode,
} from '@/store/conversations'

function resetPrefs() {
  useComposerPrefs.setState({
    mode: 'default',
    verify: false,
    optimizeImagePrompt: true,
    toolMode: 'auto',
    forceWebSearch: false,
    defaultToolMode: 'auto',
    selectedToolIdsByModel: {},
    paramValuesByModel: {},
    draftsByScope: {},
  })
}

describe('composer tool mode', () => {
  beforeEach(resetPrefs)

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('starts from the new automatic default', () => {
    const prefs = useComposerPrefs.getState()
    expect(prefs.toolMode).toBe('auto')
    expect(prefs.defaultToolMode).toBe('auto')
    expect(resolveArmedTurnFlags()).toMatchObject({ toolMode: 'auto' })
  })

  it('keeps image prompt optimization on by default and persists an explicit opt-out', () => {
    expect(parsePersistedComposerPrefs({}).optimizeImagePrompt).toBe(true)
    expect(resolveArmedTurnFlags().optimizeImagePrompt).toBe(true)

    useComposerPrefs.getState().setOptimizeImagePrompt(false)

    expect(useComposerPrefs.getState().optimizeImagePrompt).toBe(false)
    expect(resolveArmedTurnFlags().optimizeImagePrompt).toBe(false)
    expect(parsePersistedComposerPrefs({ optimizeImagePrompt: false }).optimizeImagePrompt).toBe(false)
  })

  it('forces Deep Research to enabled and clears forced search', () => {
    const prefs = useComposerPrefs.getState()
    prefs.setToolMode('disabled')
    useComposerPrefs.getState().setForceWebSearch(true)
    useComposerPrefs.getState().setMode('deep-research')

    expect(useComposerPrefs.getState()).toMatchObject({
      mode: 'deep-research',
      toolMode: 'enabled',
      forceWebSearch: false,
    })
    expect(resolveArmedTurnFlags()).toMatchObject({ mode: 'deep-research', toolMode: 'enabled' })
  })

  it('clears forced search when a new/default disabled policy is applied', () => {
    useComposerPrefs.setState({ toolMode: 'disabled', forceWebSearch: true })
    useComposerPrefs.getState().setToolMode('disabled')

    expect(useComposerPrefs.getState().forceWebSearch).toBe(false)
  })

  it('resets every new-chat entry to the complete account default', () => {
    useComposerPrefs.setState({ defaultToolMode: 'enabled', toolMode: 'disabled', forceWebSearch: true })

    resetComposerToolModeToDefault()

    expect(useComposerPrefs.getState()).toMatchObject({ toolMode: 'enabled', forceWebSearch: false })
  })

  it('allows forced search only while tools are disabled', () => {
    useComposerPrefs.getState().setForceWebSearch(true)
    expect(useComposerPrefs.getState().forceWebSearch).toBe(false)

    useComposerPrefs.getState().setToolMode('disabled')
    useComposerPrefs.getState().setForceWebSearch(true)
    expect(resolveArmedTurnFlags()).toMatchObject({ toolMode: 'disabled', webSearch: true })

    useComposerPrefs.getState().setToolMode('enabled')
    expect(useComposerPrefs.getState().forceWebSearch).toBe(false)
    expect(resolveArmedTurnFlags().webSearch).toBeUndefined()
  })

  it('does not persist a user-owned hosted-tool selection', () => {
    const setItem = vi.fn()
    vi.stubGlobal('window', {})
    vi.stubGlobal('localStorage', { setItem })

    useComposerPrefs.getState().setToolMode('enabled')

    expect(useComposerPrefs.getState()).not.toHaveProperty('officialToolNamesByModel')
    expect(setItem).toHaveBeenLastCalledWith('aivory.composer-prefs.v1', expect.not.stringContaining('officialToolNamesByModel'))
  })

  it('persists explicit per-model tool subsets while preserving all-versus-none', () => {
    useComposerPrefs.getState().setSelectedToolIds('model_1', [' builtin:web ', 'builtin:web', 'mcp:rail'])
    useComposerPrefs.getState().setSelectedToolIds('model_2', [])

    expect(useComposerPrefs.getState().selectedToolIdsByModel).toEqual({
      model_1: ['builtin:web', 'mcp:rail'],
      model_2: [],
    })

    useComposerPrefs.getState().setSelectedToolIds('model_1', undefined)
    expect(useComposerPrefs.getState().selectedToolIdsByModel).toEqual({ model_2: [] })
  })

  it('snapshots omitted, empty, and non-empty selections for the requested model', () => {
    useComposerPrefs.setState({
      selectedToolIdsByModel: {
        model_empty: [],
        model_subset: ['builtin:web_fetch', 'mcp:rail'],
      },
    })

    expect(resolveArmedTurnFlags('model_default').selectedToolIds).toBeUndefined()
    expect(resolveArmedTurnFlags('model_empty').selectedToolIds).toEqual([])
    expect(resolveArmedTurnFlags('model_subset').selectedToolIds).toEqual([
      'builtin:web_fetch',
      'mcp:rail',
    ])
  })
})

describe('model tool capability', () => {
  it('hides the per-turn selector only for models configured with no tool calls', () => {
    expect(modelAllowsToolModeSelection('none')).toBe(false)
    expect(modelAllowsToolModeSelection('native')).toBe(true)
    expect(modelAllowsToolModeSelection('prompt')).toBe(true)
  })

  it('keeps the selector compatible with older model-list responses', () => {
    expect(modelAllowsToolModeSelection(undefined)).toBe(true)
  })

  it('treats a model-level none policy as authoritative over every tool family', () => {
    expect(resolveModelToolModeCapabilities('none', { available: true })).toEqual({ available: false })
    expect(resolveModelToolModeCapabilities('native', { available: true })).toEqual({ available: true })
  })

  it('keeps a stable order while omitting unsupported modes from the menu', () => {
    expect(TOOL_MODE_MENU_ORDER).toEqual(['auto', 'enabled', 'disabled'])
    expect(toolModeAvailable('auto', { available: false })).toBe(true)
    expect(toolModeAvailable('disabled', { available: false })).toBe(true)
    expect(toolModeAvailable('enabled', { available: false })).toBe(false)
    expect(visibleToolModes({ available: true })).toEqual(['auto', 'enabled', 'disabled'])
    expect(visibleToolModes({ available: false })).toEqual(['auto', 'disabled'])
  })

  it('falls an unsupported persisted selection back to automatic', () => {
    expect(normalizeToolModeForCapabilities('enabled', { available: false })).toBe('auto')
    expect(normalizeToolModeForCapabilities('disabled', { available: false })).toBe('disabled')
  })
})

describe('tool mode migration', () => {
  it('prefers every valid new account setting over a contradictory legacy value', () => {
    expect(resolveDefaultToolMode({ tool_mode_default: 'auto', disable_tools_default: true })).toBe('auto')
    expect(resolveDefaultToolMode({ tool_mode_default: 'disabled', disable_tools_default: false })).toBe('disabled')
    expect(resolveDefaultToolMode({ tool_mode_default: 'enabled', disable_tools_default: true })).toBe('enabled')
    expect(resolveDefaultToolMode({ tool_mode_default: 'official', disable_tools_default: true })).toBe('enabled')
  })

  it('preserves explicit legacy account choices', () => {
    expect(resolveDefaultToolMode({ disable_tools_default: true })).toBe('disabled')
    expect(resolveDefaultToolMode({ disable_tools_default: false })).toBe('enabled')
  })

  it('uses automatic mode for missing or invalid account settings', () => {
    expect(resolveDefaultToolMode(undefined)).toBe('auto')
    expect(resolveDefaultToolMode({})).toBe('auto')
    expect(resolveDefaultToolMode({ tool_mode_default: 'sometimes' })).toBe('auto')
  })

  it('migrates old local booleans to auto without losing drafts or model params', () => {
    const migrated = parsePersistedComposerPrefs({
      mode: 'default',
      verify: true,
      noTools: true,
      defaultNoTools: true,
      forceWebSearch: true,
      paramValuesByModel: { model_1: { temperature: 0.4, thinking: true } },
      draftsByScope: { 'new-chat': 'unfinished question' },
    })

    expect(migrated).toMatchObject({
      mode: 'default',
      verify: true,
      toolMode: 'auto',
      defaultToolMode: 'auto',
      forceWebSearch: false,
      paramValuesByModel: { model_1: { temperature: 0.4, thinking: true } },
      draftsByScope: { 'new-chat': 'unfinished question' },
    })
  })

  it('ignores retired user-owned hosted-tool selections when loading preferences', () => {
    const migrated = parsePersistedComposerPrefs({
      officialToolNamesByModel: {
        model_1: [],
        model_2: [' web_search ', 'web_search'],
        invalid_model: 'image_generation',
      },
    })

    expect(migrated).not.toHaveProperty('officialToolNamesByModel')
  })

  it('sanitizes persisted tool subsets without collapsing an explicit empty set', () => {
    const migrated = parsePersistedComposerPrefs({
      selectedToolIdsByModel: {
        model_1: [' builtin:web ', 'builtin:web', 7, 'mcp:rail'],
        model_2: [],
        invalid: 'builtin:web',
      },
    })

    expect(migrated.selectedToolIdsByModel).toEqual({
      model_1: ['builtin:web', 'mcp:rail'],
      model_2: [],
    })
  })
})

describe('turn tool policy propagation', () => {
  it('keeps all three explicit policies distinct', () => {
    expect(resolveTurnToolMode('auto')).toBe('auto')
    expect(resolveTurnToolMode('disabled')).toBe('disabled')
    expect(resolveTurnToolMode('enabled')).toBe('enabled')
  })

  it('normalizes legacy/internal omissions to an explicit automatic policy', () => {
    expect(resolveToolRequestFlags(undefined)).toEqual({ toolMode: 'auto', webSearch: undefined })
  })

  it('forces enabled for fast and Deep Research turns', () => {
    expect(resolveToolRequestFlags('auto', { fast: true })).toEqual({ toolMode: 'enabled', webSearch: undefined })
    expect(resolveToolRequestFlags('disabled', { mode: 'deep-research', webSearch: true })).toEqual({
      toolMode: 'enabled',
      webSearch: undefined,
    })
  })

  it('serializes forced web search only with disabled mode', () => {
    expect(resolveToolRequestFlags('disabled', { webSearch: true })).toEqual({
      toolMode: 'disabled',
      webSearch: true,
    })
    expect(resolveToolRequestFlags('auto', { webSearch: true })).toEqual({ toolMode: 'auto', webSearch: undefined })
    expect(resolveToolRequestFlags('enabled', { webSearch: true })).toEqual({
      toolMode: 'enabled',
      webSearch: undefined,
    })
  })

})
