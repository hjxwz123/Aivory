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
import { useAuth } from '@/store/auth'
import { userCan } from '@/lib/user-permissions'

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
  /** §fast-mode: true when an admin configured a fast model, so the composer
   *  offers the 快速 option. */
  fastAvailable: boolean
  /** Whether the hidden fast model accepts image inputs. Its identity is never exposed. */
  fastVision: boolean
  loaded: boolean
  loading: boolean
  error: string | null

  load: () => Promise<void>
  setTags: (update: ApiModelTag[] | ((tags: ApiModelTag[]) => ApiModelTag[])) => void
  setDefaultId: (id: string) => void
  getById: (id: string) => ApiModel | undefined
}

// Permission/model invalidations can arrive while the initial picker request is
// still running. Remember one trailing reload so the latest server state always
// wins instead of silently dropping the invalidation.
let reloadRequested = false

export const useModels = create<ModelStore>((set, get) => ({
  models: [],
  imageModels: [],
  tags: [],
  defaultId: '',
  verifyAvailable: false,
  fastAvailable: false,
  fastVision: false,
  loaded: false,
  loading: false,
  error: null,

  async load() {
    if (get().loading) {
      reloadRequested = true
      return
    }
    const tagsAtLoadStart = get().tags
    set({ loading: true, error: null })
    try {
      const canDraw = userCan(useAuth.getState().user, 'allow_drawing')
      // Tags + image models are optional decoration for the picker — never let
      // their fetch failing block the chat model list.
      const [resp, tagResult, img] = await Promise.all([
        modelsApi.list(),
        modelsApi.tags().then(
          (tags) => ({ ok: true as const, tags }),
          () => ({ ok: false as const, tags: [] as ApiModelTag[] }),
        ),
        canDraw
          ? modelsApi.listImage().catch(() => ({ models: [], default_id: '' }))
          : Promise.resolve({ models: [], default_id: '' }),
      ])
      const userDefaultId = useSettings.getState().models.defaultModelId
      const firstEnabled = resp.models.find((m) => m.enabled)
      const globalDefault = resp.default_id
        ? resp.models.find((m) => m.id === resp.default_id && m.enabled)
        : undefined
      const userDefault = userDefaultId
        ? resp.models.find((m) => m.id === userDefaultId && m.enabled)
        : undefined
      set((state) => ({
        models: resp.models,
        // A group permission can change while these requests are in flight.
        // Re-check at commit time so a stale response never restores drawing.
        imageModels: userCan(useAuth.getState().user, 'allow_drawing') ? img.models : [],
        // An admin mutation may have updated the shared picker cache while
        // these requests were in flight. Never replace that newer state with
        // a stale response, and preserve the cache when optional tag loading
        // fails.
        tags: tagResult.ok && state.tags === tagsAtLoadStart ? tagResult.tags : state.tags,
        defaultId: userDefault?.id || globalDefault?.id || firstEnabled?.id || resp.models[0]?.id || '',
        verifyAvailable: Boolean(resp.verify_available),
        fastAvailable: Boolean(resp.fast_available),
        fastVision: Boolean(resp.fast_vision),
        loaded: true,
        loading: false,
      }))
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : 'Failed to load models'
      set({ error: msg, loading: false })
    } finally {
      if (reloadRequested) {
        reloadRequested = false
        set({ loading: false })
        void get().load()
      }
    }
  },

  setTags(update) {
    set((state) => ({
      tags: typeof update === 'function' ? update(state.tags) : update,
    }))
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
