import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  REQUEST_ACTIVITY_SLOW_MS,
  __resetRequestActivityForTests,
  beginRequestActivity,
  getRequestActivitySnapshot,
  subscribeRequestActivity,
  withRequestActivity,
} from '@/lib/request-activity'

describe('request activity', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-11T00:00:00Z'))
    __resetRequestActivityForTests()
  })

  afterEach(() => {
    __resetRequestActivityForTests()
    vi.useRealTimers()
  })

  it('publishes a stable idle snapshot and releases idempotently', () => {
    const initial = getRequestActivitySnapshot()
    expect(getRequestActivitySnapshot()).toBe(initial)
    expect(initial).toEqual({ pending: 0, active: false, slow: false })

    const release = beginRequestActivity()
    expect(getRequestActivitySnapshot()).toEqual({ pending: 1, active: true, slow: false })

    release()
    const idle = getRequestActivitySnapshot()
    release()
    expect(getRequestActivitySnapshot()).toBe(idle)
    expect(idle).toEqual({ pending: 0, active: false, slow: false })
  })

  it('stays active until every concurrent request has finished', () => {
    const releaseFirst = beginRequestActivity()
    const releaseSecond = beginRequestActivity()

    expect(getRequestActivitySnapshot().pending).toBe(2)
    releaseFirst()
    expect(getRequestActivitySnapshot()).toEqual({ pending: 1, active: true, slow: false })
    releaseSecond()
    expect(getRequestActivitySnapshot()).toEqual({ pending: 0, active: false, slow: false })
  })

  it('marks a stalled request slow and recalculates from the next-oldest request', () => {
    const releaseOldest = beginRequestActivity()
    vi.advanceTimersByTime(REQUEST_ACTIVITY_SLOW_MS / 2)
    const releaseYounger = beginRequestActivity()

    vi.advanceTimersByTime(REQUEST_ACTIVITY_SLOW_MS / 2)
    expect(getRequestActivitySnapshot().slow).toBe(true)

    releaseOldest()
    expect(getRequestActivitySnapshot()).toEqual({ pending: 1, active: true, slow: false })

    vi.advanceTimersByTime(REQUEST_ACTIVITY_SLOW_MS / 2 - 1)
    expect(getRequestActivitySnapshot().slow).toBe(false)
    vi.advanceTimersByTime(1)
    expect(getRequestActivitySnapshot().slow).toBe(true)

    releaseYounger()
  })

  it('does not publish background polling as foreground activity', () => {
    const listener = vi.fn()
    const unsubscribe = subscribeRequestActivity(listener)
    const release = beginRequestActivity('background')

    expect(getRequestActivitySnapshot()).toEqual({ pending: 0, active: false, slow: false })
    expect(listener).not.toHaveBeenCalled()
    release()
    expect(listener).not.toHaveBeenCalled()
    unsubscribe()
  })

  it('keeps a deferred operation active and clears it after success', async () => {
    let resolveOperation: ((value: string) => void) | undefined
    const operation = withRequestActivity(
      () => new Promise<string>((resolve) => { resolveOperation = resolve }),
    )

    expect(getRequestActivitySnapshot().active).toBe(true)
    resolveOperation?.('done')
    await expect(operation).resolves.toBe('done')
    expect(getRequestActivitySnapshot().active).toBe(false)
  })

  it('clears activity after rejection and abort errors', async () => {
    await expect(withRequestActivity(async () => {
      throw new Error('network failed')
    })).rejects.toThrow('network failed')
    expect(getRequestActivitySnapshot().active).toBe(false)

    await expect(withRequestActivity(async () => {
      throw new DOMException('Aborted', 'AbortError')
    })).rejects.toMatchObject({ name: 'AbortError' })
    expect(getRequestActivitySnapshot().active).toBe(false)
  })
})
