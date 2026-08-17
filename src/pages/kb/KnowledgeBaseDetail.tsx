/**
 * KnowledgeBaseDetail — list documents, add one (paste content or upload a
 * file), remove. Status shown live via polling while any doc is non-ready.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Upload, FileText, AlertTriangle, MoreHorizontal, RefreshCw, Search, Share2, Eye, UserPlus, UserMinus, Users, Pencil, Lock, Unlock } from 'lucide-react'
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
import { toast } from '@/hooks/use-toast'
import { toastStorageQuotaFull } from '@/lib/quota-toast'
import { formatRelativeDate, cn } from '@/lib/utils'
import { envNum } from '@/lib/env-config'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { FilePreview } from '@/components/chat/file-preview'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { useAuth } from '@/store/auth'
import { useWorkspaces } from '@/store/workspaces'
import { userCan } from '@/lib/user-permissions'
import { Switch } from '@/components/ui/switch'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { knowledgeBaseErrorText, knowledgeBaseOperationErrorText } from '@/lib/knowledge-base-errors'
import { normalizeExactUserEmailQuery } from '@/lib/user-email-search'

const kbDocStatusPollInterval = envNum('VITE_AIVORY_KB_DOC_STATUS_POLL_INTERVAL', 2200)

export default function KnowledgeBaseDetail() {
  const { t } = useTranslation(['kb', 'common'])
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const user = useAuth((s) => s.user)
  // §workspace RBAC: visibility management needs the creator id or admin role.
  const workspaceRole = useWorkspaces((s) => (kb?.workspace_id ? s.workspaces.find((w) => w.id === kb.workspace_id)?.role : undefined))
  const [kbVisibilityBusy, setKBVisibilityBusy] = useState(false)
  async function toggleKBVisibility() {
    if (!kb || kbVisibilityBusy) return
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
  const canUseKnowledgeBases = userCan(user, 'allow_knowledge_bases')
  const canShareKnowledgeBases = userCan(user, 'allow_knowledge_base_sharing')
  const canUploadFiles = userCan(user, 'allow_file_upload')
  const [kb, setKB] = useState<ApiKnowledgeBase | null>(null)
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
  const [previewDoc, setPreviewDoc] = useState<ApiDocument | null>(null)
  const [renameDoc, setRenameDoc] = useState<ApiDocument | null>(null)
  const [renameFilename, setRenameFilename] = useState('')
  const [shareOpen, setShareOpen] = useState(false)
  const [workspaceMembersOpen, setWorkspaceMembersOpen] = useState(false)
  const [accessRevoked, setAccessRevoked] = useState(false)
  const loadEpochRef = useRef(0)
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
    if (!id || deletingKBRef.current) return
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
        setPreviewDoc(null)
        setRenameDoc(null)
        setRenameFilename('')
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

  const handleOperationError = useCallback((error: unknown, fallback: string) => {
    toast.error(knowledgeBaseOperationErrorText(t, error, fallback))
    if (!(error instanceof ApiError) || (error.status !== 403 && error.status !== 404)) return

    // A permission may change while any of these surfaces is open. Close all
    // mutation UI immediately and reconcile against the server before the user
    // can submit another stale action.
    setOpen(false)
    setShareOpen(false)
    setWorkspaceMembersOpen(false)
    setPreviewDoc(null)
    setRenameDoc(null)
    setRenameFilename('')
    setConfirmDeleteKB(false)
    docOperationEpochRef.current += 1
    busyDocRef.current = null
    setBusyDoc(null)
    void load()
  }, [load, t])

  const handlePreviewLoadError = useCallback((status?: number) => {
    if (status !== 403 && status !== 404) return
    setPreviewDoc(null)
    toast.error(t('kb:permissionChanged'))
    void load()
  }, [load, t])

  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (event.kind === 'account' || event.kind === 'workspace' || event.kind === 'knowledge-base') {
          void load()
        }
      }),
    [load],
  )

  useEffect(() => {
    if (!canUseKnowledgeBases) {
      setLoading(false)
      setKB(null)
      setDocs([])
      setLoadError('')
      return
    }
    void load()
  }, [canUseKnowledgeBases, load])

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
  }, [canShareKnowledgeBases, canUploadFiles, canUseKnowledgeBases, docs, kb, renameDoc])

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
    if (!id || busyDocRef.current) return
    const operation = { id: d.id, action: 'delete' as const }
    const operationEpoch = ++docOperationEpochRef.current
    busyDocRef.current = operation
    setBusyDoc(operation)
    try {
      await kbsApi.removeDoc(id, d.id)
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
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
    if (!id || busyDocRef.current || d.status !== 'failed') return
    const operation = { id: d.id, action: 'retry' as const }
    const operationEpoch = ++docOperationEpochRef.current
    busyDocRef.current = operation
    setBusyDoc(operation)
    try {
      await kbsApi.retryDoc(id, d.id)
      if (docOperationEpochRef.current !== operationEpoch || busyDocRef.current !== operation) return
      setDocs((current) => current.map((doc) => (
        doc.id === d.id ? { ...doc, status: 'pending', error: '', chunk_count: 0 } : doc
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
    if (!id || !renameDoc || !filename || busyDocRef.current) return
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

  if (!canUseKnowledgeBases) {
    return (
      <div className="flex-1 grid place-items-center p-10">
        <EmptyState
          icon={<FileText size={20} aria-hidden />}
          title={t('kb:groupPermissionTitle', { defaultValue: 'Knowledge bases unavailable' })}
          description={t('kb:groupPermissionRequired', { defaultValue: 'Your user group does not have knowledge-base access.' })}
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

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={kb?.name ?? '…'}
        backTo="/kb"
        backLabel={t('kb:title')}
        actions={
          <div className="flex items-center gap-2">
            {kb?.can_upload && canUploadFiles ? (
              <Button
                size="sm"
                leadingIcon={<Plus size={15} aria-hidden />}
                onClick={() => setOpen(true)}
              >
                {t('kb:detail.uploadButton')}
              </Button>
            ) : null}
            {kb?.can_share && !kb.workspace_id && canShareKnowledgeBases ? (
              <Button
                variant="secondary"
                size="sm"
                leadingIcon={<Share2 size={15} aria-hidden />}
                onClick={() => setShareOpen(true)}
              >
                {t('kb:share.action', { defaultValue: 'Share' })}
              </Button>
            ) : null}
            {kb?.workspace_id && (kb.user_id === user?.id || workspaceRole === 'admin') ? (
              <Button
                variant="secondary"
                size="sm"
                loading={kbVisibilityBusy}
                leadingIcon={kb.is_public !== false ? <Lock size={15} aria-hidden /> : <Unlock size={15} aria-hidden />}
                onClick={() => void toggleKBVisibility()}
              >
                {kb.is_public !== false
                  ? t('kb:detail.makePrivate', { defaultValue: 'Make private' })
                  : t('kb:detail.makeShared', { defaultValue: 'Share with workspace' })}
              </Button>
            ) : null}
            {kb?.workspace_id && kb.can_manage_members ? (
              <Button
                variant="secondary"
                size="sm"
                leadingIcon={<Users size={15} aria-hidden />}
                onClick={() => setWorkspaceMembersOpen(true)}
              >
                {t('kb:workspaceMembers.action', { defaultValue: 'Member permissions' })}
              </Button>
            ) : null}
            {kb?.can_delete ? (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    aria-label={t('common:actions.more', { defaultValue: 'More' })}
                    className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <MoreHorizontal size={16} aria-hidden />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem destructive onSelect={() => setConfirmDeleteKB(true)}>
                    <Trash2 size={13} aria-hidden /> {t('kb:deleteAction', { defaultValue: 'Delete knowledge base' })}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            ) : null}
          </div>
        }
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-8 pb-24">
          {kb?.description ? (
            <p className="text-[var(--color-fg-muted)] text-[15px] leading-relaxed max-w-[60ch]">{kb.description}</p>
          ) : null}
          {kb ? (
            <div className="mt-3 flex flex-wrap items-center gap-2 text-[12px] text-[var(--color-fg-subtle)]">
              <Badge size="xs" variant="neutral">
                {kb.access_role === 'read'
                  ? t('kb:access.read', { defaultValue: 'Read only' })
                  : kb.access_role === 'write'
                    ? t('kb:access.write', { defaultValue: 'Can upload' })
                    : kb.access_role === 'workspace'
                      ? t('kb:access.workspace', { defaultValue: 'Workspace' })
                      : t('kb:access.owner', { defaultValue: 'Owner' })}
              </Badge>
              {kb.owner_name ? (
                <span>{t('kb:access.ownerName', { name: kb.owner_name, defaultValue: 'Owner: {{name}}' })}</span>
              ) : null}
            </div>
          ) : null}

          <section className="mt-8">
            <div className="mb-3 flex flex-col gap-2 sm:flex-row">
            <div className="relative min-w-0 flex-1">
              <Search size={14} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]" aria-hidden />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('kb:detail.searchFiles', { defaultValue: 'Search files by name' })}
                aria-label={t('kb:detail.searchFiles', { defaultValue: 'Search files by name' })}
                className="pl-9"
              />
            </div>
            <Select value={uploaderID} onValueChange={setUploaderID}>
              <SelectTrigger className="sm:w-56" aria-label={t('kb:detail.filterUploader', { defaultValue: 'Filter by uploader' })}>
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
            </div>
            {loading ? (
            <div className="space-y-3" role="status" aria-label={t('common:common.loading')}>
              {Array.from({ length: 4 }, (_, index) => (
                <div key={index} className="flex items-center gap-3 py-4">
                  <Skeleton className="size-8 shrink-0" />
                  <div className="flex-1 space-y-2">
                    <Skeleton shape="line" className="w-2/5" />
                    <Skeleton shape="line" className="h-2.5 w-3/5" />
                  </div>
                </div>
              ))}
              <span className="sr-only">{t('common:common.loading')}</span>
            </div>
          ) : docs.length === 0 ? (
            <EmptyState
              icon={<FileText size={20} aria-hidden />}
              title={debouncedSearch.trim() || uploaderID !== 'all'
                ? t('kb:detail.noMatches', { defaultValue: 'No matching files' })
                : t('kb:detail.noDocs')}
              description={debouncedSearch.trim() || uploaderID !== 'all'
                ? t('kb:detail.noMatchesBody', { defaultValue: 'Try another file name or uploader.' })
                : t('kb:detail.noDocsBody')}
              action={!debouncedSearch.trim() && uploaderID === 'all' && kb?.can_upload && canUploadFiles
                ? <Button onClick={() => setOpen(true)}>{t('kb:detail.uploadButton')}</Button>
                : undefined}
            />
            ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
              {docs.map((d) => (
                <li key={d.id} className="grid grid-cols-1 items-center gap-3 px-4 py-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:px-5">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <FileText size={13} className="text-[var(--color-fg-subtle)] shrink-0" aria-hidden />
                      <span className="font-medium text-[var(--color-fg)] truncate">{d.filename}</span>
                      <StatusBadge status={d.status} label={t(`kb:detail.status.${d.status}`)} />
                    </div>
                    <div className="mt-1 text-[12px] text-[var(--color-fg-subtle)] font-mono">
                      {(d.size_bytes / 1024).toFixed(1)} KB · {t('kb:detail.chunkCount', { count: d.chunk_count, defaultValue: '{{count}} chunks' })} · {t('kb:stats.created', { when: formatRelativeDate(d.created_at * 1000) })}
                    </div>
                    {d.uploaded_by_name || d.uploaded_by_email ? (
                      <div className="mt-1 truncate text-[11.5px] text-[var(--color-fg-subtle)]">
                        {t('kb:detail.uploadedBy', {
                          name: d.uploaded_by_name || d.uploaded_by_email,
                          defaultValue: 'Uploaded by {{name}}',
                        })}
                      </div>
                    ) : null}
                    {d.status === 'failed' ? (
                      <p className="mt-1.5 flex items-start gap-1.5 text-[12px] text-[var(--color-danger)] leading-snug">
                        <AlertTriangle size={12} className="mt-px shrink-0" aria-hidden />
                        <span>{t('kb:detail.failedReason')}</span>
                      </p>
                    ) : null}
                    {(d.status === 'parsing' || d.status === 'embedding') ? (
                      <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-[var(--color-bg-muted)]">
                        <div className="h-full w-1/3 bg-[var(--color-accent)] animate-[indeterminate_1400ms_linear_infinite]" />
                      </div>
                    ) : null}
                  </div>
                  <div className="flex flex-wrap items-center gap-1 sm:justify-end">
                    <Button
                      variant="ghost"
                      size="sm"
                      leadingIcon={<Eye size={13} aria-hidden />}
                      onClick={() => setPreviewDoc(d)}
                    >
                      {t('kb:detail.preview', { defaultValue: 'Preview' })}
                    </Button>
                    {d.status === 'failed' && d.can_delete ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        leadingIcon={<RefreshCw size={13} aria-hidden />}
                        loading={busyDoc?.id === d.id && busyDoc.action === 'retry'}
                        disabled={busyDoc !== null}
                        onClick={() => void retry(d)}
                      >
                        {t('kb:detail.retry')}
                      </Button>
                    ) : null}
                    {d.can_delete ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        leadingIcon={<Pencil size={13} aria-hidden />}
                        disabled={busyDoc !== null}
                        onClick={() => beginRename(d)}
                      >
                        {t('kb:detail.renameFile', { defaultValue: 'Rename' })}
                      </Button>
                    ) : null}
                    {d.can_delete ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        leadingIcon={<Trash2 size={13} aria-hidden />}
                        loading={busyDoc?.id === d.id && busyDoc.action === 'delete'}
                        disabled={busyDoc !== null}
                        onClick={() => void remove(d)}
                      >
                        {t('common:actions.delete')}
                      </Button>
                    ) : null}
                  </div>
                </li>
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

      <FilePreview
        open={previewDoc !== null}
        onOpenChange={(next) => { if (!next) setPreviewDoc(null) }}
        onLoadError={handlePreviewLoadError}
        file={previewDoc ? {
          name: previewDoc.filename,
          kind: 'other',
          url: apiUrl(`/documents/${encodeURIComponent(previewDoc.id)}/content`),
          authenticated: true,
        } : null}
      />

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
                        ? t('kb:workspaceMembers.manager', { defaultValue: 'Owner or library creator' })
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

function StatusBadge({ status, label }: { status: ApiDocument['status']; label: string }) {
  switch (status) {
    case 'ready':
      return <Badge size="xs" variant="sage">{label}</Badge>
    case 'failed':
      // Failed must read as an error, not just "another in-progress state".
      return <Badge size="xs" variant="danger">{label}</Badge>
    default:
      return <Badge size="xs" variant="neutral">{label}…</Badge>
  }
}
