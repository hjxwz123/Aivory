/**
 * Shared AI PPT runtime config (§ AI PPT / Docmee iframe).
 *
 * Two surfaces need the same answer: the /ppt page (does the iframe have a
 * pinned SDK URL, what does a deck cost, what is the balance) and the sidebar
 * (should the entry exist at all on this deployment). One cached request keeps
 * them consistent and lets the billing callbacks in the page update a balance the
 * sidebar chip can read.
 */
import { create } from 'zustand'

import { aipptApi } from '@/api'
import type { ApiAiPPTConfig } from '@/api/types'

type AiPPTStatus = 'idle' | 'loading' | 'ready' | 'error'

interface AiPPTStore {
  config: ApiAiPPTConfig | null
  scope: string
  status: AiPPTStatus
  error: string | null
  /** Authoritative spendable balance, refreshed by every billing transition. */
  available: number
  /** Fetch the config once (or again with force). Concurrent calls share one request. */
  load: (force?: boolean) => Promise<ApiAiPPTConfig | null>
  setAvailable: (available: number) => void
  reset: () => void
}

let inFlight: { scope: string; promise: Promise<ApiAiPPTConfig | null> } | null = null
let revision = 0

export const useAiPPT = create<AiPPTStore>((set, get) => ({
  config: null,
  scope: '',
  status: 'idle',
  error: null,
  available: 0,
  load: (force = false) => {
    const scope = aipptApi.scopedPath('/me/ppt/config')
    if (!force && get().scope === scope && get().status === 'ready' && get().config) {
      return Promise.resolve(get().config)
    }
    if (!force && inFlight?.scope === scope) return inFlight.promise
    const requestRevision = ++revision
    set({ scope, config: null, status: 'loading' })
    const promise = aipptApi
      .config()
      .then((config) => {
        if (revision === requestRevision) set({ config, status: 'ready', error: null, available: config.credits_available })
        return config
      })
      .catch((error: unknown) => {
        if (revision === requestRevision) set({
          status: 'error',
          error: error instanceof Error ? error.message : String(error),
        })
        return null
      })
      .finally(() => {
        if (inFlight?.promise === promise) inFlight = null
      })
    inFlight = { scope, promise }
    return promise
  },
  setAvailable: (available) => {
    if (Number.isFinite(available)) set({ available })
  },
  reset: () => {
    revision += 1
    inFlight = null
    set({ config: null, scope: '', status: 'idle', error: null, available: 0 })
  },
}))
