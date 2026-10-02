import { useEffect, useMemo, useRef, useState } from 'react'
import { flushSync } from 'react-dom'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Composer } from '@/components/chat/composer'
import { HomeLayout } from '@/components/chat/home-layout'
import { useConversations } from '@/store/conversations'
import { useAuth } from '@/store/auth'
import { useModels } from '@/store/models'
import { useUI } from '@/store/ui'
import { useComposerPrefs } from '@/store/composer-prefs'
import { useWorkspaces } from '@/store/workspaces'
import { conversationsApi } from '@/api'
import {
  clearPendingConversation,
  pendingConversationKey,
  readPendingConversation,
  writePendingConversation,
} from '@/lib/pending-conversation'
import type { Attachment } from '@/types/chat'
import type { ApiConversation } from '@/api/types'
import type { ToolMode } from '@/lib/tool-mode'
import { resolveNewConversationFastMode } from '@/lib/chat-defaults'
import { isModelCatalogReadyForScope } from '@/lib/model-selection'
import { userCan } from '@/lib/user-permissions'
import { workspaceCapabilitiesForScope, workspaceModelPolicyKey } from '@/lib/workspace-permissions'
import { enterOptimisticConversation } from '@/lib/optimistic-conversation-start'
import { runViewTransition } from '@/lib/view-transition'

