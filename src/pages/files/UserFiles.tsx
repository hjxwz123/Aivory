/**
 * UserFiles is a master-detail workspace over the signed-in user's uploads.
 * Desktop keeps the compact file browser and preview visible together; smaller
 * screens use a list -> preview flow so neither surface is squeezed.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import {
  ArrowLeft,
  ChevronDown,
  ChevronRight,
  Database,
  Download,
  FileQuestion,
  Folder as FolderIcon,
  FolderOpen,
  HardDrive,
  MessageSquare,
  RefreshCw,
  Search,
  Trash2,
} from 'lucide-react'
import { authApi, apiUrl, ApiError } from '@/api'
import type { ApiAdminFile } from '@/api/types'
import { DocumentPreview } from '@/components/files/document-preview'
import { FileFiltersPopover } from '@/components/files/file-filters-popover'
import { ContentHeader } from '@/components/layout/content-header'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'
import { useMediaQuery } from '@/hooks/use-media-query'
import { envNum } from '@/lib/env-config'
import { fileTypeIcon, fileTypeTileClass } from '@/lib/file-icon'
import { fileFolderTree, type FolderTreeNode } from '@/lib/folder-attachments'
import {
  documentPreviewByteLimit,
  documentPreviewKind,
  type FileTypeFilter,
} from '@/lib/file-preview-kind'
import { cn, formatBytes } from '@/lib/utils'
import { useConversations } from '@/store/conversations'

const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE', 50)
const ALL = 'all'

function fileTreeOf(rows: readonly ApiAdminFile[]): {
  rootFiles: ApiAdminFile[]
  folders: Array<FolderTreeNode<ApiAdminFile>>
} {
  return fileFolderTree(rows, (file) => ({ relPath: file.rel_path, size: file.size_bytes }))
}

function typeLabel(file: ApiAdminFile): string {
  const mime = file.mime_type.toLowerCase()
  if (mime.startsWith('image/')) return mime.slice(6).split(';', 1)[0].toUpperCase()
  const name = file.filename.split(/[?#]/, 1)[0]
  const ext = name.includes('.') ? name.split('.').pop() ?? '' : ''
  return ext ? ext.toUpperCase() : mime || '-'
}

function rowKey(file: ApiAdminFile): string {
  return `${file.source}:${file.id}`
}

interface PreviewState {
  key: string
  file: ApiAdminFile
  loading: boolean
  data?: ArrayBuffer
  url?: string
  error?: string
  retryable?: boolean
}

function fileContentUrl(file: ApiAdminFile): string {
  const query = new URLSearchParams({ source: file.source, id: file.id })
  return apiUrl(`/me/files/content?${query}`)
}

export default function UserFiles() {
  const { t, i18n } = useTranslation(['files', 'common'])
  const compact = useMediaQuery('(max-width: 1023px)')
  const [search, setSearch] = useState('')
  const [searchDebounced, setSearchDebounced] = useState('')
  const [origin, setOrigin] = useState(ALL)
  const [fileType, setFileType] = useState<FileTypeFilter>('all')
  const [sort, setSort] = useState('created_at')
  const [order, setOrder] = useState<'desc' | 'asc'>('desc')

  const [rows, setRows] = useState<ApiAdminFile[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState('')
  const [page, setPage] = useState(1)
  const [storage, setStorage] = useState<{ used_bytes: number; quota_bytes: number } | null>(null)

  const [selectedKey, setSelectedKey] = useState('')
  const [mobilePreviewOpen, setMobilePreviewOpen] = useState(false)
  const [preview, setPreview] = useState<PreviewState | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<ApiAdminFile | null>(null)
  // Deleting a folder is one decision, not N: the whole subtree is collected and
  // removed together, with the dialog naming how many files that is.
  const [confirmDeleteFolder, setConfirmDeleteFolder] = useState<FolderTreeNode<ApiAdminFile> | null>(null)
  const [busy, setBusy] = useState(false)
  // Which directory rows are open on this page. A folder upload arrives as N
  // rows carrying the same rel_path prefix; showing them flat loses the folder
  // the user actually picked, so they are folded into one expandable row.
  const [expandedFolders, setExpandedFolders] = useState<Set<string>>(new Set())

  const listRequestRef = useRef(0)
  const previewRequestRef = useRef(0)
  const previewAbortRef = useRef<AbortController | null>(null)
  const previewUrlRef = useRef<string | null>(null)

  useEffect(() => {
    const id = window.setTimeout(() => setSearchDebounced(search.trim()), 350)
    return () => window.clearTimeout(id)
  }, [search])

  const releasePreviewResources = useCallback(() => {
    previewAbortRef.current?.abort()
    previewAbortRef.current = null
    if (previewUrlRef.current) {
      URL.revokeObjectURL(previewUrlRef.current)
      previewUrlRef.current = null
    }
  }, [])

  useEffect(() => releasePreviewResources, [releasePreviewResources])

  const clearPreview = useCallback(() => {
    previewRequestRef.current += 1
    releasePreviewResources()
    setPreview(null)
  }, [releasePreviewResources])

  const loadStorage = useCallback(() => {
    authApi
      .myStorage()
      .then(setStorage)
      .catch(() => {})
  }, [])

  const load = useCallback(async () => {
    const request = ++listRequestRef.current
    setLoading(true)
    setListError('')
    try {
      const response = await authApi.myFiles({
        search: searchDebounced,
        origin,
        type: fileType,
        sort,
        order,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      })
      if (request !== listRequestRef.current) return
      setTotal(response.total)
      const lastPage = Math.max(1, Math.ceil(response.total / PAGE_SIZE))
      if (page > lastPage) {
        setRows([])
        setPage(lastPage)
        return
      }
      setRows(response.files)
    } catch (error) {
      if (request !== listRequestRef.current) return
      const message = error instanceof ApiError ? error.message : t('files:preview.failed')
      setListError(message)
      toast.error(message)
    } finally {
      if (request === listRequestRef.current) setLoading(false)
    }
  }, [fileType, order, origin, page, searchDebounced, sort, t])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    loadStorage()
  }, [loadStorage])

  useEffect(() => {
    setPage(1)
    setMobilePreviewOpen(false)
  }, [searchDebounced, origin, fileType, sort, order])

  const openPreview = useCallback(
    async (file: ApiAdminFile, openOnCompact = true) => {
      const key = rowKey(file)
      setSelectedKey(key)
      if (compact && openOnCompact) setMobilePreviewOpen(true)
      if (preview?.key === key && (preview.loading || preview.data || preview.url)) return

      const request = ++previewRequestRef.current
      releasePreviewResources()
      const kind = documentPreviewKind(file.filename, file.mime_type)
      const byteLimit = documentPreviewByteLimit(kind)
      if (byteLimit === 0) {
        setPreview({ key, file, loading: false })
        return
      }
      if (byteLimit !== null && file.size_bytes > byteLimit) {
        setPreview({
          key,
          file,
          loading: false,
          error: t('files:preview.tooLarge'),
          retryable: false,
        })
        return
      }

      const controller = new AbortController()
      previewAbortRef.current = controller
      setPreview({ key, file, loading: true })
      try {
        const blob = await authApi.myFileContentBlob(file.source, file.id, controller.signal)
        if (request !== previewRequestRef.current || controller.signal.aborted) return
        if (kind === 'image') {
          const url = URL.createObjectURL(blob)
          previewUrlRef.current = url
          setPreview({ key, file, loading: false, url })
          return
        }
        const data = await blob.arrayBuffer()
        if (request !== previewRequestRef.current || controller.signal.aborted) return
        setPreview({ key, file, loading: false, data })
      } catch (error) {
        if (controller.signal.aborted || request !== previewRequestRef.current) return
        const message = error instanceof ApiError ? error.message : t('files:preview.failed')
        setPreview({ key, file, loading: false, error: message })
      } finally {
        if (previewAbortRef.current === controller) previewAbortRef.current = null
      }
    },
    [compact, preview, releasePreviewResources, t],
  )

  // Keep a valid selection as server-side filters and pagination change. On
  // desktop the first result opens automatically; mobile waits for an explicit
  // tap so entering Files never downloads a document in the background.
  useEffect(() => {
    if (loading) return
    if (rows.length === 0) {
      setSelectedKey('')
      setMobilePreviewOpen(false)
      clearPreview()
      return
    }
    const selected = rows.find((file) => rowKey(file) === selectedKey)
    if (compact) {
      if (selectedKey && !selected) {
        setSelectedKey('')
        clearPreview()
      }
      return
    }
    const next = selected ?? rows[0]
    const nextKey = rowKey(next)
    if (!selected) setSelectedKey(nextKey)
    if (!compact && preview?.key !== nextKey) void openPreview(next, false)
  }, [clearPreview, compact, loading, openPreview, preview?.key, rows, selectedKey])

  /**
   * Remove every file under a folder row in one go.
   *
   * The API deletes by row, so the subtree is flattened first. Chunks keep a
   * 300-file project from becoming one enormous request body, and the successful
   * ids are remembered so a later chunk failing does not silently claim the whole
   * folder was removed.
   */
  const runDeleteFolder = async (node: FolderTreeNode<ApiAdminFile>) => {
    const items: Array<{ source: ApiAdminFile['source']; id: string }> = []
    const collect = (current: FolderTreeNode<ApiAdminFile>) => {
      current.files.forEach((file) => items.push({ source: file.source, id: file.id }))
      current.children.forEach(collect)
    }
    collect(node)
    if (!items.length) {
      setConfirmDeleteFolder(null)
      return
    }
    setBusy(true)
    const removedIds: string[] = []
    try {
      for (let i = 0; i < items.length; i += 50) {
        const chunk = items.slice(i, i + 50)
        await authApi.deleteMyFiles(chunk)
        removedIds.push(...chunk.map((item) => item.id))
      }
      useConversations
        .getState()
        .markAttachmentsDeleted(
          removedIds,
          node.files[0]?.conversation_id || undefined,
        )
      if (preview && removedIds.includes(preview.file.id)) {
        clearPreview()
        setSelectedKey('')
        setMobilePreviewOpen(false)
      }
      toast.success(t('files:deleted'))
      setConfirmDeleteFolder(null)
      await load()
      loadStorage()
    } catch (error) {
      // Report what did go, so the list the user sees matches the server.
      if (removedIds.length) {
        useConversations.getState().markAttachmentsDeleted(removedIds, node.files[0]?.conversation_id || undefined)
        await load()
      }
      toast.error(error instanceof ApiError ? error.message : t('common:actions.failed', { defaultValue: 'Failed' }))
    } finally {
      setBusy(false)
    }
  }

  const runDelete = async (file: ApiAdminFile) => {    setBusy(true)
    try {
      await authApi.deleteMyFiles([{ source: file.source, id: file.id }])
      useConversations
        .getState()
        .markAttachmentsDeleted([file.id], file.conversation_id || undefined)
      if (rowKey(file) === preview?.key) {
        clearPreview()
        setSelectedKey('')
        setMobilePreviewOpen(false)
      }
      toast.success(t('files:deleted'))
      setConfirmDelete(null)
      await load()
      loadStorage()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('common:actions.failed', { defaultValue: 'Failed' }))
    } finally {
      setBusy(false)
    }
  }

  const timeFormat = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  )
  const shortDateFormat = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium' }),
    [i18n.language],
  )

  // One row per uploaded folder — and per subdirectory inside it — instead of one
  // row per file. `rel_path` is what identifies the folder the user picked;
  // single-file uploads have none and stay top-level rows.
  const fileTree = useMemo(() => fileTreeOf(rows), [rows])

  const quota = storage?.quota_bytes ?? 0
  const used = storage?.used_bytes ?? 0
  const storagePercent = quota > 0 ? Math.min(100, (used / quota) * 100) : 0
  const storageNearFull = quota > 0 && storagePercent >= 90
  const pageCount = Math.ceil(total / PAGE_SIZE)
  const activeFilterCount =
    (fileType !== 'all' ? 1 : 0) +
    (origin !== ALL ? 1 : 0) +
    (sort !== 'created_at' || order !== 'desc' ? 1 : 0)
  const filtersActive = Boolean(searchDebounced) || origin !== ALL || fileType !== 'all'
  // A refetch (search, filter, page) keeps the current rows visible under a
  // thin progress bar instead of collapsing the list back into skeletons.
  const firstLoad = loading && rows.length === 0 && !listError
  const refreshing = loading && !firstLoad
  const resetFilters = () => {
    setSearch('')
    setFileType('all')
    setOrigin(ALL)
    setSort('created_at')
    setOrder('desc')
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ContentHeader title={t('files:title')} fluid />
      <main className="min-h-0 flex-1 overflow-hidden border-t border-[var(--color-divider)]">
        <div className="flex h-full min-h-0 w-full overflow-hidden bg-[var(--color-surface)]">
          <aside
            className={cn(
              'min-h-0 w-full flex-col border-[var(--color-divider)] bg-[var(--color-bg)] lg:flex lg:w-[21rem] lg:shrink-0 lg:border-r xl:w-[22rem]',
              mobilePreviewOpen ? 'hidden' : 'flex',
            )}
            aria-label={t('files:accessibility.fileList')}
          >
            <StorageMeter
              loaded={storage !== null}
              quota={quota}
              percent={storagePercent}
              nearFull={storageNearFull}
              labels={{
                title: t('files:storage.title'),
                usedOf: quota > 0
                  ? t('files:storage.usedOf', { used: formatBytes(used), quota: formatBytes(quota) })
                  : t('files:storage.usedUnlimited', { used: formatBytes(used) }),
                percent: t('files:storage.percent', { percent: Math.round(storagePercent) }),
                nearFull: t('files:storage.nearFull'),
              }}
            />

            <div className="flex items-center gap-2 px-3 pb-2">
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                leadingIcon={<Search size={15} aria-hidden />}
                placeholder={t('files:searchPlaceholder')}
                aria-label={t('files:searchPlaceholder')}
                wrapperClassName="min-w-0 flex-1"
              />
              <FileFiltersPopover
                fileType={fileType}
                onFileTypeChange={setFileType}
                origin={origin}
                onOriginChange={setOrigin}
                sort={sort}
                order={order}
                onSortChange={(nextSort, nextOrder) => {
                  setSort(nextSort)
                  setOrder(nextOrder)
                }}
                activeCount={activeFilterCount}
                onReset={() => {
                  setFileType('all')
                  setOrigin(ALL)
                  setSort('created_at')
                  setOrder('desc')
                }}
              />
            </div>

            <div className="relative flex min-h-0 flex-1 flex-col">
              <div className="flex h-9 shrink-0 items-center justify-between border-b border-[var(--color-divider)] px-3 text-[12px] text-[var(--color-fg-subtle)]">
                <span>{t('files:list.title')}</span>
                {firstLoad ? (
                  <Skeleton shape="line" className="h-3 w-14" />
                ) : (
                  <span className="tabular-nums">{t('files:total', { count: total })}</span>
                )}
              </div>
              {refreshing ? (
                <div className="pointer-events-none absolute inset-x-0 top-9 z-10 h-0.5 overflow-hidden bg-[var(--color-accent-soft)]" role="progressbar" aria-label={t('files:list.loading')}>
                  <span className="block h-full w-1/3 bg-[var(--color-accent)] animate-[indeterminate_1200ms_ease-in-out_infinite] motion-reduce:animate-none" />
                </div>
              ) : null}

              {firstLoad ? (
                <FileListSkeleton label={t('files:list.loading')} />
              ) : listError ? (
                <div className="flex flex-1 flex-col items-center justify-center px-6 text-center" role="alert">
                  <span className="inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-danger-soft)] text-[var(--color-danger)]">
                    <FileQuestion size={21} aria-hidden />
                  </span>
                  <p className="mt-4 text-sm text-[var(--color-fg-muted)]">{listError}</p>
                  <Button
                    variant="secondary"
                    size="sm"
                    className="mt-4"
                    leadingIcon={<RefreshCw size={14} aria-hidden />}
                    onClick={() => void load()}
                  >
                    {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
                  </Button>
                </div>
              ) : rows.length === 0 ? (
                <EmptyState
                  className="my-auto py-10"
                  icon={filtersActive ? <Search size={21} aria-hidden /> : <FolderOpen size={21} aria-hidden />}
                  title={filtersActive ? t('files:list.emptyFilteredTitle') : t('files:empty.title')}
                  description={filtersActive ? t('files:list.emptyFilteredBody') : t('files:empty.body')}
                  action={filtersActive ? (
                    <Button variant="secondary" size="sm" onClick={resetFilters}>
                      {t('files:list.clearFilters')}
                    </Button>
                  ) : undefined}
                />
              ) : (
                <ul
                  className={cn(
                    'min-h-0 flex-1 overflow-y-auto p-1.5 scrollbar-thin transition-opacity duration-150',
                    refreshing && 'opacity-60',
                  )}
                  aria-busy={refreshing || undefined}
                  aria-label={t('files:accessibility.fileList')}
                >
                  {fileTree.folders.map((folder) => (
                    <UserFileFolderRows
                      key={`folder:${folder.path}`}
                      node={folder}
                      depth={0}
                      expanded={expandedFolders}
                      onToggle={(path) =>
                        setExpandedFolders((current) => {
                          const next = new Set(current)
                          if (next.has(path)) next.delete(path)
                          else next.add(path)
                          return next
                        })
                      }
                      onDeleteFolder={setConfirmDeleteFolder}
                      selectedKey={selectedKey}
                      onOpen={(file) => void openPreview(file)}
                      onDeleteFile={setConfirmDelete}
                      shortDateFormat={shortDateFormat}
                      labels={{
                        conversation: t('files:origin.conversation'),
                        kb: t('files:origin.kb'),
                        deleteAction: t('common:actions.delete', { defaultValue: 'Delete' }),
                        folderSummary: (count, size) =>
                          t('files:folderSummary', { defaultValue: '{{count}} files · {{size}}', count, size }),
                        expand: (name) => t('composer.folderExpand', { defaultValue: 'Expand {{folder}}', folder: name }),
                        collapse: (name) => t('composer.folderCollapse', { defaultValue: 'Collapse {{folder}}', folder: name }),
                        removeFolder: (name) => t('composer.folderRemoveAll', { defaultValue: 'Remove {{folder}}', folder: name }),
                      }}
                    />
                  ))}
                  {fileTree.rootFiles.map((file) => (
                    <UserFileRow
                      key={rowKey(file)}
                      file={file}
                      depth={0}
                      selected={rowKey(file) === selectedKey}
                      onOpen={() => void openPreview(file)}
                      onDelete={() => setConfirmDelete(file)}
                      shortDateFormat={shortDateFormat}
                      labels={{
                        conversation: t('files:origin.conversation'),
                        kb: t('files:origin.kb'),
                        deleteAction: t('common:actions.delete', { defaultValue: 'Delete' }),
                      }}
                    />
                  ))}
                </ul>
              )}

              {pageCount > 1 ? (
                <div className="shrink-0 px-3 pb-3">
                  <Pagination
                    page={page}
                    pageCount={pageCount}
                    onPage={setPage}
                    className="[&_button]:size-[var(--tap-min)] sm:[&_button]:size-8"
                  />
                </div>
              ) : null}
            </div>
          </aside>

          <section
            className={cn(
              'min-h-0 min-w-0 flex-1 flex-col bg-[var(--color-surface-sunken)] lg:flex',
              mobilePreviewOpen ? 'flex' : 'hidden',
            )}
            aria-label={t('files:accessibility.previewPane')}
          >
            {preview ? (
              <>
                <header className="flex min-h-14 shrink-0 items-center gap-2 border-b border-[var(--color-divider)] bg-[var(--color-surface)] px-2 sm:px-4">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="lg:hidden [@media(pointer:coarse)]:size-11"
                    aria-label={t('files:preview.backToList')}
                    onClick={() => setMobilePreviewOpen(false)}
                  >
                    <ArrowLeft size={18} aria-hidden />
                  </Button>
                  <PreviewFileTile file={preview.file} />
                  <div className="min-w-0 flex-1">
                    <h2 className="truncate text-[0.9375rem] font-semibold text-[var(--color-fg)]" title={preview.file.filename}>
                      {preview.file.filename}
                    </h2>
                    <p className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[12px] tabular-nums text-[var(--color-fg-subtle)]">
                      <span className="shrink-0">{typeLabel(preview.file)}</span>
                      <span aria-hidden className="opacity-50">·</span>
                      <span className="shrink-0">{formatBytes(preview.file.size_bytes)}</span>
                      <span aria-hidden className="opacity-50 max-sm:hidden">·</span>
                      <span className="truncate max-sm:hidden">{timeFormat.format(new Date(preview.file.created_at * 1000))}</span>
                      <span aria-hidden className="opacity-50">·</span>
                      <FileSourceLink
                        file={preview.file}
                        labels={{ conversation: t('files:origin.conversation'), kb: t('files:origin.kb') }}
                      />
                    </p>
                  </div>
                  <Tooltip content={t('files:preview.download')}>
                    <Button
                      asChild
                      variant="ghost"
                      size="icon-sm"
                      className="[@media(pointer:coarse)]:size-11"
                      aria-label={t('files:preview.download')}
                    >
                      <a href={preview.url ?? fileContentUrl(preview.file)} download={preview.file.filename}>
                        <Download size={17} aria-hidden />
                      </a>
                    </Button>
                  </Tooltip>
                  <Tooltip content={t('common:actions.delete', { defaultValue: 'Delete' })}>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      className="text-[var(--color-fg-muted)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] [@media(pointer:coarse)]:size-11"
                      aria-label={`${t('common:actions.delete', { defaultValue: 'Delete' })}: ${preview.file.filename}`}
                      onClick={() => setConfirmDelete(preview.file)}
                    >
                      <Trash2 size={17} aria-hidden />
                    </Button>
                  </Tooltip>
                </header>
                <div className="min-h-0 flex-1">
                  <DocumentPreview
                    key={preview.key}
                    name={preview.file.filename}
                    mimeType={preview.file.mime_type}
                    data={preview.data}
                    objectUrl={preview.url}
                    loading={preview.loading}
                    error={preview.error}
                    onRetry={preview.retryable === false ? undefined : () => void openPreview(preview.file, false)}
                  />
                </div>
              </>
            ) : firstLoad ? (
              <PreviewPaneSkeleton label={t('files:preview.loading')} />
            ) : (
              <div className="flex h-full items-center justify-center p-6">
                <EmptyState
                  className="max-w-sm py-10"
                  icon={<FileQuestion size={21} aria-hidden />}
                  title={rows.length === 0 && !filtersActive ? t('files:empty.title') : t('files:preview.selectTitle')}
                  description={rows.length === 0 && !filtersActive ? t('files:empty.body') : t('files:preview.selectBody')}
                />
              </div>
            )}
          </section>
        </div>
      </main>

      <Dialog
        open={confirmDelete !== null || confirmDeleteFolder !== null}
        onOpenChange={(open) => {
          if (open) return
          setConfirmDelete(null)
          setConfirmDeleteFolder(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>
              {confirmDeleteFolder ? t('files:confirmFolderTitle', { defaultValue: 'Delete this folder?' }) : t('files:confirmTitle')}
            </DialogTitle>
            <DialogDescription>
              {confirmDeleteFolder
                ? t('files:confirmFolderBody', {
                    defaultValue: 'All {{count}} files in “{{name}}” will be removed.',
                    count: confirmDeleteFolder.fileCount,
                    name: confirmDeleteFolder.name,
                  })
                : t('files:confirmBody', { name: confirmDelete?.filename ?? '' })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => {
                setConfirmDelete(null)
                setConfirmDeleteFolder(null)
              }}
              disabled={busy}
            >
              {t('common:actions.cancel', { defaultValue: 'Cancel' })}
            </Button>
            <Button
              variant="destructive"
              loading={busy}
              onClick={() => {
                if (confirmDeleteFolder) void runDeleteFolder(confirmDeleteFolder)
                else if (confirmDelete) void runDelete(confirmDelete)
              }}
            >
              {t('common:actions.delete', { defaultValue: 'Delete' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** Labels the rows need, resolved once by the page so the rows stay dumb. */
interface FileRowLabels {
  conversation: string
  kb: string
  deleteAction: string
}

/**
 * Storage usage as a quiet header for the list pane. It renders a fixed-height
 * placeholder until the first response so the search bar never jumps down.
 */
function StorageMeter({
  loaded,
  quota,
  percent,
  nearFull,
  labels,
}: {
  loaded: boolean
  quota: number
  percent: number
  nearFull: boolean
  labels: { title: string; usedOf: string; percent: string; nearFull: string }
}) {
  return (
    <div className="px-3 pb-3 pt-3">
      <div className="flex min-h-5 items-center justify-between gap-3 text-[13px]">
        <span className="inline-flex min-w-0 items-center gap-2 font-medium text-[var(--color-fg)]">
          <HardDrive size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
          <span className="truncate">{labels.title}</span>
        </span>
        {loaded ? (
          <span className={cn('shrink-0 tabular-nums', nearFull ? 'text-[var(--color-danger)]' : 'text-[var(--color-fg-muted)]')}>
            {labels.usedOf}
          </span>
        ) : (
          <Skeleton shape="line" className="h-3 w-24" />
        )}
      </div>
      {loaded && quota > 0 ? (
        <>
          <div
            className="mt-2 h-1.5 overflow-hidden rounded-full bg-[var(--color-bg-muted)]"
            role="progressbar"
            aria-label={labels.title}
            aria-valuetext={labels.percent}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(percent)}
          >
            <div
              className={cn(
                'h-full rounded-full transition-[width] duration-300',
                nearFull ? 'bg-[var(--color-danger)]' : 'bg-[var(--color-accent)]',
              )}
              style={{ width: `${Math.max(percent, percent > 0 ? 2 : 0)}%` }}
            />
          </div>
          {nearFull ? (
            <p className="mt-1.5 text-[12px] text-[var(--color-danger)]">{labels.nearFull}</p>
          ) : null}
        </>
      ) : null}
    </div>
  )
}

/** "From: <knowledge base | conversation>" that jumps to where the file lives. */
function FileSourceLink({ file, labels }: { file: ApiAdminFile; labels: { conversation: string; kb: string } }) {
  const isKB = file.origin === 'kb'
  const Icon = isKB ? Database : MessageSquare
  const label = isKB ? file.kb_name || labels.kb : labels.conversation
  const to = isKB ? (file.kb_id ? `/kb/${file.kb_id}` : '') : (file.conversation_id ? `/chat/${file.conversation_id}` : '')
  const content = (
    <>
      <Icon size={11} className="shrink-0" aria-hidden />
      <span className="truncate">{label}</span>
    </>
  )
  if (!to) return <span className="inline-flex min-w-0 items-center gap-1">{content}</span>
  return (
    <Link
      to={to}
      className="inline-flex min-w-0 items-center gap-1 rounded-[4px] hover:text-[var(--color-accent)] hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
    >
      {content}
    </Link>
  )
}

function PreviewFileTile({ file }: { file: ApiAdminFile }) {
  const Icon = fileTypeIcon(file.filename, file.mime_type)
  return (
    <span
      aria-hidden
      className={cn('hidden size-9 shrink-0 items-center justify-center rounded-[8px] sm:inline-flex', fileTypeTileClass(file.filename, file.mime_type))}
    >
      <Icon size={17} />
    </span>
  )
}

/** Same row geometry as UserFileRow so the first paint does not shift. */
function FileListSkeleton({ label }: { label: string }) {
  return (
    <div className="space-y-0.5 p-1.5" role="status" aria-label={label}>
      {Array.from({ length: 8 }, (_, index) => (
        <div key={index} className="flex min-h-14 items-center gap-3 px-2.5 py-2">
          <Skeleton className="size-9 shrink-0 rounded-[8px]" />
          <div className="min-w-0 flex-1 space-y-1.5">
            <Skeleton shape="line" className="h-3.5" style={{ width: `${70 - (index % 3) * 12}%` }} />
            <Skeleton shape="line" className="h-3 w-1/2" />
          </div>
        </div>
      ))}
      <span className="sr-only">{label}</span>
    </div>
  )
}

/** Preview pane while the list itself is still loading. */
function PreviewPaneSkeleton({ label }: { label: string }) {
  return (
    <div className="flex h-full min-h-0 flex-col" role="status" aria-label={label}>
      <div className="flex min-h-14 shrink-0 items-center gap-3 border-b border-[var(--color-divider)] bg-[var(--color-surface)] px-4">
        <Skeleton className="hidden size-9 rounded-[8px] sm:block" />
        <div className="min-w-0 flex-1 space-y-1.5">
          <Skeleton shape="line" className="h-3.5 w-48" />
          <Skeleton shape="line" className="h-3 w-64" />
        </div>
      </div>
      <div className="flex min-h-0 flex-1 justify-center p-4 sm:p-6">
        <Skeleton className="h-full w-full max-w-[52rem] rounded-[8px]" />
      </div>
      <span className="sr-only">{label}</span>
    </div>
  )
}

interface FolderRowLabels extends FileRowLabels {
  folderSummary: (count: number, size: string) => string
  expand: (name: string) => string
  collapse: (name: string) => string
  removeFolder: (name: string) => string
}

interface UserFileRowProps {
  file: ApiAdminFile
  depth: number
  selected: boolean
  onOpen: () => void
  onDelete: () => void
  shortDateFormat: Intl.DateTimeFormat
  labels: FileRowLabels
}

/**
 * One file row. `depth` indents it inside the directory that contains it; the
 * markup mirrors the flat list that came before, so selection, preview and delete
 * behave identically whether the file is loose or inside a folder.
 */
function UserFileRow({ file, depth, selected, onOpen, onDelete, shortDateFormat, labels }: UserFileRowProps) {
  const FileIcon = fileTypeIcon(file.filename, file.mime_type)
  const SourceIcon = file.origin === 'kb' ? Database : MessageSquare
  return (
    <li
      className={cn(
        'group/file relative flex min-h-14 items-stretch rounded-[8px] transition-colors',
        selected ? 'bg-[var(--color-accent-soft)]' : 'hover:bg-[var(--color-bg-muted)]',
      )}
    >
      {selected ? (
        <span aria-hidden className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-[var(--color-accent)]" />
      ) : null}
      <button
        type="button"
        aria-current={selected ? 'true' : undefined}
        style={{ paddingLeft: `${0.625 + depth * 0.875}rem` }}
        className="flex min-w-0 flex-1 items-center gap-3 rounded-l-[8px] py-2 pr-2.5 text-left focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
        onClick={onOpen}
      >
        <span
          className={cn(
            'inline-flex size-9 shrink-0 items-center justify-center rounded-[8px]',
            fileTypeTileClass(file.filename, file.mime_type),
          )}
        >
          <FileIcon size={17} aria-hidden />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[0.875rem] font-medium leading-5 text-[var(--color-fg)]" title={file.filename}>
            {file.filename}
          </span>
          <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[12px] leading-4 text-[var(--color-fg-subtle)]">
            <span className="shrink-0 tabular-nums">{formatBytes(file.size_bytes)}</span>
            <span aria-hidden className="opacity-50">·</span>
            <span className="shrink-0 tabular-nums">{shortDateFormat.format(new Date(file.created_at * 1000))}</span>
            <span aria-hidden className="opacity-50">·</span>
            <SourceIcon size={11} className="shrink-0" aria-hidden />
            <span className="truncate">
              {file.origin === 'kb' ? file.kb_name || labels.kb : labels.conversation}
            </span>
          </span>
        </span>
      </button>
      <Tooltip content={labels.deleteAction} side="left">
        <button
          type="button"
          aria-label={`${labels.deleteAction}: ${file.filename}`}
          className="inline-flex w-11 shrink-0 items-center justify-center rounded-r-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] lg:opacity-0 lg:group-hover/file:opacity-100 lg:group-focus-within/file:opacity-100"
          onClick={onDelete}
        >
          <Trash2 size={15} aria-hidden />
        </button>
      </Tooltip>
    </li>
  )
}

interface UserFileFolderRowsProps {
  node: FolderTreeNode<ApiAdminFile>
  depth: number
  expanded: Set<string>
  onToggle: (path: string) => void
  onDeleteFolder: (node: FolderTreeNode<ApiAdminFile>) => void
  selectedKey: string
  onOpen: (file: ApiAdminFile) => void
  onDeleteFile: (file: ApiAdminFile) => void
  shortDateFormat: Intl.DateTimeFormat
  labels: FolderRowLabels
}

/**
 * A directory row plus, when open, its subdirectories and files. This is what
 * turns an uploaded folder back into the one thing the user picked.
 */
function UserFileFolderRows({
  node,
  depth,
  expanded,
  onToggle,
  onDeleteFolder,
  selectedKey,
  onOpen,
  onDeleteFile,
  shortDateFormat,
  labels,
}: UserFileFolderRowsProps) {
  const open = expanded.has(node.path)
  return (
    <li className="flex flex-col">
      <div
        style={{ paddingLeft: `${0.375 + depth * 0.875}rem` }}
        className="group/folder flex min-h-14 items-center gap-1 rounded-[8px] transition-colors hover:bg-[var(--color-bg-muted)]"
      >
        <button
          type="button"
          onClick={() => onToggle(node.path)}
          aria-expanded={open}
          aria-label={open ? labels.collapse(node.name) : labels.expand(node.name)}
          className="flex min-w-0 flex-1 items-center gap-2 py-2 text-left focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
        >
          {open ? (
            <ChevronDown size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
          ) : (
            <ChevronRight size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
          )}
          <span className="inline-flex size-9 shrink-0 items-center justify-center rounded-[8px] bg-[var(--color-warning-soft)] text-[var(--color-warning)]">
            {open ? <FolderOpen size={17} aria-hidden /> : <FolderIcon size={17} aria-hidden />}
          </span>
          <span className="ml-1 min-w-0 flex-1">
            <span className="block truncate text-[0.875rem] font-medium leading-5 text-[var(--color-fg)]" title={node.path}>
              {node.name}
            </span>
            <span className="mt-0.5 block truncate text-[12px] leading-4 tabular-nums text-[var(--color-fg-subtle)]">
              {labels.folderSummary(node.fileCount, formatBytes(node.size))}
            </span>
          </span>
        </button>
        <Tooltip content={labels.removeFolder(node.name)} side="left">
          <button
            type="button"
            aria-label={labels.removeFolder(node.name)}
            className="inline-flex w-11 shrink-0 items-center justify-center rounded-r-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] lg:opacity-0 lg:group-hover/folder:opacity-100 lg:group-focus-within/folder:opacity-100"
            onClick={() => onDeleteFolder(node)}
          >
            <Trash2 size={15} aria-hidden />
          </button>
        </Tooltip>
      </div>
      {open ? (
        <ul className="flex flex-col">
          {node.children.map((child) => (
            <UserFileFolderRows
              key={`folder:${child.path}`}
              node={child}
              depth={depth + 1}
              expanded={expanded}
              onToggle={onToggle}
              onDeleteFolder={onDeleteFolder}
              selectedKey={selectedKey}
              onOpen={onOpen}
              onDeleteFile={onDeleteFile}
              shortDateFormat={shortDateFormat}
              labels={labels}
            />
          ))}
          {node.files.map((file) => (
            <UserFileRow
              key={rowKey(file)}
              file={file}
              depth={depth + 1}
              selected={rowKey(file) === selectedKey}
              onOpen={() => onOpen(file)}
              onDelete={() => onDeleteFile(file)}
              shortDateFormat={shortDateFormat}
              labels={labels}
            />
          ))}
        </ul>
      ) : null}
    </li>
  )
}
