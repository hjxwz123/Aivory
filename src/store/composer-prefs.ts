import { create } from 'zustand'

export type ComposerMode = 'default' | 'deep-research' | 'canvas'

type ParamValue = string | number | boolean | null
export type ComposerParamValues = Record<string, ParamValue>

interface PersistedComposerPrefs {
  mode: ComposerMode
  verify: boolean
  paramValuesByModel: Record<string, ComposerParamValues>
}

interface ComposerPrefsStore extends PersistedComposerPrefs {
  setMode: (mode: ComposerMode) => void
  setVerify: (verify: boolean) => void
  setParamValues: (modelId: string, values: Record<string, unknown>) => void
}

const STORAGE_KEY = 'aurelia.composer-prefs.v1'

const DEFAULT_PREFS: PersistedComposerPrefs = {
  mode: 'default',
  verify: false,
  paramValuesByModel: {},
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

function loadPrefs(): PersistedComposerPrefs {
  if (typeof window === 'undefined') return DEFAULT_PREFS
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return DEFAULT_PREFS
    const parsed = JSON.parse(raw) as unknown
    if (!isRecord(parsed)) return DEFAULT_PREFS
    return {
      mode: isMode(parsed.mode) ? parsed.mode : DEFAULT_PREFS.mode,
      verify: parsed.verify === true,
      paramValuesByModel: sanitizeParamValuesByModel(parsed.paramValuesByModel),
    }
  } catch {
    return DEFAULT_PREFS
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
  return {
    ...initial,
    setMode(mode) {
      set((state) => {
        const next: PersistedComposerPrefs = {
          mode,
          verify: state.verify,
          paramValuesByModel: state.paramValuesByModel,
        }
        persistPrefs(next)
        return { mode }
      })
    },
    setVerify(verify) {
      set((state) => {
        const next: PersistedComposerPrefs = {
          mode: state.mode,
          verify,
          paramValuesByModel: state.paramValuesByModel,
        }
        persistPrefs(next)
        return { verify }
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
        const next: PersistedComposerPrefs = {
          mode: state.mode,
          verify: state.verify,
          paramValuesByModel,
        }
        persistPrefs(next)
        return { paramValuesByModel }
      })
    },
  }
})
