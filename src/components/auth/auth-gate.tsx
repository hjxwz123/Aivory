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
import { isChatShellPath } from '@/lib/app-paths'
import { PanelFallback } from '@/components/ui/panel-fallback'

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
  const hydratedChatDataForUser = useRef<string | null>(null)

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
    // The deployment-wide administrator policy is the only global default.
    // Historical account-level tool settings are intentionally ignored.
    useComposerPrefs.getState().setDefaultToolMode(resolveDefaultToolMode(user.tool_mode_default))
  }, [status, user?.settings, user?.tool_mode_default, syncUserSettings])

  // Hydrate chat-shell caches only on routes that render them. Admin, legal and
  // public pages must not pay for conversations/projects/workspaces/models they
  // never display. The user id guard still prevents equivalent profile refreshes
  // from fanning out into duplicate loads.
  useEffect(() => {
    const userId = user?.id ?? null
    if (status !== 'authenticated' || !user || !userId) {
      hydratedChatDataForUser.current = null
      return
    }
    if (!isChatShellPath(location.pathname)) return
    const passwordPolicy = user.oauth_initial_password_policy ?? authPolicy.oauth_initial_password_policy
    if (user.has_password === false && passwordPolicy === 'required') {
      hydratedChatDataForUser.current = null
      return
    }
    if (hydratedChatDataForUser.current === userId) return
    hydratedChatDataForUser.current = userId
    // Apply the administrator default exactly once per login. Conversation
    // overrides stay scoped by id; only the unrelated new-chat draft is reset.
    const currentUser = useAuth.getState().user
    const defaultToolMode = resolveDefaultToolMode(currentUser?.tool_mode_default)
    useComposerPrefs.getState().setDefaultToolMode(defaultToolMode)
    useComposerPrefs.getState().resetForNewConversation()
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
    location.pathname,
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

  // Reused while authentication and the first-run probe are unresolved so the
  // app never routes using incomplete state.
  const loadingFallback = <PanelFallback scope="screen" />
  if (status === 'idle' || status === 'authenticating') {
    const policyRoute = ['/welcome', '/login', '/register', '/forgot-password'].includes(location.pathname)
    if (isPublic(location.pathname) && (!policyRoute || authPolicyLoaded)) return <>{children}</>
    return loadingFallback
  }
  // First-run probe not resolved yet — deciding setup-vs-login on the default
  // (needsSetup=false) is exactly what flickered a fresh deploy /setup → /login.
  // Public pages render immediately; protected paths wait for the probe.
  if (!setupProbed) {
    if (isPublic(location.pathname)) return <>{children}</>
    return loadingFallback
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
    return loadingFallback
  }

  if (shouldAutoRedirect) {
    return loadingFallback
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
