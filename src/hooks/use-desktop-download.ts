import { useEffect } from 'react'
import { create } from 'zustand'
import { desktopDownloadConfig } from '@/api/desktop-update'

function publicDownloadUrl(config: { enabled?: boolean; url?: string }) {
  if (!config.enabled || typeof config.url !== 'string') return null
  try {
    const url = new URL(config.url)
    return ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password ? url.href : null
  } catch { return null }
}

let inFlight: Promise<void> | undefined
let loadedAt = 0
const useDownloadState = create<{ url: string | null }>(() => ({ url: null }))

function load(force = false) {
  if (inFlight) return inFlight
  if (!force && Date.now() - loadedAt < 60_000) return Promise.resolve()
  inFlight = desktopDownloadConfig().then((config) => {
    useDownloadState.setState({ url: publicDownloadUrl(config) })
  }).catch(() => { useDownloadState.setState({ url: null }) }).finally(() => {
    loadedAt = Date.now()
    inFlight = undefined
  })
  return inFlight
}

/** Both web entry points share one background request; native clients skip it. */
export function useDesktopDownload() {
  const url = useDownloadState((state) => state.url)
  const desktop = typeof window !== 'undefined' && Boolean(window.aivoryDesktop)
  useEffect(() => {
    if (desktop) return
    void load()
    const refresh = () => { void load() }
    const changed = () => { void load(true) }
    window.addEventListener('focus', refresh)
    window.addEventListener('aivory:desktop-download-changed', changed)
    return () => {
      window.removeEventListener('focus', refresh)
      window.removeEventListener('aivory:desktop-download-changed', changed)
    }
  }, [desktop])
  return desktop ? null : url
}
