/**
 * AuthGate — runs the auth hydration on mount and decides whether the user
 * sees the app or the login screen. Public routes (/welcome, /login,
 * /register, /forgot-password) are always rendered. Everything else requires
 * an authenticated user — unauthenticated visitors get redirected to /login
 * with the original location preserved in the `from` state.
 */
import { useEffect, type ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { useAuth } from '@/store/auth'
import { useConversations } from '@/store/conversations'
import { useProjects } from '@/store/projects'
import { useModels } from '@/store/models'
import { useSettings } from '@/store/settings'
import { useAccent } from '@/store/accent'
import { useLanguage, toSupportedLanguage } from '@/store/language'
import { useTheme } from '@/store/theme'
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
  const loadConversations = useConversations((s) => s.load)
  const loadProjects = useProjects((s) => s.load)
  const loadModels = useModels((s) => s.load)
  const syncUserSettings = useSettings((s) => s.syncUserSettings)
  const location = useLocation()

  useEffect(() => {
    void hydrate()
  }, [hydrate])

  // Once authenticated, hydrate the per-user data caches.
  useEffect(() => {
    if (status === 'authenticated') {
      if (user?.settings) {
        syncUserSettings(user.settings)
        const language = toSupportedLanguage(user.settings.language)
        if (language) useLanguage.getState().applyLang(language)
        const theme = user.settings.theme
        if (theme === 'light' || theme === 'dark' || theme === 'system') {
          useTheme.getState().applyPref(theme as ThemePref)
        }
        const accent = user.settings.accent_color
        if (typeof accent === 'string' && (ACCENT_PRESETS as readonly string[]).includes(accent)) {
          useAccent.getState().applyAccent(accent as AccentPref)
        }
      }
      void loadConversations()
      void loadProjects()
      void loadModels()
    }
  }, [status, user?.settings, syncUserSettings, loadConversations, loadProjects, loadModels])

  // Loading state — quick shimmer (auth check + initial paint).
  if (status === 'idle' || status === 'authenticating') {
    if (isPublic(location.pathname)) return <>{children}</>
    return (
      <div className="min-h-svh w-full flex items-center justify-center text-[var(--color-fg-subtle)] text-sm">
        <span className="inline-block size-4 rounded-full border-2 border-[var(--color-fg-faint)] border-r-transparent animate-[spin_900ms_linear_infinite]" />
      </div>
    )
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
