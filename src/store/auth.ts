/**
 * Auth store — drives the signed-in user, hydrates on mount, and exposes the
 * three transitions the UI cares about: login, register, logout.
 *
 * The backend keeps the auth cookie httpOnly; we also hold the short-lived
 * access token in memory so the api client can attach it as a Bearer header.
 * On refresh, the cookie-backed /api/auth/refresh restores both.
 */
import { create } from 'zustand'
import { authApi, ApiError, resetAuthFailureState, setAccessToken } from '@/api'
import { isAuthRefreshSuppressed, setAuthLostHandler, setBannedHandler, setRefreshHandler } from '@/api/client'
import type { ApiUser } from '@/api/types'

// Auth requests can overlap: AuthGate hydrates even on /login, while the user can
// submit the login form before that stale hydrate finishes. Only the latest auth
// operation may write user/status, otherwise a late 401 from the old hydrate can
// sign out a freshly logged-in user.
let authOpSeq = 0
function beginAuthOp(): number {
  authOpSeq += 1
  return authOpSeq
}
// Observe the current op WITHOUT starting a new one. The background
// refresh-on-401 handler uses this: it must notice when a real auth transition
// (login/register/logout) supersedes it, but must not itself invalidate an
// in-flight hydrate — bumping the seq from here made hydrate's finally drop the
// first-run probe result, leaving `setupProbed` false forever on a logged-out
// load, and the AuthGate then hung on its shimmer right after register/login.
function currentAuthOp(): number {
  return authOpSeq
}
function isLatestAuthOp(seq: number): boolean {
  return seq === authOpSeq
}

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
  /** False until the first-run probe (/public/needs-setup) has resolved at least
   *  once. The gate waits for this before choosing setup-vs-login, so a fresh
   *  deploy doesn't flicker /setup → /login on the default (false) value. */
  setupProbed: boolean
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
    captchaToken?: string,
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
  setupProbed: false,
  pendingVerification: null,
  pendingTwoFactor: null,

  setUser(user) {
    // An authenticated user proves the deployment is past first-run setup, so
    // also resolve the probe — flows that sign in via setUser (email
    // verification, OAuth callback) must never leave the gate waiting on it.
    if (user) {
      set({ user, status: 'authenticated', needsSetup: false, setupProbed: true })
    } else {
      set({ user: null, status: 'unauthenticated' })
    }
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
    const seq = beginAuthOp()
    set({ status: 'authenticating' })
    try {
      // Try refresh first — it lets the user back in even after the access
      // token expired.
      try {
        const resp = await authApi.refresh()
        if (!isLatestAuthOp(seq)) return
        resetAuthFailureState()
        setAccessToken(resp.access_token)
        // Authenticated ⇒ the deployment has users; resolve the setup probe
        // immediately so the gate can route without waiting on the sibling
        // /public/needs-setup call.
        set({ user: resp.user, status: 'authenticated', needsSetup: false, setupProbed: true, error: null })
        return
      } catch {
        /* fall through to /me */
      }
      const user = await authApi.me()
      if (!isLatestAuthOp(seq)) return
      resetAuthFailureState()
      set({ user, status: 'authenticated', needsSetup: false, setupProbed: true, error: null })
    } catch {
      if (!isLatestAuthOp(seq)) return
      set({ user: null, status: 'unauthenticated' })
    } finally {
      // First-run probe: a fresh deployment (zero users) routes to /setup. Probe
      // it in PARALLEL with signup-open so a slow/failed sibling call can't delay
      // the routing decision, and mark it resolved either way so the AuthGate
      // stops waiting (otherwise the gate is stuck on the default needsSetup).
      const [signup, setup] = await Promise.allSettled([authApi.signupOpen(), authApi.needsSetup()])
      if (isLatestAuthOp(seq)) {
        if (signup.status === 'fulfilled') {
          set({ signupOpen: signup.value.open, captchaRequired: signup.value.captcha_required })
        }
        if (setup.status === 'fulfilled') {
          set({ needsSetup: setup.value.needs_setup })
        }
        set({ setupProbed: true })
      }
    }
  },

  async setup(name, email, password) {
    const seq = beginAuthOp()
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.setup(name, email, password)
      if (!isLatestAuthOp(seq)) return false
      resetAuthFailureState()
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated', needsSetup: false, setupProbed: true, error: null })
      return true
    } catch (e) {
      if (!isLatestAuthOp(seq)) return false
      set({ error: e instanceof ApiError ? e.message : 'Setup failed', status: 'unauthenticated' })
      return false
    }
  },

  async login(email, password) {
    const seq = beginAuthOp()
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.login(email, password)
      if (!isLatestAuthOp(seq)) return false
      // 2FA-enabled accounts get a ticket instead of a session — hold it and
      // let the UI collect the code (§ 2FA login).
      if ('totp_required' in resp) {
        set({ status: 'unauthenticated', pendingTwoFactor: { ticket: resp.ticket } })
        return '2fa'
      }
      resetAuthFailureState()
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated', needsSetup: false, setupProbed: true, pendingTwoFactor: null, banned: false })
      return true
    } catch (e) {
      if (!isLatestAuthOp(seq)) return false
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
    const seq = beginAuthOp()
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.loginTwoFactor(pending.ticket, code)
      if (!isLatestAuthOp(seq)) return false
      resetAuthFailureState()
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated', needsSetup: false, setupProbed: true, pendingTwoFactor: null })
      return true
    } catch (e) {
      if (!isLatestAuthOp(seq)) return false
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

  async register(email, password, name, captchaToken) {
    const seq = beginAuthOp()
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.register(email, password, name, captchaToken)
      if (!isLatestAuthOp(seq)) return false
      if ('verification_required' in resp && resp.verification_required) {
        set({ pendingVerification: resp.email as string, status: 'unauthenticated' })
        return 'verify'
      }
      const auth = resp as { user: ApiUser; access_token: string }
      resetAuthFailureState()
      setAccessToken(auth.access_token)
      set({ user: auth.user, status: 'authenticated', needsSetup: false, setupProbed: true })
      return true
    } catch (e) {
      if (!isLatestAuthOp(seq)) return false
      const msg = e instanceof ApiError ? e.message : 'Registration failed'
      set({ error: msg, status: 'unauthenticated' })
      return false
    }
  },

  async logout() {
    beginAuthOp()
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
  beginAuthOp()
  setAccessToken(null)
  useAuth.setState({ user: null, status: 'unauthenticated', banned: true, pendingTwoFactor: null })
})

setAuthLostHandler(() => {
  beginAuthOp()
  setAccessToken(null)
  useAuth.setState({ user: null, status: 'unauthenticated', pendingTwoFactor: null })
})

// Refresh-on-401: the access token is short-lived (2h); rather than letting an
// open tab fall over with "auth required", mint a fresh one from the refresh
// cookie and let the api client retry. Returns false (→ the original 401 stands,
// surfacing as logged-out) when the refresh token is gone/expired/revoked.
setRefreshHandler(async () => {
  const seq = currentAuthOp()
  try {
    const resp = await authApi.refresh()
    if (isAuthRefreshSuppressed()) return false
    if (!isLatestAuthOp(seq)) return true
    setAccessToken(resp.access_token)
    const current = useAuth.getState()
    if (current.status !== 'authenticated' || current.user?.id !== resp.user.id) {
      useAuth.setState({ user: resp.user, status: 'authenticated', needsSetup: false, setupProbed: true })
    }
    return true
  } catch {
    if (!isLatestAuthOp(seq)) return false
    setAccessToken(null)
    useAuth.setState({ user: null, status: 'unauthenticated' })
    return false
  }
})