export default function ChatHome() {
  const navigate = useNavigate()
  const beginOptimisticConversation = useConversations((s) => s.beginOptimisticConversation)
  const sendMessage = useConversations((s) => s.sendMessage)
  const defaultModelId = useModels((s) => s.defaultId)
  const imageModels = useModels((s) => s.imageModels)
  const modelsLoaded = useModels((s) => s.loaded)
  const modelsLoadedScope = useModels((s) => s.loadedScope)
  const modelsLoadedPolicyKey = useModels((s) => s.loadedPolicyKey)
  const modelsLoading = useModels((s) => s.loading)
  const user = useAuth((s) => s.user)
  const workspaceId = useWorkspaces((s) => s.activeId ?? undefined)
  const workspacesLoaded = useWorkspaces((s) => s.loaded)
  const workspacePolicyLoading = useWorkspaces((s) =>
    s.activeId ? s.policyLoading[s.activeId] === true : false,
  )
  const workspaceSwitching = useWorkspaces((s) => s.switching)
  const workspacePolicyError = useWorkspaces((s) =>
    s.activeId ? s.policyErrors[s.activeId] : null,
  )
  const workspacePolicy = useWorkspaces((s) =>
    s.activeId ? s.policies[s.activeId] : undefined,
  )
  const workspaceCaps = workspaceCapabilitiesForScope(workspaceId, workspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: workspaceSwitching,
    policyError: workspacePolicyError,
  })
  const canDraw = userCan(user, 'allow_drawing') && workspaceCaps.drawing
  const workspacePolicyPending = Boolean(
    workspaceId && !workspacePolicy && (!workspacesLoaded || workspacePolicyLoading || workspaceSwitching),
  )
  const modelCatalogReady = isModelCatalogReadyForScope({
    loaded: modelsLoaded,
    loadedScope: modelsLoadedScope,
    loadedPolicyKey: modelsLoadedPolicyKey,
    expectedScope: workspaceId ?? null,
    expectedPolicyKey: workspaceModelPolicyKey(workspaceId, workspacePolicy),
  })
  const clearComposerDraft = useComposerPrefs((s) => s.clearDraft)

  // The home screen has no title to show, so on mobile it drops the layout's
  // standalone brand bar entirely (§ mobile home redesign) — HomeLayout's
  // light floating button replaces it for opening the sidebar drawer.
  useEffect(() => {
    useUI.getState().setPageOwnsTopBar(true)
    return () => useUI.getState().setPageOwnsTopBar(false)
  }, [])

  // §4.20: the sidebar "Draw" entry links here with ?mode=draw to open the
  // composer pre-set to an image model (drawing mode).
  const [searchParams] = useSearchParams()
  const drawRequested = searchParams.get('mode') === 'draw'
  const drawMode = drawRequested && canDraw && imageModels.length > 0
  const draftScope = drawMode ? 'new-draw' : 'new-chat'
  const savedImageModelId =
    typeof user?.settings?.image_model_id === 'string' ? user.settings.image_model_id : ''
  const savedImageModelAvailable = imageModels.some((model) => model.id === savedImageModelId)
  const drawDefault = drawMode ? (savedImageModelAvailable ? savedImageModelId : imageModels[0]?.id ?? '') : ''
  const pendingStorageKey = useMemo(
    () => pendingConversationKey(user?.id, draftScope, workspaceId),
    [draftScope, user?.id, workspaceId],
  )

  // The model the user picks in the composer before the conversation exists.
  // Falls back to the draw default (if any), then the async-loaded chat default,
  // so a new chat honours the picker instead of always using the default model.
  const [pickedModelId, setPickedModelId] = useState<string | null>(null)
  const modelId = pickedModelId ?? (drawDefault || defaultModelId)
  // A user's explicit default model starts new chats in advanced mode. Accounts
  // without one start in 快速 when the deployment provides a fast model. Draw
  // mode (image models) is always advanced.
  const fastAvailable = useModels((s) => s.fastAvailable)
  const [pickedFast, setPickedFast] = useState<boolean | null>(null)
  const [selectedKnowledgeBaseIds, setSelectedKnowledgeBaseIds] = useState<string[]>([])
  const fast =
    !drawMode &&
    (pickedFast ?? resolveNewConversationFastMode(user?.settings, fastAvailable, drawMode))

  useEffect(() => {
    // Keep a draw deep-link alive while the workspace policy and model catalog
    // hydrate. Redirect only once both are known to deny drawing.
    if (!drawRequested || drawMode || workspacePolicyPending) return
    // A previously loaded personal/workspace catalog must not decide the draw
    // route for the newly selected workspace. The model store records the
    // scope represented by its latest response; wait for that scope to finish
    // loading whenever drawing is otherwise permitted.
    const modelScope = workspaceId ?? null
    const modelPolicyKey = workspaceModelPolicyKey(workspaceId, workspacePolicy)
    if (canDraw && (
      modelsLoadedScope !== modelScope ||
      modelsLoadedPolicyKey !== modelPolicyKey ||
      !modelsLoaded ||
      modelsLoading
    )) return
    navigate('/', { replace: true })
  }, [canDraw, drawMode, drawRequested, modelsLoaded, modelsLoadedPolicyKey, modelsLoadedScope, modelsLoading, navigate, workspaceId, workspacePolicy, workspacePolicyPending])

  useEffect(() => {
    setPickedModelId(null)
    setPickedFast(null)
    setSelectedKnowledgeBaseIds([])
    // A fresh home composer always starts from the administrator's deployment
    // default. Conversation-specific overrides are kept under their IDs.
    useComposerPrefs.getState().resetForNewConversation()
  }, [draftScope, workspaceId])

  // When the user attaches a file BEFORE sending, we must create the
  // conversation up front so the upload is scoped + RAG-ingested (§4.11.2).
  // Stash it here so the eventual send reuses the SAME conversation instead of
  // spawning a second empty one. Created OUTSIDE the store on purpose: the
  // draft stays off the sidebar (no "Untitled" row from merely attaching) and
  // only enters the cache when the optimistic send adopts its server id. Its id
  // is persisted so a refresh can reclaim the draft without exposing it in the
  // sidebar.
  const pendingConvRef = useRef<ApiConversation | null>(null)
  const pendingCreateRef = useRef<Promise<ApiConversation | undefined> | null>(null)
  const pendingConsumedRef = useRef(false)
  // Set when the composer drains its last attachment while the lazy create is
  // still in flight — the create then discards its own conversation on landing
  // instead of installing a draft nobody references ("Untitled ghost").
  const draftAbandonedRef = useRef(false)
  const mountedRef = useRef(true)
  // Read synchronously on the first render so Composer starts in its restoring
  // state and cannot submit an attachment-less turn before recovery finishes.
  const [pendingConversationId, setPendingConversationId] = useState<string | undefined>(() =>
    readPendingConversation(pendingStorageKey),
  )
  const pendingStorageKeyRef = useRef(pendingStorageKey)
  pendingStorageKeyRef.current = pendingStorageKey
  // Guards startNew against a double fire (rapid re-click / repeated Enter)
  // spawning duplicate conversations + sends.
  const startedRef = useRef(false)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  // Reclaim an attachment-created conversation after a refresh. Keep the id in
  // durable browser storage instead of deleting it from pagehide: unload-time
  // DELETE requests are inherently racy and used to discard minutes of parsing.
  useEffect(() => {
    pendingConvRef.current = null
    pendingCreateRef.current = null
    pendingConsumedRef.current = false
    const savedID = readPendingConversation(pendingStorageKey)
    setPendingConversationId(savedID)
    if (!savedID) return
    let cancelled = false
    const recovery = (async () => {
      try {
        const loaded = await conversationsApi.get(savedID, { limit: 1 })
        if (loaded.messages.length > 0) {
          clearPendingConversation(pendingStorageKey)
          if (!cancelled) setPendingConversationId(undefined)
          return undefined
        }
        // A send can claim this recovery promise before the request settles.
        // Return the row to that background handoff without reinstalling the
        // draft state or storage entry after the home route has gone away.
        if (!cancelled && !pendingConsumedRef.current) {
          pendingConvRef.current = loaded.conversation
          setPendingConversationId(savedID)
        }
        return loaded.conversation
      } catch {
        clearPendingConversation(pendingStorageKey)
        if (!cancelled) setPendingConversationId(undefined)
        return undefined
      }
    })()
    pendingCreateRef.current = recovery
    void recovery.finally(() => {
      if (pendingCreateRef.current === recovery) pendingCreateRef.current = null
    })
    return () => {
      cancelled = true
    }
  }, [pendingStorageKey])

  // Lazily create (once) the conversation the first attachment will be scoped
  // to. Idempotent: repeat attaches in the same draft reuse the same id — the
  // in-flight promise is memoized so two quick attaches share ONE create. Does
  // NOT navigate — that happens on send, so attaching a file doesn't yank the
  // user off the home screen mid-compose. Returning undefined on failure lets
  // the composer fall back to a scope-less (non-RAG) upload instead of
  // uploading against a fabricated id the server would reject.
  function ensureConversation(): Promise<string | undefined> {
    // A fresh attach revives an abandoned draft scope (see discardDraftConversation).
    draftAbandonedRef.current = false
    if (pendingConvRef.current) return Promise.resolve(pendingConvRef.current.id)
    if (!pendingCreateRef.current) {
      const storageKey = pendingStorageKey
      const creation = (async () => {
        try {
          const created = await conversationsApi.create({
            model_id: modelId || undefined,
            workspace_id: workspaceId,
            fast,
          })
          // A mode/workspace switch invalidates this scope before any upload
          // starts. So does removing every attachment before the create lands
          // (draft abandoned).
          if (draftAbandonedRef.current || pendingStorageKeyRef.current !== storageKey) {
            void conversationsApi.remove(created.id).catch(() => {})
            return undefined
          }
          // Keep a tool-mode choice made before the first attachment attached
          // to the hidden conversation that now owns that upload.
          useComposerPrefs.getState().moveToolModeScope(draftScope, created.id)
          // startNew claimed this in-flight reservation. Hand the row to the
          // optimistic send, but do not recreate a pending-draft storage entry
          // after navigation has already consumed it.
          if (pendingConsumedRef.current) return created
          writePendingConversation(storageKey, created.id)
          if (!mountedRef.current) return created
          pendingConvRef.current = created
          setPendingConversationId(created.id)
          return created
        } catch {
          return undefined
        }
      })()
      pendingCreateRef.current = creation
      void creation.finally(() => {
        if (pendingCreateRef.current === creation) pendingCreateRef.current = null
      })
    }
    return pendingCreateRef.current.then((conversation) => conversation?.id)
  }

  // The composer removed its LAST attachment: the draft conversation existed
  // purely to scope those uploads, so delete it — otherwise it lingers
  // server-side forever and surfaces as an "Untitled" row on the next sidebar
  // load. A create still in flight is flagged instead (it self-discards on
  // landing); a subsequent attach simply creates a fresh scope.
  function discardDraftConversation() {
    if (pendingConsumedRef.current) return
    draftAbandonedRef.current = true
    const pending = pendingConvRef.current
    pendingConvRef.current = null
    clearPendingConversation(pendingStorageKey)
    setPendingConversationId(undefined)
    if (pending) void conversationsApi.remove(pending.id).catch(() => {})
  }

  // A suggestion is placed in the composer for the user to finish; the id makes
  // picking the same suggestion twice still refill a draft edited since.
  const [fillRequest, setFillRequest] = useState<{ text: string; id: number }>()
  const fillIdRef = useRef(0)

  function startNew(
    text: string,
    attachments: Attachment[],
    opts: {
      mode?: 'default' | 'deep-research' | 'canvas'
      params?: Record<string, unknown>
      imageStyleId?: string
      optimizeImagePrompt?: boolean
      verify?: boolean
      toolMode: ToolMode
      webSearch?: boolean
      selectedUserSkillIds?: string[]
      selectedToolIds?: string[]
      fast?: boolean
    },
  ) {
    if (!modelCatalogReady) return
    if (startedRef.current) return
    startedRef.current = true

    // Claim the attachment/recovery reservation synchronously, but wait for it
    // only inside sendMessage after the optimistic route is already visible.
    // A resolved draft is reused so uploaded files keep their original owner;
    // an invalid/failed reservation falls back to creating a fresh conversation.
    const pending = pendingConvRef.current
    const sourceToolModeScope = pendingConversationId ?? draftScope
    const preparedConversation = pending
      ? Promise.resolve(pending)
      : pendingCreateRef.current ?? undefined
    pendingConsumedRef.current = true
    pendingConvRef.current = null
    setPendingConversationId(undefined)
    clearPendingConversation(pendingStorageKey)

    // The route commit runs inside a view transition (the composer glides from
    // the centered home to the thread's dock), whose update callback fires a
    // frame later. Background work waits for that commit so the temp→real id
    // swap below always sees the optimistic thread URL.
    let routeCommitted: Promise<void> = Promise.resolve()
    enterOptimisticConversation({
      createConversation: () =>
        beginOptimisticConversation(
          text,
          modelId,
          opts.fast === true,
          selectedKnowledgeBaseIds,
        ),
      beforeNavigate: (tempId) => {
        clearComposerDraft(draftScope)
        useComposerPrefs.getState().moveToolModeScope(sourceToolModeScope, tempId)
      },
      // Commit the already-loaded thread before background conversation work
      // starts. This is the one transition where a visible response in the same
      // click matters more than React's normal event-batch deferral.
      navigate: (tempId) => {
        routeCommitted = runViewTransition('send', () => flushSync(() => navigate(`/chat/${tempId}`)))
      },
      startBackgroundWork: (tempId) => routeCommitted.then(() => sendMessage({
        conversationId: tempId,
        createFirst: true,
        preparedConversation,
        text,
        modelId,
        attachments,
        mode: opts.mode,
        params: opts.params,
        imageStyleId: opts.imageStyleId,
        optimizeImagePrompt: opts.optimizeImagePrompt,
        verify: opts.verify,
        toolMode: opts.toolMode,
        webSearch: opts.webSearch,
        selectedUserSkillIds: opts.selectedUserSkillIds,
        selectedToolIds: opts.selectedToolIds,
        fast: opts.fast,
        // Swap temp→real id in the URL only if the user is STILL on the optimistic
        // thread. If they navigated elsewhere during the create round-trip, leave
        // them be — the stream still lands in the (re-keyed) real conversation,
        // reachable from the sidebar; yanking them would be worse than a stale URL.
        onConversationId: (realId) => {
          if (window.location.pathname === `/chat/${tempId}`) {
            // Same thread, new id: the layout keeps the mounted thread (and a
            // still-running send transition) instead of remounting it.
            navigate(`/chat/${realId}`, { replace: true, state: { rekeyedFrom: tempId } })
          }
        },
      })),
    })
  }

  const composer = (
    <Composer
      modelId={modelId}
      onModelChange={setPickedModelId}
      fast={fast}
      onFastChange={setPickedFast}
      onSubmit={(text, atts, opts) => void startNew(text, atts, opts)}
      draftScope={draftScope}
      conversationId={pendingConversationId}
      ensureConversationId={ensureConversation}
      onAttachmentsDrained={discardDraftConversation}
      kbIds={selectedKnowledgeBaseIds}
      onKBChange={setSelectedKnowledgeBaseIds}
      autoFocus
      fillRequest={fillRequest}
      menuSide="bottom"
      viewTransitionAnchor
    />
  )

  return (
    <HomeLayout
      variant={drawMode ? 'draw' : 'chat'}
      composer={composer}
      onSuggestion={(prompt) => setFillRequest({ text: prompt, id: ++fillIdRef.current })}
    />
  )
}
