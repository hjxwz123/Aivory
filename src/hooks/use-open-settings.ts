import { useCallback } from 'react'
import { useLocation, useNavigate, type Location } from 'react-router-dom'

/** State carried when settings is opened as a modal OVER another page. */
export interface SettingsBackgroundState {
  backgroundLocation?: Location
}

/**
 * useOpenSettings returns a function that opens the settings modal OVER the
 * current page — the page is stashed as `backgroundLocation` so App renders it
 * live behind the (blurred) modal, and closing returns to it. Every settings
 * entry point (⌘, / sidebar / command menu) goes through this so the modal
 * always has a live backdrop.
 *
 * A direct navigation to /settings/* (OAuth ?linked redirect, a fresh load)
 * carries no state, so App falls back to rendering the modal over the app shell
 * — still correct, just without a live page behind.
 */
export function useOpenSettings() {
  const navigate = useNavigate()
  const location = useLocation()
  return useCallback(
    (tab: string = 'account') => {
      const state = location.state as SettingsBackgroundState | null
      // Already inside settings (a menu re-opens it) → keep the ORIGINAL
      // background; otherwise the page we're on becomes the background.
      const background = location.pathname.startsWith('/settings') ? state?.backgroundLocation : location
      navigate(`/settings/${tab}`, background ? { state: { backgroundLocation: background } } : undefined)
    },
    [navigate, location],
  )
}
