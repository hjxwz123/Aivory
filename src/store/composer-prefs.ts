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
  // Deployment default mirrored from the administrator's `tool_mode_default`.
  // Users cannot replace this globally; a conversation may override it below.
  defaultToolMode: ToolMode
  /** Per-conversation tool policy overrides. Missing means inherit the current
   * administrator default. Draft scopes such as `new-chat` are cleared whenever
   * the user explicitly starts another conversation. */
  toolModesByScope: Record<string, ToolMode>
  /** Forced non-tool web search is meaningful only while the same scope resolves
   * to disabled mode. Keeping it scoped prevents one chat from arming another. */
  forceWebSearchByScope: Record<string, true>
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
  setToolMode: (scope: string, toolMode: ToolMode) => void
  clearToolMode: (scope: string) => void
  moveToolModeScope: (fromScope: string, toScope: string) => void
  // Update the mirror of the administrator-controlled default tool policy.
  setDefaultToolMode: (toolMode: ToolMode) => void
  setForceWebSearch: (scope: string, on: boolean) => void
  setSelectedToolIds: (modelId: string, ids: string[] | undefined) => void
  /** Drop explicit tool subsets when the active workspace changes. */
  clearSelectedToolIds: () => void
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
  defaultToolMode: 'auto',
  toolModesByScope: {},
  forceWebSearchByScope: {},
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

function sanitizeToolModesByScope(raw: unknown): Record<string, ToolMode> {
  if (!isRecord(raw)) return {}
  const out: Record<string, ToolMode> = {}
  for (const [scope, value] of Object.entries(raw)) {
    if (!scope || scope.length > 180 || !isToolMode(value)) continue
    out[scope] = value
    if (Object.keys(out).length >= 512) break
  }
  return out
}

function sanitizeForceWebSearchByScope(raw: unknown): Record<string, true> {
  if (!isRecord(raw)) return {}
  const out: Record<string, true> = {}
  for (const [scope, value] of Object.entries(raw)) {
    if (!scope || scope.length > 180 || value !== true) continue
    out[scope] = true
    if (Object.keys(out).length >= 512) break
  }
  return out
}

