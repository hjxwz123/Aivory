import { create } from 'zustand'
import { isToolMode, type ToolMode } from '@/lib/tool-mode'

export type ComposerMode = 'default' | 'deep-research' | 'canvas'

type ParamValue = string | number | boolean | null
export type ComposerParamValues = Record<string, ParamValue>

export interface PersistedComposerPrefs {
  mode: ComposerMode
  verify: boolean
  /** Direct image-model turns rewrite prompts by default; users can opt out and
   * send their exact wording to the image provider. */
  optimizeImagePrompt: boolean
  // Per-turn tool policy. Deep Research requires tools, so selecting it forces
  // this to enabled; the setters keep that invariant in persisted state.
  toolMode: ToolMode
  // Forced non-tool web search is only meaningful in disabled mode; switching
  // to auto/enabled clears it automatically.
  forceWebSearch: boolean
  // Account-level default mirrored from `tool_mode_default`. New conversations
  // reset the live toolMode to this complete value (including auto/enabled), so
  // a prior conversation's override cannot leak into the next one.
  defaultToolMode: ToolMode
  /** Per-model explicit tool subsets. A missing model key means the model's
   * current defaults; a present empty array means the user selected none. */
  selectedToolIdsByModel: Record<string, string[]>
  paramValuesByModel: Record<string, ComposerParamValues>
  draftsByScope: Record<string, string>
}

interface ComposerPrefsStore extends PersistedComposerPrefs {
  setMode: (mode: ComposerMode) => void
  setVerify: (verify: boolean) => void
  setOptimizeImagePrompt: (enabled: boolean) => void
  setToolMode: (toolMode: ToolMode) => void
  // Update the mirror of the server-side default tool policy.
  setDefaultToolMode: (toolMode: ToolMode) => void
  setForceWebSearch: (on: boolean) => void
  setSelectedToolIds: (modelId: string, ids: string[] | undefined) => void
  /** Restore the account/model defaults before starting an unrelated chat. */
  resetForNewConversation: () => void
  setParamValues: (modelId: string, values: Record<string, unknown>) => void
  setDraft: (scope: string, value: string) => void
  clearDraft: (scope: string) => void
}

const STORAGE_KEY = 'aivory.composer-prefs.v1'
const MAX_DRAFT_LEN = 12_000

const DEFAULT_PREFS: PersistedComposerPrefs = {
  mode: 'default',
  verify: false,
  optimizeImagePrompt: true,
  toolMode: 'auto',
  forceWebSearch: false,
  defaultToolMode: 'auto',
  selectedToolIdsByModel: {},
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

function sanitizeToolIds(raw: unknown): string[] | null {
  if (!Array.isArray(raw)) return null
  const out: string[] = []
  const seen = new Set<string>()
  for (const value of raw) {
    if (typeof value !== 'string') continue
    const id = value.trim()
    if (!id || id.length > 160 || seen.has(id)) continue
    seen.add(id)
    out.push(id)
    if (out.length >= 256) break
  }
  return out
}

function sanitizeSelectedToolIdsByModel(raw: unknown): Record<string, string[]> {
  if (!isRecord(raw)) return {}
  const out: Record<string, string[]> = {}
  for (const [modelId, values] of Object.entries(raw)) {
    if (!modelId || modelId.length > 160) continue
    const ids = sanitizeToolIds(values)
    if (ids !== null) out[modelId] = ids
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
  const toolMode =
    parsed.toolMode === 'official'
      ? 'enabled'
      : isToolMode(parsed.toolMode)
        ? parsed.toolMode
        : DEFAULT_PREFS.toolMode
  const defaultToolMode =
    parsed.defaultToolMode === 'official'
      ? 'enabled'
      : isToolMode(parsed.defaultToolMode)
        ? parsed.defaultToolMode
        : DEFAULT_PREFS.defaultToolMode
  return {
    mode: isMode(parsed.mode) ? parsed.mode : DEFAULT_PREFS.mode,
    verify: parsed.verify === true,
    optimizeImagePrompt: parsed.optimizeImagePrompt !== false,
    toolMode,
    // forced search only exists inside an explicitly disabled-tools turn
    forceWebSearch: toolMode === 'disabled' && parsed.forceWebSearch === true,
    defaultToolMode,
    selectedToolIdsByModel: sanitizeSelectedToolIdsByModel(parsed.selectedToolIdsByModel),
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
    optimizeImagePrompt: state.optimizeImagePrompt,
    toolMode: state.toolMode,
    forceWebSearch: state.forceWebSearch,
    defaultToolMode: state.defaultToolMode,
    selectedToolIdsByModel: state.selectedToolIdsByModel,
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
    setOptimizeImagePrompt(optimizeImagePrompt) {
      commit({ optimizeImagePrompt })
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
    setForceWebSearch(on) {
      // Only togglable while tools are explicitly disabled (the UI gates it too).
      set((state) => {
        if (state.toolMode !== 'disabled') return {}
        const patch = { forceWebSearch: on }
        persistPrefs(persistedFrom(state, patch))
        return patch
      })
    },
    setSelectedToolIds(modelId, ids) {
      const id = modelId.trim()
      if (!id) return
      set((state) => {
        const selectedToolIdsByModel = { ...state.selectedToolIdsByModel }
        if (ids === undefined) {
          delete selectedToolIdsByModel[id]
        } else {
          selectedToolIdsByModel[id] = sanitizeToolIds(ids) ?? []
        }
        persistPrefs(persistedFrom(state, { selectedToolIdsByModel }))
        return { selectedToolIdsByModel }
      })
    },
    resetForNewConversation() {
      set((state) => {
        const toolMode = state.defaultToolMode
        const patch: Partial<PersistedComposerPrefs> = {
          toolMode,
          forceWebSearch: false,
          // A chosen subset is a turn override, not the next conversation's
          // model default. Clear every model so changing models in the new
          // conversation cannot revive a stale selection either.
          selectedToolIdsByModel: {},
        }
        if (toolMode !== 'enabled') patch.mode = 'default'
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

/** Restore all composer tool preferences when the user explicitly starts a new chat. */
export function resetComposerForNewConversation(): void {
  useComposerPrefs.getState().resetForNewConversation()
}
