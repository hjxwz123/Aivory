/**
 * AuthGate — runs the auth hydration on mount and decides whether the user
 * sees the app or the login screen. Public routes (/welcome, /login,
 * /register, /forgot-password) are always rendered. Everything else requires
 * an authenticated user — unauthenticated visitors get redirected to /login
 * with the original location preserved in the `from` state.
 */
import { useEffect, useRef, type ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { useProjects } from '@/store/projects'
import { useModels } from '@/store/models'
import { useSettings } from '@/store/settings'
import { useComposerPrefs } from '@/store/composer-prefs'
import { useAccent } from '@/store/accent'
import { useWorkspaces } from '@/store/workspaces'
import { useLanguage, detectBrowserLanguage, toSupportedLanguage } from '@/store/language'
import { useTheme } from '@/store/theme'
import { persistUserSettings } from '@/lib/user-settings'
import { ACCENT_PRESETS, type AccentPref, type ThemePref } from '@/types/settings'

const PUBLIC_PATHS = ['/welcome', '/login', '/register', '/forgot-password', '/share', '/setup', '/privacy', '/terms']

function isPublic(path: string): boolean {
  return PUBLIC_PATHS.some((p) => path === p || path.startsWith(p + '/'))
}

export function AuthGate({ children }: { children: ReactNode }) {
  const status = useAuth((s) => s.status)
  const hydrate = useAuth((s) => s.hydrate)
  const user = useAuth((s) => s.user)
  const needsSetup = useAuth((s) => s.needsSetup)
  const setupProbed = useAuth((s) => s.setupProbed)
  const loadConversations = useConversations((s) => s.load)
  const loadProjects = useProjects((s) => s.load)
  const loadModels = useModels((s) => s.load)
  const syncUserSettings = useSettings((s) => s.syncUserSettings)
  const location = useLocation()
  const hydratedDataForUser = useRef<string | null>(null)

  useEffect(() => {
    void hydrate()
  }, [hydrate])

  // Keep local UI preferences in sync with the authenticated profile.
  useEffect(() => {
    if (status !== 'authenticated' || !user?.settings) return
    syncUserSettings(user.settings)
    const language = toSupportedLanguage(user.settings.language)
    if (language) {
      useLanguage.getState().applyLang(language)
    } else {
      const detected = detectBrowserLanguage()
      if (detected) {
        useLanguage.getState().applyLang(detected)
        void persistUserSettings({ language: detected }).catch(() => {})
      }
    }
    const theme = user.settings.theme
    if (theme === 'light' || theme === 'dark' || theme === 'system') {
      useTheme.getState().applyPref(theme as ThemePref)
    }
    const accent = user.settings.accent_color
    if (typeof accent === 'string' && (ACCENT_PRESETS as readonly string[]).includes(accent)) {
      useAccent.getState().applyAccent(accent as AccentPref)
    }
    // §personalization: mirror the "disable tools by default" preference so the
    // composer + new-chat re-arm can read it synchronously. Mirror-only here (runs
    // on every settings change) — the live noTools seed is done once per login below.
    // Opt-out semantics: absent counts as ON (new accounts and everyone who never
    // touched the toggle default to tools-disabled); only an explicit `false`
    // — the user flipping it off in Settings — keeps tools armed by default.
    useComposerPrefs.getState().setDefaultNoTools(user.settings.disable_tools_default !== false)
  }, [status, user?.settings, syncUserSettings])

  // Once authenticated, hydrate the per-user data caches. This is keyed by user
  // id so a refresh that returns an equivalent user object cannot fan out into
  // repeated conversations/projects/models requests.
  useEffect(() => {
    const userId = user?.id ?? null
    if (status !== 'authenticated' || !userId) {
      hydratedDataForUser.current = null
      return
    }
    if (hydratedDataForUser.current === userId) return
    hydratedDataForUser.current = userId
    // §personalization: seed the composer's no-tools toggle from the user's
    // "disable tools by default" preference, once per login. Read imperatively
    // (this effect is keyed by user id, not settings) so a later settings change
    // can't re-arm mid-session. Only arms (never un-arms), so it can't clobber a
    // session where the user kept tools on; new-chat re-arms it (sidebar).
    // Opt-out semantics (same as the mirror above): absent counts as ON.
    if (useAuth.getState().user?.settings?.disable_tools_default !== false) {
      useComposerPrefs.getState().setNoTools(true)
    }
    void useWorkspaces
      .getState()
      .load()
      .then(() => {
        void loadConversations()
        void loadProjects()
      })
    void loadModels()
  }, [status, user?.id, loadConversations, loadProjects, loadModels])

  // Loading shimmer (auth check + initial paint) — reused while the first-run
  // probe is still pending so we never route on a not-yet-known needsSetup.
  const shimmer = (
    <div className="min-h-svh w-full flex items-center justify-center text-[var(--color-fg-subtle)] text-sm">
      <span className="inline-block size-4 rounded-full border-2 border-[var(--color-fg-faint)] border-r-transparent animate-[spin_900ms_linear_infinite]" />
    </div>
  )
  if (status === 'idle' || status === 'authenticating') {
    if (isPublic(location.pathname)) return <>{children}</>
    return shimmer
  }
  // First-run probe not resolved yet — deciding setup-vs-login on the default
  // (needsSetup=false) is exactly what flickered a fresh deploy /setup → /login.
  // Public pages render immediately; protected paths wait for the probe.
  if (!setupProbed) {
    if (isPublic(location.pathname)) return <>{children}</>
    return shimmer
  }

  // First-run: a deployment with no users routes everything to the setup screen
  // (create the first admin); once it's done, /setup bounces back out.
  if (needsSetup && location.pathname !== '/setup') {
    return <Navigate to="/setup" replace />
  }
  if (!needsSetup && location.pathname === '/setup') {
    return <Navigate to={user ? '/' : '/login'} replace />
  }

  if (!user) {
    if (isPublic(location.pathname)) return <>{children}</>
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  }

  // Authenticated user trying to access auth pages → redirect home.
  if (user && (location.pathname === '/login' || location.pathname === '/register')) {
    return <Navigate to="/" replace />
  }

  return <>{children}</>
}
