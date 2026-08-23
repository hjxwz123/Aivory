import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  readNetworkOnlineStatus,
  subscribeToNetworkStatus,
} from '@/hooks/use-network-status'

function setBrowserOnlineStatus(online: boolean): EventTarget {
  const eventTarget = new EventTarget()
  vi.stubGlobal('window', eventTarget)
  vi.stubGlobal('navigator', { onLine: online })
  return eventTarget
}

describe('network status', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('is online unless the browser explicitly reports an offline connection', () => {
    vi.stubGlobal('navigator', {})
    expect(readNetworkOnlineStatus()).toBe(true)

    setBrowserOnlineStatus(false)
    expect(readNetworkOnlineStatus()).toBe(false)

    setBrowserOnlineStatus(true)
    expect(readNetworkOnlineStatus()).toBe(true)
  })

  it('publishes online and offline transitions and releases listeners on cleanup', () => {
    const browser = setBrowserOnlineStatus(true)
    const listener = vi.fn()
    const unsubscribe = subscribeToNetworkStatus(listener)

    browser.dispatchEvent(new Event('offline'))
    browser.dispatchEvent(new Event('online'))
    expect(listener).toHaveBeenCalledTimes(2)

    unsubscribe()
    browser.dispatchEvent(new Event('offline'))
    expect(listener).toHaveBeenCalledTimes(2)
  })
})
