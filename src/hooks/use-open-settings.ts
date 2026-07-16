import { useCallback } from 'react'
import { useSettingsModal } from '@/store/settings-modal'
import { useUI } from '@/store/ui'

/**
 * useOpenSettings returns a function that opens the settings DIALOG. It's pure
 * UI state (no route change, § 设置-去路由化) — the page behind stays exactly
 * where it was and closing simply hides the dialog. Every entry point (⌘, /
 * sidebar user menu / command menu / the /settings deep-link redirect) goes
 * through this. The mobile nav drawer is closed first so the dialog never
 * reveals a stale open drawer underneath when dismissed.
 */
export function useOpenSettings() {
  return useCallback((tab: string = 'account') => {
    useUI.getState().setNavOpen(false)
    useSettingsModal.getState().openSettings(tab)
  }, [])
}
