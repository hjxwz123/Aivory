import { create } from 'zustand'
import type { ThemePref } from '@/types/settings'

const STORAGE_KEY = 'aurelia.theme'

function getStored(): ThemePref {
  if (typeof window === 'undefined') return 'system'
  const v = localStorage.getItem(STORAGE_KEY)
  if (v === 'light' || v === 'dark' || v === 'system') return v
  return 'system'
}

function resolveTheme(pref: ThemePref): 'light' | 'dark' {
  if (pref === 'system') {
    if (typeof window === 'undefined') return 'light'
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  }
  return pref
}

function applyTheme(resolved: 'light' | 'dark') {
  document.documentElement.dataset.theme = resolved
  document.documentElement.classList.toggle('dark', resolved === 'dark')
}

interface ThemeStore {
  pref: ThemePref
  resolved: 'light' | 'dark'
  setPref: (pref: ThemePref) => void
  /** Subscribe to system theme changes when pref === 'system'. */
  syncSystem: () => () => void
}

export const useTheme = create<ThemeStore>((set, get) => {
  const initialPref = getStored()
  const initialResolved = resolveTheme(initialPref)
  if (typeof document !== 'undefined') applyTheme(initialResolved)

  return {
    pref: initialPref,
    resolved: initialResolved,
    setPref(pref) {
      localStorage.setItem(STORAGE_KEY, pref)
      const resolved = resolveTheme(pref)
      applyTheme(resolved)
      set({ pref, resolved })
    },
    syncSystem() {
      if (typeof window === 'undefined') return () => {}
      const mq = window.matchMedia('(prefers-color-scheme: dark)')
      const handler = () => {
        if (get().pref === 'system') {
          const resolved = mq.matches ? 'dark' : 'light'
          applyTheme(resolved)
          set({ resolved })
        }
      }
      mq.addEventListener('change', handler)
      return () => mq.removeEventListener('change', handler)
    },
  }
})
