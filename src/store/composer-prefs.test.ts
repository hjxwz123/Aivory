import { beforeEach, describe, expect, it } from 'vitest'
import { parsePersistedComposerPrefs, resetComposerToolModeToDefault, useComposerPrefs } from './composer-prefs'
import { resolveDefaultToolMode } from '@/lib/tool-mode'
import {
  resolveArmedTurnFlags,
  resolveToolRequestFlags,
  resolveTurnToolMode,
} from './conversations'

function resetPrefs() {
  useComposerPrefs.setState({
    mode: 'default',
    verify: false,
    toolMode: 'auto',
    forceWebSearch: false,
    defaultToolMode: 'auto',
    paramValuesByModel: {},
    draftsByScope: {},
  })
}

describe('composer tool mode', () => {
  beforeEach(resetPrefs)

  it('starts from the new automatic default', () => {
    const prefs = useComposerPrefs.getState()
    expect(prefs.toolMode).toBe('auto')
    expect(prefs.defaultToolMode).toBe('auto')
    expect(resolveArmedTurnFlags()).toMatchObject({ toolMode: 'auto' })
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
})

describe('tool mode migration', () => {
  it('prefers every valid new account setting over a contradictory legacy value', () => {
    expect(resolveDefaultToolMode({ tool_mode_default: 'auto', disable_tools_default: true })).toBe('auto')
    expect(resolveDefaultToolMode({ tool_mode_default: 'disabled', disable_tools_default: false })).toBe('disabled')
    expect(resolveDefaultToolMode({ tool_mode_default: 'enabled', disable_tools_default: true })).toBe('enabled')
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
