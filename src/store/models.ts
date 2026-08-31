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
import { activeWorkspaceId, useWorkspaces } from '@/store/workspaces'
import { workspaceCapabilitiesForScope, workspaceModelPolicyKey } from '@/lib/workspace-permissions'

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
  /** Scope assigned to the current cache/request. Null denotes personal space;
   *  undefined means no scope has started hydrating yet. `loaded` distinguishes
   *  an in-flight empty cache from a completed response. */
  loadedScope: string | null | undefined
  /** Policy fingerprint represented by the current workspace model cache. */
  loadedPolicyKey: string | undefined
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
  loadedScope: undefined,
  loadedPolicyKey: undefined,
  loading: false,
  error: null,

  async load() {
    const workspaceId = activeWorkspaceId()
    const scopeKey = workspaceId ?? null
    const workspaceStateAtCall = useWorkspaces.getState()
    const workspacePolicyAtCall = workspaceId ? workspaceStateAtCall.policies[workspaceId] : undefined
    const policyKey = workspaceModelPolicyKey(workspaceId, workspacePolicyAtCall)
    if (get().loading) {
      reloadRequested = true
      // A request for the previous workspace may still be in flight when the
      // switch requests its trailing reload. Clear that previous catalog now;
      // otherwise the picker can expose old models until the first request
      // settles and the queued request gets a chance to start.
      if (get().loadedScope !== scopeKey || get().loadedPolicyKey !== policyKey) {
        set({
          models: [],
          imageModels: [],
          defaultId: '',
          verifyAvailable: false,
          fastAvailable: false,
          fastVision: false,
          loaded: false,
          loadedScope: scopeKey,
          loadedPolicyKey: policyKey,
          error: null,
        })
      }
      return
    }
    const stateAtLoadStart = get()
    const tagsAtLoadStart = stateAtLoadStart.tags
    // `loadedScope` is assigned as soon as a cross-scope request is queued so
    // consumers stop trusting the old cache immediately. `loaded=false` keeps
    // that queued/empty scope on the full hydration path when its request
    // actually starts after an older in-flight load settles.
    const catalogContextChanged =
      stateAtLoadStart.loadedScope !== scopeKey ||
      stateAtLoadStart.loadedPolicyKey !== policyKey ||
      !stateAtLoadStart.loaded
    if (catalogContextChanged) {
      // Every catalog-derived value is workspace-scoped. Assign the new scope
      // only after clearing all previous values so a failed request cannot
      // relabel another workspace's catalog as the current one.
      set({
        models: [],
        imageModels: [],
        defaultId: '',
        verifyAvailable: false,
        fastAvailable: false,
        fastVision: false,
        loaded: false,
        loadedScope: scopeKey,
        loadedPolicyKey: policyKey,
        loading: true,
        error: null,
      })
    } else {
      // Same-scope refreshes keep the usable catalog on screen. Permission and
      // drawing gates are checked independently at every consumer and again at
      // commit time, so this avoids needless picker flicker without crossing a
      // workspace boundary.
      set({ loading: true, error: null })
    }
    try {
      const workspaceCaps = workspaceCapabilitiesForScope(workspaceId, workspacePolicyAtCall, {
        workspacesLoaded: workspaceStateAtCall.loaded,
        policyLoading: workspaceId ? workspaceStateAtCall.policyLoading[workspaceId] === true : false,
        switching: workspaceStateAtCall.switching,
        policyError: workspaceId ? workspaceStateAtCall.policyErrors[workspaceId] : null,
      })
      const canDraw = userCan(useAuth.getState().user, 'allow_drawing') && workspaceCaps.drawing
      // §workspace RBAC: the picker follows the ACTIVE space — a workspace
      // scope returns only its policy-allowed models; personal returns all.
      // Tags + image models are optional decoration for the picker — never let
      // their fetch failing block the chat model list.
      const [resp, tagResult, img] = await Promise.all([
        modelsApi.list(workspaceId),
        modelsApi.tags().then(
          (tags) => ({ ok: true as const, tags }),
          () => ({ ok: false as const, tags: [] as ApiModelTag[] }),
        ),
          canDraw
          ? modelsApi.listImage(workspaceId).catch(() => ({ models: [], default_id: '' }))
          : Promise.resolve({ models: [], default_id: '' }),
      ])
      // A workspace switch can finish while the requests above are in flight.
      // Do not publish the previous scope's models into the new space; the
      // trailing reload scheduled by the switch will hydrate the right list.
      if (activeWorkspaceId() !== workspaceId) {
        reloadRequested = true
        return
      }
      const userDefaultId = useSettings.getState().models.defaultModelId
      const firstEnabled = resp.models.find((m) => m.enabled)
      const globalDefault = resp.default_id
        ? resp.models.find((m) => m.id === resp.default_id && m.enabled)
        : undefined
      const userDefault = userDefaultId
        ? resp.models.find((m) => m.id === userDefaultId && m.enabled)
        : undefined
      const latestWorkspaceId = activeWorkspaceId()
      const latestWorkspaceState = useWorkspaces.getState()
      const latestWorkspacePolicy = latestWorkspaceId
        ? latestWorkspaceState.policies[latestWorkspaceId]
        : undefined
      const latestPolicyKey = workspaceModelPolicyKey(latestWorkspaceId, latestWorkspacePolicy)
      // A policy update can land without changing the active workspace id. Do
      // not publish a response fetched under the previous allowlist/drawing
      // ceiling; the policy setter has already requested a trailing reload.
      if (latestPolicyKey !== policyKey) {
        reloadRequested = true
        return
      }
      const latestWorkspaceCaps = workspaceCapabilitiesForScope(latestWorkspaceId, latestWorkspacePolicy, {
        workspacesLoaded: latestWorkspaceState.loaded,
        policyLoading: latestWorkspaceId ? latestWorkspaceState.policyLoading[latestWorkspaceId] === true : false,
        switching: latestWorkspaceState.switching,
        policyError: latestWorkspaceId ? latestWorkspaceState.policyErrors[latestWorkspaceId] : null,
      })
      const latestCanDraw = userCan(useAuth.getState().user, 'allow_drawing') &&
        latestWorkspaceCaps.drawing
      set((state) => ({
        models: resp.models,
        // A group permission can change while these requests are in flight.
        // Re-check at commit time so a stale response never restores drawing.
        imageModels: latestCanDraw ? img.models : [],
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
        loadedScope: scopeKey,
        loadedPolicyKey: policyKey,
        loading: false,
      }))
    } catch (e) {
      // A superseded request must not overwrite the empty cache already
      // assigned to the newly active workspace. Its queued reload runs below.
      if (activeWorkspaceId() !== workspaceId) {
        reloadRequested = true
        return
      }
      const msg = e instanceof ApiError ? e.message : 'Failed to load models'
      if (catalogContextChanged) {
        // The cross-scope cache was cleared before the request. Keep every
        // catalog-derived value empty on failure; only mark the request as
        // completed so consumers can render their error/empty state.
        set({
          models: [],
          imageModels: [],
          defaultId: '',
          verifyAvailable: false,
          fastAvailable: false,
          fastVision: false,
          error: msg,
          loaded: true,
          loadedScope: scopeKey,
          loadedPolicyKey: policyKey,
          loading: false,
        })
      } else {
        set({ error: msg, loading: false })
      }
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
