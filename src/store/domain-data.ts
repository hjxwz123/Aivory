import { create } from 'zustand'
import { domainDataApi, type DomainPersonalDataStatus } from '@/api/domains'

interface DomainDataState {
  status: DomainPersonalDataStatus | null
  loadedForUser: string | null
  loading: boolean
  open: boolean
  load: (userId: string, force?: boolean) => Promise<void>
  show: () => void
  hide: () => void
  markDismissed: () => void
  markMigrated: () => void
  reset: () => void
}

let loadSequence = 0

export const useDomainData = create<DomainDataState>((set, get) => ({
  status: null,
  loadedForUser: null,
  loading: false,
  open: false,

  async load(userId, force = false) {
    if (!userId || (!force && get().loadedForUser === userId)) return
    const sequence = ++loadSequence
    set({ loading: true })
    try {
      const status = await domainDataApi.status()
      if (sequence !== loadSequence) return
      set({
        status,
        loadedForUser: userId,
        loading: false,
        open: status.needs_action && !status.prompt_dismissed,
      })
    } catch {
      if (sequence === loadSequence) set({ loading: false, loadedForUser: userId })
    }
  },

  show() {
    if (get().status?.needs_action) set({ open: true })
  },
  hide() { set({ open: false }) },
  markDismissed() {
    set((state) => ({
      open: false,
      status: state.status ? { ...state.status, prompt_dismissed: true } : null,
    }))
  },
  markMigrated() {
    set((state) => ({
      open: false,
      status: state.status ? { ...state.status, needs_action: false, personal_conversation_count: 0 } : null,
    }))
  },
  reset() {
    loadSequence += 1
    set({ status: null, loadedForUser: null, loading: false, open: false })
  },
}))
