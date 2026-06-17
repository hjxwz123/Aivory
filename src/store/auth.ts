/**
 * Auth store — drives the signed-in user, hydrates on mount, and exposes the
 * three transitions the UI cares about: login, register, logout.
 *
 * The backend keeps the auth cookie httpOnly; we also hold the short-lived
 * access token in memory so the api client can attach it as a Bearer header.
 * On refresh, the cookie-backed /api/auth/refresh restores both.
 */
import { create } from 'zustand'
import { authApi, ApiError, setAccessToken } from '@/api'
import { setBannedHandler, setRefreshHandler } from '@/api/client'
import type { ApiUser } from '@/api/types'

interface AuthState {
  user: ApiUser | null
  status: 'idle' | 'authenticating' | 'authenticated' | 'unauthenticated'
  error: string | null
  /** True after the account was suspended (live ban or a banned login attempt).
   *  Drives the suspended notice on the login screen. */
  banned: boolean
  signupOpen: boolean
  /** True when the admin requires the arithmetic captcha on the register form. */
  captchaRequired: boolean
  /** True when the deployment has no users yet — the app routes to the first-run
   *  setup screen (create the first admin) instead of login (§ first-run setup). */
  needsSetup: boolean
  /** Set when registration returns verification_required — drives the code UI. */
  pendingVerification: string | null
  /** Set when password login returns totp_required — drives the 2FA code UI. */
  pendingTwoFactor: { ticket: string } | null

  hydrate: () => Promise<void>
  login: (email: string, password: string) => Promise<boolean | '2fa'>
  loginTwoFactor: (code: string) => Promise<boolean>
  register: (
    email: string,
    password: string,
    name: string,
    captcha?: { id: string; answer: string },
  ) => Promise<boolean | 'verify'>
  /** First-run: create the initial admin account, then sign in. */
  setup: (name: string, email: string, password: string) => Promise<boolean>
  logout: () => Promise<void>
  updateProfile: (patch: { name?: string; email?: string }) => Promise<void>
  setUser: (user: ApiUser | null) => void
  setSignupOpen: (open: boolean) => void
  clearPendingVerification: () => void
  clearPendingTwoFactor: () => void
  /** Resume a 2FA challenge from a ticket (e.g. an OAuth redirect). */
  startTwoFactor: (ticket: string) => void
}

export const useAuth = create<AuthState>((set, get) => ({
  user: null,
  status: 'idle',
  error: null,
  banned: false,
  signupOpen: true,
  captchaRequired: false,
  needsSetup: false,
  pendingVerification: null,
  pendingTwoFactor: null,

  setUser(user) {
    set({ user, status: user ? 'authenticated' : 'unauthenticated' })
  },
  setSignupOpen(open) {
    set({ signupOpen: open })
  },
  clearPendingVerification() {
    set({ pendingVerification: null })
  },
  clearPendingTwoFactor() {
    set({ pendingTwoFactor: null })
  },
  startTwoFactor(ticket) {
    set({ pendingTwoFactor: { ticket }, status: 'unauthenticated' })
  },

  async hydrate() {
    set({ status: 'authenticating' })
    try {
      // Try refresh first — it lets the user back in even after the access
      // token expired.
      try {
        const resp = await authApi.refresh()
        setAccessToken(resp.access_token)
        set({ user: resp.user, status: 'authenticated', error: null })
        return
      } catch {
        /* fall through to /me */
      }
      const user = await authApi.me()
      set({ user, status: 'authenticated', error: null })
    } catch {
      set({ user: null, status: 'unauthenticated' })
    } finally {
      try {
        const r = await authApi.signupOpen()
        set({ signupOpen: r.open, captchaRequired: r.captcha_required })
      } catch {
        /* ignore */
      }
      // First-run probe: a fresh deployment (zero users) routes to /setup.
      try {
        const s = await authApi.needsSetup()
        set({ needsSetup: s.needs_setup })
      } catch {
        /* ignore */
      }
    }
  },

  async setup(name, email, password) {
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.setup(name, email, password)
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated', needsSetup: false, error: null })
      return true
    } catch (e) {
      set({ error: e instanceof ApiError ? e.message : 'Setup failed', status: 'unauthenticated' })
      return false
    }
  },

  async login(email, password) {
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.login(email, password)
      // 2FA-enabled accounts get a ticket instead of a session — hold it and
      // let the UI collect the code (§ 2FA login).
      if ('totp_required' in resp) {
        set({ status: 'unauthenticated', pendingTwoFactor: { ticket: resp.ticket } })
        return '2fa'
      }
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated', pendingTwoFactor: null, banned: false })
      return true
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Login failed'
      // A banned account trying to log in → show the suspended notice, not the
      // raw code.
      if (msg === 'account_suspended') {
        set({ banned: true, error: null, status: 'unauthenticated' })
        return false
      }
      // If the backend says "email not verified", flip to verification flow
      if (e instanceof ApiError && msg.toLowerCase().includes('not verified')) {
        set({ error: msg, status: 'unauthenticated', pendingVerification: email })
        return false
      }
      set({ error: msg, status: 'unauthenticated' })
      return false
    }
  },

  async loginTwoFactor(code) {
    const pending = get().pendingTwoFactor
    if (!pending) return false
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.loginTwoFactor(pending.ticket, code)
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated', pendingTwoFactor: null })
      return true
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Verification failed'
      // An expired ticket means the password step must be redone.
      if (e instanceof ApiError && e.status === 401 && msg.toLowerCase().includes('expired')) {
        set({ error: msg, status: 'unauthenticated', pendingTwoFactor: null })
        return false
      }
      set({ error: msg, status: 'unauthenticated' })
      return false
    }
  },

  async register(email, password, name, captcha) {
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.register(email, password, name, captcha)
      if ('verification_required' in resp && resp.verification_required) {
        set({ pendingVerification: resp.email as string, status: 'unauthenticated' })
        return 'verify'
      }
      const auth = resp as { user: ApiUser; access_token: string }
      setAccessToken(auth.access_token)
      set({ user: auth.user, status: 'authenticated' })
      return true
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Registration failed'
      set({ error: msg, status: 'unauthenticated' })
      return false
    }
  },

  async logout() {
    try {
      await authApi.logout()
    } catch {
      /* ignore */
    }
    setAccessToken(null)
    set({ user: null, status: 'unauthenticated', pendingTwoFactor: null })
  },

  async updateProfile(patch) {
    const updated = await authApi.updateProfile(patch)
    set({ user: updated })
  },
}))

// Live ban: an admin banning a signed-in user makes their very next request
// 403 with `account_suspended`. The api client calls this once — sign the user
// out and flip `banned` so the login screen shows the suspended notice.
setBannedHandler(() => {
  setAccessToken(null)
  useAuth.setState({ user: null, status: 'unauthenticated', banned: true, pendingTwoFactor: null })
})

// Refresh-on-401: the access token is short-lived (2h); rather than letting an
// open tab fall over with "auth required", mint a fresh one from the refresh
// cookie and let the api client retry. Returns false (→ the original 401 stands,
// surfacing as logged-out) when the refresh token is gone/expired/revoked.
setRefreshHandler(async () => {
  try {
    const resp = await authApi.refresh()
    setAccessToken(resp.access_token)
    useAuth.setState({ user: resp.user, status: 'authenticated' })
    return true
  } catch {
    setAccessToken(null)
    useAuth.setState({ user: null, status: 'unauthenticated' })
    return false
  }
})
