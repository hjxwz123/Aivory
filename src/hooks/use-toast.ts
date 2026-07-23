import { create } from 'zustand'
import type { ReactNode } from 'react'

export type ToastVariant = 'info' | 'success' | 'warning' | 'danger'

export interface ToastItem {
  id: string
  title?: string
  description?: ReactNode
  variant?: ToastVariant
  /** ms; defaults to 4500; pass 0 for sticky */
  duration?: number
  action?: { label: string; onClick: () => void }
}

export interface ToastStateItem extends ToastItem {
  open: boolean
}

interface ToastStore {
  toasts: ToastStateItem[]
  push: (toast: Omit<ToastItem, 'id'>) => string
  dismiss: (id: string) => void
  clear: () => void
}

let _id = 0
const nextId = () => `t_${++_id}`

// Keep a closed toast mounted long enough for Radix Presence to play its exit.
// The CSS exit uses --duration-fast (140ms); the small buffer avoids removing
// the node before the final animation frame is painted.
export const TOAST_REMOVE_DELAY_MS = 160

const dismissTimers = new Map<string, ReturnType<typeof setTimeout>>()
const removeTimers = new Map<string, ReturnType<typeof setTimeout>>()

function clearTimer(
  timers: Map<string, ReturnType<typeof setTimeout>>,
  id: string,
) {
  const t = timers.get(id)
  if (t) {
    clearTimeout(t)
    timers.delete(id)
  }
}

export const useToastStore = create<ToastStore>((set, get) => ({
  toasts: [],
  push(t) {
    const id = nextId()
    const duration = t.duration ?? 4500
    set((s) => ({ toasts: [...s.toasts, { ...t, id, open: true }] }))
    if (duration > 0) {
      const handle = setTimeout(() => {
        dismissTimers.delete(id)
        get().dismiss(id)
      }, duration)
      dismissTimers.set(id, handle)
    }
    return id
  },
  dismiss(id) {
    clearTimer(dismissTimers, id)

    const toast = get().toasts.find((item) => item.id === id)
    if (!toast?.open) return

    set((s) => ({
      toasts: s.toasts.map((item) =>
        item.id === id ? { ...item, open: false } : item,
      ),
    }))

    const handle = setTimeout(() => {
      removeTimers.delete(id)
      set((s) => ({ toasts: s.toasts.filter((item) => item.id !== id) }))
    }, TOAST_REMOVE_DELAY_MS)
    removeTimers.set(id, handle)
  },
  clear() {
    for (const id of dismissTimers.keys()) clearTimer(dismissTimers, id)
    for (const toast of get().toasts) get().dismiss(toast.id)
  },
}))

/** Convenience helpers. */
export const toast = {
  info: (title: string, description?: ReactNode) =>
    useToastStore.getState().push({ title, description, variant: 'info' }),
  success: (title: string, description?: ReactNode) =>
    useToastStore.getState().push({ title, description, variant: 'success' }),
  warning: (title: string, description?: ReactNode) =>
    useToastStore.getState().push({ title, description, variant: 'warning' }),
  danger: (title: string, description?: ReactNode) =>
    useToastStore.getState().push({ title, description, variant: 'danger' }),
  /**
   * Semantic alias for `danger` — what most code naturally reaches for when an
   * action fails. We keep `danger` as the canonical variant name (it matches
   * the design system tokens / Button variant), and surface `error` as the
   * call-site name for "this is a failure path".
   */
  error: (title: string, description?: ReactNode) =>
    useToastStore.getState().push({ title, description, variant: 'danger' }),
  custom: (t: Omit<ToastItem, 'id'>) => useToastStore.getState().push(t),
}
