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
import { resolveDefaultToolMode } from '@/lib/tool-mode'
import { invalidateAccessState } from '@/lib/access-events'
import { ACCENT_PRESETS, type AccentPref, type ThemePref } from '@/types/settings'
import { apiUrl } from '@/api'
import { oauthStartPath } from '@/lib/oauth'

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
  const authPolicy = useAuth((s) => s.authPolicy)
  const authPolicyLoaded = useAuth((s) => s.authPolicyLoaded)
  const pendingTwoFactor = useAuth((s) => s.pendingTwoFactor)
  const pendingVerification = useAuth((s) => s.pendingVerification)
  const loadConversations = useConversations((s) => s.load)
  const loadProjects = useProjects((s) => s.load)
  const loadModels = useModels((s) => s.load)
  const refreshProfile = useAuth((s) => s.refreshProfile)
  const syncUserSettings = useSettings((s) => s.syncUserSettings)
  const location = useLocation()
  const hydratedDataForUser = useRef<string | null>(null)

  const authQuery = new URLSearchParams(location.search)
  const emailVerificationInProgress =
    location.pathname === '/register' && (authQuery.has('verify_email') || Boolean(pendingVerification))
  const callbackNeedsAttention =
    (location.pathname === '/login' && ['oauth_error', 'twofa'].some((key) => authQuery.has(key))) ||
    emailVerificationInProgress
  const shouldAutoRedirect =
    status === 'unauthenticated' &&
    setupProbed &&
    !needsSetup &&
    authPolicyLoaded &&
    !user &&
    authPolicy.entry_mode === 'auto_redirect' &&
    Boolean(authPolicy.default_provider) &&
    !pendingTwoFactor &&
    !callbackNeedsAttention &&
    !['/setup', '/privacy', '/terms'].some(
      (path) => location.pathname === path || location.pathname.startsWith(path + '/'),
    ) &&
    !location.pathname.startsWith('/share/')

  useEffect(() => {
    void hydrate()
  }, [hydrate])

  useEffect(() => {
    if (!shouldAutoRedirect || !authPolicy.default_provider) return
    // Full-page navigation is required for OAuth. replace() also prevents the
    // Back button from returning to a route that immediately redirects again.
    window.location.replace(apiUrl(oauthStartPath(authPolicy.default_provider.id)))
  }, [authPolicy.default_provider, shouldAutoRedirect])

  // Keep local UI preferences in sync with the authenticated profile.
  useEffect(() => {
    if (status !== 'authenticated' || !user?.settings) {
      return
    }
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
    // Keep the account default available synchronously to the composer and the
    // new-chat action. The resolver also preserves explicit choices from the
    // legacy disable_tools_default boolean; an entirely absent value is auto.
    useComposerPrefs.getState().setDefaultToolMode(resolveDefaultToolMode(user.settings))
  }, [status, user?.settings, syncUserSettings])

  // Once authenticated, hydrate the per-user data caches. This is keyed by user
  // id so a refresh that returns an equivalent user object cannot fan out into
  // repeated conversations/projects/models requests.
  useEffect(() => {
    const userId = user?.id ?? null
    if (status !== 'authenticated' || !user || !userId) {
      hydratedDataForUser.current = null
      return
    }
    const passwordPolicy = user.oauth_initial_password_policy ?? authPolicy.oauth_initial_password_policy
    if (user.has_password === false && passwordPolicy === 'required') {
      hydratedDataForUser.current = null
      return
    }
    if (hydratedDataForUser.current === userId) return
    hydratedDataForUser.current = userId
    // Apply the account default exactly once per login. All three values matter:
    // unlike the former boolean, auto/enabled must also replace a
    // persisted mode left by another account or an earlier session.
    const defaultToolMode = resolveDefaultToolMode(useAuth.getState().user?.settings)
    useComposerPrefs.getState().setDefaultToolMode(defaultToolMode)
    useComposerPrefs.getState().setToolMode(defaultToolMode)
    void useWorkspaces
      .getState()
      .load()
      .then(() => {
        void loadConversations()
        void loadProjects()
      })
    void loadModels()
  }, [
    authPolicy.oauth_initial_password_policy,
    status,
    user,
    user?.has_password,
    user?.id,
    user?.oauth_initial_password_policy,
    loadConversations,
    loadProjects,
    loadModels,
  ])

  // A temporary plan can expire while the tab stays open and no realtime event
  // is emitted. Refresh just after the deadline, then reconcile every cache
  // whose visible choices depend on the user's group.
  useEffect(() => {
    if (status !== 'authenticated' || !user?.id || !user.group_expires_at) return
    const expiresAtMs = user.group_expires_at * 1000
    const schedule = () => {
      const remaining = expiresAtMs - Date.now() + 750
      if (remaining > 2_147_000_000) {
        return window.setTimeout(schedule, 2_147_000_000)
      }
      return window.setTimeout(() => {
        void refreshProfile().then((fresh) => {
          if (!fresh) return
          invalidateAccessState({ kind: 'account' })
          void loadModels()
          void useWorkspaces.getState().load()
        })
      }, Math.max(0, remaining))
    }
    const timer = schedule()
    return () => window.clearTimeout(timer)
  }, [loadModels, refreshProfile, status, user?.group_expires_at, user?.id])

  // Loading shimmer (auth check + initial paint) — reused while the first-run
  // probe is still pending so we never route on a not-yet-known needsSetup.
  const shimmer = (
    <div className="min-h-svh w-full flex items-center justify-center text-[var(--color-fg-subtle)] text-sm">
      <span className="inline-block size-4 rounded-full border-2 border-[var(--color-fg-faint)] border-r-transparent animate-[spin_900ms_linear_infinite]" />
    </div>
  )
  if (status === 'idle' || status === 'authenticating') {
    const policyRoute = ['/welcome', '/login', '/register', '/forgot-password'].includes(location.pathname)
    if (isPublic(location.pathname) && (!policyRoute || authPolicyLoaded)) return <>{children}</>
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

  if (!user && !authPolicyLoaded) {
    return shimmer
  }

  if (shouldAutoRedirect) {
    return shimmer
  }

  if (!user) {
    if (
      (!authPolicy.password_login_enabled || authPolicy.entry_mode !== 'login_page') &&
      ((location.pathname === '/register' && !emailVerificationInProgress) ||
        location.pathname === '/forgot-password')
    ) {
      return <Navigate to="/login" replace />
    }
    if (isPublic(location.pathname)) return <>{children}</>
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  }

  // Authenticated user trying to access auth pages → redirect home.
  if (user && (location.pathname === '/login' || location.pathname === '/register')) {
    return <Navigate to="/" replace />
  }

  return <>{children}</>
}
