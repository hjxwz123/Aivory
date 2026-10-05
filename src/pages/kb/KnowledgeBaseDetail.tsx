/**
 * KnowledgeBaseDetail — list documents, add one (paste content or upload a
 * file), remove. Status shown live via polling while any doc is non-ready.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Upload, FileText, AlertTriangle, MoreHorizontal, RefreshCw, Search, Share2, Eye, UserPlus, UserMinus, Users, Pencil, Lock, Unlock, Loader2 } from 'lucide-react'
import { ApiError, kbsApi } from '@/api'
import type { ApiDocument, ApiKnowledgeBase, ApiKnowledgeBaseShare, ApiKnowledgeBaseUploader, ApiWorkspaceKnowledgeBaseMemberPermission } from '@/api/types'
import { apiUpload, apiUrl } from '@/api/client'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Badge } from '@/components/ui/badge'
import { ProgressRing } from '@/components/ui/progress-ring'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { ContentHeader } from '@/components/layout/content-header'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'
import { toastStorageQuotaFull } from '@/lib/quota-toast'
import { formatRelativeDate, formatBytes, cn } from '@/lib/utils'
import { fileTypeIcon, fileTypeTileClass } from '@/lib/file-icon'
import { envNum } from '@/lib/env-config'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { useAuth } from '@/store/auth'
import { useArtifactPanel } from '@/store/artifact-panel'
import { useWorkspaces } from '@/store/workspaces'
import { userCan } from '@/lib/user-permissions'
import { workspaceCapabilitiesForScope } from '@/lib/workspace-permissions'
import { Switch } from '@/components/ui/switch'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { knowledgeBaseErrorText, knowledgeBaseOperationErrorText } from '@/lib/knowledge-base-errors'
import { DOCUMENT_PARSER_NOT_CONFIGURED } from '@/lib/document-errors'
import { normalizeExactUserEmailQuery } from '@/lib/user-email-search'

const kbDocStatusPollInterval = envNum('VITE_AIVORY_KB_DOC_STATUS_POLL_INTERVAL', 2200)

export default function KnowledgeBaseDetail() {
  const { t } = useTranslation(['kb', 'common'])
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const user = useAuth((s) => s.user)
  const [kb, setKB] = useState<ApiKnowledgeBase | null>(null)
  const activeWorkspaceId = useWorkspaces((s) => s.activeId)
  // Before the KB is hydrated, use the active scope to prefetch its policy.
  // Once the row arrives, an explicitly personal KB must stay personal even if
  // the user currently has a workspace selected; otherwise a workspace upload
  // policy could hide or expose controls on the wrong library.
  const kbWorkspaceId = kb
    ? (kb.workspace_id || undefined)
    : (activeWorkspaceId || undefined)
  const workspacePolicy = useWorkspaces((s) =>
    kbWorkspaceId ? s.policies[kbWorkspaceId] : undefined,
  )
  const loadWorkspacePolicy = useWorkspaces((s) => s.loadPolicy)
  const workspacesLoaded = useWorkspaces((s) => s.loaded)
  const workspacePolicyLoading = useWorkspaces((s) =>
    kbWorkspaceId ? s.policyLoading[kbWorkspaceId] === true : false,
  )
  const workspaceSwitching = useWorkspaces((s) => s.switching)
  const workspacePolicyError = useWorkspaces((s) =>
    kbWorkspaceId ? s.policyErrors[kbWorkspaceId] : null,
  )
  const workspaceCaps = workspaceCapabilitiesForScope(kbWorkspaceId, workspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: workspaceSwitching,
    policyError: workspacePolicyError,
  })
  const workspacePolicyPending = Boolean(
    kbWorkspaceId && !workspacePolicy && (!workspacesLoaded || workspacePolicyLoading || workspaceSwitching),
  )
  const canUseWorkspaceKnowledgeBases = workspaceCaps.knowledgeBases
  // §workspace RBAC: visibility management needs the creator id or admin role.
  const workspaceRole = useWorkspaces((s) => (kb?.workspace_id ? s.workspaces.find((w) => w.id === kb.workspace_id)?.role : undefined))
  const [kbVisibilityBusy, setKBVisibilityBusy] = useState(false)
  async function toggleKBVisibility() {
    if (!kb || !canUseKnowledgeBases || kbVisibilityBusy) return
    setKBVisibilityBusy(true)
    try {
      const updated = await kbsApi.update(kb.id, { is_public: kb.is_public === false })
      setKB(updated)
      toast.success(updated.is_public
        ? t('kb:detail.visibilityShared', { defaultValue: 'Knowledge base is now shared with the workspace.' })
        : t('kb:detail.visibilityPrivate', { defaultValue: 'Knowledge base is now private to you and workspace admins.' }))
    } catch {
      toast.error(t('kb:detail.visibilityFailed', { defaultValue: 'Could not update the visibility.' }))
    } finally {
      setKBVisibilityBusy(false)
    }
  }
  const canUseKnowledgeBases = userCan(user, 'allow_knowledge_bases') && canUseWorkspaceKnowledgeBases
  const canShareKnowledgeBases = userCan(user, 'allow_knowledge_base_sharing')
  const canUploadFiles = userCan(user, 'allow_file_upload') &&
    workspaceCaps.fileUpload
  const [docs, setDocs] = useState<ApiDocument[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState({ filename: '', content: '' })
  const [uploading, setUploading] = useState(false)
  const [uploadJob, setUploadJob] = useState<{ name: string; progress: number; phase: 'uploading' | 'processing' } | null>(null)
  const [tab, setTab] = useState<'paste' | 'upload'>('paste')
  const fileInput = useRef<HTMLInputElement>(null)
  // Delete the whole KB (documents + vectors; unbinds it from conversations).
  const [confirmDeleteKB, setConfirmDeleteKB] = useState(false)
  const [deletingKB, setDeletingKB] = useState(false)
  const deletingKBRef = useRef(false)
  // Per-row delete guard + single-flight guard for the paste-tab Save.
  const [busyDoc, setBusyDoc] = useState<{ id: string; action: 'retry' | 'rename' | 'delete' } | null>(null)
  const busyDocRef = useRef<{ id: string; action: 'retry' | 'rename' | 'delete' } | null>(null)
  const docOperationEpochRef = useRef(0)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const [uploaderID, setUploaderID] = useState('all')
  const [uploaders, setUploaders] = useState<ApiKnowledgeBaseUploader[]>([])
  const [renameDoc, setRenameDoc] = useState<ApiDocument | null>(null)
  const [renameFilename, setRenameFilename] = useState('')
  // Removing a document drops its index data; ask once instead of deleting on
  // a single stray click in a dense action row.
  const [confirmRemoveDoc, setConfirmRemoveDoc] = useState<ApiDocument | null>(null)
  const [shareOpen, setShareOpen] = useState(false)
  const [workspaceMembersOpen, setWorkspaceMembersOpen] = useState(false)
  const [accessRevoked, setAccessRevoked] = useState(false)
  const loadEpochRef = useRef(0)
  const unfilteredSummaryRef = useRef<DocumentSummary | null>(null)
  const uploadControllerRef = useRef<AbortController | null>(null)
  const uploadAttemptRef = useRef(0)
  const pasteControllerRef = useRef<AbortController | null>(null)
  const canUploadRef = useRef(false)
  canUploadRef.current = Boolean(canUseKnowledgeBases && canUploadFiles && kb?.can_upload)

  useEffect(() => {
    const handle = window.setTimeout(() => setDebouncedSearch(search), 200)
    return () => window.clearTimeout(handle)
  }, [search])

  async function deleteKB() {
    if (!id || !canUseKnowledgeBases || deletingKBRef.current) return
    deletingKBRef.current = true
    setDeletingKB(true)
    try {
      await kbsApi.remove(id)
      toast.success(t('kb:deleted', { defaultValue: 'Knowledge base deleted' }))
      navigate('/kb')
    } catch (e) {
      handleOperationError(e, t('common:common.error'))
    } finally {
      deletingKBRef.current = false
      setDeletingKB(false)
    }
  }

  // load(silent) refreshes the KB + its docs. Only the FIRST load toggles the
  // page-level skeleton; the background status poll passes silent=true so the
  // list refreshes in place without flipping the whole page to "loading…"
  // every ~2s (which read as a flicker).
  const load = useCallback(async (silent = false) => {
    if (!id) return
    const epoch = ++loadEpochRef.current
    if (!silent) {
      setLoading(true)
      setLoadError('')
    }
    try {
      const [current, d, uploaderResult] = await Promise.all([
        kbsApi.get(id),
        kbsApi.listDocs(id, {
          search: debouncedSearch,
          uploaded_by_user_id: uploaderID === 'all' ? undefined : uploaderID,
        }),
        kbsApi.uploaders(id).then(
          (rows) => ({ ok: true as const, rows }),
          (error: unknown) => ({ ok: false as const, error }),
        ),
      ])
      if (
        !uploaderResult.ok &&
        uploaderResult.error instanceof ApiError &&
        (uploaderResult.error.status === 403 || uploaderResult.error.status === 404)
      ) {
        throw uploaderResult.error
      }
      if (epoch === loadEpochRef.current) {
        setKB(current)
        setDocs(d)
        // The uploader list is auxiliary filter metadata. A transient failure
        // must not replace a usable knowledge-base page with a fatal error.
        if (uploaderResult.ok) setUploaders(uploaderResult.rows)
        setAccessRevoked(false)
        setLoadError('')
      }
    } catch (e) {
      if (epoch === loadEpochRef.current && e instanceof ApiError && (e.status === 403 || e.status === 404)) {
        setKB(null)
        setDocs([])
        setUploaders([])
        setAccessRevoked(true)
        setOpen(false)
        setShareOpen(false)
        setWorkspaceMembersOpen(false)
        useArtifactPanel.getState().close()
        setRenameDoc(null)
        setRenameFilename('')
        setConfirmRemoveDoc(null)
        setConfirmDeleteKB(false)
        docOperationEpochRef.current += 1
        busyDocRef.current = null
        setBusyDoc(null)
        setLoadError('')
      }
      // A failed background poll shouldn't nag the user — only surface errors
      // on an explicit (non-silent) load.
      if (
        !silent &&
        epoch === loadEpochRef.current &&
        (!(e instanceof ApiError) || (e.status !== 403 && e.status !== 404))
      ) {
        const message = knowledgeBaseErrorText(t, e, t('common:common.error'))
        setLoadError(message)
        toast.error(message)
      }
    } finally {
      if (!silent && epoch === loadEpochRef.current) setLoading(false)
    }
  }, [debouncedSearch, id, t, uploaderID])

  // A direct detail link can resolve a KB outside the currently selected
  // workspace. Hydrate that workspace's policy once the KB identity is known;
  // the capability helper remains permissive while the request is in flight to
  // match the rest of the workspace UI's rollout-compatible behavior.
  useEffect(() => {
    if (!kbWorkspaceId) return
    const state = useWorkspaces.getState()
    if (state.policies[kbWorkspaceId] || state.policyLoading[kbWorkspaceId]) return
    void loadWorkspacePolicy(kbWorkspaceId)
  }, [kbWorkspaceId, loadWorkspacePolicy])

  const handleOperationError = useCallback((error: unknown, fallback: string) => {
    toast.error(knowledgeBaseOperationErrorText(t, error, fallback))
    if (!(error instanceof ApiError) || (error.status !== 403 && error.status !== 404)) return

    // A permission may change while any of these surfaces is open. Close all
    // mutation UI immediately and reconcile against the server before the user
    // can submit another stale action.
    setOpen(false)
    setShareOpen(false)
    setWorkspaceMembersOpen(false)
    useArtifactPanel.getState().close()
    setRenameDoc(null)
    setRenameFilename('')
    setConfirmRemoveDoc(null)
    setConfirmDeleteKB(false)
    docOperationEpochRef.current += 1
    busyDocRef.current = null
    setBusyDoc(null)
    void load()
  }, [load, t])

  // Document previews open in the shared right-edge Artifact panel (never a
  // centered dialog), so the knowledge-base list stays visible while the bytes
  // load. A 403/404 keeps its old meaning: access changed under us.
  const handlePreviewLoadError = useCallback((status?: number) => {
    if (status !== 403 && status !== 404) return
    useArtifactPanel.getState().close()
    toast.error(t('kb:permissionChanged'))
    void load()
  }, [load, t])

  function previewDocument(doc: ApiDocument) {
    useArtifactPanel.getState().openArtifact({
      type: 'file',
      name: doc.filename,
      kind: 'other',
      url: apiUrl(`/documents/${encodeURIComponent(doc.id)}/content`),
      authenticated: true,
      onLoadError: handlePreviewLoadError,
    })
  }

  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (event.kind === 'account' || event.kind === 'workspace' || event.kind === 'knowledge-base') {
          if (workspacePolicyPending) return
          void load()
        }
      }),
    [load, workspacePolicyPending],
  )

  useEffect(() => {
    if (workspacePolicyPending) {
      loadEpochRef.current += 1
      setLoading(true)
      setLoadError('')
      return
    }
    if (!canUseKnowledgeBases) {
      setLoading(false)
      setKB(null)
      setDocs([])
      setLoadError('')
      return
    }
    void load()
  }, [canUseKnowledgeBases, load, workspacePolicyPending])

  useEffect(() => {
    if (!canUseKnowledgeBases || !canUploadFiles || !kb?.can_upload) {
      uploadAttemptRef.current += 1
      uploadControllerRef.current?.abort()
      uploadControllerRef.current = null
      pasteControllerRef.current?.abort()
      pasteControllerRef.current = null
      savingRef.current = false
      setSaving(false)
      setUploading(false)
      setUploadJob(null)
      setOpen(false)
    }
    if (!canUseKnowledgeBases || !kb) {
      docOperationEpochRef.current += 1
      busyDocRef.current = null
      setBusyDoc(null)
    }
    if (!canUseKnowledgeBases || !canShareKnowledgeBases || !kb?.can_share || kb.workspace_id) {
      setShareOpen(false)
    }
    if (!canUseKnowledgeBases || !kb?.workspace_id || !kb.can_manage_members) {
      setWorkspaceMembersOpen(false)
    }
    if (!kb?.can_delete) setConfirmDeleteKB(false)
    if (renameDoc && !docs.some((doc) => doc.id === renameDoc.id && doc.can_delete)) {
      setRenameDoc(null)
      setRenameFilename('')
    }
    if (confirmRemoveDoc && !docs.some((doc) => doc.id === confirmRemoveDoc.id && doc.can_delete)) {
      setConfirmRemoveDoc(null)
    }
  }, [canShareKnowledgeBases, canUploadFiles, canUseKnowledgeBases, confirmRemoveDoc, docs, kb, renameDoc])

  useEffect(() => {
    if (
      uploaderID !== 'all' &&
      !loading &&
      !uploaders.some((uploader) => uploader.user_id === uploaderID)
    ) {
      setUploaderID('all')
    }
  }, [loading, uploaderID, uploaders])

  // Poll silently while any document is mid-pipeline.
  useEffect(() => {
    if (!id) return
    const pending = docs.some(
      (d) => d.status === 'pending' || d.status === 'parsing' || d.status === 'embedding',
    )
    if (!pending) return
    const handle = setInterval(() => void load(true), kbDocStatusPollInterval)
    return () => clearInterval(handle)
  }, [docs, id, load])

  async function addPasted() {
    if (!id || !canUploadRef.current) return
    if (!draft.filename.trim()) {
      toast.error(t('kb:dialog.nameRequired'))
      return
    }
    if (savingRef.current) return
    const controller = new AbortController()
    pasteControllerRef.current?.abort()
    pasteControllerRef.current = controller
    savingRef.current = true
    setSaving(true)
    try {
      await kbsApi.addDoc(id, { filename: draft.filename, content: draft.content }, controller.signal)
      if (pasteControllerRef.current !== controller || !canUploadRef.current) return
      toast.success(t('kb:detail.uploaded'))
      setOpen(false)
      setDraft({ filename: '', content: '' })
      await load()
    } catch (e) {
      if (e instanceof Error && e.name === 'AbortError') {
        return
      } else if (e instanceof ApiError && e.status === 507) {
        toastStorageQuotaFull(navigate)
      } else {
        handleOperationError(e, t('kb:detail.uploadFailed'))
      }
    } finally {
      if (pasteControllerRef.current === controller) {
        pasteControllerRef.current = null
        savingRef.current = false
        setSaving(false)
      }
    }
  }

  async function uploadFiles(files: FileList | null) {
    if (!files || !id || !canUploadRef.current) return
    const selected = Array.from(files)
    if (!selected.length) return
    const attempt = uploadAttemptRef.current + 1
    uploadAttemptRef.current = attempt
    const controller = new AbortController()
    uploadControllerRef.current?.abort()
    uploadControllerRef.current = controller
    setUploading(true)
    try {
      for (const file of selected) {
        if (!canUploadRef.current || uploadAttemptRef.current !== attempt) return
        setUploadJob({ name: file.name, progress: 0, phase: 'uploading' })
        const form = new FormData()
        form.append('file', file)
        await apiUpload<ApiDocument>(`/kbs/${encodeURIComponent(id)}/documents`, form, {
          signal: controller.signal,
          onProgress: (progress) => {
            if (
              typeof progress.percent !== 'number' ||
              !canUploadRef.current ||
              uploadAttemptRef.current !== attempt
            ) return
            setUploadJob({ name: file.name, progress: progress.percent, phase: 'uploading' })
          },
        })
        if (!canUploadRef.current || uploadAttemptRef.current !== attempt) return
        setUploadJob({ name: file.name, progress: 100, phase: 'processing' })
      }
      if (!canUploadRef.current || uploadAttemptRef.current !== attempt) return
      toast.success(t('kb:detail.uploaded'))
      setOpen(false)
      await load()
    } catch (e) {
      if (e instanceof ApiError && e.status === 507) {
        toastStorageQuotaFull(navigate)
      } else if (!(e instanceof Error && e.name === 'AbortError')) {
        handleOperationError(e, t('kb:detail.uploadFailed'))
      }
    } finally {
      if (uploadControllerRef.current === controller) uploadControllerRef.current = null
      if (uploadAttemptRef.current === attempt) {
        setUploading(false)
        setUploadJob(null)
      }
    }
  }

  useEffect(
    () => () => {
      uploadAttemptRef.current += 1
      uploadControllerRef.current?.abort()
      uploadControllerRef.current = null
      pasteControllerRef.current?.abort()
      pasteControllerRef.current = null
    },
    [],
  )

  async function remove(d: ApiDocument) {
    if (!id || !canUseKnowledgeBases || busyDocRef.current) return
    const operation = { id: d.id, action: 'delete' as const }
    const operationEpoch = ++docOperationEpochRef.current
    busyDocRef.current = operation
    setBusyDoc(operation)
    try {
      await kbsApi.removeDoc(id, d.id)
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
      setConfirmRemoveDoc(null)
      toast.success(t('kb:detail.removed'))
      await load()
    } catch (e) {
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
      handleOperationError(e, t('common:common.error'))
    } finally {
      if (docOperationEpochRef.current === operationEpoch && busyDocRef.current === operation) {
        busyDocRef.current = null
        setBusyDoc(null)
      }
    }
  }

  async function retry(d: ApiDocument) {
    if (!id || !canUseKnowledgeBases || busyDocRef.current || d.status !== 'failed') return
    const operation = { id: d.id, action: 'retry' as const }
    const operationEpoch = ++docOperationEpochRef.current
    busyDocRef.current = operation
    setBusyDoc(operation)
    try {
      await kbsApi.retryDoc(id, d.id)
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
      setDocs((current) => current.map((doc) => (
        doc.id === d.id ? { ...doc, status: 'pending', error: '', error_code: undefined, chunk_count: 0 } : doc
      )))
      toast.success(t('kb:detail.retryQueued'))
      await load(true)
    } catch (e) {
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
      handleOperationError(e, t('kb:detail.retryFailed'))
    } finally {
      if (docOperationEpochRef.current === operationEpoch && busyDocRef.current === operation) {
        busyDocRef.current = null
        setBusyDoc(null)
      }
    }
  }

  function beginRename(d: ApiDocument) {
    setRenameDoc(d)
    setRenameFilename(d.filename)
  }

  async function saveRename() {
    const filename = renameFilename.trim()
    if (!id || !canUseKnowledgeBases || !renameDoc || !filename || busyDocRef.current) return
    const operation = { id: renameDoc.id, action: 'rename' as const }
    const operationEpoch = ++docOperationEpochRef.current
    busyDocRef.current = operation
    setBusyDoc(operation)
    try {
      await kbsApi.renameDoc(id, renameDoc.id, filename)
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
      setDocs((current) => current.map((doc) => doc.id === renameDoc.id ? { ...doc, filename } : doc))
      setRenameDoc(null)
      setRenameFilename('')
      toast.success(t('kb:detail.renamed', { defaultValue: 'File renamed' }))
    } catch (error) {
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
      handleOperationError(error, t('kb:detail.renameFailed', { defaultValue: 'Could not rename file' }))
    } finally {
      if (docOperationEpochRef.current === operationEpoch && busyDocRef.current === operation) {
        busyDocRef.current = null
        setBusyDoc(null)
      }
    }
  }

  if (workspacePolicyPending) {
    return <KnowledgeBaseDetailSkeleton label={t('common:common.loading')} backLabel={t('kb:title')} />
  }

  if (!canUseKnowledgeBases) {
    return (
      <div className="flex-1 grid place-items-center p-10">
        <EmptyState
          icon={<FileText size={20} aria-hidden />}
          title={t('kb:groupPermissionTitle', { defaultValue: 'Knowledge bases unavailable' })}
          description={
            canUseWorkspaceKnowledgeBases
              ? t('kb:groupPermissionRequired', { defaultValue: 'Your user group does not have knowledge-base access.' })
              : t('kb:workspaceDisabledBody', { defaultValue: 'The workspace administrator has disabled knowledge bases.' })
          }
          action={<Button onClick={() => navigate('/')}>{t('common:actions.back')}</Button>}
        />
      </div>
    )
  }

  if (loadError && !loading) {
    return (
      <div className="flex-1 grid place-items-center p-10">
        <EmptyState
          icon={<AlertTriangle size={20} aria-hidden />}
          title={t('kb:detail.loadFailedTitle', { defaultValue: 'Could not load this knowledge base' })}
          description={loadError}
          action={<Button onClick={() => void load()}>{t('common:actions.tryAgain')}</Button>}
        />
      </div>
    )
  }

  if (!kb && !loading) {
    return (
      <div className="flex-1 grid place-items-center p-10">
        <EmptyState
          title={accessRevoked
            ? t('kb:accessRevokedTitle', { defaultValue: 'Knowledge-base access removed' })
            : t('kb:emptyTitle')}
          description={accessRevoked
            ? t('kb:accessRevokedBody', { defaultValue: 'This knowledge base was deleted or is no longer shared with you.' })
            : t('kb:emptyBody')}
          action={<Button onClick={() => navigate('/kb')}>{t('common:actions.back')}</Button>}
        />
      </div>
    )
  }

  // First load: keep the page frame (back link, title slot, summary, toolbar,
  // rows) instead of a bare "…" title, so the real content lands in place.
  if (!kb) {
    return <KnowledgeBaseDetailSkeleton label={t('common:common.loading')} backLabel={t('kb:title')} />
  }

  const filtersActive = Boolean(debouncedSearch.trim()) || uploaderID !== 'all'
  // The summary describes the whole library. While a filter narrows the list,
  // keep the last unfiltered totals instead of shrinking the headline numbers.
  if (!filtersActive) unfilteredSummaryRef.current = documentSummary(docs)
  const summary = unfilteredSummaryRef.current ?? documentSummary(docs)
  // An empty library is just its empty state: no all-zero summary and no
  // search box over nothing.
  const libraryEmpty = !loading && !filtersActive && docs.length === 0

  const canUpload = Boolean(kb.can_upload && canUploadFiles)
  const canShare = Boolean(kb.can_share && !kb.workspace_id && canShareKnowledgeBases)
  const canToggleVisibility = Boolean(kb.workspace_id && (kb.user_id === user?.id || workspaceRole === 'admin'))
  const canManageMembers = Boolean(kb.workspace_id && kb.can_manage_members)
  // Secondary library actions live in one overflow menu so the bar keeps a
  // single primary command (upload) and fits on phones.
  const hasOverflow = canToggleVisibility || canManageMembers || Boolean(kb.can_delete)
  const accessLabel = kb.access_role === 'read'
    ? t('kb:access.read', { defaultValue: 'Read only' })
    : kb.access_role === 'write'
      ? t('kb:access.write', { defaultValue: 'Can upload' })
      : kb.access_role === 'workspace'
        ? t('kb:access.workspace', { defaultValue: 'Workspace' })
        : t('kb:access.owner', { defaultValue: 'Owner' })

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={kb.name}
        backTo="/kb"
        backLabel={t('kb:title')}
        actions={
          <div className="flex items-center gap-1.5 sm:gap-2">
            {canShare ? (
              <Button
                variant="secondary"
                size="sm"
                className="max-sm:size-9 max-sm:px-0"
                aria-label={t('kb:share.action', { defaultValue: 'Share' })}
                leadingIcon={<Share2 size={15} aria-hidden />}
                onClick={() => setShareOpen(true)}
              >
                <span className="max-sm:sr-only">{t('kb:share.action', { defaultValue: 'Share' })}</span>
              </Button>
            ) : null}
            {canUpload ? (
              <Button
                size="sm"
                leadingIcon={<Plus size={15} aria-hidden />}
                onClick={() => setOpen(true)}
              >
                {t('kb:detail.uploadButton')}
              </Button>
            ) : null}
            {hasOverflow ? (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    aria-label={t('common:actions.more', { defaultValue: 'More' })}
                    className="inline-flex size-9 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:size-8"
                  >
                    <MoreHorizontal size={16} aria-hidden />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="min-w-[200px]">
                  {canToggleVisibility ? (
                    <DropdownMenuItem disabled={kbVisibilityBusy} onSelect={() => void toggleKBVisibility()}>
                      {kb.is_public !== false ? <Lock size={13} aria-hidden /> : <Unlock size={13} aria-hidden />}
                      {kb.is_public !== false
                        ? t('kb:detail.makePrivate', { defaultValue: 'Make private' })
                        : t('kb:detail.makeShared', { defaultValue: 'Share with workspace' })}
                    </DropdownMenuItem>
                  ) : null}
                  {canManageMembers ? (
                    <DropdownMenuItem onSelect={() => setWorkspaceMembersOpen(true)}>
                      <Users size={13} aria-hidden />
                      {t('kb:workspaceMembers.action', { defaultValue: 'Member permissions' })}
                    </DropdownMenuItem>
                  ) : null}
                  {kb.can_delete && (canToggleVisibility || canManageMembers) ? <DropdownMenuSeparator /> : null}
                  {kb.can_delete ? (
                    <DropdownMenuItem destructive onSelect={() => setConfirmDeleteKB(true)}>
                      <Trash2 size={13} aria-hidden /> {t('kb:deleteAction', { defaultValue: 'Delete knowledge base' })}
                    </DropdownMenuItem>
                  ) : null}
                </DropdownMenuContent>
              </DropdownMenu>
            ) : null}
          </div>
        }
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 pb-24 pt-5 sm:px-8 sm:pt-6">
          {kb.description ? (
            <p className="max-w-[65ch] text-[14px] leading-6 text-[var(--color-fg-muted)]">{kb.description}</p>
          ) : null}
          <div className={cn('flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-[var(--color-fg-subtle)]', kb.description && 'mt-2')}>
            <Badge size="xs" variant="neutral">{accessLabel}</Badge>
            {kb.owner_name ? (
              <span>{t('kb:access.ownerName', { name: kb.owner_name, defaultValue: 'Owner: {{name}}' })}</span>
            ) : null}
          </div>

          {libraryEmpty ? null : (
            <DocumentSummaryStrip summary={summary} loading={loading && !unfilteredSummaryRef.current} t={t} />
          )}

          <section className={libraryEmpty ? 'mt-2' : 'mt-6'} aria-label={t('kb:detail.summary.documents')}>
            {libraryEmpty ? null : (
            <div className="flex flex-col gap-2.5 border-b border-[var(--color-divider)] pb-3 sm:flex-row sm:items-center">
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                leadingIcon={<Search size={14} aria-hidden />}
                placeholder={t('kb:detail.searchFiles', { defaultValue: 'Search files by name' })}
                aria-label={t('kb:detail.searchFiles', { defaultValue: 'Search files by name' })}
                wrapperClassName="w-full sm:max-w-xs"
              />
              {uploaders.length > 1 || uploaderID !== 'all' ? (
                <Select value={uploaderID} onValueChange={setUploaderID}>
                  <SelectTrigger className="sm:w-52" aria-label={t('kb:detail.filterUploader', { defaultValue: 'Filter by uploader' })}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">{t('kb:detail.allUploaders', { defaultValue: 'All uploaders' })}</SelectItem>
                    {uploaders.map((uploader) => (
                      <SelectItem key={uploader.user_id} value={uploader.user_id}>
                        {uploader.name || uploader.email || uploader.user_id}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : null}
              <span className="text-[12.5px] tabular-nums text-[var(--color-fg-subtle)] sm:ml-auto">
                {loading ? null : t('kb:detail.docCount', { count: docs.length })}
              </span>
            </div>
            )}

            {loading ? (
              <DocumentRowsSkeleton label={t('common:common.loading')} />
            ) : docs.length === 0 ? (
              <EmptyState
                className="mt-6"
                icon={filtersActive ? <Search size={20} aria-hidden /> : <FileText size={20} aria-hidden />}
                title={filtersActive
                  ? t('kb:detail.noMatches', { defaultValue: 'No matching files' })
                  : t('kb:detail.noDocs')}
                description={filtersActive
                  ? t('kb:detail.noMatchesBody', { defaultValue: 'Try another file name or uploader.' })
                  : t('kb:detail.noDocsBody')}
                action={!filtersActive && canUpload
                  ? (
                    <Button variant="secondary" leadingIcon={<Upload size={15} aria-hidden />} onClick={() => setOpen(true)}>
                      {t('kb:detail.uploadButton')}
                    </Button>
                  )
                  : undefined}
              />
            ) : (
              <ul className="mt-1 flex flex-col divide-y divide-[var(--color-divider)]">
                {docs.map((d) => (
                  <DocumentRow
                    key={d.id}
                    doc={d}
                    busy={busyDoc}
                    onPreview={() => previewDocument(d)}
                    onRetry={() => void retry(d)}
                    onRename={() => beginRename(d)}
                    onRemove={() => setConfirmRemoveDoc(d)}
                    t={t}
                  />
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>

      <Dialog
        open={open && Boolean(kb?.can_upload) && canUploadFiles}
        onOpenChange={(next) => {
          if (!next && (saving || uploading)) return
          setOpen(next)
        }}
      >
        <DialogContent size="md" closeDisabled={saving || uploading}>
          <DialogHeader>
            <DialogTitle>{t('kb:detail.uploadButton')}</DialogTitle>
            <DialogDescription>{t('kb:detail.noDocsBody')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <Tabs value={tab} onValueChange={(v) => setTab(v as 'paste' | 'upload')}>
              <TabsList className="mb-4">
                <TabsTrigger value="paste">
                  <FileText size={12} aria-hidden /> {t('kb:detail.tabPaste')}
                </TabsTrigger>
                <TabsTrigger value="upload">
                  <Upload size={12} aria-hidden /> {t('kb:detail.tabUpload')}
                </TabsTrigger>
              </TabsList>
              <TabsContent value="paste">
                <div className="grid gap-4">
                  <Field label={t('kb:detail.tableHeaders.filename')} htmlFor="doc-name">
                    <Input
                      id="doc-name"
                      value={draft.filename}
                      onChange={(e) => setDraft({ ...draft, filename: e.target.value })}
                      placeholder="notes.md"
                    />
                  </Field>
                  <Field label={t('kb:detail.contentLabel')} htmlFor="doc-body">
                    <Textarea
                      id="doc-body"
                      rows={10}
                      value={draft.content}
                      onChange={(e) => setDraft({ ...draft, content: e.target.value })}
                    />
                  </Field>
                </div>
              </TabsContent>
              <TabsContent value="upload">
                <input
                  ref={fileInput}
                  type="file"
                  hidden
                  multiple
                  onChange={(e) => {
                    void uploadFiles(e.currentTarget.files)
                    e.currentTarget.value = ''
                  }}
                />
                <button
                  type="button"
                  disabled={uploading}
                  className={cn(
                    'w-full rounded-[14px] border border-dashed border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] p-10 text-center interactive',
                    'cursor-pointer hover:border-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    'disabled:cursor-not-allowed disabled:opacity-70',
                  )}
                  onClick={() => fileInput.current?.click()}
                >
                  {uploading ? (
                    <ProgressRing
                      value={uploadJob?.progress ?? 0}
                      size={44}
                      strokeWidth={4}
                      showValue
                      label={
                        uploadJob?.phase === 'processing'
                          ? t('kb:detail.uploadProcessing', { defaultValue: 'Parsing / indexing…' })
                          : t('kb:detail.uploadProgress', {
                              defaultValue: 'Uploading {{percent}}%',
                              percent: uploadJob?.progress ?? 0,
                            })
                      }
                      className="mx-auto text-[var(--color-accent)]"
                    />
                  ) : (
                    <Upload size={24} className="mx-auto text-[var(--color-fg-subtle)]" aria-hidden />
                  )}
                  <p className="mt-3 text-[var(--color-fg-muted)] text-sm">
                    {uploading && uploadJob
                      ? uploadJob.phase === 'processing'
                        ? t('kb:detail.uploadProcessing', { defaultValue: 'Parsing / indexing…' })
                        : t('kb:detail.uploadProgress', {
                            defaultValue: 'Uploading {{percent}}%',
                            percent: Math.round(uploadJob.progress),
                          })
                      : t('kb:detail.clickToChoose')}
                  </p>
                  {uploading && uploadJob ? (
                    <p className="mt-1 truncate text-xs text-[var(--color-fg-subtle)]">{uploadJob.name}</p>
                  ) : null}
                </button>
              </TabsContent>
            </Tabs>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={saving || uploading}>
              {t('common:actions.cancel')}
            </Button>
            {tab === 'paste' ? (
              <Button loading={saving} onClick={() => void addPasted()}>{t('common:actions.save')}</Button>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={renameDoc !== null}
        onOpenChange={(next) => {
          if (next || busyDoc?.action === 'rename') return
          setRenameDoc(null)
          setRenameFilename('')
        }}
      >
        <DialogContent size="sm" closeDisabled={busyDoc?.action === 'rename'}>
          <DialogHeader>
            <DialogTitle>{t('kb:detail.renameFileTitle', { defaultValue: 'Rename file' })}</DialogTitle>
            <DialogDescription>
              {t('kb:detail.renameFileDescription', { defaultValue: 'Change the name shown in this knowledge base.' })}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <Field label={t('kb:detail.tableHeaders.filename')} htmlFor="kb-document-filename">
              <Input
                id="kb-document-filename"
                value={renameFilename}
                maxLength={255}
                autoFocus
                onChange={(event) => setRenameFilename(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter' && renameFilename.trim()) void saveRename()
                }}
              />
            </Field>
          </DialogBody>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={busyDoc?.action === 'rename'}
              onClick={() => {
                setRenameDoc(null)
                setRenameFilename('')
              }}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              loading={busyDoc?.action === 'rename'}
              disabled={!renameFilename.trim()}
              onClick={() => void saveRename()}
            >
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={confirmRemoveDoc !== null}
        onOpenChange={(next) => {
          if (next || busyDoc?.action === 'delete') return
          setConfirmRemoveDoc(null)
        }}
      >
        <DialogContent size="sm" closeDisabled={busyDoc?.action === 'delete'}>
          <DialogHeader>
            <DialogTitle>{t('kb:detail.confirmRemoveTitle')}</DialogTitle>
            <DialogDescription>
              {t('kb:detail.confirmRemoveBody', { name: confirmRemoveDoc?.filename ?? '' })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={busyDoc?.action === 'delete'}
              onClick={() => setConfirmRemoveDoc(null)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              loading={busyDoc?.action === 'delete'}
              onClick={() => { if (confirmRemoveDoc) void remove(confirmRemoveDoc) }}
            >
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {kb?.can_share && !kb.workspace_id && canShareKnowledgeBases ? (
        <KnowledgeBaseShareDialog
          key={kb.id}
          kb={kb}
          open={shareOpen}
          onOpenChange={setShareOpen}
          onOperationError={handleOperationError}
          t={t}
        />
      ) : null}

      {kb?.workspace_id && kb.can_manage_members ? (
        <WorkspaceKnowledgeBaseMembersDialog
          key={kb.id}
          kb={kb}
          open={workspaceMembersOpen}
          onOpenChange={setWorkspaceMembersOpen}
          onOperationError={handleOperationError}
          t={t}
        />
      ) : null}

      <Dialog open={confirmDeleteKB} onOpenChange={(o) => { if (!o && !deletingKBRef.current) setConfirmDeleteKB(false) }}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('kb:deleteTitle', { defaultValue: 'Delete knowledge base?' })}</DialogTitle>
            <DialogDescription>
              {t('kb:deleteBody', {
                name: kb?.name ?? '',
                defaultValue:
                  'This permanently deletes “{{name}}” and all its documents and index data. Conversations that reference it will be unlinked. This cannot be undone.',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDeleteKB(false)} disabled={deletingKB}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deletingKB} onClick={() => void deleteKB()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function KnowledgeBaseShareDialog({
  kb,
  open,
  onOpenChange,
  onOperationError,
  t,
}: {
  kb: ApiKnowledgeBase
  open: boolean
  onOpenChange: (open: boolean) => void
  onOperationError: (error: unknown, fallback: string) => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  const [shares, setShares] = useState<ApiKnowledgeBaseShare[]>([])
  const [candidates, setCandidates] = useState<ApiKnowledgeBaseShare[]>([])
  const [query, setQuery] = useState('')
  const [sharesLoading, setSharesLoading] = useState(false)
  const [sharesLoadFailed, setSharesLoadFailed] = useState(false)
  const [sharesLoadAttempt, setSharesLoadAttempt] = useState(0)
  const [candidatesLoading, setCandidatesLoading] = useState(false)
  const [candidatesLoadFailed, setCandidatesLoadFailed] = useState(false)
  const [candidatesLoadAttempt, setCandidatesLoadAttempt] = useState(0)
  const [busyID, setBusyID] = useState<string | null>(null)
  const busyIDRef = useRef<string | null>(null)
  const searchEpochRef = useRef(0)
  const operationEpochRef = useRef(0)
  const openRef = useRef(open)
  openRef.current = open
  const normalizedEmailQuery = normalizeExactUserEmailQuery(query)

  // Sharing can be changed from another tab or by a workspace/account
  // permission update. Keep an open dialog authoritative instead of leaving
  // stale roles and removed users actionable until it is reopened.
  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (!openRef.current) return
        if (event.kind !== 'account' && event.kind !== 'workspace' && event.kind !== 'knowledge-base') return
        operationEpochRef.current += 1
        busyIDRef.current = null
        setBusyID(null)
        setSharesLoadAttempt((attempt) => attempt + 1)
        setCandidatesLoadAttempt((attempt) => attempt + 1)
      }),
    [],
  )

  useEffect(() => {
    operationEpochRef.current += 1
    busyIDRef.current = null
    setBusyID(null)
    if (!open) {
      searchEpochRef.current += 1
      setQuery('')
      setCandidates([])
      setCandidatesLoading(false)
      setCandidatesLoadFailed(false)
    }
  }, [kb.id, open])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setSharesLoading(true)
    setSharesLoadFailed(false)
    void kbsApi.shares(kb.id)
      .then((shareRows) => {
        if (cancelled) return
        setShares(shareRows)
      })
      .catch((error) => {
        if (cancelled) return
        setShares([])
        setSharesLoadFailed(true)
        onOperationError(error, t('common:common.error'))
      })
      .finally(() => { if (!cancelled) setSharesLoading(false) })
    return () => { cancelled = true }
  }, [kb.id, onOperationError, open, sharesLoadAttempt, t])

  useEffect(() => {
    if (!open || !normalizedEmailQuery) {
      searchEpochRef.current += 1
      setCandidates([])
      setCandidatesLoading(false)
      setCandidatesLoadFailed(false)
      return
    }
    const epoch = ++searchEpochRef.current
    setCandidatesLoading(true)
    setCandidatesLoadFailed(false)
    setCandidates([])
    const handle = window.setTimeout(() => {
      void kbsApi.shareCandidates(kb.id, normalizedEmailQuery).then((rows) => {
        if (epoch === searchEpochRef.current) setCandidates(rows)
      }).catch((error) => {
        if (epoch !== searchEpochRef.current) return
        setCandidates([])
        setCandidatesLoadFailed(true)
        onOperationError(error, t('common:common.error'))
      }).finally(() => {
        if (epoch === searchEpochRef.current) setCandidatesLoading(false)
      })
    }, 180)
    return () => {
      window.clearTimeout(handle)
      if (searchEpochRef.current === epoch) searchEpochRef.current = epoch + 1
    }
  }, [candidatesLoadAttempt, kb.id, normalizedEmailQuery, onOperationError, open, t])

  const candidateRows = useMemo(() => {
    const sharesByID = new Map(shares.map((share) => [share.user_id, share]))
    return candidates.map((candidate) => ({
      ...candidate,
      role: sharesByID.get(candidate.user_id)?.role ?? candidate.role,
    }))
  }, [candidates, shares])

  async function setRole(row: ApiKnowledgeBaseShare, role: 'read' | 'write') {
    if (busyIDRef.current) return
    const kbID = kb.id
    const epoch = ++operationEpochRef.current
    busyIDRef.current = row.user_id
    setBusyID(row.user_id)
    try {
      const updated = await kbsApi.upsertShare(kbID, { email: row.email, role })
      if (!openRef.current || kb.id !== kbID || operationEpochRef.current !== epoch) return
      setShares((current) => [...current.filter((share) => share.user_id !== row.user_id), updated])
      setCandidates((current) => current.map((candidate) => candidate.user_id === row.user_id ? { ...candidate, role } : candidate))
    } catch (error) {
      if (!openRef.current || kb.id !== kbID || operationEpochRef.current !== epoch) return
      onOperationError(error, t('common:common.error'))
    } finally {
      if (operationEpochRef.current === epoch) {
        busyIDRef.current = null
        setBusyID(null)
      }
    }
  }

  async function removeShare(row: ApiKnowledgeBaseShare) {
    if (busyIDRef.current) return
    const kbID = kb.id
    const epoch = ++operationEpochRef.current
    busyIDRef.current = row.user_id
    setBusyID(row.user_id)
    try {
      await kbsApi.removeShare(kbID, row.user_id)
      if (!openRef.current || kb.id !== kbID || operationEpochRef.current !== epoch) return
      setShares((current) => current.filter((share) => share.user_id !== row.user_id))
      setCandidates((current) => current.map((candidate) => candidate.user_id === row.user_id ? { ...candidate, role: undefined } : candidate))
    } catch (error) {
      if (!openRef.current || kb.id !== kbID || operationEpochRef.current !== epoch) return
      onOperationError(error, t('common:common.error'))
    } finally {
      if (operationEpochRef.current === epoch) {
        busyIDRef.current = null
        setBusyID(null)
      }
    }
  }

  const changeQuery = (value: string) => {
    const nextEmail = normalizeExactUserEmailQuery(value)
    if (nextEmail !== normalizedEmailQuery) {
      // Hide a previous exact match in the same input event. The epoch guard
      // also prevents its in-flight response from resurfacing after the user
      // has moved on to another address.
      searchEpochRef.current += 1
      setCandidates([])
      setCandidatesLoadFailed(false)
      setCandidatesLoading(Boolean(open && nextEmail))
    }
    setQuery(value)
  }

  const changeOpen = (next: boolean) => {
    if (!next && busyIDRef.current) return
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        size="md"
        closeDisabled={busyID !== null}
        className="max-sm:h-[calc(100dvh-1rem)] max-sm:max-h-[calc(100dvh-1rem)]"
      >
        <DialogHeader>
          <DialogTitle>{t('kb:share.title', { defaultValue: 'Share knowledge base' })}</DialogTitle>
          <DialogDescription>{t('kb:share.description', { defaultValue: "Enter someone's full email address, then give them read-only or upload access." })}</DialogDescription>
        </DialogHeader>
        <DialogBody className="min-h-0">
          <div className="relative mb-3">
            <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]" aria-hidden />
            <Input
              type="email"
              inputMode="email"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={320}
              value={query}
              onChange={(event) => changeQuery(event.target.value)}
              className="pl-9"
              placeholder={t('kb:share.search', { defaultValue: 'Full email address' })}
              aria-label={t('kb:share.search', { defaultValue: 'Full email address' })}
            />
          </div>
          <div aria-live="polite" className="mb-4 min-h-16 rounded-[8px] bg-[var(--color-bg-muted)] px-3 py-2.5">
            {!query.trim() ? (
              <p className="flex min-h-11 items-center text-[12px] leading-relaxed text-[var(--color-fg-muted)]">
                {t('kb:share.searchHint', { defaultValue: 'Enter the complete email address before searching.' })}
              </p>
            ) : !normalizedEmailQuery ? (
              <p className="flex min-h-11 items-center text-[12px] leading-relaxed text-[var(--color-fg-muted)]">
                {t('kb:share.incompleteEmail', { defaultValue: 'Enter a complete email address.' })}
              </p>
            ) : candidatesLoading ? (
              <Skeleton className="h-11 w-full" />
            ) : candidatesLoadFailed ? (
              <div role="alert" className="flex min-h-11 flex-col items-start gap-2 sm:flex-row sm:items-center sm:justify-between">
                <p className="text-[12px] text-[var(--color-fg-muted)]">
                  {t('kb:share.searchFailed', { defaultValue: 'Could not search for that email.' })}
                </p>
                <Button size="xs" variant="secondary" onClick={() => setCandidatesLoadAttempt((attempt) => attempt + 1)}>
                  {t('common:actions.tryAgain')}
                </Button>
              </div>
            ) : candidateRows.length === 0 ? (
              <p className="flex min-h-11 items-center text-[12px] leading-relaxed text-[var(--color-fg-muted)]">
                {t('kb:share.empty', { defaultValue: 'No user was found for that email.' })}
              </p>
            ) : candidateRows.map((row) => {
              const shared = row.role === 'read' || row.role === 'write'
              return (
                <div key={row.user_id} className="grid min-h-11 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3">
                  <Avatar size="sm">
                    {row.avatar_url ? <AvatarImage src={row.avatar_url} alt={row.name || row.email} /> : null}
                    <AvatarFallback>{initials(row.name || row.email)}</AvatarFallback>
                  </Avatar>
                  <div className="min-w-0">
                    <div className="truncate text-[13px] font-medium text-[var(--color-fg)]">{row.name || row.email}</div>
                    <div className="truncate text-[11.5px] text-[var(--color-fg-subtle)]">{row.email}</div>
                  </div>
                  {shared ? (
                    <Badge variant="neutral">{t('kb:share.alreadyShared', { defaultValue: 'Already shared' })}</Badge>
                  ) : (
                    <Button
                      size="sm"
                      variant="secondary"
                      leadingIcon={<UserPlus size={13} aria-hidden />}
                      loading={busyID === row.user_id}
                      disabled={busyID !== null}
                      onClick={() => void setRole(row, 'read')}
                    >
                      {t('kb:share.add', { defaultValue: 'Share' })}
                    </Button>
                  )}
                </div>
              )
            })}
          </div>

          <div className="mb-2 flex items-center justify-between gap-3">
            <h3 className="text-[12px] font-medium text-[var(--color-fg)]">
              {t('kb:share.current', { defaultValue: 'People with access' })}
            </h3>
            {!sharesLoading && !sharesLoadFailed ? (
              <span className="text-[11px] tabular-nums text-[var(--color-fg-subtle)]">{shares.length}</span>
            ) : null}
          </div>
          <div className="max-h-[min(20rem,42dvh)] overflow-y-auto border-y border-[var(--color-divider)] scrollbar-thin">
            {sharesLoading ? (
              <div className="space-y-2 py-3">{[0, 1, 2].map((row) => <Skeleton key={row} className="h-12 w-full" />)}</div>
            ) : sharesLoadFailed ? (
              <div role="alert" className="flex min-h-40 flex-col items-center justify-center gap-3 px-4 py-8 text-center">
                <p className="text-sm text-[var(--color-fg-muted)]">
                  {t('kb:share.loadFailed', { defaultValue: 'Could not load sharing settings.' })}
                </p>
                <Button size="sm" variant="secondary" onClick={() => setSharesLoadAttempt((attempt) => attempt + 1)}>
                  {t('common:actions.tryAgain')}
                </Button>
              </div>
            ) : shares.length === 0 ? (
              <div className="py-10 text-center text-sm text-[var(--color-fg-muted)]">
                {t('kb:share.currentEmpty', { defaultValue: 'This knowledge base has not been shared with anyone yet.' })}
              </div>
            ) : shares.map((row) => {
              return (
                <div key={row.user_id} className="grid min-h-16 grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2 px-1 py-2.5 sm:grid-cols-[auto_minmax(0,1fr)_auto]">
                  <Avatar size="sm">
                    {row.avatar_url ? <AvatarImage src={row.avatar_url} alt={row.name || row.email} /> : null}
                    <AvatarFallback>{initials(row.name || row.email)}</AvatarFallback>
                  </Avatar>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px] font-medium text-[var(--color-fg)]">{row.name || row.email}</div>
                    <div className="truncate text-[11.5px] text-[var(--color-fg-subtle)]">{row.email}</div>
                  </div>
                  <div className="col-span-2 flex min-w-0 items-center justify-end gap-1 sm:col-span-1">
                    <Select value={row.role} onValueChange={(value) => void setRole(row, value as 'read' | 'write')} disabled={busyID !== null}>
                      <SelectTrigger className="h-8 w-32 px-2.5 text-xs"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="read">{t('kb:access.read', { defaultValue: 'Read only' })}</SelectItem>
                        <SelectItem value="write">{t('kb:access.write', { defaultValue: 'Can upload' })}</SelectItem>
                      </SelectContent>
                    </Select>
                    <Button
                      size="icon"
                      variant="ghost"
                      aria-label={t('kb:share.remove', { defaultValue: 'Remove access' })}
                      loading={busyID === row.user_id}
                      disabled={busyID !== null}
                      onClick={() => void removeShare(row)}
                    >
                      {busyID === row.user_id ? null : <UserMinus size={14} aria-hidden />}
                    </Button>
                  </div>
                </div>
              )
            })}
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={busyID !== null} onClick={() => changeOpen(false)}>{t('common:actions.close', { defaultValue: 'Close' })}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function WorkspaceKnowledgeBaseMembersDialog({
  kb,
  open,
  onOpenChange,
  onOperationError,
  t,
}: {
  kb: ApiKnowledgeBase
  open: boolean
  onOpenChange: (open: boolean) => void
  onOperationError: (error: unknown, fallback: string) => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  const [members, setMembers] = useState<ApiWorkspaceKnowledgeBaseMemberPermission[]>([])
  const [loading, setLoading] = useState(false)
  const [loadFailed, setLoadFailed] = useState(false)
  const [loadAttempt, setLoadAttempt] = useState(0)
  const [busyID, setBusyID] = useState<string | null>(null)
  const busyIDRef = useRef<string | null>(null)
  const operationEpochRef = useRef(0)
  const openRef = useRef(open)
  openRef.current = open

  // The workspace member total permissions are edited in a separate dialog.
  // Refresh this library-specific view when those totals or the library ACL
  // change elsewhere, otherwise a disabled capability can look enabled until
  // the user closes and reopens the dialog.
  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (!openRef.current) return
        if (event.kind !== 'account' && event.kind !== 'workspace' && event.kind !== 'knowledge-base') return
        operationEpochRef.current += 1
        busyIDRef.current = null
        setBusyID(null)
        setLoadAttempt((attempt) => attempt + 1)
      }),
    [],
  )

  useEffect(() => {
    operationEpochRef.current += 1
    busyIDRef.current = null
    setBusyID(null)
  }, [kb.id, open])

  useEffect(() => {
    if (!open) return
    let cancelled = false
    setLoading(true)
    setLoadFailed(false)
    void kbsApi.workspaceMembers(kb.id)
      .then((rows) => { if (!cancelled) setMembers(rows) })
      .catch((error) => {
        if (cancelled) return
        setMembers([])
        setLoadFailed(true)
        onOperationError(error, t('common:common.error'))
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [kb.id, loadAttempt, onOperationError, open, t])

  async function update(
    member: ApiWorkspaceKnowledgeBaseMemberPermission,
    patch: Partial<Pick<ApiWorkspaceKnowledgeBaseMemberPermission, 'can_add_files' | 'can_delete_content'>>,
  ) {
    if (member.locked || busyIDRef.current) return
    const kbID = kb.id
    const epoch = ++operationEpochRef.current
    busyIDRef.current = member.user_id
    setBusyID(member.user_id)
    try {
      const updated = await kbsApi.updateWorkspaceMember(kbID, member.user_id, {
        can_add_files: patch.can_add_files ?? member.can_add_files,
        can_delete_content: patch.can_delete_content ?? member.can_delete_content,
      })
      if (!openRef.current || kb.id !== kbID || operationEpochRef.current !== epoch) return
      setMembers((current) => current.map((row) => row.user_id === updated.user_id ? updated : row))
    } catch (error) {
      if (!openRef.current || kb.id !== kbID || operationEpochRef.current !== epoch) return
      onOperationError(error, t('common:common.error'))
    } finally {
      if (operationEpochRef.current === epoch) {
        busyIDRef.current = null
        setBusyID(null)
      }
    }
  }

  const changeOpen = (next: boolean) => {
    if (!next && busyIDRef.current) return
    onOpenChange(next)
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent
        size="lg"
        closeDisabled={busyID !== null}
        className="max-sm:h-[calc(100dvh-1rem)] max-sm:max-h-[calc(100dvh-1rem)]"
      >
        <DialogHeader>
          <DialogTitle>{t('kb:workspaceMembers.title', { defaultValue: 'Knowledge-base member permissions' })}</DialogTitle>
          <DialogDescription>
            {t('kb:workspaceMembers.description', {
              defaultValue: 'These settings apply only to this knowledge base. Workspace member permissions remain the upper limit.',
            })}
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="min-h-0 py-0">
          <div className="hidden grid-cols-[minmax(0,1fr)_9rem_9rem] gap-3 border-b border-[var(--color-divider)] px-1 py-2 text-[11px] font-medium text-[var(--color-fg-subtle)] sm:grid">
            <span>{t('kb:workspaceMembers.member', { defaultValue: 'Member' })}</span>
            <span className="text-center">{t('kb:workspaceMembers.addFiles', { defaultValue: 'Add files' })}</span>
            <span className="text-center">{t('kb:workspaceMembers.deleteContent', { defaultValue: 'Delete content' })}</span>
          </div>
          <div className="max-h-[min(27rem,62dvh)] divide-y divide-[var(--color-divider)] overflow-y-auto scrollbar-thin">
            {loading ? (
              <div className="space-y-2 py-3">{[0, 1, 2].map((row) => <Skeleton key={row} className="h-16 w-full" />)}</div>
            ) : loadFailed ? (
              <div role="alert" className="flex min-h-40 flex-col items-center justify-center gap-3 px-4 py-8 text-center">
                <p className="text-sm text-[var(--color-fg-muted)]">
                  {t('kb:workspaceMembers.loadFailed', { defaultValue: 'Could not load workspace members.' })}
                </p>
                <Button size="sm" variant="secondary" onClick={() => setLoadAttempt((attempt) => attempt + 1)}>
                  {t('common:actions.tryAgain')}
                </Button>
              </div>
            ) : members.length === 0 ? (
              <div className="py-10 text-center text-sm text-[var(--color-fg-muted)]">
                {t('kb:workspaceMembers.empty', { defaultValue: 'No workspace members.' })}
              </div>
            ) : members.map((member) => (
              <div key={member.user_id} className="grid gap-3 px-1 py-3 sm:grid-cols-[minmax(0,1fr)_9rem_9rem] sm:items-center">
                <div className="flex min-w-0 items-center gap-3">
                  <Avatar size="sm">
                    {member.avatar_url ? <AvatarImage src={member.avatar_url} alt={member.name || member.email} /> : null}
                    <AvatarFallback>{initials(member.name || member.email)}</AvatarFallback>
                  </Avatar>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[13px] font-medium text-[var(--color-fg)]">{member.name || member.email}</div>
                    <div className="truncate text-[11.5px] text-[var(--color-fg-subtle)]">
                      {member.locked
                        ? t('kb:workspaceMembers.manager', { defaultValue: 'Workspace owner or admin' })
                        : member.email}
                    </div>
                  </div>
                </div>
                <KnowledgeBasePermissionSwitch
                  label={t('kb:workspaceMembers.addFiles', { defaultValue: 'Add files' })}
                  checked={member.can_add_files && member.total_can_add_kb_files}
                  disabled={member.locked || !member.total_can_add_kb_files || busyID !== null}
                  capped={!member.total_can_add_kb_files}
                  onCheckedChange={(checked) => void update(member, { can_add_files: checked })}
                  t={t}
                />
                <KnowledgeBasePermissionSwitch
                  label={t('kb:workspaceMembers.deleteContent', { defaultValue: 'Delete content' })}
                  checked={member.can_delete_content && member.total_can_delete_kb_content}
                  disabled={member.locked || !member.total_can_delete_kb_content || busyID !== null}
                  capped={!member.total_can_delete_kb_content}
                  onCheckedChange={(checked) => void update(member, { can_delete_content: checked })}
                  t={t}
                />
              </div>
            ))}
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" disabled={busyID !== null} onClick={() => changeOpen(false)}>{t('common:actions.close', { defaultValue: 'Close' })}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function KnowledgeBasePermissionSwitch({
  label,
  checked,
  disabled,
  capped,
  onCheckedChange,
  t,
}: {
  label: string
  checked: boolean
  disabled: boolean
  capped: boolean
  onCheckedChange: (checked: boolean) => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-3 sm:flex-col sm:justify-center sm:gap-1">
      <span className="text-[12px] text-[var(--color-fg-muted)] sm:hidden">{label}</span>
      <div className="flex min-w-0 items-center gap-2 sm:flex-col sm:gap-1">
        <Switch checked={checked} disabled={disabled} onCheckedChange={onCheckedChange} aria-label={label} />
        {capped ? (
          <span className="max-w-32 text-right text-[10.5px] leading-tight text-[var(--color-fg-subtle)] sm:text-center">
            {t('kb:workspaceMembers.disabledByWorkspace', { defaultValue: 'Disabled for member' })}
          </span>
        ) : null}
      </div>
    </div>
  )
}

interface DocumentSummary {
  total: number
  ready: number
  processing: number
  failed: number
  sizeBytes: number
}

function documentSummary(docs: readonly ApiDocument[]): DocumentSummary {
  const summary: DocumentSummary = { total: docs.length, ready: 0, processing: 0, failed: 0, sizeBytes: 0 }
  for (const doc of docs) {
    summary.sizeBytes += doc.size_bytes
    if (doc.status === 'ready') summary.ready += 1
    else if (doc.status === 'failed') summary.failed += 1
    else summary.processing += 1
  }
  return summary
}

/**
 * Library health at a glance: how many documents exist, how many are actually
 * searchable, and whether anything still needs attention.
 */
