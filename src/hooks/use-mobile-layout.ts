import { useSyncExternalStore } from 'react'

// Include phones in landscape, without treating a short desktop window as a phone.
const query = '(max-width: 639px), (pointer: coarse) and (max-width: 1023px) and (max-height: 520px)'
const subscribe = (notify: () => void) => {
  const media = window.matchMedia(query)
  media.addEventListener('change', notify)
  return () => media.removeEventListener('change', notify)
}
const snapshot = () => window.matchMedia(query).matches

export function useMobileLayout() {
  return useSyncExternalStore(subscribe, snapshot, () => false)
}
