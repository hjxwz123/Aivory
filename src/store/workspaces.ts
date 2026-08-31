/**
 * Workspaces store (§workspaces) — the user's collaborative spaces plus which
 * one is ACTIVE. Personal space = activeId null. The active choice persists in
 * localStorage (`aivory.workspace`) so a reload / next visit reopens the same
 * space; it is validated against the fetched list (a kicked member falls back
 * to personal silently).
 *
 * Switching spaces reloads the per-space data stores (conversations, projects)
 * — AuthGate hydrates them only once per session, so the switch must do it.
 */
import { create } from 'zustand'
import { workspacesApi } from '@/api'
import type { ApiWorkspace, ApiWorkspacePolicy } from '@/api/types'
import { useComposerPrefs } from '@/store/composer-prefs'

const ACTIVE_KEY = 'aivory.workspace'

// Monotonic switch sequence: overlapping switchTo() calls must not let an
// EARLIER switch's finally clear `switching` while a newer switch's loads are
// still in flight — consumers (ChatThread's re-hydrate) rely on "switching
// flips false only after the newest space's list landed".
let switchSeq = 0
let loadSeq = 0
let policySeq = 0
const policyRequestSeq = new Map<string, number>()

/**
 * Advance the authoritative request token for one workspace. Direct policy
 * writes and membership removal use the same token source as GETs so an older
 * response can never restore policy state that has already been replaced or
 * deleted.
 */
function bumpPolicyRequest(workspaceID: string): number {
  const token = ++policySeq
  policyRequestSeq.set(workspaceID, token)
  return token
}

function readStoredActive(): string | null {
  try {
    return localStorage.getItem(ACTIVE_KEY) || null
  } catch {
    return null
  }
}

interface WorkspacesState {
  workspaces: ApiWorkspace[]
  /** Effective workspace-wide capability policies keyed by workspace id. */
  policies: Record<string, ApiWorkspacePolicy>
  policyLoading: Record<string, boolean>
  policyErrors: Record<string, string | null>
  /** Active workspace id; null = personal space. */
  activeId: string | null
  loaded: boolean
  /** True while switchTo() is reloading the space-scoped stores — drives the
   *  switch transition/loading state in the sidebar and content area. */
  switching: boolean
  load: () => Promise<void>
  loadPolicy: (id: string) => Promise<ApiWorkspacePolicy | null>
  setPolicy: (id: string, policy: ApiWorkspacePolicy) => void
  /** Switch space and reload the space-scoped stores. null = personal. */
  switchTo: (id: string | null) => Promise<void>
  create: (name: string) => Promise<ApiWorkspace>
  remove: (id: string) => Promise<void>
  leave: (id: string) => Promise<void>
  active: () => ApiWorkspace | undefined
}

// Reload every space-scoped cache. Imported lazily to dodge an import cycle
// (conversations store ← workspaces store ← conversations store). The model
// catalog is workspace-scoped too (§workspace RBAC policy filter).
async function reloadSpaceData() {
  const [{ useConversations }, { useProjects }, { useModels }] = await Promise.all([
    import('./conversations'),
    import('./projects'),
    import('./models'),
  ])
  await Promise.all([
    useConversations.getState().load(),
    useProjects.getState().load(),
    useModels.getState().load(),
  ])
}

