/**
 * AdminFiles — inventory of every user upload (§ admin files): conversation
 * attachments (files table) and knowledge-base documents (documents table) in
 * one list. Filter by filename, owner, and source; sort by time or size;
 * preview file contents; delete one or many. Deletion runs the same cleanup
 * chain as the user-facing routes (DB rows → vectors → physical bytes).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, FileText, FolderOpen, MessageSquare, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiAdminFile } from '@/api/types'
import { Badge } from '@/components/ui/badge'
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
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { envNum } from '@/lib/env-config'
import { PanelFallback } from '@/components/ui/panel-fallback'

const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE', 50)
const ALL = 'all'
// Text preview stays readable and cheap; anything bigger is download-only.
const TEXT_PREVIEW_MAX_BYTES = 2 * 1024 * 1024

const TEXT_EXTENSIONS = /\.(txt|md|markdown|json|csv|tsv|log|xml|yaml|yml|toml|ini|py|js|ts|go|java|c|cpp|h|rs|sh|sql|html|css)$/i

type PreviewKind = 'image' | 'pdf' | 'text' | 'binary'

function previewKind(f: ApiAdminFile): PreviewKind {
  const mime = f.mime_type.toLowerCase()
  if (mime.startsWith('image/')) return 'image'
  if (mime === 'application/pdf' || /\.pdf$/i.test(f.filename)) return 'pdf'
  if (mime.startsWith('text/') || TEXT_EXTENSIONS.test(f.filename)) return 'text'
  return 'binary'
}

function fmtBytes(n: number): string {
  if (n >= 1024 * 1024 * 1024) return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${n} B`
}

function rowKey(f: ApiAdminFile): string {
  return `${f.source}:${f.id}`
}

interface PreviewState {
  file: ApiAdminFile
  kind: PreviewKind
  loading: boolean
  /** Object URL for image/pdf, decoded text for text files. */
  url?: string
  text?: string
  error?: string
}

