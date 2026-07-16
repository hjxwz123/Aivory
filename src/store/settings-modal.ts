import { create } from 'zustand'

/**
 * The settings dialog is pure UI state — opening it never navigates, so the
 * URL stays on the page the user is reading (§设置-去路由化). The only
 * route-shaped entry left is the /settings/:tab redirect in App.tsx, kept for
 * old links and the OAuth ?linked callback.
 */
export const SETTINGS_TABS = [
  'account',
  'personalization',
  'appearance',
  'models',
  'privacy',
  'shortcuts',
  'about',
] as const

export type SettingsTab = (typeof SETTINGS_TABS)[number]

/** Deep links / callers may hand us any string — fall back to the first tab. */
export function normalizeSettingsTab(tab: string | null | undefined): SettingsTab {
  return (SETTINGS_TABS as readonly string[]).includes(tab ?? '') ? (tab as SettingsTab) : 'account'
}

interface SettingsModalState {
  open: boolean
  tab: SettingsTab
  openSettings: (tab?: string) => void
  setTab: (tab: SettingsTab) => void
  close: () => void
}

export const useSettingsModal = create<SettingsModalState>((set) => ({
  open: false,
  tab: 'account',
  openSettings: (tab) => set({ open: true, tab: normalizeSettingsTab(tab) }),
  setTab: (tab) => set({ tab }),
  close: () => set({ open: false }),
}))
