import { create } from 'zustand'
import { isToolMode, type ToolMode } from '@/lib/tool-mode'
import { sanitizeOfficialToolNames } from '@/lib/official-tools'

export type ComposerMode = 'default' | 'deep-research' | 'canvas'

type ParamValue = string | number | boolean | null
export type ComposerParamValues = Record<string, ParamValue>

export interface PersistedComposerPrefs {
  mode: ComposerMode
  verify: boolean
  // Per-turn tool policy. Deep Research requires tools, so selecting it forces
  // this to enabled; the setters keep that invariant in persisted state.
  toolMode: ToolMode
  // Forced non-tool web search is only meaningful in disabled mode; switching
  // to auto/enabled/official clears it automatically.
  forceWebSearch: boolean
  // Account-level default mirrored from `tool_mode_default`. New conversations
  // reset the live toolMode to this complete value (including auto/enabled), so
  // a prior conversation's override cannot leak into the next one.
  defaultToolMode: ToolMode
  // Provider-native selections are model-scoped because each model owns a
  // different administrator-defined allowlist.
  officialToolNamesByModel: Record<string, string[]>
  paramValuesByModel: Record<string, ComposerParamValues>
  draftsByScope: Record<string, string>
}

interface ComposerPrefsStore extends PersistedComposerPrefs {
  setMode: (mode: ComposerMode) => void
  setVerify: (verify: boolean) => void
  setToolMode: (toolMode: ToolMode) => void
  // Update the mirror of the server-side default tool policy.
  setDefaultToolMode: (toolMode: ToolMode) => void
  setOfficialToolNames: (modelId: string, names: string[] | undefined) => void
  setForceWebSearch: (on: boolean) => void
  setParamValues: (modelId: string, values: Record<string, unknown>) => void
  setDraft: (scope: string, value: string) => void
  clearDraft: (scope: string) => void
}

const STORAGE_KEY = 'aivory.composer-prefs.v1'
const MAX_DRAFT_LEN = 12_000

const DEFAULT_PREFS: PersistedComposerPrefs = {
  mode: 'default',
  verify: false,
  toolMode: 'auto',
  forceWebSearch: false,
  defaultToolMode: 'auto',
  officialToolNamesByModel: {},
  paramValuesByModel: {},
  draftsByScope: {},
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isMode(value: unknown): value is ComposerMode {
  return value === 'default' || value === 'deep-research' || value === 'canvas'
}

function isParamValue(value: unknown): value is ParamValue {
  return value === null || typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean'
}

function sanitizeParamValues(raw: unknown): ComposerParamValues {
  if (!isRecord(raw)) return {}
  const out: ComposerParamValues = {}
  for (const [key, value] of Object.entries(raw)) {
    if (!key || !isParamValue(value)) continue
    out[key] = value
  }
  return out
}

function sanitizeParamValuesByModel(raw: unknown): Record<string, ComposerParamValues> {
  if (!isRecord(raw)) return {}
  const out: Record<string, ComposerParamValues> = {}
  for (const [modelId, values] of Object.entries(raw)) {
    if (!modelId) continue
    const clean = sanitizeParamValues(values)
    if (Object.keys(clean).length > 0) {
      out[modelId] = clean
    }
  }
  return out
}

function sanitizeOfficialToolNamesByModel(raw: unknown): Record<string, string[]> {
  if (!isRecord(raw)) return {}
  const out: Record<string, string[]> = {}
  for (const [modelId, value] of Object.entries(raw)) {
    const id = modelId.trim()
    if (!id || !Array.isArray(value)) continue
    out[id] = sanitizeOfficialToolNames(value)
  }
  return out
}

function sanitizeDraftsByScope(raw: unknown): Record<string, string> {
  if (!isRecord(raw)) return {}
  const out: Record<string, string> = {}
  for (const [scope, value] of Object.entries(raw)) {
    if (!scope || typeof value !== 'string' || value.length === 0) continue
    out[scope] = value.slice(0, MAX_DRAFT_LEN)
  }
  return out
}

/** Sanitizes the localStorage payload and migrates the retired boolean policy. */
export function parsePersistedComposerPrefs(parsed: unknown): PersistedComposerPrefs {
  if (!isRecord(parsed)) return DEFAULT_PREFS
  // Do not translate the old local `noTools` booleans here. Older clients
  // armed that value for every account whose server setting was absent, so it
  // cannot distinguish an explicit user choice from the retired implicit
  // default. Auth hydration resolves explicit legacy account settings; a
  // missing new local value intentionally starts at the new default, auto.
  const toolMode = isToolMode(parsed.toolMode) ? parsed.toolMode : DEFAULT_PREFS.toolMode
  return {
    mode: isMode(parsed.mode) ? parsed.mode : DEFAULT_PREFS.mode,
    verify: parsed.verify === true,
    toolMode,
    // forced search only exists inside an explicitly disabled-tools turn
    forceWebSearch: toolMode === 'disabled' && parsed.forceWebSearch === true,
    defaultToolMode: isToolMode(parsed.defaultToolMode) ? parsed.defaultToolMode : DEFAULT_PREFS.defaultToolMode,
    officialToolNamesByModel: sanitizeOfficialToolNamesByModel(parsed.officialToolNamesByModel),
    paramValuesByModel: sanitizeParamValuesByModel(parsed.paramValuesByModel),
    draftsByScope: sanitizeDraftsByScope(parsed.draftsByScope),
  }
}

function loadPrefs(): PersistedComposerPrefs {
  if (typeof window === 'undefined') return DEFAULT_PREFS
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULT_PREFS
    return parsePersistedComposerPrefs(JSON.parse(raw) as unknown)
  } catch {
    return DEFAULT_PREFS
  }
}

