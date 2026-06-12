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
import type { ApiUser } from '@/api/types'

interface AuthState {
  user: ApiUser | null
  status: 'idle' | 'authenticating' | 'authenticated' | 'unauthenticated'
  error: string | null
  signupOpen: boolean

  hydrate: () => Promise<void>
  login: (email: string, password: string) => Promise<boolean>
  register: (email: string, password: string, name: string) => Promise<boolean>
  logout: () => Promise<void>
  updateProfile: (patch: { name?: string; email?: string }) => Promise<void>
  setUser: (user: ApiUser | null) => void
  setSignupOpen: (open: boolean) => void
}

export const useAuth = create<AuthState>((set, get) => ({
  user: null,
  status: 'idle',
  error: null,
  signupOpen: true,

  setUser(user) {
    set({ user, status: user ? 'authenticated' : 'unauthenticated' })
  },
  setSignupOpen(open) {
    set({ signupOpen: open })
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
        set({ signupOpen: r.open })
      } catch {
        /* ignore */
      }
    }
  },

  async login(email, password) {
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.login(email, password)
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated' })
      return true
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Login failed'
      set({ error: msg, status: 'unauthenticated' })
      return false
    }
  },

  async register(email, password, name) {
    set({ status: 'authenticating', error: null })
    try {
      const resp = await authApi.register(email, password, name)
      setAccessToken(resp.access_token)
      set({ user: resp.user, status: 'authenticated' })
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
    set({ user: null, status: 'unauthenticated' })
  },

  async updateProfile(patch) {
    const updated = await authApi.updateProfile(patch)
    set({ user: updated })
    void get()
  },
}))
