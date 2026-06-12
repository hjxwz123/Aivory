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

interface ToastStore {
  toasts: ToastItem[]
  push: (toast: Omit<ToastItem, 'id'>) => string
  dismiss: (id: string) => void
  clear: () => void
}

let _id = 0
const nextId = () => `t_${++_id}`

// Module-scope timer registry — we own dismiss timing, not Radix.
const timers = new Map<string, ReturnType<typeof setTimeout>>()

function clearTimer(id: string) {
  const t = timers.get(id)
  if (t) {
    clearTimeout(t)
    timers.delete(id)
  }
}

export const useToastStore = create<ToastStore>((set) => ({
  toasts: [],
  push(t) {
    const id = nextId()
    const duration = t.duration ?? 4500
    set((s) => ({ toasts: [...s.toasts, { ...t, id }] }))
    if (duration > 0) {
      const handle = setTimeout(() => {
        timers.delete(id)
        set((s) => ({ toasts: s.toasts.filter((x) => x.id !== id) }))
      }, duration)
      timers.set(id, handle)
    }
    return id
  },
  dismiss(id) {
    clearTimer(id)
    set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) }))
  },
  clear() {
    for (const id of timers.keys()) clearTimer(id)
    set({ toasts: [] })
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
