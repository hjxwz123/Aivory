/**
 * AdminFiles is a master-detail workspace over every user upload. Desktop
 * keeps inventory controls and the native preview visible together; narrower
 * screens use a list -> preview flow so neither surface is squeezed.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  Download,
  FileQuestion,
  FolderOpen,
  MessageSquare,
  Search,
  Trash2,
  UserRound,
} from 'lucide-react'
import { adminApi, apiUrl, ApiError } from '@/api'
import type { ApiAdminFile } from '@/api/types'
import { DocumentPreview } from '@/components/files/document-preview'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
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

function ownerLabel(file: ApiAdminFile): string {
  return file.user_email || file.user_name || file.user_id || '-'
}

function ownerTitle(file: ApiAdminFile): string {
  return [file.user_name, file.user_email, file.user_id].filter(Boolean).join(' / ')
}

function rowKey(file: ApiAdminFile): string {
  return `${file.source}:${file.id}`
}

function fileContentUrl(file: ApiAdminFile): string {
  const query = new URLSearchParams({ source: file.source, id: file.id })
  return apiUrl(`/admin/files/content?${query}`)
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

export default function AdminFiles() {
  const { t, i18n } = useTranslation(['admin', 'files', 'common'])
  // The admin rail leaves too little preview width at tablet sizes. Keep the
  // two-pane workspace for wide desktops and use master -> detail below that.
  const compact = useMediaQuery('(max-width: 1279px)')
  const [search, setSearch] = useState('')
  const [searchDebounced, setSearchDebounced] = useState('')
  const [userQ, setUserQ] = useState('')
  const [userQDebounced, setUserQDebounced] = useState('')
  const [origin, setOrigin] = useState(ALL)
  const [fileType, setFileType] = useState<FileTypeFilter>('all')
  const [sort, setSort] = useState('created_at')
  const [order, setOrder] = useState<'desc' | 'asc'>('desc')

  const [rows, setRows] = useState<ApiAdminFile[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [listError, setListError] = useState('')
  const [page, setPage] = useState(1)

  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [selectedKey, setSelectedKey] = useState('')
  const [mobilePreviewOpen, setMobilePreviewOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState<ApiAdminFile[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<PreviewState | null>(null)

  const listRequestRef = useRef(0)
  const previewRequestRef = useRef(0)
  const previewUrlRef = useRef<string | null>(null)
  const previewAbortRef = useRef<AbortController | null>(null)
  const selectAllRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    const id = window.setTimeout(() => setSearchDebounced(search.trim()), 350)
    return () => window.clearTimeout(id)
  }, [search])

  useEffect(() => {
    const id = window.setTimeout(() => setUserQDebounced(userQ.trim()), 350)
    return () => window.clearTimeout(id)
  }, [userQ])

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

  const load = useCallback(async () => {
    const request = ++listRequestRef.current
    setLoading(true)
    setListError('')
    try {
      const response = await adminApi.files({
        search: searchDebounced,
        user: userQDebounced,
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
        setSelected(new Set())
        setPage(lastPage)
        return
      }
      setRows(response.files)
      setSelected(new Set())
    } catch (error) {
      if (request !== listRequestRef.current) return
      const message = error instanceof ApiError ? error.message : t('common:actions.failed', { defaultValue: 'Failed' })
      setListError(message)
      toast.error(message)
    } finally {
      if (request === listRequestRef.current) setLoading(false)
    }
  }, [fileType, order, origin, page, searchDebounced, sort, t, userQDebounced])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    setPage(1)
    setMobilePreviewOpen(false)
  }, [searchDebounced, userQDebounced, origin, fileType, sort, order])

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
        const blob = await adminApi.fileContentBlob(file.source, file.id, controller.signal)
        if (controller.signal.aborted || request !== previewRequestRef.current) return
        if (kind === 'image') {
          const url = URL.createObjectURL(blob)
          previewUrlRef.current = url
          setPreview({ key, file, loading: false, url })
          return
        }
        const data = await blob.arrayBuffer()
        if (controller.signal.aborted || request !== previewRequestRef.current) return
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

  // Desktop selects the first valid result. Compact layouts wait for a tap so
  // opening the page never downloads a document behind the file list.
  useEffect(() => {
    if (loading) return
    if (rows.length === 0) {
      setSelectedKey('')
      setMobilePreviewOpen(false)
      clearPreview()
      return
    }
    const active = rows.find((file) => rowKey(file) === selectedKey)
    if (compact) {
      if (selectedKey && !active) {
        setSelectedKey('')
        clearPreview()
      }
      return
    }
    const next = active ?? rows[0]
    const nextKey = rowKey(next)
    if (!active) setSelectedKey(nextKey)
    if (preview?.key !== nextKey) void openPreview(next, false)
  }, [clearPreview, compact, loading, openPreview, preview?.key, rows, selectedKey])

  const runDelete = async (items: ApiAdminFile[]) => {
    setBusy(true)
    try {
      const response = await adminApi.deleteFiles(items.map((file) => ({ source: file.source, id: file.id })))
      if (preview && items.some((file) => rowKey(file) === preview.key)) {
        clearPreview()
        setSelectedKey('')
        setMobilePreviewOpen(false)
      }
      toast.success(t('admin:files.deleted', { count: response.deleted }))
      setConfirmDelete(null)
      await load()
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

  const selectedRows = rows.filter((file) => selected.has(rowKey(file)))
  const allChecked = rows.length > 0 && selectedRows.length === rows.length
  const partiallyChecked = selectedRows.length > 0 && !allChecked
  const pageCount = Math.ceil(total / PAGE_SIZE)

  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = partiallyChecked
  }, [partiallyChecked])

  const toggleAll = () => {
    setSelected(allChecked ? new Set() : new Set(rows.map(rowKey)))
  }

  const toggleOne = (file: ApiAdminFile) => {
    setSelected((current) => {
      const next = new Set(current)
      const key = rowKey(file)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <div className="flex h-[calc(100svh-var(--layout-topbar-h-mobile)-4rem)] min-h-0 flex-col sm:h-[calc(100svh-var(--layout-topbar-h-mobile)-6rem)] md:h-[calc(100svh-6rem)]">
      <header className="mb-4 flex shrink-0 items-end justify-between gap-4">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl text-[var(--color-fg)] sm:text-3xl">
            {t('admin:files.title')}
          </h1>
          <p className="mt-1.5 hidden max-w-2xl text-sm text-[var(--color-fg-muted)] sm:block">
            {t('admin:files.lead')}
          </p>
        </div>
        <span className="shrink-0 text-xs tabular-nums text-[var(--color-fg-subtle)]">
          {t('admin:files.total', { count: total })}
        </span>
      </header>

      <div className="flex min-h-0 flex-1 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
        <aside
          className={cn(
            'min-h-0 w-full flex-col bg-[var(--color-bg)] xl:flex xl:w-[22rem] xl:shrink-0 xl:border-r xl:border-[var(--color-border)] 2xl:w-[23rem]',
            mobilePreviewOpen ? 'hidden' : 'flex',
          )}
          aria-label={t('files:accessibility.fileList')}
        >
          <div className="space-y-2 border-b border-[var(--color-divider)] p-3">
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              leadingIcon={<Search size={15} aria-hidden />}
              placeholder={t('admin:files.filters.searchPlaceholder')}
              aria-label={t('admin:files.filters.search')}
              wrapperClassName="w-full"
            />
            <Input
              value={userQ}
              onChange={(event) => setUserQ(event.target.value)}
              leadingIcon={<UserRound size={15} aria-hidden />}
              placeholder={t('admin:files.filters.userSearchPlaceholder')}
              aria-label={t('admin:files.filters.user')}
              wrapperClassName="w-full"
            />
            <div className="grid grid-cols-2 gap-2">
              <Select value={fileType} onValueChange={(value) => setFileType(value as FileTypeFilter)}>
                <SelectTrigger
                  aria-label={t('files:filters.typeLabel')}
                  className="min-w-0 px-3 [&>span:first-child]:truncate"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(['all', 'pdf', 'document', 'presentation', 'spreadsheet', 'image', 'text', 'other'] as const).map(
                    (value) => (
                      <SelectItem key={value} value={value}>
                        {t(`files:types.${value}`)}
                      </SelectItem>
                    ),
                  )}
                </SelectContent>
              </Select>
              <Select value={origin} onValueChange={setOrigin}>
                <SelectTrigger
                  aria-label={t('admin:files.filters.origin')}
                  className="min-w-0 px-3 [&>span:first-child]:truncate"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('admin:files.origin.all')}</SelectItem>
                  <SelectItem value="conversation">{t('admin:files.origin.conversation')}</SelectItem>
                  <SelectItem value="kb">{t('admin:files.origin.kb')}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Select
              value={`${sort}-${order}`}
              onValueChange={(value) => {
                const [nextSort, nextOrder] = value.split('-') as [string, 'desc' | 'asc']
                setSort(nextSort)
                setOrder(nextOrder)
              }}
            >
              <SelectTrigger aria-label={t('admin:files.filters.sort')} className="px-3">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="created_at-desc">{t('admin:files.sort.newest')}</SelectItem>
                <SelectItem value="created_at-asc">{t('admin:files.sort.oldest')}</SelectItem>
                <SelectItem value="size_bytes-desc">{t('admin:files.sort.largest')}</SelectItem>
                <SelectItem value="size_bytes-asc">{t('admin:files.sort.smallest')}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="flex min-h-0 flex-1 flex-col">
            <div className="flex h-11 shrink-0 items-center border-b border-[var(--color-divider)] px-1.5 text-xs text-[var(--color-fg-subtle)]">
              <label className="inline-flex size-10 shrink-0 cursor-pointer items-center justify-center rounded-[8px] hover:bg-[var(--color-bg-muted)]">
                <input
                  ref={selectAllRef}
                  type="checkbox"
                  className="cursor-pointer accent-[var(--color-accent)]"
                  checked={allChecked}
                  onChange={toggleAll}
                  aria-label={t('admin:files.selectAll')}
                />
              </label>
              <span className="min-w-0 flex-1 truncate">
                {selectedRows.length > 0
                  ? t('files:selection.selected', { defaultValue: 'Selected' }) + `: ${selectedRows.length}`
                  : t('files:list.title')}
              </span>
              <span className="mr-1 shrink-0 tabular-nums">{t('admin:files.total', { count: total })}</span>
              {selectedRows.length > 0 ? (
                <Tooltip content={t('admin:files.deleteSelected', { count: selectedRows.length })} side="left">
                  <Button
                    variant="ghost"
                    size="icon"
                    className="shrink-0 text-[var(--color-danger)]"
                    aria-label={t('admin:files.deleteSelected', { count: selectedRows.length })}
                    disabled={busy}
                    onClick={() => setConfirmDelete(selectedRows)}
                  >
                    <Trash2 size={15} aria-hidden />
                  </Button>
                </Tooltip>
              ) : null}
            </div>

            {loading ? (
              <div className="space-y-1 p-2" aria-label={t('common:loading', { defaultValue: 'Loading' })}>
                {Array.from({ length: 7 }, (_, index) => (
                  <div key={index} className="flex min-h-20 items-center gap-2 px-1 py-2">
                    <Skeleton className="size-9 shrink-0" />
                    <div className="min-w-0 flex-1 space-y-2">
                      <Skeleton shape="line" className="w-4/5" />
                      <Skeleton shape="line" className="h-2.5 w-3/5" />
                      <Skeleton shape="line" className="h-2.5 w-2/3" />
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
                title={t('admin:files.empty')}
                description={
                  searchDebounced || userQDebounced || origin !== ALL || fileType !== 'all'
                    ? t('files:list.emptyFilteredBody')
                    : t('admin:files.lead')
                }
              />
            ) : (
              <ul
                className="min-h-0 flex-1 overflow-y-auto p-1.5 scrollbar-thin"
                aria-label={t('files:accessibility.fileList')}
              >
                {rows.map((file) => {
                  const key = rowKey(file)
                  const active = key === selectedKey
                  const checked = selected.has(key)
                  const FileIcon = fileIconFor(file.filename)
                  const source =
                    file.origin === 'kb'
                      ? file.kb_name || t('admin:files.origin.kb')
                      : t('admin:files.origin.conversation')
                  return (
                    <li
                      key={key}
                      className={cn(
                        'group/file flex min-h-20 items-stretch rounded-[8px] transition-colors',
                        active ? 'bg-[var(--color-accent-soft)]' : 'hover:bg-[var(--color-bg-muted)]',
                      )}
                    >
                      <label className="inline-flex w-11 shrink-0 cursor-pointer items-center justify-center rounded-l-[8px] focus-within:ring-2 focus-within:ring-inset focus-within:ring-[var(--color-ring)]">
                        <input
                          type="checkbox"
                          className="cursor-pointer accent-[var(--color-accent)]"
                          checked={checked}
                          onChange={() => toggleOne(file)}
                          aria-label={t('admin:files.selectOne', { name: file.filename })}
                        />
                      </label>
                      <button
                        type="button"
                        aria-current={active ? 'true' : undefined}
                        className="flex min-w-0 flex-1 items-center gap-2 py-2 pr-1 text-left focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
                        onClick={() => void openPreview(file)}
                      >
                        <span
                          className={cn(
                            'inline-flex size-9 shrink-0 items-center justify-center rounded-[8px]',
                            active
                              ? 'bg-[var(--color-surface)] text-[var(--color-accent)]'
                              : 'bg-[var(--color-surface-sunken)] text-[var(--color-fg-muted)]',
                          )}
                        >
                          <FileIcon size={17} aria-hidden />
                        </span>
                        <span className="min-w-0 flex-1">
                          <span
                            className="block truncate text-sm font-medium text-[var(--color-fg)]"
                            title={file.filename}
                          >
                            {file.filename}
                          </span>
                          <span
                            className="mt-0.5 flex min-w-0 items-center gap-1 text-[0.71875rem] text-[var(--color-fg-subtle)]"
                            title={ownerTitle(file)}
                          >
                            <UserRound size={11} className="shrink-0" aria-hidden />
                            <span className="truncate">{ownerLabel(file)}</span>
                          </span>
                          <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-[0.71875rem] text-[var(--color-fg-subtle)]">
                            {file.origin === 'kb' ? (
                              <FolderOpen size={11} className="shrink-0" aria-hidden />
                            ) : (
                              <MessageSquare size={11} className="shrink-0" aria-hidden />
                            )}
                            <span className="min-w-0 truncate" title={source}>{source}</span>
                            <span className="shrink-0" aria-hidden>·</span>
                            <span className="shrink-0 tabular-nums">{fmtBytes(file.size_bytes)}</span>
                            <span className="shrink-0" aria-hidden>·</span>
                            <span className="min-w-0 truncate">{shortDateFormat.format(new Date(file.created_at * 1000))}</span>
                          </span>
                        </span>
                      </button>
                      <Tooltip content={t('common:actions.delete', { defaultValue: 'Delete' })} side="left">
                        <button
                          type="button"
                          aria-label={`${t('common:actions.delete', { defaultValue: 'Delete' })}: ${file.filename}`}
                          className="inline-flex w-11 shrink-0 items-center justify-center rounded-r-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
                          onClick={() => setConfirmDelete([file])}
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
            'min-h-0 min-w-0 flex-1 flex-col bg-[var(--color-surface-sunken)] xl:flex',
            mobilePreviewOpen ? 'flex' : 'hidden',
          )}
          aria-label={t('files:accessibility.previewPane')}
        >
          {preview ? (
            <>
              <header className="flex min-h-16 shrink-0 items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-2 sm:px-4">
                <Button
                  variant="ghost"
                  size="icon-lg"
                  className="xl:hidden"
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
                  <p className="mt-0.5 truncate text-[0.71875rem] text-[var(--color-fg-subtle)]" title={ownerTitle(preview.file)}>
                    {ownerLabel(preview.file)} · {preview.file.origin === 'kb' ? preview.file.kb_name || t('admin:files.origin.kb') : t('admin:files.origin.conversation')}
                  </p>
                </div>
                <Tooltip content={t('admin:files.download')}>
                  <Button asChild variant="ghost" size="icon-lg" aria-label={t('admin:files.download')}>
                    <a href={preview.url ?? fileContentUrl(preview.file)} download={preview.file.filename}>
                      <Download size={17} aria-hidden />
                    </a>
                  </Button>
                </Tooltip>
                <Tooltip content={t('common:actions.delete', { defaultValue: 'Delete' })}>
                  <Button
                    variant="ghost"
                    size="icon-lg"
                    className="text-[var(--color-danger)]"
                    aria-label={`${t('common:actions.delete', { defaultValue: 'Delete' })}: ${preview.file.filename}`}
                    onClick={() => setConfirmDelete([preview.file])}
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

      <Dialog open={confirmDelete !== null} onOpenChange={(open) => !open && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>
              {t('admin:files.confirmTitle', { count: confirmDelete?.length ?? 0 })}
            </DialogTitle>
            <DialogDescription>{t('admin:files.confirmBody')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <ul className="max-h-40 space-y-1 overflow-y-auto text-sm text-[var(--color-fg-muted)]">
              {(confirmDelete ?? []).slice(0, 12).map((file) => (
                <li key={rowKey(file)} className="truncate">
                  {file.filename}
                </li>
              ))}
              {(confirmDelete?.length ?? 0) > 12 ? (
                <li className="text-[var(--color-fg-subtle)]">
                  {t('admin:files.confirmMore', { count: (confirmDelete?.length ?? 0) - 12 })}
                </li>
              ) : null}
            </ul>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)} disabled={busy}>
              {t('common:actions.cancel', { defaultValue: 'Cancel' })}
            </Button>
            <Button
              variant="destructive"
              loading={busy}
              onClick={() => confirmDelete && void runDelete(confirmDelete)}
            >
              {t('common:actions.delete', { defaultValue: 'Delete' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
