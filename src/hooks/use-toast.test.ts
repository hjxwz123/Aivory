import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TOAST_REMOVE_DELAY_MS, useToastStore } from './use-toast'

describe('toast lifecycle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    useToastStore.setState({ toasts: [] })
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
    useToastStore.setState({ toasts: [] })
  })

  it('keeps a dismissed toast mounted while its exit animation runs', () => {
    const id = useToastStore.getState().push({ title: 'Saved', duration: 0 })

    useToastStore.getState().dismiss(id)

    expect(useToastStore.getState().toasts).toMatchObject([{ id, open: false }])

    vi.advanceTimersByTime(TOAST_REMOVE_DELAY_MS - 1)
    expect(useToastStore.getState().toasts).toHaveLength(1)

    vi.advanceTimersByTime(1)
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('uses the same animated close path when a toast expires', () => {
    const id = useToastStore.getState().push({ title: 'Saved', duration: 1000 })

    vi.advanceTimersByTime(1000)
    expect(useToastStore.getState().toasts).toMatchObject([{ id, open: false }])

    vi.advanceTimersByTime(TOAST_REMOVE_DELAY_MS)
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })

  it('closes every toast before clearing the stack', () => {
    useToastStore.getState().push({ title: 'First', duration: 0 })
    useToastStore.getState().push({ title: 'Second', duration: 0 })

    useToastStore.getState().clear()

    expect(useToastStore.getState().toasts).toHaveLength(2)
    expect(useToastStore.getState().toasts.every((toast) => !toast.open)).toBe(true)

    vi.advanceTimersByTime(TOAST_REMOVE_DELAY_MS)
    expect(useToastStore.getState().toasts).toHaveLength(0)
  })
})
