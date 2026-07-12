/**
 * UserFiles — the signed-in user's upload inventory (§ user files page):
 * conversation attachments and knowledge-base documents in one list, with a
 * storage meter on top (group-configurable quota; images don't count).
 * Deleting removes the same three layers everywhere else does: database rows,
 * search vectors, and the bytes on disk.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Eye, FileText, FolderOpen, HardDrive, MessageSquare, Trash2 } from 'lucide-react'
import { authApi, ApiError } from '@/api'
import type { ApiAdminFile } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ContentHeader } from '@/components/layout/content-header'
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
import { toast } from '@/hooks/use-toast'
import { envNum } from '@/lib/env-config'

const PAGE_SIZE = envNum('VITE_AIVORY_PAGE_SIZE', 50)
const ALL = 'all'
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

function typeLabel(f: ApiAdminFile): string {
  const mime = f.mime_type.toLowerCase()
  if (mime.startsWith('image/')) return mime.slice(6).toUpperCase()
  const ext = f.filename.includes('.') ? f.filename.split('.').pop()! : ''
  return ext ? ext.toUpperCase() : mime || '—'
}

function rowKey(f: ApiAdminFile): string {
  return `${f.source}:${f.id}`
}

interface PreviewState {
  file: ApiAdminFile
  kind: PreviewKind
  loading: boolean
  url?: string
  text?: string
  error?: string
}

export default function UserFiles() {
  const { t, i18n } = useTranslation(['files', 'common'])
  const [search, setSearch] = useState('')
  const [searchDebounced, setSearchDebounced] = useState('')
  const [origin, setOrigin] = useState(ALL)
  const [sort, setSort] = useState('created_at')
  const [order, setOrder] = useState<'desc' | 'asc'>('desc')

  const [rows, setRows] = useState<ApiAdminFile[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState(1)
  const [storage, setStorage] = useState<{ used_bytes: number; quota_bytes: number } | null>(null)

  const [confirmDelete, setConfirmDelete] = useState<ApiAdminFile | null>(null)
  const [busy, setBusy] = useState(false)
  const [preview, setPreview] = useState<PreviewState | null>(null)
  const previewUrlRef = useRef<string | null>(null)

  useEffect(() => {
    const id = setTimeout(() => setSearchDebounced(search.trim()), 400)
    return () => clearTimeout(id)
  }, [search])

  const loadStorage = useCallback(() => {
    authApi
      .myStorage()
      .then(setStorage)
      .catch(() => {})
  }, [])

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await authApi.myFiles({
        search: searchDebounced,
        origin,
        sort,
        order,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      })
      setRows(resp.files)
      setTotal(resp.total)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:actions.failed', { defaultValue: 'Failed' }))
    } finally {
      setLoading(false)
    }
  }, [searchDebounced, origin, sort, order, page, t])

  useEffect(() => {
    void load()
  }, [load])
  useEffect(() => {
    loadStorage()
  }, [loadStorage])
  useEffect(() => {
    setPage(1)
  }, [searchDebounced, origin, sort, order])

  const timeFmt = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  )

  const openPreview = async (f: ApiAdminFile) => {
    const kind = previewKind(f)
    if (kind === 'text' && f.size_bytes > TEXT_PREVIEW_MAX_BYTES) {
      setPreview({ file: f, kind: 'binary', loading: false })
      return
    }
    setPreview({ file: f, kind, loading: kind !== 'binary' })
    if (kind === 'binary') return
    try {
      const blob = await authApi.myFileContentBlob(f.source, f.id)
      if (kind === 'text') {
        const text = await blob.text()
        setPreview((p) => (p && p.file.id === f.id ? { ...p, loading: false, text } : p))
      } else {
        const url = URL.createObjectURL(blob)
        previewUrlRef.current = url
        setPreview((p) => (p && p.file.id === f.id ? { ...p, loading: false, url } : p))
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : t('files:previewFailed')
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

  const runDelete = async (f: ApiAdminFile) => {
    setBusy(true)
    try {
      await authApi.deleteMyFiles([{ source: f.source, id: f.id }])
      toast.success(t('files:deleted'))
      setConfirmDelete(null)
      await load()
      loadStorage()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:actions.failed', { defaultValue: 'Failed' }))
    } finally {
      setBusy(false)
    }
  }

  const quota = storage?.quota_bytes ?? 0
  const used = storage?.used_bytes ?? 0
  const pct = quota > 0 ? Math.min(100, (used / quota) * 100) : 0
  const nearFull = quota > 0 && pct >= 90

  return (
    <div className="flex h-full flex-col">
      <ContentHeader title={t('files:title')} />
      <main className="flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-2 sm:px-8 pb-12">
          {/* Storage meter */}
          <section className="mt-2 rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] p-5">
            <div className="flex items-center justify-between gap-3">
              <div className="inline-flex items-center gap-2 text-sm font-medium text-[var(--color-fg)]">
                <HardDrive size={15} aria-hidden className="text-[var(--color-fg-subtle)]" />
                {t('files:storage.title')}
              </div>
              <div className="text-[13px] tabular-nums text-[var(--color-fg-muted)]">
                {quota > 0
                  ? t('files:storage.usedOf', { used: fmtBytes(used), quota: fmtBytes(quota) })
                  : t('files:storage.usedUnlimited', { used: fmtBytes(used) })}
              </div>
            </div>
            {quota > 0 && (
              <div
                className="mt-3 h-2 w-full overflow-hidden rounded-full bg-[var(--color-bg-muted)]"
                role="progressbar"
                aria-valuenow={Math.round(pct)}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-label={t('files:storage.title')}
              >
                <div
                  className={
                    nearFull
                      ? 'h-full rounded-full bg-[var(--color-danger)] transition-[width]'
                      : 'h-full rounded-full bg-[var(--color-accent)] transition-[width]'
                  }
                  style={{ width: `${pct}%` }}
                />
              </div>
            )}
            <p className="mt-2 text-[12px] text-[var(--color-fg-subtle)]">{t('files:storage.note')}</p>
          </section>

          {/* Filters */}
          <section className="mt-6 flex flex-wrap items-end gap-3">
            <div className="w-full sm:w-64">
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={t('files:searchPlaceholder')}
              />
            </div>
            <div className="w-44">
              <Select value={origin} onValueChange={setOrigin}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('files:origin.all')}</SelectItem>
                  <SelectItem value="conversation">{t('files:origin.conversation')}</SelectItem>
                  <SelectItem value="kb">{t('files:origin.kb')}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="w-44">
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
                  <SelectItem value="created_at-desc">{t('files:sort.newest')}</SelectItem>
                  <SelectItem value="created_at-asc">{t('files:sort.oldest')}</SelectItem>
                  <SelectItem value="size_bytes-desc">{t('files:sort.largest')}</SelectItem>
                  <SelectItem value="size_bytes-asc">{t('files:sort.smallest')}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="ml-auto self-center text-[13px] text-[var(--color-fg-subtle)] tabular-nums">
              {t('files:total', { count: total })}
            </div>
          </section>

          {/* List */}
          <section className="mt-4">
            {loading ? (
              <div className="py-10 text-center text-sm text-[var(--color-fg-subtle)]">
                {t('common:loading', { defaultValue: 'Loading…' })}
              </div>
            ) : rows.length === 0 ? (
              <EmptyState
                icon={<FolderOpen size={22} aria-hidden />}
                title={t('files:empty.title')}
                description={t('files:empty.body')}
              />
            ) : (
              <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] overflow-x-auto">
                <table className="min-w-[760px] w-full text-sm">
                  <thead className="bg-[var(--color-bg-muted)] text-[12px] text-[var(--color-fg-subtle)]">
                    <tr>
                      <th className="text-left py-2.5 px-4 font-medium">{t('files:table.filename')}</th>
                      <th className="text-left py-2.5 px-3 font-medium">{t('files:table.type')}</th>
                      <th className="text-left py-2.5 px-3 font-medium">{t('files:table.origin')}</th>
                      <th className="text-right py-2.5 px-3 font-medium">{t('files:table.size')}</th>
                      <th className="text-left py-2.5 px-3 font-medium">{t('files:table.uploaded')}</th>
                      <th className="text-right py-2.5 px-4 font-medium" aria-label={t('files:table.actions')} />
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((f) => (
                      <tr key={rowKey(f)} className="border-t border-[var(--color-divider)]">
                        <td className="py-2 px-4 max-w-[18rem]">
                          <div className="flex items-center gap-2 min-w-0">
                            <FileText size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                            <span className="truncate" title={f.filename}>
                              {f.filename}
                            </span>
                          </div>
                        </td>
                        <td className="py-2 px-3 text-[12px] text-[var(--color-fg-muted)] whitespace-nowrap">{typeLabel(f)}</td>
                        <td className="py-2 px-3">
                          {f.origin === 'kb' ? (
                            <Badge variant="neutral" className="gap-1">
                              <FolderOpen size={11} aria-hidden />
                              {f.kb_name || t('files:origin.kb')}
                            </Badge>
                          ) : (
                            <Badge variant="neutral" className="gap-1">
                              <MessageSquare size={11} aria-hidden />
                              {t('files:origin.conversation')}
                            </Badge>
                          )}
                        </td>
                        <td className="py-2 px-3 text-right tabular-nums whitespace-nowrap">{fmtBytes(f.size_bytes)}</td>
                        <td className="py-2 px-3 text-[12px] text-[var(--color-fg-muted)] whitespace-nowrap">
                          {timeFmt.format(new Date(f.created_at * 1000))}
                        </td>
                        <td className="py-2 px-4 text-right whitespace-nowrap">
                          <Button variant="ghost" size="sm" leadingIcon={<Eye size={13} aria-hidden />} onClick={() => void openPreview(f)}>
                            {t('files:view')}
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-[var(--color-danger)]"
                            leadingIcon={<Trash2 size={13} aria-hidden />}
                            onClick={() => setConfirmDelete(f)}
                          >
                            {t('common:actions.delete', { defaultValue: 'Delete' })}
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
        </div>
      </main>

      {/* Delete confirmation */}
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

      {/* Preview */}
      <Dialog open={preview !== null} onOpenChange={(open) => !open && closePreview()}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle className="truncate">{preview?.file.filename}</DialogTitle>
            <DialogDescription>
              {preview ? `${fmtBytes(preview.file.size_bytes)} · ${typeLabel(preview.file)}` : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            {preview?.loading ? (
              <div className="py-10 text-center text-sm text-[var(--color-fg-subtle)]">
                {t('common:loading', { defaultValue: 'Loading…' })}
              </div>
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
              <div className="py-8 text-center text-sm text-[var(--color-fg-muted)]">{t('files:noInlinePreview')}</div>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={closePreview}>
              {t('common:actions.close', { defaultValue: 'Close' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
