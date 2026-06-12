/**
 * Models store — hydrates the chat-model picker from the backend. While the
 * backend is loading we expose an empty array; consumers must handle that.
 *
 * We deliberately don't carry a local mock fallback — every model the picker
 * shows must come from the configured channels/models tables so the user
 * never picks something that won't actually run.
 */
import { create } from 'zustand'
import { modelsApi, ApiError } from '@/api'
import type { ApiModel } from '@/api/types'

interface ModelStore {
  models: ApiModel[]
  defaultId: string
  loaded: boolean
  loading: boolean
  error: string | null

  load: () => Promise<void>
  getById: (id: string) => ApiModel | undefined
}

export const useModels = create<ModelStore>((set, get) => ({
  models: [],
  defaultId: '',
  loaded: false,
  loading: false,
  error: null,

  async load() {
    if (get().loading) return
    set({ loading: true, error: null })
    try {
      const resp = await modelsApi.list()
      set({
        models: resp.models,
        defaultId: resp.default_id || resp.models[0]?.id || '',
        loaded: true,
        loading: false,
      })
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Failed to load models'
      set({ error: msg, loading: false })
    }
  },

  getById(id) {
    return get().models.find((m) => m.id === id)
  },
}))
