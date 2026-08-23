import { useSyncExternalStore } from 'react'

type NetworkStatusListener = () => void

/**
 * `navigator.onLine` only reports the browser's network reachability. It does
 * not treat a provider or API failure as an offline state, which keeps a
 * temporary server-side error from incorrectly blocking the whole interface.
 */
export function readNetworkOnlineStatus(): boolean {
  return typeof navigator === 'undefined' || navigator.onLine !== false
}

export function subscribeToNetworkStatus(listener: NetworkStatusListener): () => void {
  if (typeof window === 'undefined') return () => undefined

  window.addEventListener('online', listener)
  window.addEventListener('offline', listener)
  return () => {
    window.removeEventListener('online', listener)
    window.removeEventListener('offline', listener)
  }
}

/** Keep app-wide connectivity UI in sync with browser network transitions. */
export function useNetworkOnlineStatus(): boolean {
  return useSyncExternalStore(
    subscribeToNetworkStatus,
    readNetworkOnlineStatus,
    () => true,
  )
}
