import { create } from 'zustand'

interface ProjectNavigationPreference {
  collapsed: boolean
  expanded: Record<string, boolean>
}

interface SidebarProjectsState {
  preferences: Record<string, ProjectNavigationPreference>
  setCollapsed: (scope: string, collapsed: boolean) => void
  setExpanded: (scope: string, projectId: string, expanded: boolean) => void
}

const KEY = 'aivory.sidebar-projects'
export const DEFAULT_PROJECT_NAVIGATION: ProjectNavigationPreference = { collapsed: false, expanded: {} }

function load(): SidebarProjectsState['preferences'] {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(KEY) ?? '{}')
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
    return Object.fromEntries(Object.entries(raw).flatMap(([scope, value]) => {
      if (!value || typeof value !== 'object' || Array.isArray(value)) return []
      const preference = value as Record<string, unknown>
      const expanded = preference.expanded
      return [[scope, {
        collapsed: preference.collapsed === true,
        expanded: expanded && typeof expanded === 'object' && !Array.isArray(expanded)
          ? Object.fromEntries(Object.entries(expanded).filter(([, flag]) => typeof flag === 'boolean'))
          : {},
      }]]
    }))
  } catch {
    return {}
  }
}

function persist(preferences: SidebarProjectsState['preferences']) {
  try {
    localStorage.setItem(KEY, JSON.stringify(preferences))
  } catch {
    // Storage may be unavailable; toggles still work for the current session.
  }
}

// These are local navigation preferences, scoped to the account and workspace.
// Both the desktop sidebar and mobile drawer subscribe to the same state.
export const useSidebarProjects = create<SidebarProjectsState>((set) => ({
  preferences: load(),
  setCollapsed(scope, collapsed) {
    set((state) => {
      const current = state.preferences[scope] ?? DEFAULT_PROJECT_NAVIGATION
      const preferences = { ...state.preferences, [scope]: { ...current, collapsed } }
      persist(preferences)
      return { preferences }
    })
  },
  setExpanded(scope, projectId, expanded) {
    set((state) => {
      const current = state.preferences[scope] ?? DEFAULT_PROJECT_NAVIGATION
      const preferences = {
        ...state.preferences,
        [scope]: { ...current, expanded: { ...current.expanded, [projectId]: expanded } },
      }
      persist(preferences)
      return { preferences }
    })
  },
}))
