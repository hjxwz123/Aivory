/**
 * Accent preset store — sits alongside `useTheme` and toggles a `data-accent`
 * attribute on <html> so tokens.css can swap accent vars at runtime.
 *
 * Models the same shape as theme.ts (zustand + localStorage + applyOnBoot)
 * so the Settings page can read/write it identically. The FOUC <script> in
 * index.html re-applies the stored accent before React boots — that's what
 * stops a one-frame flash of the violet default on every reload.
 */
import { create } from 'zustand'
import { type AccentPref, ACCENT_PRESETS } from '@/types/settings'
import { persistUserSettings } from '@/lib/user-settings'

const STORAGE_KEY = 'aivory.accent'
const DEFAULT_ACCENT: AccentPref = 'violet'

function getStored(): AccentPref {
  if (typeof window === 'undefined') return DEFAULT_ACCENT
  const v = localStorage.getItem(STORAGE_KEY)
  if (v && (ACCENT_PRESETS as readonly string[]).includes(v)) return v as AccentPref
  return DEFAULT_ACCENT
}

function applyAccent(accent: AccentPref) {
  document.documentElement.dataset.accent = accent
}

interface AccentStore {
  accent: AccentPref
  applyAccent: (accent: AccentPref) => void
  setAccent: (accent: AccentPref) => void
}

export const useAccent = create<AccentStore>((set) => {
  const initial = getStored()
  if (typeof document !== 'undefined') applyAccent(initial)
  return {
    accent: initial,
    applyAccent(accent) {
      localStorage.setItem(STORAGE_KEY, accent)
      applyAccent(accent)
      set({ accent })
    },
    setAccent(accent) {
      localStorage.setItem(STORAGE_KEY, accent)
      applyAccent(accent)
      set({ accent })
      void persistUserSettings({ accent_color: accent }).catch(() => {})
    },
  }
})
