/**
 * UserFiles is a master-detail workspace over the signed-in user's uploads.
 * Desktop keeps the compact file browser and preview visible together; smaller
 * screens use a list -> preview flow so neither surface is squeezed.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  Download,
  FileQuestion,
  FolderOpen,
  HardDrive,
  MessageSquare,
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
import { fileIconFor } from '@/lib/file-icon'
import {
  documentPreviewByteLimit,
  documentPreviewKind,
  type FileTypeFilter,
} from '@/lib/file-preview-kind'
import { cn } from '@/lib/utils'

const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE', 50)
const ALL = 'all'

function fmtBytes(n: number): string {
  if (n >= 1024 * 1024 * 1024) return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${n} B`
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
  const [busy, setBusy] = useState(false)

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

  const runDelete = async (file: ApiAdminFile) => {
    setBusy(true)
    try {
      await authApi.deleteMyFiles([{ source: file.source, id: file.id }])
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

  const quota = storage?.quota_bytes ?? 0
  const used = storage?.used_bytes ?? 0
  const storagePercent = quota > 0 ? Math.min(100, (used / quota) * 100) : 0
  const storageNearFull = quota > 0 && storagePercent >= 90
  const pageCount = Math.ceil(total / PAGE_SIZE)
  const activeFilterCount =
    (fileType !== 'all' ? 1 : 0) +
    (origin !== ALL ? 1 : 0) +
    (sort !== 'created_at' || order !== 'desc' ? 1 : 0)

  return (
    <div className="flex h-full min-h-0 flex-col">
      <ContentHeader title={t('files:title')} fluid />
      <main className="min-h-0 flex-1 overflow-hidden border-t border-[var(--color-divider)]">
        <div className="flex h-full min-h-0 w-full overflow-hidden bg-[var(--color-surface)]">
          <aside
            className={cn(
              'min-h-0 w-full flex-col bg-[var(--color-bg)] lg:flex lg:w-[20rem] lg:shrink-0 lg:border-r lg:border-[var(--color-border)] xl:w-[21rem]',
              mobilePreviewOpen ? 'hidden' : 'flex',
            )}
            aria-label={t('files:accessibility.fileList')}
          >
            <div className="border-b border-[var(--color-divider)] px-3 py-3">
              <div className="flex items-center justify-between gap-3 text-[0.8125rem]">
                <span className="inline-flex min-w-0 items-center gap-2 font-medium text-[var(--color-fg)]">
                  <HardDrive size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                  <span className="truncate">{t('files:storage.title')}</span>
                </span>
                <span className="shrink-0 tabular-nums text-[var(--color-fg-muted)]">
                  {quota > 0
                    ? t('files:storage.usedOf', { used: fmtBytes(used), quota: fmtBytes(quota) })
                    : t('files:storage.usedUnlimited', { used: fmtBytes(used) })}
                </span>
              </div>
              {quota > 0 ? (
                <div
                  className="mt-2 h-1.5 overflow-hidden rounded-full bg-[var(--color-bg-muted)]"
                  role="progressbar"
                  aria-label={t('files:storage.title')}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={Math.round(storagePercent)}
                >
                  <div
                    className={cn(
                      'h-full rounded-full transition-[width] duration-200',
                      storageNearFull ? 'bg-[var(--color-danger)]' : 'bg-[var(--color-accent)]',
                    )}
                    style={{ width: `${storagePercent}%` }}
                  />
                </div>
              ) : null}
            </div>

            <div className="flex items-center gap-2 border-b border-[var(--color-divider)] px-3 py-2">
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

            <div className="flex min-h-0 flex-1 flex-col">
              <div className="flex h-9 shrink-0 items-center justify-between border-b border-[var(--color-divider)] px-3 text-[0.75rem] text-[var(--color-fg-subtle)]">
                <span>{t('files:list.title')}</span>
                <span className="tabular-nums">{t('files:total', { count: total })}</span>
              </div>

              {loading ? (
                <div className="space-y-1 p-2" aria-label={t('common:loading', { defaultValue: 'Loading' })}>
                  {Array.from({ length: 7 }, (_, index) => (
                    <div key={index} className="flex items-center gap-3 px-2 py-2.5">
                      <Skeleton className="size-9 shrink-0" />
                      <div className="min-w-0 flex-1 space-y-2">
                        <Skeleton shape="line" className="w-4/5" />
                        <Skeleton shape="line" className="h-2.5 w-3/5" />
                      </div>
                    </div>
                  ))}
                </div>
              ) : listError ? (
                <div className="flex flex-1 flex-col items-center justify-center px-6 text-center">
                  <FileQuestion size={24} className="text-[var(--color-danger)]" aria-hidden />
                  <p className="mt-3 text-sm text-[var(--color-fg-muted)]">{listError}</p>
                  <Button variant="secondary" size="sm" className="mt-4" onClick={() => void load()}>
                    {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
                  </Button>
                </div>
              ) : rows.length === 0 ? (
                <EmptyState
                  className="my-auto py-10"
                  icon={<FolderOpen size={21} aria-hidden />}
                  title={
                    searchDebounced || origin !== ALL || fileType !== 'all'
                      ? t('files:list.emptyFilteredTitle')
                      : t('files:empty.title')
                  }
                  description={
                    searchDebounced || origin !== ALL || fileType !== 'all'
                      ? t('files:list.emptyFilteredBody')
                      : t('files:empty.body')
                  }
                />
              ) : (
                <ul
                  className="min-h-0 flex-1 overflow-y-auto p-1.5 scrollbar-thin"
                  aria-label={t('files:accessibility.fileList')}
                >
                  {rows.map((file) => {
                    const key = rowKey(file)
                    const selected = key === selectedKey
                    const FileIcon = fileIconFor(file.filename)
                    return (
                      <li
                        key={key}
                        className={cn(
                          'group/file flex min-h-16 items-stretch rounded-[8px] transition-colors',
                          selected ? 'bg-[var(--color-accent-soft)]' : 'hover:bg-[var(--color-bg-muted)]',
                        )}
                      >
                        <button
                          type="button"
                          aria-current={selected ? 'true' : undefined}
                          className="flex min-w-0 flex-1 items-center gap-2.5 rounded-l-[8px] px-2.5 py-2 text-left focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
                          onClick={() => void openPreview(file)}
                        >
                          <span
                            className={cn(
                              'inline-flex size-9 shrink-0 items-center justify-center rounded-[8px]',
                              selected
                                ? 'bg-[var(--color-surface)] text-[var(--color-accent)]'
                                : 'bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]',
                            )}
                          >
                            <FileIcon size={17} aria-hidden />
                          </span>
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-[0.875rem] font-medium text-[var(--color-fg)]" title={file.filename}>
                              {file.filename}
                            </span>
                            <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[0.71875rem] text-[var(--color-fg-subtle)]">
                              <span className="shrink-0 font-medium">{typeLabel(file)}</span>
                              <span aria-hidden>·</span>
                              <span className="shrink-0 tabular-nums">{fmtBytes(file.size_bytes)}</span>
                              <span aria-hidden>·</span>
                              <span className="truncate">{shortDateFormat.format(new Date(file.created_at * 1000))}</span>
                            </span>
                            <span className="mt-0.5 flex min-w-0 items-center gap-1 text-[0.71875rem] text-[var(--color-fg-subtle)]">
                              {file.origin === 'kb' ? <FolderOpen size={11} className="shrink-0" aria-hidden /> : <MessageSquare size={11} className="shrink-0" aria-hidden />}
                              <span className="truncate">
                                {file.origin === 'kb' ? file.kb_name || t('files:origin.kb') : t('files:origin.conversation')}
                              </span>
                            </span>
                          </span>
                        </button>
                        <Tooltip content={t('common:actions.delete', { defaultValue: 'Delete' })} side="left">
                          <button
                            type="button"
                            aria-label={`${t('common:actions.delete', { defaultValue: 'Delete' })}: ${file.filename}`}
                            className="inline-flex w-11 shrink-0 items-center justify-center rounded-r-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] lg:opacity-0 lg:group-hover/file:opacity-100 lg:group-focus-within/file:opacity-100"
                            onClick={() => setConfirmDelete(file)}
                          >
                            <Trash2 size={15} aria-hidden />
                          </button>
                        </Tooltip>
                      </li>
                    )
                  })}
                </ul>
              )}

              {pageCount > 1 ? (
                <div className="shrink-0 border-t border-[var(--color-divider)] px-3 pb-3">
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
                <header className="flex min-h-12 shrink-0 items-center gap-1 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-2 sm:px-3">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="lg:hidden [@media(pointer:coarse)]:size-11"
                    aria-label={t('files:preview.backToList')}
                    onClick={() => setMobilePreviewOpen(false)}
                  >
                    <ArrowLeft size={18} aria-hidden />
                  </Button>
                  <div className="min-w-0 flex-1">
                    <h2 className="truncate text-[0.9375rem] font-semibold text-[var(--color-fg)]" title={preview.file.filename}>
                      {preview.file.filename}
                    </h2>
                    <p className="mt-0.5 truncate text-[0.71875rem] tabular-nums text-[var(--color-fg-subtle)]">
                      {typeLabel(preview.file)} · {fmtBytes(preview.file.size_bytes)} · {timeFormat.format(new Date(preview.file.created_at * 1000))}
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
                      className="text-[var(--color-danger)] [@media(pointer:coarse)]:size-11"
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
            ) : (
              <div className="flex h-full items-center justify-center p-6">
                <div className="max-w-sm text-center">
                  <span className="mx-auto inline-flex size-12 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                    <FileQuestion size={21} aria-hidden />
                  </span>
                  <h2 className="mt-4 text-lg font-semibold text-[var(--color-fg)]">{t('files:preview.selectTitle')}</h2>
                  <p className="mt-2 text-sm leading-relaxed text-[var(--color-fg-muted)]">{t('files:preview.selectBody')}</p>
                </div>
              </div>
            )}
          </section>
        </div>
      </main>

      <Dialog open={confirmDelete !== null} onOpenChange={(open) => !open && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('files:confirmTitle')}</DialogTitle>
            <DialogDescription>{t('files:confirmBody', { name: confirmDelete?.filename ?? '' })}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)} disabled={busy}>
              {t('common:actions.cancel', { defaultValue: 'Cancel' })}
            </Button>
            <Button variant="destructive" loading={busy} onClick={() => confirmDelete && void runDelete(confirmDelete)}>
              {t('common:actions.delete', { defaultValue: 'Delete' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