/** Sanitizes localStorage without reviving the retired user-global tool mode. */
export function parsePersistedComposerPrefs(parsed: unknown): PersistedComposerPrefs {
  if (!isRecord(parsed)) return DEFAULT_PREFS
  // `toolMode`, `defaultToolMode`, `forceWebSearch`, and the older `noTools`
  // values were account-wide user preferences. Ignore them deliberately: only
  // /me may hydrate the administrator default, and only the new scoped maps may
  // retain a user's conversation-specific choice.
  return {
    mode: isMode(parsed.mode) ? parsed.mode : DEFAULT_PREFS.mode,
    verify: parsed.verify === true,
    optimizeImagePrompt: parsed.optimizeImagePrompt !== false,
    defaultToolMode: DEFAULT_PREFS.defaultToolMode,
    toolModesByScope: sanitizeToolModesByScope(parsed.toolModesByScope),
    forceWebSearchByScope: sanitizeForceWebSearchByScope(parsed.forceWebSearchByScope),
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
    const parsed = parsePersistedComposerPrefs(JSON.parse(raw) as unknown)
    // Rewrite once on load so retired account-wide fields disappear from the
    // browser storage instead of merely being ignored at runtime.
    persistPrefs(parsed)
    return parsed
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
    defaultToolMode: state.defaultToolMode,
    toolModesByScope: state.toolModesByScope,
    forceWebSearchByScope: state.forceWebSearchByScope,
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
      commit({ mode })
    },
    setVerify(verify) {
      commit({ verify })
    },
    setOptimizeImagePrompt(optimizeImagePrompt) {
      commit({ optimizeImagePrompt })
    },
    setToolMode(scope, toolMode) {
      const key = scope.trim()
      if (!key) return
      set((state) => {
        const toolModesByScope = { ...state.toolModesByScope }
        const forceWebSearchByScope = { ...state.forceWebSearchByScope }
        if (toolMode === state.defaultToolMode) delete toolModesByScope[key]
        else toolModesByScope[key] = toolMode
        if (toolMode !== 'disabled') delete forceWebSearchByScope[key]
        const patch: Partial<PersistedComposerPrefs> = {
          toolModesByScope,
          forceWebSearchByScope,
        }
        if (toolMode !== 'enabled') patch.mode = 'default'
        persistPrefs(persistedFrom(state, patch))
        return patch
      })
    },
    clearToolMode(scope) {
      const key = scope.trim()
      if (!key) return
      set((state) => {
        if (!(key in state.toolModesByScope) && !(key in state.forceWebSearchByScope)) return {}
        const toolModesByScope = { ...state.toolModesByScope }
        const forceWebSearchByScope = { ...state.forceWebSearchByScope }
        delete toolModesByScope[key]
        delete forceWebSearchByScope[key]
        const patch = { toolModesByScope, forceWebSearchByScope }
        persistPrefs(persistedFrom(state, patch))
        return patch
      })
    },
    moveToolModeScope(fromScope, toScope) {
      const from = fromScope.trim()
      const to = toScope.trim()
      if (!from || !to || from === to) return
      set((state) => {
        const hasMode = from in state.toolModesByScope
        const hasSearch = from in state.forceWebSearchByScope
        if (!hasMode && !hasSearch) return {}
        const toolModesByScope = { ...state.toolModesByScope }
        const forceWebSearchByScope = { ...state.forceWebSearchByScope }
        if (hasMode) toolModesByScope[to] = toolModesByScope[from]
        if (hasSearch) forceWebSearchByScope[to] = true
        delete toolModesByScope[from]
        delete forceWebSearchByScope[from]
        const patch = { toolModesByScope, forceWebSearchByScope }
        persistPrefs(persistedFrom(state, patch))
        return patch
      })
    },
    setDefaultToolMode(toolMode) {
      set((state) => {
        const forceWebSearchByScope = { ...state.forceWebSearchByScope }
        for (const scope of Object.keys(forceWebSearchByScope)) {
          const explicit = state.toolModesByScope[scope]
          if ((explicit ?? toolMode) !== 'disabled') delete forceWebSearchByScope[scope]
        }
        const patch = { defaultToolMode: toolMode, forceWebSearchByScope }
        persistPrefs(persistedFrom(state, patch))
        return patch
      })
    },
    setForceWebSearch(scope, on) {
      const key = scope.trim()
      if (!key) return
      set((state) => {
        const toolMode = state.toolModesByScope[key] ?? state.defaultToolMode
        if (toolMode !== 'disabled') return {}
        const forceWebSearchByScope = { ...state.forceWebSearchByScope }
        if (on) forceWebSearchByScope[key] = true
        else delete forceWebSearchByScope[key]
        const patch = { forceWebSearchByScope }
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
    clearSelectedToolIds() {
      set((state) => {
        if (Object.keys(state.selectedToolIdsByModel).length === 0) return {}
        const patch = { selectedToolIdsByModel: {} }
        persistPrefs(persistedFrom(state, patch))
        return patch
      })
    },
    resetForNewConversation() {
      set((state) => {
        const toolModesByScope = { ...state.toolModesByScope }
        const forceWebSearchByScope = { ...state.forceWebSearchByScope }
        delete toolModesByScope['new-chat']
        delete toolModesByScope['new-draw']
        delete forceWebSearchByScope['new-chat']
        delete forceWebSearchByScope['new-draw']
        const patch: Partial<PersistedComposerPrefs> = {
          toolModesByScope,
          forceWebSearchByScope,
          // A chosen subset is a turn override, not the next conversation's
          // model default. Clear every model so changing models in the new
          // conversation cannot revive a stale selection either.
          selectedToolIdsByModel: {},
        }
        if (state.defaultToolMode !== 'enabled') patch.mode = 'default'
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
