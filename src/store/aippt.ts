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
  status: AiPPTStatus
  error: string | null
  /** Authoritative spendable balance, refreshed by every billing transition. */
  available: number
  /** Fetch the config once (or again with force). Concurrent calls share one request. */
  load: (force?: boolean) => Promise<ApiAiPPTConfig | null>
  setAvailable: (available: number) => void
  reset: () => void
}

let inFlight: Promise<ApiAiPPTConfig | null> | null = null

export const useAiPPT = create<AiPPTStore>((set, get) => ({
  config: null,
  status: 'idle',
  error: null,
  available: 0,
  load: (force = false) => {
    if (!force && get().status === 'ready' && get().config) {
      return Promise.resolve(get().config)
    }
    if (inFlight) return inFlight
    set({ status: 'loading' })
    inFlight = aipptApi
      .config()
      .then((config) => {
        set({ config, status: 'ready', error: null, available: config.credits_available })
        return config
      })
      .catch((error: unknown) => {
        set({
          status: 'error',
          error: error instanceof Error ? error.message : String(error),
        })
        return null
      })
      .finally(() => {
        inFlight = null
      })
    return inFlight
  },
  setAvailable: (available) => {
    if (Number.isFinite(available)) set({ available })
  },
  reset: () => {
    inFlight = null
    set({ config: null, status: 'idle', error: null, available: 0 })
  },
}))