function DocumentSummaryStrip({
  summary,
  loading,
  t,
}: {
  summary: DocumentSummary
  loading: boolean
  t: ReturnType<typeof useTranslation>['t']
}) {
  const items: Array<{ key: string; label: string; value: string; tone?: 'accent' | 'danger' }> = [
    { key: 'documents', label: t('kb:detail.summary.documents'), value: String(summary.total) },
    { key: 'ready', label: t('kb:detail.summary.ready'), value: String(summary.ready) },
    {
      key: 'processing',
      label: t('kb:detail.summary.processing'),
      value: String(summary.processing),
      tone: summary.processing > 0 ? 'accent' : undefined,
    },
    {
      key: 'failed',
      label: t('kb:detail.summary.failed'),
      value: String(summary.failed),
      tone: summary.failed > 0 ? 'danger' : undefined,
    },
    { key: 'size', label: t('kb:detail.summary.size'), value: formatBytes(summary.sizeBytes) },
  ]
  return (
    <dl className="mt-5 grid grid-cols-3 gap-px overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-divider)] sm:grid-cols-5">
      {items.map((item, index) => (
        <div
          key={item.key}
          className={cn(
            'min-w-0 bg-[var(--color-surface)] px-3 py-2.5 sm:px-4 sm:py-3',
            // Phones show three cells per row; total size spans the last two.
            index === items.length - 1 && 'max-sm:col-span-2',
          )}
        >
          <dt className="truncate text-[12px] text-[var(--color-fg-subtle)]">{item.label}</dt>
          <dd
            className={cn(
              'mt-0.5 text-base font-semibold tabular-nums leading-6 text-[var(--color-fg)] sm:text-lg sm:leading-7',
              item.tone === 'accent' && 'text-[var(--color-accent)]',
              item.tone === 'danger' && 'text-[var(--color-danger)]',
            )}
          >
            {loading ? <Skeleton shape="line" className="my-2 h-3.5 w-10" /> : item.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}

/**
 * One document: a type-tinted tile, name + status, a single metadata line, and
 * the actions folded into a menu. Preview stays one tap away because it is the
 * common action; destructive ones need the menu and a confirmation.
 */
function DocumentRow({
  doc,
  busy,
  onPreview,
  onRetry,
  onRename,
  onRemove,
  t,
}: {
  doc: ApiDocument
  busy: { id: string; action: 'retry' | 'rename' | 'delete' } | null
  onPreview: () => void
  onRetry: () => void
  onRename: () => void
  onRemove: () => void
  t: ReturnType<typeof useTranslation>['t']
}) {
  const Icon = fileTypeIcon(doc.filename, doc.mime_type)
  const pending = doc.status === 'pending' || doc.status === 'parsing' || doc.status === 'embedding'
  const retrying = busy?.id === doc.id && busy.action === 'retry'
  const uploader = doc.uploaded_by_name || doc.uploaded_by_email
  return (
    <li className="group/doc relative">
      <div className="grid min-h-16 grid-cols-[2.25rem_minmax(0,1fr)_auto] items-center gap-x-3 py-2.5">
        <span
          className={cn('inline-flex size-9 shrink-0 items-center justify-center self-start rounded-[8px] sm:self-center', fileTypeTileClass(doc.filename, doc.mime_type))}
          aria-hidden
        >
          <Icon size={17} />
        </span>
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <button
              type="button"
              onClick={onPreview}
              title={doc.filename}
              className="min-w-0 truncate rounded-[6px] text-left text-[14px] font-medium leading-5 text-[var(--color-fg)] interactive hover:text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              {doc.filename}
            </button>
            <DocumentStatus status={doc.status} label={t(`kb:detail.status.${doc.status}`)} />
          </div>
          <p className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-1.5 text-[12px] leading-4 tabular-nums text-[var(--color-fg-subtle)]">
            <span>{formatBytes(doc.size_bytes)}</span>
            {doc.status === 'ready' ? (
              <>
                <span aria-hidden className="opacity-50">·</span>
                <span>{t('kb:detail.chunkCount', { count: doc.chunk_count, defaultValue: '{{count}} chunks' })}</span>
              </>
            ) : null}
            <span aria-hidden className="opacity-50">·</span>
            <time dateTime={new Date(doc.created_at * 1000).toISOString()}>
              {t('kb:stats.created', { when: formatRelativeDate(doc.created_at * 1000) })}
            </time>
            {uploader ? (
              <>
                <span aria-hidden className="opacity-50">·</span>
                <span className="min-w-0 truncate">
                  {t('kb:detail.uploadedBy', { name: uploader, defaultValue: 'Uploaded by {{name}}' })}
                </span>
              </>
            ) : null}
          </p>
          {doc.status === 'failed' ? (
            <p className="mt-1 flex items-start gap-1.5 text-[12px] leading-snug text-[var(--color-danger)]">
              <AlertTriangle size={12} className="mt-px shrink-0" aria-hidden />
              <span>
                {doc.error_code === DOCUMENT_PARSER_NOT_CONFIGURED
                  ? t('kb:detail.parserNotConfigured', { defaultValue: 'Indexing failed because document parsing (MinerU) is not configured. Ask an administrator to configure it, then retry.' })
                  : t('kb:detail.failedReason')}
              </span>
            </p>
          ) : null}
          {pending ? (
            <div
              className="mt-1.5 h-1 w-full max-w-xs overflow-hidden rounded-full bg-[var(--color-bg-muted)]"
              role="progressbar"
              aria-label={t('kb:detail.indexing')}
            >
              <div className="h-full w-1/3 bg-[var(--color-accent)] animate-[indeterminate_1400ms_linear_infinite] motion-reduce:animate-none" />
            </div>
          ) : null}
        </div>
        <div className="flex items-center gap-1">
          {doc.status === 'failed' && doc.can_delete ? (
            <Button
              variant="secondary"
              size="xs"
              className="max-sm:hidden"
              leadingIcon={<RefreshCw size={12} aria-hidden />}
              loading={retrying}
              disabled={busy !== null}
              onClick={onRetry}
            >
              {t('kb:detail.retry')}
            </Button>
          ) : null}
          <Tooltip content={t('kb:detail.preview', { defaultValue: 'Preview' })}>
            <Button
              variant="ghost"
              size="icon-sm"
              className="max-sm:size-[var(--tap-min)]"
              aria-label={`${t('kb:detail.preview', { defaultValue: 'Preview' })}: ${doc.filename}`}
              onClick={onPreview}
            >
              <Eye size={15} aria-hidden />
            </Button>
          </Tooltip>
          {doc.can_delete ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="max-sm:size-[var(--tap-min)]"
                  aria-label={t('kb:detail.moreActions', { name: doc.filename })}
                  disabled={busy !== null && busy.id === doc.id}
                >
                  {busy?.id === doc.id ? (
                    <Loader2 size={15} className="animate-spin motion-reduce:animate-none" aria-hidden />
                  ) : (
                    <MoreHorizontal size={15} aria-hidden />
                  )}
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-[180px]">
                {doc.status === 'failed' ? (
                  <DropdownMenuItem className="sm:hidden" disabled={busy !== null} onSelect={onRetry}>
                    <RefreshCw size={13} aria-hidden /> {t('kb:detail.retry')}
                  </DropdownMenuItem>
                ) : null}
                <DropdownMenuItem disabled={busy !== null} onSelect={onRename}>
                  <Pencil size={13} aria-hidden /> {t('kb:detail.renameFile', { defaultValue: 'Rename' })}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem destructive disabled={busy !== null} onSelect={onRemove}>
                  <Trash2 size={13} aria-hidden /> {t('common:actions.delete')}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>
      </div>
    </li>
  )
}

function DocumentStatus({ status, label }: { status: ApiDocument['status']; label: string }) {
  switch (status) {
    case 'ready':
      return <Badge size="xs" variant="success" className="shrink-0">{label}</Badge>
    case 'failed':
      // Failed must read as an error, not just "another in-progress state".
      return <Badge size="xs" variant="danger" className="shrink-0">{label}</Badge>
    default:
      return (
        <Badge size="xs" variant="accent" className="shrink-0" leadingIcon={<Loader2 size={10} className="animate-spin motion-reduce:animate-none" aria-hidden />}>
          {label}
        </Badge>
      )
  }
}

function DocumentRowsSkeleton({ label }: { label: string }) {
  return (
    <div className="mt-1 divide-y divide-[var(--color-divider)]" role="status" aria-label={label}>
      {Array.from({ length: 5 }, (_, index) => (
        <div key={index} className="grid min-h-16 grid-cols-[2.25rem_minmax(0,1fr)_auto] items-center gap-x-3 py-2.5">
          <Skeleton className="size-9 rounded-[8px]" />
          <div className="min-w-0 space-y-1.5">
            <Skeleton shape="line" className="h-3.5 w-2/5" />
            <Skeleton shape="line" className="h-3 w-3/5" />
          </div>
          <Skeleton className="size-7 rounded-[8px]" />
        </div>
      ))}
      <span className="sr-only">{label}</span>
    </div>
  )
}

/** Full-page frame for the first load: real header chrome, skeleton content. */
function KnowledgeBaseDetailSkeleton({ label, backLabel }: { label: string; backLabel: string }) {
  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]" aria-busy="true">
      <ContentHeader title={backLabel} backTo="/kb" backLabel={backLabel} actions={<Skeleton className="h-8 w-24 rounded-[10px]" />} />
      <div className="flex-1 min-h-0 overflow-hidden">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 pt-5 sm:px-8 sm:pt-6" role="status" aria-label={label}>
          <Skeleton shape="line" className="h-3.5 w-1/2 max-w-md" />
          <Skeleton shape="line" className="mt-3 h-3 w-40" />
          <div className="mt-5 grid grid-cols-3 gap-px overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-divider)] sm:grid-cols-5">
            {Array.from({ length: 5 }, (_, index) => (
              <div key={index} className={cn('space-y-2 bg-[var(--color-surface)] px-3 py-2.5 sm:px-4 sm:py-3', index === 4 && 'max-sm:col-span-2')}>
                <Skeleton shape="line" className="h-3 w-12" />
                <Skeleton shape="line" className="h-4 w-8" />
              </div>
            ))}
          </div>
          <div className="mt-6 flex items-center gap-2.5 border-b border-[var(--color-divider)] pb-3">
            <Skeleton className="h-10 w-full max-w-xs" />
          </div>
          <DocumentRowsSkeleton label={label} />
        </div>
      </div>
    </div>
  )
}