// persistedFrom snapshots only the persisted keys, then merges a patch, so new
// prefs auto-persist without every setter re-listing every field (a common
// source of "the setting resets on reload" bugs).
function persistedFrom(state: PersistedComposerPrefs, patch: Partial<PersistedComposerPrefs>): PersistedComposerPrefs {
  return {
    mode: state.mode,
    verify: state.verify,
    toolMode: state.toolMode,
    forceWebSearch: state.forceWebSearch,
    defaultToolMode: state.defaultToolMode,
    officialToolNamesByModel: state.officialToolNamesByModel,
    paramValuesByModel: state.paramValuesByModel,
    draftsByScope: state.draftsByScope,
    ...patch,
  }
}

function persistPrefs(prefs: PersistedComposerPrefs): void {
  if (typeof window === 'undefined') return
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs))
  } catch {
    /* noop */
  }
}

export const useComposerPrefs = create<ComposerPrefsStore>((set) => {
  const initial = loadPrefs()
  // commit persists the merged persisted-subset and returns the same patch as
  // the state update — the single write path for every scalar/map setter.
  const commit = (patch: Partial<PersistedComposerPrefs>) =>
    set((state) => {
      persistPrefs(persistedFrom(state, patch))
      return patch
    })
  return {
    ...initial,
    setMode(mode) {
      // Deep Research always uses tools and bypasses automatic classification.
      if (mode === 'deep-research') commit({ mode, toolMode: 'enabled', forceWebSearch: false })
      else commit({ mode })
    },
    setVerify(verify) {
      commit({ verify })
    },
    setToolMode(toolMode) {
      // Every policy except enabled exits Deep Research, whose pipeline always
      // requires tools. Only disabled mode may retain forced non-tool search.
      if (toolMode === 'enabled') commit({ toolMode, forceWebSearch: false })
      else if (toolMode === 'disabled') commit({ toolMode, mode: 'default', forceWebSearch: false })
      else commit({ toolMode, mode: 'default', forceWebSearch: false })
    },
    setDefaultToolMode(toolMode) {
      // Mirror-only: callers apply the live mode through setToolMode so the
      // Deep Research / forced-search invariants run in one place.
      commit({ defaultToolMode: toolMode })
    },
    setOfficialToolNames(modelId, names) {
      const id = modelId.trim()
      if (!id) return
      set((state) => {
        const officialToolNamesByModel = { ...state.officialToolNamesByModel }
        if (names === undefined) delete officialToolNamesByModel[id]
        else officialToolNamesByModel[id] = sanitizeOfficialToolNames(names)
        persistPrefs(persistedFrom(state, { officialToolNamesByModel }))
        return { officialToolNamesByModel }
      })
    },
    setForceWebSearch(on) {
      // Only togglable while tools are explicitly disabled (the UI gates it too).
      set((state) => {
        if (state.toolMode !== 'disabled') return {}
        const patch = { forceWebSearch: on }
        persistPrefs(persistedFrom(state, patch))
        return patch
      })
    },
    setParamValues(modelId, values) {
      const id = modelId.trim()
      if (!id) return
      set((state) => {
        const clean = sanitizeParamValues(values)
        const paramValuesByModel = { ...state.paramValuesByModel }
        if (Object.keys(clean).length > 0) {
          paramValuesByModel[id] = clean
        } else {
          delete paramValuesByModel[id]
        }
        persistPrefs(persistedFrom(state, { paramValuesByModel }))
        return { paramValuesByModel }
      })
    },
    setDraft(scope, value) {
      const key = scope.trim()
      if (!key) return
      set((state) => {
        const draftsByScope = { ...state.draftsByScope }
        if (value.length > 0) {
          draftsByScope[key] = value.slice(0, MAX_DRAFT_LEN)
        } else {
          delete draftsByScope[key]
        }
        persistPrefs(persistedFrom(state, { draftsByScope }))
        return { draftsByScope }
      })
    },
    clearDraft(scope) {
      const key = scope.trim()
      if (!key) return
      set((state) => {
        if (state.draftsByScope[key] === undefined) return {}
        const draftsByScope = { ...state.draftsByScope }
        delete draftsByScope[key]
        persistPrefs(persistedFrom(state, { draftsByScope }))
        return { draftsByScope }
      })
    },
  }
})

/** Reset the live per-turn policy when the user explicitly starts a new chat. */
export function resetComposerToolModeToDefault(): void {
  const prefs = useComposerPrefs.getState()
  prefs.setToolMode(prefs.defaultToolMode)
}
