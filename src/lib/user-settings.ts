import { authApi } from '@/api'
import { useAuth } from '@/store/auth'

/**
 * Persists account-level preferences when a user is signed in. Local stores still
 * update immediately for logged-out use and first-paint caches; this helper only
 * mirrors the same choice to users.settings and refreshes the auth user payload.
 */
export async function persistUserSettings(patch: Record<string, unknown>): Promise<Record<string, unknown> | null> {
  const { user, status, setUser } = useAuth.getState()
  if (status !== 'authenticated' || !user) return null

  const updated = await authApi.updateSettings(patch)
  const latest = useAuth.getState().user
  if (latest?.id === user.id) {
    setUser({ ...latest, settings: { ...(latest.settings ?? {}), ...updated } })
  }
  return updated
}
