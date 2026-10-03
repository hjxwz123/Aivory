import { authApi } from '@/api'
import { useAuth } from '@/store/auth'
import { advanceUserSettingsRevision } from './user-settings-revision'

/**
 * Persists account-level preferences when a user is signed in. Local stores still
 * update immediately for logged-out use and first-paint caches; this helper only
 * mirrors the same choice to users.settings and refreshes the auth user payload.
 */
export async function persistUserSettings(patch: Record<string, unknown>): Promise<Record<string, unknown> | null> {
  const { user, status, setUser } = useAuth.getState()
  if (status !== 'authenticated' || !user) return null

  const updated = await authApi.updateSettings(patch)
  const current = useAuth.getState()
  const latest = current.user
  if (current.status === 'authenticated' && latest?.id === user.id) {
    const accepted = Object.fromEntries(Object.keys(patch).filter((key) => Object.hasOwn(updated, key)).map((key) => [key, updated[key]]))
    advanceUserSettingsRevision()
    setUser({ ...latest, settings: { ...(latest.settings ?? {}), ...accepted } })
  }
  return updated
}
