import { create } from 'zustand'
import type { AppearanceSettings, ModelSettings, PrivacySettings } from '@/types/settings'

interface SettingsState {
  appearance: AppearanceSettings
  models: ModelSettings
  privacy: PrivacySettings
  sidebarCollapsed: boolean
  setAppearance: (patch: Partial<AppearanceSettings>) => void
  setModels: (patch: Partial<ModelSettings>) => void
  setPrivacy: (patch: Partial<PrivacySettings>) => void
  setSidebarCollapsed: (v: boolean) => void
  toggleSidebar: () => void
}

const KEY = 'aurelia.settings'

function load(): Partial<SettingsState> {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return {}
    return JSON.parse(raw) as Partial<SettingsState>
  } catch {
    return {}
  }
}

function persist(s: Pick<SettingsState, 'appearance' | 'models' | 'privacy' | 'sidebarCollapsed'>) {
  try {
    localStorage.setItem(
      KEY,
      JSON.stringify({
        appearance: s.appearance,
        models: s.models,
        privacy: s.privacy,
        sidebarCollapsed: s.sidebarCollapsed,
      }),
    )
  } catch {
    /* noop */
  }
}

const initial = load()

export const useSettings = create<SettingsState>((set) => ({
  appearance: {
    theme: 'system',
    density: 'comfortable',
    fontSize: 'md',
    ...(initial.appearance ?? {}),
  },
  models: {
    defaultModelId: '',
    customInstructions: '',
    responseLength: 'balanced',
    ...(initial.models ?? {}),
  },
  privacy: {
    trainingOptOut: false,
    retainHistory: true,
    memoriesEnabled: true,
    ...(initial.privacy ?? {}),
  },
  sidebarCollapsed: initial.sidebarCollapsed ?? false,
  setAppearance(patch) {
    set((s) => {
      const next = { ...s, appearance: { ...s.appearance, ...patch } }
      persist(next)
      return next
    })
  },
  setModels(patch) {
    set((s) => {
      const next = { ...s, models: { ...s.models, ...patch } }
      persist(next)
      return next
    })
  },
  setPrivacy(patch) {
    set((s) => {
      const next = { ...s, privacy: { ...s.privacy, ...patch } }
      persist(next)
      return next
    })
  },
  setSidebarCollapsed(v) {
    set((s) => {
      const next = { ...s, sidebarCollapsed: v }
      persist(next)
      return next
    })
  },
  toggleSidebar() {
    set((s) => {
      const next = { ...s, sidebarCollapsed: !s.sidebarCollapsed }
      persist(next)
      return next
    })
  },
}))