export const useWorkspaces = create<WorkspacesState>((set, get) => ({
  workspaces: [],
  policies: {},
  policyLoading: {},
  policyErrors: {},
  activeId: readStoredActive(),
  loaded: false,
  switching: false,

  async load() {
    const loadToken = ++loadSeq
    try {
      const { workspaces } = await workspacesApi.list()
      if (loadToken !== loadSeq) return
      const activeId = get().activeId
      // A stale persisted id (kicked / deleted space) falls back to personal.
      const valid = activeId != null && workspaces.some((w) => w.id === activeId)
      set({ workspaces, loaded: true })
      // Refresh the active policy in the background. Policy reads are kept
      // separate from the membership list so a transient policy outage cannot
      // make a valid workspace disappear from the switcher.
      if (activeId != null && valid) void get().loadPolicy(activeId)
      if (activeId != null && !valid) await get().switchTo(null)
    } catch {
      if (loadToken !== loadSeq) return
      set({ loaded: true })
    }
  },

  async loadPolicy(id) {
    const workspaceID = id.trim()
    if (!workspaceID) return null
    const current = get().policies[workspaceID]
    if (get().policyLoading[workspaceID]) return current ?? null
    const requestToken = bumpPolicyRequest(workspaceID)
    set((state) => ({
      policyLoading: { ...state.policyLoading, [workspaceID]: true },
      policyErrors: { ...state.policyErrors, [workspaceID]: null },
    }))
    try {
      const policy = await workspacesApi.getPolicy(workspaceID)
      if (policyRequestSeq.get(workspaceID) !== requestToken) {
        return get().policies[workspaceID] ?? null
      }
      set((state) => ({
        policies: { ...state.policies, [workspaceID]: policy },
        policyLoading: { ...state.policyLoading, [workspaceID]: false },
        policyErrors: { ...state.policyErrors, [workspaceID]: null },
      }))
      // The image-model cache is also policy-scoped. Refresh it after the
      // policy arrives so a newly disabled drawing capability cannot remain in
      // the picker until the next workspace switch.
      if (get().activeId === workspaceID) {
        void import('./models').then(({ useModels }) => useModels.getState().load())
      }
      return policy
    } catch (error) {
      if (policyRequestSeq.get(workspaceID) !== requestToken) {
        return get().policies[workspaceID] ?? null
      }
      set((state) => ({
        // A failed refresh invalidates any cached ceiling. Keeping the stale
        // policy would let capability-gated UI remain available after the
        // server has become unreachable; callers resolve a missing policy
        // fail-closed and can retry through the owning surface.
        policies: (() => {
          const policies = { ...state.policies }
          delete policies[workspaceID]
          return policies
        })(),
        policyLoading: { ...state.policyLoading, [workspaceID]: false },
        policyErrors: {
          ...state.policyErrors,
          [workspaceID]: error instanceof Error ? error.message : 'policy_load_failed',
        },
      }))
      return null
    }
  },

  setPolicy(id, policy) {
    const workspaceID = id.trim()
    if (!workspaceID) return
    bumpPolicyRequest(workspaceID)
    set((state) => ({
      policies: { ...state.policies, [workspaceID]: policy },
      policyLoading: { ...state.policyLoading, [workspaceID]: false },
      policyErrors: { ...state.policyErrors, [workspaceID]: null },
    }))
    if (get().activeId === workspaceID) {
      void import('./models').then(({ useModels }) => useModels.getState().load())
    }
  },

  async switchTo(id) {
    if (id === get().activeId) return
    // Explicit tool subsets are tied to the catalog in the current space.
    // Clear them before flipping `activeId`, so a fast submit during the
    // reload window cannot send a stale `usermcp:*` (or disallowed official)
    // selection from the previous workspace.
    useComposerPrefs.getState().clearSelectedToolIds()
    // activeId flips synchronously (instant switch, no delay) — `switching`
    // drives the loading transition while the space-scoped data catches up.
    const token = ++switchSeq
    set({ activeId: id, switching: true })
    try {
      if (id) localStorage.setItem(ACTIVE_KEY, id)
      else localStorage.removeItem(ACTIVE_KEY)
    } catch {
      /* ignore */
    }
    try {
      await Promise.all([
        reloadSpaceData(),
        id ? get().loadPolicy(id) : Promise.resolve(null),
      ])
    } finally {
      // Only the NEWEST switch settles the flag — a superseded switch's loads
      // resolving early must not signal "data landed" for the newer space.
      if (token === switchSeq) set({ switching: false })
    }
  },

  async create(name) {
    const ws = await workspacesApi.create(name)
    set((s) => ({ workspaces: [...s.workspaces, ws] }))
    return ws
  },

  async remove(id) {
    await workspacesApi.remove(id)
    bumpPolicyRequest(id)
    set((s) => {
      const policies = { ...s.policies }
      const policyLoading = { ...s.policyLoading }
      const policyErrors = { ...s.policyErrors }
      delete policies[id]
      delete policyLoading[id]
      delete policyErrors[id]
      return {
        workspaces: s.workspaces.filter((w) => w.id !== id),
        policies,
        policyLoading,
        policyErrors,
      }
    })
    if (get().activeId === id) await get().switchTo(null)
  },

  async leave(id) {
    await workspacesApi.leave(id)
    bumpPolicyRequest(id)
    set((s) => {
      const policies = { ...s.policies }
      const policyLoading = { ...s.policyLoading }
      const policyErrors = { ...s.policyErrors }
      delete policies[id]
      delete policyLoading[id]
      delete policyErrors[id]
      return {
        workspaces: s.workspaces.filter((w) => w.id !== id),
        policies,
        policyLoading,
        policyErrors,
      }
    })
    if (get().activeId === id) await get().switchTo(null)
  },

  active() {
    const { workspaces, activeId } = get()
    return activeId ? workspaces.find((w) => w.id === activeId) : undefined
  },
}))

/** The active workspace id for API scoping ('' when personal). Non-hook helper
 *  for stores/api call sites. */
export function activeWorkspaceId(): string | undefined {
  return useWorkspaces.getState().activeId ?? undefined
}
