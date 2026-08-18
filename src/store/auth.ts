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
import {
  isAuthRefreshSuppressed,
  setAuthLostHandler,
  setBannedHandler,
  setInitialPasswordRequiredHandler,
  setRefreshHandler,
} from '@/api/client'
import type { ApiAuthPolicy, ApiUser } from '@/api/types'

export const DEFAULT_AUTH_POLICY: ApiAuthPolicy = {
  password_login_enabled: true,
  entry_mode: 'login_page',
  default_provider: null,
  oauth_initial_password_policy: 'required',
  oauth_auto_provision_enabled: true,
  providers: [],
}

// Auth requests can overlap: AuthGate hydrates even on /login, while the user can
// submit the login form before that stale hydrate finishes. Only the latest auth
// operation may write user/status, otherwise a late 401 from the old hydrate can
// sign out a freshly logged-in user.
let authOpSeq = 0
let profileRefresh: { userId: string; promise: Promise<ApiUser | null> } | null = null
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
  /** Public deployment authentication policy. Always resolves to a compatible
   * fallback so a transient policy request failure cannot strand the login UI. */
  authPolicy: ApiAuthPolicy
  authPolicyLoaded: boolean
  /** True when the admin requires the slider-puzzle captcha on the register form. */
  captchaRequired: boolean
  /** True when the admin requires the slider-puzzle captcha on password sign-in
   *  (§ anti credential-stuffing). */
  loginCaptchaRequired: boolean
  /** True when the deployment has no users yet — the app routes to the first-run
   *  setup screen (create the first admin) instead of login (§ first-run setup). */
  needsSetup: boolean
  /** False until the first-run probe (/public/needs-setup) has resolved at least
   *  once. The gate waits for this before choosing setup-vs-login, so a fresh
   *  deploy doesn't flicker /setup → /login on the default (false) value. */
  setupProbed: boolean
  /** Set when registration returns verification_required — drives the code UI. */
  pendingVerification: string | null
  /** Server-authoritative delay before the verification email may be resent. */
  pendingVerificationRetryAfter: number
  /** Set when password login returns totp_required — drives the 2FA code UI. */
  pendingTwoFactor: { ticket: string } | null

  hydrate: () => Promise<void>
  refreshAuthPolicy: () => Promise<ApiAuthPolicy>
  /** Refresh the server-authoritative profile without changing auth status or
   *  invalidating an in-flight login/logout transition. Permission events and
   *  expiring plans use this to converge an already-open tab. */
  refreshProfile: () => Promise<ApiUser | null>
  login: (email: string, password: string, captchaToken?: string) => Promise<boolean | '2fa'>
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
  startEmailVerification: (email: string, retryAfter: number) => void
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
  authPolicy: DEFAULT_AUTH_POLICY,
  authPolicyLoaded: false,
  captchaRequired: false,
  loginCaptchaRequired: false,
  needsSetup: false,
  setupProbed: false,
  pendingVerification: null,
  pendingVerificationRetryAfter: 0,
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
    set({ pendingVerification: null, pendingVerificationRetryAfter: 0 })
  },
  startEmailVerification(email, retryAfter) {
    set({
      pendingVerification: email,
      pendingVerificationRetryAfter: Math.max(0, retryAfter),
      status: 'unauthenticated',
      error: null,
    })
  },
  clearPendingTwoFactor() {
    set({ pendingTwoFactor: null })
  },
  startTwoFactor(ticket) {
    set({ pendingTwoFactor: { ticket }, status: 'unauthenticated' })
  },

  async refreshAuthPolicy() {
    try {
      const authPolicy = await authApi.authPolicy()
      set({ authPolicy, authPolicyLoaded: true })
      return authPolicy
    } catch {
      set({ authPolicy: DEFAULT_AUTH_POLICY, authPolicyLoaded: true })
      return DEFAULT_AUTH_POLICY
    }
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
      // it in PARALLEL with public login policy so a slow/failed sibling call can't delay
      // the routing decision, and mark it resolved either way so the AuthGate
      // stops waiting (otherwise the gate is stuck on the default needsSetup).
      const [signup, setup, authPolicy] = await Promise.allSettled([
        authApi.signupOpen(),
        authApi.needsSetup(),
        authApi.authPolicy(),
      ])
      if (isLatestAuthOp(seq)) {
        if (signup.status === 'fulfilled') {
          set({
            signupOpen: signup.value.open,
            captchaRequired: signup.value.captcha_required,
            loginCaptchaRequired: signup.value.login_captcha_required,
          })
        }
        if (setup.status === 'fulfilled') {
          set({ needsSetup: setup.value.needs_setup })
        }
        set({
          authPolicy: authPolicy.status === 'fulfilled' ? authPolicy.value : DEFAULT_AUTH_POLICY,
          authPolicyLoaded: true,
          setupProbed: true,
        })
      }
    }
  },

  async refreshProfile() {
    const expectedUserId = get().user?.id
    if (!expectedUserId || get().status !== 'authenticated') return null
    if (profileRefresh?.userId === expectedUserId) return profileRefresh.promise
    const promise = authApi
      .me()
      .then((fresh) => {
        const current = get()
        if (current.status === 'authenticated' && current.user?.id === expectedUserId && fresh.id === expectedUserId) {
          set({ user: fresh, error: null })
          return fresh
        }
        return null
      })
      .catch(() => null)
      .finally(() => {
        if (profileRefresh?.promise === promise) profileRefresh = null
      })
    profileRefresh = { userId: expectedUserId, promise }
    return promise
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

  async login(email, password, captchaToken) {
    const seq = beginAuthOp()
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.login(email, password, captchaToken)
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
      // The backend signals an unverified account with the exact code
      // "email_not_verified" — flip to the verification flow.
      if (msg === 'email_not_verified') {
        set({ error: msg, status: 'unauthenticated', pendingVerification: email, pendingVerificationRetryAfter: 0 })
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
        set({
          pendingVerification: resp.email as string,
          pendingVerificationRetryAfter: resp.retry_after,
          status: 'unauthenticated',
        })
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
    set({ user: null, status: 'unauthenticated', pendingTwoFactor: null, authPolicyLoaded: false })
    await get().refreshAuthPolicy()
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
  useAuth.setState({ user: null, status: 'unauthenticated', banned: true, pendingTwoFactor: null, authPolicyLoaded: false })
  void useAuth.getState().refreshAuthPolicy()
})

setAuthLostHandler(() => {
  beginAuthOp()
  setAccessToken(null)
  useAuth.setState({ user: null, status: 'unauthenticated', pendingTwoFactor: null, authPolicyLoaded: false })
  void useAuth.getState().refreshAuthPolicy()
})

setInitialPasswordRequiredHandler(() => {
  const current = useAuth.getState().user
  if (!current || current.has_password !== false) return
  useAuth.setState({
    user: { ...current, oauth_initial_password_policy: 'required' },
  })
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
