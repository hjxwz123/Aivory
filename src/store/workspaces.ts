/**
 * Workspaces store (§workspaces) — the user's collaborative spaces plus which
 * one is ACTIVE. Personal space = activeId null. The active choice persists in
 * localStorage (`aurelia.workspace`) so a reload / next visit reopens the same
 * space; it is validated against the fetched list (a kicked member falls back
 * to personal silently).
 *
 * Switching spaces reloads the per-space data stores (conversations, projects)
 * — AuthGate hydrates them only once per session, so the switch must do it.
 */
import { create } from 'zustand'
import { workspacesApi } from '@/api'
import type { ApiWorkspace } from '@/api/types'

const ACTIVE_KEY = 'aurelia.workspace'

function readStoredActive(): string | null {
  try {
    return localStorage.getItem(ACTIVE_KEY) || null
  } catch {
    return null
  }
}

interface WorkspacesState {
  workspaces: ApiWorkspace[]
  /** Active workspace id; null = personal space. */
  activeId: string | null
  loaded: boolean
  /** True while switchTo() is reloading the space-scoped stores — drives the
   *  switch transition/loading state in the sidebar and content area. */
  switching: boolean
  load: () => Promise<void>
  /** Switch space and reload the space-scoped stores. null = personal. */
  switchTo: (id: string | null) => Promise<void>
  create: (name: string) => Promise<ApiWorkspace>
  remove: (id: string) => Promise<void>
  leave: (id: string) => Promise<void>
  active: () => ApiWorkspace | undefined
}

// Reload every space-scoped cache. Imported lazily to dodge an import cycle
// (conversations store ← workspaces store ← conversations store).
async function reloadSpaceData() {
  const [{ useConversations }, { useProjects }] = await Promise.all([
    import('./conversations'),
    import('./projects'),
  ])
  await Promise.all([useConversations.getState().load(), useProjects.getState().load()])
}

export const useWorkspaces = create<WorkspacesState>((set, get) => ({
  workspaces: [],
  activeId: readStoredActive(),
  loaded: false,
  switching: false,

  async load() {
    try {
      const { workspaces } = await workspacesApi.list()
      const activeId = get().activeId
      // A stale persisted id (kicked / deleted space) falls back to personal.
      const valid = activeId != null && workspaces.some((w) => w.id === activeId)
      if (activeId != null && !valid) {
        try {
          localStorage.removeItem(ACTIVE_KEY)
        } catch {
          /* ignore */
        }
      }
      set({ workspaces, loaded: true, activeId: valid ? activeId : null })
    } catch {
      set({ loaded: true })
    }
  },

  async switchTo(id) {
    if (id === get().activeId) return
    // activeId flips synchronously (instant switch, no delay) — `switching`
    // drives the loading transition while the space-scoped data catches up.
    set({ activeId: id, switching: true })
    try {
      if (id) localStorage.setItem(ACTIVE_KEY, id)
      else localStorage.removeItem(ACTIVE_KEY)
    } catch {
      /* ignore */
    }
    try {
      await reloadSpaceData()
    } finally {
      set({ switching: false })
    }
  },

  async create(name) {
    const ws = await workspacesApi.create(name)
    set((s) => ({ workspaces: [...s.workspaces, ws] }))
    return ws
  },

  async remove(id) {
    await workspacesApi.remove(id)
    set((s) => ({ workspaces: s.workspaces.filter((w) => w.id !== id) }))
    if (get().activeId === id) await get().switchTo(null)
  },

  async leave(id) {
    await workspacesApi.leave(id)
    set((s) => ({ workspaces: s.workspaces.filter((w) => w.id !== id) }))
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