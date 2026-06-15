import { create } from 'zustand'

/**
 * Transient UI state shared across the chat layout shell — not persisted.
 *
 * `navOpen` drives the mobile sidebar drawer so any page header (not just the
 * layout's own top bar) can open it. `pageOwnsTopBar` lets a page declare it
 * renders its own combined header on mobile (e.g. ChatThread), so the layout
 * suppresses its standalone brand bar and the two don't stack into two rows.
 */
interface UIState {
  navOpen: boolean
  setNavOpen: (open: boolean) => void
  toggleNav: () => void
  pageOwnsTopBar: boolean
  setPageOwnsTopBar: (owns: boolean) => void
}

export const useUI = create<UIState>((set) => ({
  navOpen: false,
  setNavOpen: (navOpen) => set({ navOpen }),
  toggleNav: () => set((s) => ({ navOpen: !s.navOpen })),
  pageOwnsTopBar: false,
  setPageOwnsTopBar: (pageOwnsTopBar) => set({ pageOwnsTopBar }),
}))