export default function AdminFiles() {
  const { t, i18n } = useTranslation('admin')
  const [search, setSearch] = useState('')
  const [searchDebounced, setSearchDebounced] = useState('')
  // Free-text owner filter (user_id exact or email/name substring) — same UX
  // as the usage page. A dropdown of every user doesn't scale.
  const [userQ, setUserQ] = useState('')
  const [userQDebounced, setUserQDebounced] = useState('')
  const [origin, setOrigin] = useState(ALL)
  const [sort, setSort] = useState('created_at')
  const [order, setOrder] = useState<'desc' | 'asc'>('desc')

  const [rows, setRows] = useState<ApiAdminFile[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState(1)

  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [confirmDelete, setConfirmDelete] = useState<ApiAdminFile[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<PreviewState | null>(null)
  const previewUrlRef = useRef<string | null>(null)

  useEffect(() => {
    const id = setTimeout(() => setSearchDebounced(search.trim()), 400)
    return () => clearTimeout(id)
  }, [search])

  useEffect(() => {
    const id = setTimeout(() => setUserQDebounced(userQ.trim()), 400)
    return () => clearTimeout(id)
  }, [userQ])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await adminApi.files({
        search: searchDebounced,
        user: userQDebounced,
        origin,
        sort,
        order,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      })
      setRows(resp.files)
      setTotal(resp.total)
      setSelected(new Set())
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setLoading(false)
    }
  }, [searchDebounced, userQDebounced, origin, sort, order, page, t])

  useEffect(() => {
    void load()
  }, [load])

  // Reset to page 1 whenever a filter changes.
  useEffect(() => {
    setPage(1)
  }, [searchDebounced, userQDebounced, origin, sort, order])

  const timeFmt = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  )

  const allChecked = rows.length > 0 && rows.every((f) => selected.has(rowKey(f)))
  const toggleAll = () => {
    setSelected(allChecked ? new Set() : new Set(rows.map(rowKey)))
  }
  const toggleOne = (f: ApiAdminFile) => {
    setSelected((prev) => {
      const next = new Set(prev)
      const k = rowKey(f)
      if (next.has(k)) next.delete(k)
      else next.add(k)
      return next
    })
  }

  const openPreview = async (f: ApiAdminFile) => {
    const kind = previewKind(f)
    if (kind === 'text' && f.size_bytes > TEXT_PREVIEW_MAX_BYTES) {
      setPreview({ file: f, kind: 'binary', loading: false })
      return
    }
    setPreview({ file: f, kind, loading: kind !== 'binary' })
    if (kind === 'binary') return
    try {
      const blob = await adminApi.fileContentBlob(f.source, f.id)
      if (kind === 'text') {
        const text = await blob.text()
        setPreview((p) => (p && p.file.id === f.id ? { ...p, loading: false, text } : p))
      } else {
        const url = URL.createObjectURL(blob)
        previewUrlRef.current = url
        setPreview((p) => (p && p.file.id === f.id ? { ...p, loading: false, url } : p))
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : t('files.previewFailed', { defaultValue: 'Preview failed' })
      setPreview((p) => (p && p.file.id === f.id ? { ...p, loading: false, error: msg } : p))
    }
  }

  const closePreview = () => {
    if (previewUrlRef.current) {
      URL.revokeObjectURL(previewUrlRef.current)
      previewUrlRef.current = null
    }
    setPreview(null)
  }

  const downloadPreviewFile = async (f: ApiAdminFile) => {
    try {
      const blob = await adminApi.fileContentBlob(f.source, f.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = f.filename
      a.click()
      URL.revokeObjectURL(url)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    }
  }

  const runDelete = async (items: ApiAdminFile[]) => {
    setBusy(true)
    try {
      const resp = await adminApi.deleteFiles(items.map((f) => ({ source: f.source, id: f.id })))
      toast.success(t('files.deleted', { count: resp.deleted, defaultValue: 'Deleted {{count}} file(s)' }))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setBusy(false)
    }
  }

  const selectedRows = rows.filter((f) => selected.has(rowKey(f)))

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('files.title', { defaultValue: 'Files' })}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">
            {t('files.lead', { defaultValue: 'Every upload across conversations and knowledge bases. Deleting removes the database rows, vectors, and the file on disk.' })}
          </p>
        </div>
        <Button
          variant="destructive"
          leadingIcon={<Trash2 size={13} aria-hidden />}
          disabled={selectedRows.length === 0 || busy}
          onClick={() => setConfirmDelete(selectedRows)}
        >
          {t('files.deleteSelected', { count: selectedRows.length, defaultValue: 'Delete selected ({{count}})' })}
        </Button>
      </header>

      {/* Filters: filename search · owner · source · sort */}
      <section className="mt-6 flex flex-wrap items-end gap-3">
        <div className="w-64">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('files.filters.search', { defaultValue: 'Filename' })}</label>
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={t('files.filters.searchPlaceholder', { defaultValue: 'Search filenames' })} />
        </div>
        <div className="w-56">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('files.filters.user', { defaultValue: 'User' })}</label>
          <Input
            value={userQ}
            onChange={(e) => setUserQ(e.target.value)}
            placeholder={t('files.filters.userSearchPlaceholder', { defaultValue: 'Email / name / user id' })}
          />
        </div>
        <div className="w-44">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('files.filters.origin', { defaultValue: 'Source' })}</label>
          <Select value={origin} onValueChange={setOrigin}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('files.origin.all', { defaultValue: 'All sources' })}</SelectItem>
              <SelectItem value="conversation">{t('files.origin.conversation', { defaultValue: 'Conversation' })}</SelectItem>
              <SelectItem value="kb">{t('files.origin.kb', { defaultValue: 'Knowledge base' })}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="w-44">
          <label className="block text-[12px] text-[var(--color-fg-subtle)] mb-1">{t('files.filters.sort', { defaultValue: 'Sort by' })}</label>
          <Select
            value={`${sort}-${order}`}
            onValueChange={(v) => {
              const [s, o] = v.split('-') as [string, 'desc' | 'asc']
              setSort(s)
              setOrder(o)
            }}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="created_at-desc">{t('files.sort.newest', { defaultValue: 'Newest first' })}</SelectItem>
              <SelectItem value="created_at-asc">{t('files.sort.oldest', { defaultValue: 'Oldest first' })}</SelectItem>
              <SelectItem value="size_bytes-desc">{t('files.sort.largest', { defaultValue: 'Largest first' })}</SelectItem>
              <SelectItem value="size_bytes-asc">{t('files.sort.smallest', { defaultValue: 'Smallest first' })}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="ml-auto self-center text-[13px] text-[var(--color-fg-subtle)] tabular-nums">
          {t('files.total', { count: total, defaultValue: '{{count}} file(s)' })}
        </div>
      </section>

      <section className="mt-6">
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('files.empty', { defaultValue: 'No files match the current filters.' })}
          </div>
        ) : (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] overflow-x-auto">
            <table className="min-w-[960px] w-full text-sm">
              <thead className="bg-[var(--color-bg-muted)] text-[12px] text-[var(--color-fg-subtle)]">
                <tr>
                  <th className="py-2.5 pl-4 pr-1 w-8">
                    <input
                      type="checkbox"
                      className="accent-[var(--color-accent)] cursor-pointer align-middle"
                      checked={allChecked}
                      onChange={toggleAll}
                      aria-label={t('files.selectAll', { defaultValue: 'Select all on this page' })}
                    />
                  </th>
                  <th className="text-left py-2.5 px-3 font-medium">{t('files.table.filename', { defaultValue: 'Filename' })}</th>
                  <th className="text-left py-2.5 px-3 font-medium">{t('files.table.user', { defaultValue: 'User' })}</th>
                  <th className="text-left py-2.5 px-3 font-medium">{t('files.table.origin', { defaultValue: 'Source' })}</th>
                  <th className="text-right py-2.5 px-3 font-medium">{t('files.table.size', { defaultValue: 'Size' })}</th>
                  <th className="text-left py-2.5 px-3 font-medium">{t('files.table.uploaded', { defaultValue: 'Uploaded' })}</th>
                  <th className="text-right py-2.5 px-4 font-medium" aria-label={t('files.table.actions', { defaultValue: 'Actions' })} />
                </tr>
              </thead>
              <tbody>
                {rows.map((f) => (
                  <tr key={rowKey(f)} className="border-t border-[var(--color-divider)]">
                    <td className="py-2 pl-4 pr-1">
                      <input
                        type="checkbox"
                        className="accent-[var(--color-accent)] cursor-pointer align-middle"
                        checked={selected.has(rowKey(f))}
                        onChange={() => toggleOne(f)}
                        aria-label={t('files.selectOne', { name: f.filename, defaultValue: 'Select {{name}}' })}
                      />
                    </td>
                    <td className="py-2 px-3 max-w-[20rem]">
                      <div className="flex items-center gap-2 min-w-0">
                        <FileText size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                        <span className="truncate" title={f.filename}>
                          {f.filename}
                        </span>
                      </div>
                    </td>
                    <td className="py-2 px-3 truncate max-w-[13rem]" title={f.user_email}>
                      {f.user_email || f.user_id || <span className="text-[var(--color-fg-faint)]">—</span>}
                    </td>
                    <td className="py-2 px-3">
                      {f.origin === 'kb' ? (
                        <Badge variant="neutral" className="gap-1">
                          <FolderOpen size={11} aria-hidden />
                          {f.kb_name || t('files.origin.kb', { defaultValue: 'Knowledge base' })}
                        </Badge>
                      ) : (
                        <Badge variant="neutral" className="gap-1">
                          <MessageSquare size={11} aria-hidden />
                          {t('files.origin.conversation', { defaultValue: 'Conversation' })}
                        </Badge>
                      )}
                    </td>
                    <td className="py-2 px-3 text-right tabular-nums whitespace-nowrap">{fmtBytes(f.size_bytes)}</td>
                    <td className="py-2 px-3 text-[12px] text-[var(--color-fg-muted)] whitespace-nowrap">
                      {timeFmt.format(new Date(f.created_at * 1000))}
                    </td>
                    <td className="py-2 px-4 text-right whitespace-nowrap">
                      <Button variant="ghost" size="sm" leadingIcon={<Eye size={13} aria-hidden />} onClick={() => void openPreview(f)}>
                        {t('files.view', { defaultValue: 'View' })}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="text-[var(--color-danger)]"
                        leadingIcon={<Trash2 size={13} aria-hidden />}
                        onClick={() => setConfirmDelete([f])}
                      >
                        {t('common.delete')}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {total > PAGE_SIZE && (
          <div className="mt-4">
            <Pagination page={page} pageCount={Math.ceil(total / PAGE_SIZE)} onPage={setPage} />
          </div>
        )}
      </section>

      {/* Delete confirmation */}
      <Dialog open={confirmDelete !== null} onOpenChange={(open) => !open && setConfirmDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('files.confirmTitle', { count: confirmDelete?.length ?? 0, defaultValue: 'Delete {{count}} file(s)?' })}</DialogTitle>
            <DialogDescription>
              {t('files.confirmBody', { defaultValue: 'This permanently removes the file from disk, its database records, and its search vectors. It cannot be undone.' })}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <ul className="max-h-40 overflow-y-auto text-sm text-[var(--color-fg-muted)] space-y-1">
              {(confirmDelete ?? []).slice(0, 12).map((f) => (
                <li key={rowKey(f)} className="truncate">
                  {f.filename}
                </li>
              ))}
              {(confirmDelete?.length ?? 0) > 12 && (
                <li className="text-[var(--color-fg-subtle)]">
                  {t('files.confirmMore', { count: (confirmDelete?.length ?? 0) - 12, defaultValue: '…and {{count}} more' })}
                </li>
              )}
            </ul>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)} disabled={busy}>
              {t('common.cancel')}
            </Button>
            <Button variant="destructive" onClick={() => confirmDelete && void runDelete(confirmDelete)} disabled={busy}>
              {t('common.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Content preview */}
      <Dialog open={preview !== null} onOpenChange={(open) => !open && closePreview()}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle className="truncate">{preview?.file.filename}</DialogTitle>
            <DialogDescription>
              {preview ? `${fmtBytes(preview.file.size_bytes)} · ${preview.file.mime_type || t('files.unknownType', { defaultValue: 'unknown type' })}` : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            {preview?.loading ? (
              <PanelFallback />
            ) : preview?.error ? (
              <div className="py-10 text-center text-sm text-[var(--color-danger)]">{preview.error}</div>
            ) : preview?.kind === 'image' && preview.url ? (
              <img src={preview.url} alt={preview.file.filename} className="max-h-[60vh] mx-auto rounded-[8px]" />
            ) : preview?.kind === 'pdf' && preview.url ? (
              <iframe src={preview.url} title={preview.file.filename} className="w-full h-[60vh] rounded-[8px] border border-[var(--color-border)]" />
            ) : preview?.kind === 'text' && preview.text !== undefined ? (
              <pre className="max-h-[60vh] overflow-auto rounded-[8px] bg-[var(--color-bg-muted)] p-4 text-[12.5px] leading-relaxed whitespace-pre-wrap break-words">
                {preview.text}
              </pre>
            ) : preview ? (
              <div className="py-8 text-center text-sm text-[var(--color-fg-muted)]">
                {t('files.noInlinePreview', { defaultValue: 'No inline preview for this file type.' })}
              </div>
            ) : null}
          </DialogBody>
          <DialogFooter>
            {preview && (
              <Button variant="secondary" onClick={() => void downloadPreviewFile(preview.file)}>
                {t('files.download', { defaultValue: 'Download' })}
              </Button>
            )}
            <Button variant="ghost" onClick={closePreview}>
              {t('common.close', { defaultValue: 'Close' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
