import { create } from 'zustand'

// A request counter also lets callers reopen the center after it was closed.
export const useNotificationCenter = create<{ request: number; openNotifications: () => void }>((set) => ({
  request: 0,
  openNotifications: () => set((state) => ({ request: state.request + 1 })),
}))
