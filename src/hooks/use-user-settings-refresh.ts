import { useEffect } from 'react'
import { useAuth } from '@/store/auth'
import { useSettings } from '@/store/settings'

export function useUserSettingsRefresh(): void {
  const status = useAuth((state) => state.status)
  const userId = useAuth((state) => state.user?.id)
  useEffect(() => {
    if (status !== 'authenticated' || !userId) return
    let lastRefresh = Date.now()
    const refresh = () => {
      if (document.visibilityState === 'hidden' || useSettings.getState().codeBlockWrapPending) return
      if (Date.now() - lastRefresh < 30_000) return
      lastRefresh = Date.now()
      void useAuth.getState().refreshProfile()
    }
    window.addEventListener('focus', refresh)
    document.addEventListener('visibilitychange', refresh)
    return () => {
      window.removeEventListener('focus', refresh)
      document.removeEventListener('visibilitychange', refresh)
    }
  }, [status, userId])
}
