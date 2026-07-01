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
import type { ApiModel, ApiModelTag } from '@/api/types'
import { useSettings } from '@/store/settings'

interface ModelStore {
  models: ApiModel[]
  /** §4.20 image (kind=image) models — selectable in the picker to draw. */
  imageModels: ApiModel[]
  /** Admin-managed tags (§ model tags) — drives the picker's filter chips. */
  tags: ApiModelTag[]
  defaultId: string
  /** §verify: true when an admin configured an auditor model, so the composer
   *  shows the Verify toggle. */
  verifyAvailable: boolean
  loaded: boolean
  loading: boolean
  error: string | null

  load: () => Promise<void>
  setDefaultId: (id: string) => void
  getById: (id: string) => ApiModel | undefined
}

export const useModels = create<ModelStore>((set, get) => ({
  models: [],
  imageModels: [],
  tags: [],
  defaultId: '',
  verifyAvailable: false,
  loaded: false,
  loading: false,
  error: null,

  async load() {
    if (get().loading) return
    set({ loading: true, error: null })
    try {
      // Tags + image models are optional decoration for the picker — never let
      // their fetch failing block the chat model list.
      const [resp, tags, img] = await Promise.all([
        modelsApi.list(),
        modelsApi.tags().catch(() => []),
        modelsApi.listImage().catch(() => ({ models: [], default_id: '' })),
      ])
      const userDefaultId = useSettings.getState().models.defaultModelId
      const firstEnabled = resp.models.find((m) => m.enabled)
      const globalDefault = resp.default_id
        ? resp.models.find((m) => m.id === resp.default_id && m.enabled)
        : undefined
      const userDefault = userDefaultId
        ? resp.models.find((m) => m.id === userDefaultId && m.enabled)
        : undefined
      set({
        models: resp.models,
        imageModels: img.models,
        tags,
        defaultId: userDefault?.id || globalDefault?.id || firstEnabled?.id || resp.models[0]?.id || '',
        verifyAvailable: Boolean(resp.verify_available),
        loaded: true,
        loading: false,
      })
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Failed to load models'
      set({ error: msg, loading: false })
    }
  },

  setDefaultId(id) {
    set((s) => {
      const exists = s.models.some((m) => m.id === id && m.enabled)
      return { defaultId: exists ? id : s.defaultId }
    })
  },

  getById(id) {
    return get().models.find((m) => m.id === id) ?? get().imageModels.find((m) => m.id === id)
  },
}))
