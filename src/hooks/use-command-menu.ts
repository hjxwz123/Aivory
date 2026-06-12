import { create } from 'zustand'

interface CmdMenuStore {
  open: boolean
  setOpen: (v: boolean) => void
  toggle: () => void
}

export const useCommandMenu = create<CmdMenuStore>((set) => ({
  open: false,
  setOpen: (v) => set({ open: v }),
  toggle: () => set((s) => ({ open: !s.open })),
}))
