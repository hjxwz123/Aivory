import { create } from 'zustand'
import { ACCENT_PRESETS, normalizeHomeScene, SCENE_ACCENTS, type AccentPref, type HomeScene } from '@/types/settings'
import { persistUserSettings } from '@/lib/user-settings'
import { useAuth } from '@/store/auth'

function normalizeAccent(value: unknown): AccentPref {
  return ACCENT_PRESETS.find(accent => accent === value) ?? 'violet'
}

function readCache(key: string): string | null {
  try { return localStorage.getItem(key) } catch { return null }
}

function applyColors(scene: HomeScene, plainAccent: AccentPref) {
  const accent = scene === 'plain' ? plainAccent : SCENE_ACCENTS[scene]
  if (typeof document !== 'undefined') {
    document.documentElement.dataset.accent = accent
    if (scene === 'plain') delete document.documentElement.dataset.illustration
    else document.documentElement.dataset.illustration = scene
    try {
      localStorage.setItem('aivory.home-scene', scene)
      localStorage.setItem('aivory.plain-accent', plainAccent)
      // The preboot script reads the effective color before React mounts.
      localStorage.setItem('aivory.accent', accent)
    } catch { /* Preferences still apply for this session. */ }
  }
  return { scene, plainAccent, accent }
}

interface AccentStore {
  accent: AccentPref
  plainAccent: AccentPref
  scene: HomeScene
  pendingOwner: string | null
  applyProfile: (userId: string, scene: unknown, accent: unknown) => void
  setAccent: (accent: AccentPref) => void
  setScene: (scene: HomeScene) => Promise<void>
}

export const useAccent = create<AccentStore>((set, get) => ({
  ...applyColors(normalizeHomeScene(readCache('aivory.home-scene')), normalizeAccent(readCache('aivory.plain-accent') ?? readCache('aivory.accent'))),
  pendingOwner: null,
  applyProfile(userId, scene, accent) {
    if (get().pendingOwner === userId) return
    set({ ...applyColors(normalizeHomeScene(scene), normalizeAccent(accent)), pendingOwner: null })
  },
  setAccent(accent) {
    if (get().scene !== 'plain' || get().pendingOwner !== null) return
    set(applyColors('plain', accent))
    void persistUserSettings({ accent_color: accent }).catch(() => {})
  },
  async setScene(scene) {
    const { scene: previous, plainAccent, pendingOwner } = get()
    if (pendingOwner !== null || previous === scene) return
    const owner = useAuth.getState().user?.id ?? ''
    set({ ...applyColors(scene, plainAccent), pendingOwner: owner })
    try {
      const saved = await persistUserSettings({ home_scene: scene })
      if (owner && saved?.home_scene !== scene) throw new Error('Home theme was not saved')
    } catch (error) {
      if (get().pendingOwner === owner && (useAuth.getState().user?.id ?? '') === owner) {
        set(applyColors(previous, plainAccent))
        throw error
      }
    } finally {
      if (get().pendingOwner === owner) set({ pendingOwner: null })
    }
  },
}))
