import { create } from 'zustand'

export type ComposerMode = 'default' | 'deep-research' | 'canvas'

type ParamValue = string | number | boolean | null
export type ComposerParamValues = Record<string, ParamValue>

interface PersistedComposerPrefs {
  mode: ComposerMode
  verify: boolean
  // §4.13-B: run this turn with NO tool calling. Mutually exclusive with the
  // 'deep-research' mode (research needs tools) — the setters enforce it.
  noTools: boolean
  // Forced non-tool web search — only meaningful while noTools is on; cleared
  // automatically when noTools turns off.
  forceWebSearch: boolean
  paramValuesByModel: Record<string, ComposerParamValues>
  draftsByScope: Record<string, string>
}

interface ComposerPrefsStore extends PersistedComposerPrefs {
  setMode: (mode: ComposerMode) => void
  setVerify: (verify: boolean) => void
  setNoTools: (noTools: boolean) => void
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
  noTools: false,
  forceWebSearch: false,
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

function sanitizeDraftsByScope(raw: unknown): Record<string, string> {
  if (!isRecord(raw)) return {}
  const out: Record<string, string> = {}
  for (const [scope, value] of Object.entries(raw)) {
    if (!scope || typeof value !== 'string' || value.length === 0) continue
    out[scope] = value.slice(0, MAX_DRAFT_LEN)
  }
  return out
}

function loadPrefs(): PersistedComposerPrefs {
  if (typeof window === 'undefined') return DEFAULT_PREFS
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULT_PREFS
    const parsed = JSON.parse(raw) as unknown
    if (!isRecord(parsed)) return DEFAULT_PREFS
    const noTools = parsed.noTools === true
    return {
      mode: isMode(parsed.mode) ? parsed.mode : DEFAULT_PREFS.mode,
      verify: parsed.verify === true,
      noTools,
      // web search can only be on inside a no-tools turn
      forceWebSearch: noTools && parsed.forceWebSearch === true,
      paramValuesByModel: sanitizeParamValuesByModel(parsed.paramValuesByModel),
      draftsByScope: sanitizeDraftsByScope(parsed.draftsByScope),
    }
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
    noTools: state.noTools,
    forceWebSearch: state.forceWebSearch,
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
      // deep-research needs tools → it clears the no-tools feature.
      if (mode === 'deep-research') commit({ mode, noTools: false, forceWebSearch: false })
      else commit({ mode })
    },
    setVerify(verify) {
      commit({ verify })
    },
    setNoTools(noTools) {
      // no-tools ↔ deep-research are mutually exclusive; web search only lives
      // inside a no-tools turn, so turning it off clears the web-search flag.
      if (noTools) commit({ noTools: true, mode: 'default', forceWebSearch: false })
      else commit({ noTools: false, forceWebSearch: false })
    },
    setForceWebSearch(on) {
      // Only togglable while no-tools is on (the UI gates it too).
      set((state) => {
        if (!state.noTools) return {}
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
