import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertCircle, Check, Copy, ExternalLink, Link2, Search, Trash2 } from 'lucide-react'

import { adminApi, apiUrl, ApiError } from '@/api'
import type { ApiAdminHTMLPreviewShare, ApiAdminHTMLPreviewSharePage } from '@/api/types'
import { Button } from '@/components/ui/button'
import { AdminTable } from '@/components/admin/AdminTable'
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
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'
import { copyText } from '@/lib/utils'
import { AdminPageHeader } from '@/components/admin/admin-page-header'

const PAGE_SIZE = 50

function ownerLabel(item: ApiAdminHTMLPreviewShare): string {
  return item.user_name || item.user_email || item.user_id || '—'
}

function previewUrl(id: string): string {
  return new URL(apiUrl(`/public/html-previews/${encodeURIComponent(id)}`), window.location.origin).href
}

export default function AdminHTMLPreviews() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [search, setSearch] = useState('')
  const [searchDebounced, setSearchDebounced] = useState('')
  const [page, setPage] = useState(1)
  const [data, setData] = useState<ApiAdminHTMLPreviewSharePage | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [copiedID, setCopiedID] = useState('')
  const [deleteTarget, setDeleteTarget] = useState<ApiAdminHTMLPreviewShare | null>(null)
  const [deleting, setDeleting] = useState(false)
  const requestRef = useRef(0)
  const copyTimerRef = useRef<number | null>(null)
  const deletingRef = useRef(false)

  useEffect(() => {
    const timer = window.setTimeout(() => setSearchDebounced(search.trim()), 300)
    return () => window.clearTimeout(timer)
  }, [search])

  useEffect(() => {
    setPage(1)
  }, [searchDebounced])

  useEffect(() => () => {
    if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current)
  }, [])

  const load = useCallback(async () => {
    const request = ++requestRef.current
    setLoading(true)
    setError('')
    try {
      const result = await adminApi.htmlPreviewShares({
        q: searchDebounced,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      })
      if (request === requestRef.current) setData(result)
    } catch (cause) {
      if (request === requestRef.current) {
        setError(cause instanceof ApiError ? cause.message : t('admin:htmlPreviews.loadFailed'))
      }
    } finally {
      if (request === requestRef.current) setLoading(false)
    }
  }, [page, searchDebounced, t])

  useEffect(() => {
    void load()
  }, [load])

  const pageCount = Math.max(1, Math.ceil((data?.total ?? 0) / PAGE_SIZE))
  const dateFormatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language || undefined, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  )

  function formatDate(value: number): string {
    if (!value) return '—'
    return dateFormatter.format(new Date(value > 1_000_000_000_000 ? value : value * 1000))
  }

  async function copyLink(item: ApiAdminHTMLPreviewShare) {
    if (!(await copyText(previewUrl(item.id)))) {
      toast.error(t('admin:htmlPreviews.copyFailed'))
      return
    }
    setCopiedID(item.id)
    if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current)
    copyTimerRef.current = window.setTimeout(() => setCopiedID(''), 1800)
    toast.success(t('admin:htmlPreviews.copied'))
  }

  async function remove() {
    if (!deleteTarget || deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeHTMLPreviewShare(deleteTarget.id)
      toast.success(t('admin:htmlPreviews.deleted'))
      setDeleteTarget(null)
      if ((data?.items.length ?? 0) === 1 && page > 1) setPage((current) => current - 1)
      else await load()
    } catch (cause) {
      toast.error(cause instanceof ApiError ? cause.message : t('admin:htmlPreviews.deleteFailed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  return (
    <div className="min-w-0 pb-10">
      <AdminPageHeader
        title={t('admin:htmlPreviews.title')}
        description={t('admin:htmlPreviews.lead')}
        actions={(
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            leadingIcon={<Search size={15} aria-hidden />}
            placeholder={t('admin:htmlPreviews.searchPlaceholder')}
            aria-label={t('admin:htmlPreviews.searchPlaceholder')}
            wrapperClassName="h-8 w-full max-sm:h-[var(--tap-min)] sm:w-72"
          />
        )}
      />

      <section className="mt-7 overflow-hidden rounded-[12px] bg-[var(--color-surface)]" aria-label={t('admin:htmlPreviews.title')}>
        <div className="flex min-h-11 items-center justify-between gap-3 px-4 py-2.5 sm:px-5">
          <span className="text-[12.5px] tabular-nums text-[var(--color-fg-subtle)]">
            {t('admin:htmlPreviews.total', { count: data?.total ?? 0 })}
          </span>
          {loading && data ? <span className="text-[12px] text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</span> : null}
        </div>

        {loading && !data ? (
          <div className="space-y-1 p-2" aria-label={t('admin:common.loading')}>
            {Array.from({ length: 6 }, (_, index) => (
              <div key={index} className="flex min-h-20 items-center gap-3 px-3 py-3">
                <Skeleton className="size-9 shrink-0" />
                <div className="min-w-0 flex-1 space-y-2">
                  <Skeleton shape="line" className="w-2/5" />
                  <Skeleton shape="line" className="w-4/5" />
                </div>
              </div>
            ))}
          </div>
        ) : error ? (
          <div className="flex flex-col items-center px-6 py-16 text-center" role="alert">
            <AlertCircle size={24} className="text-[var(--color-danger)]" aria-hidden />
            <p className="mt-3 max-w-md text-sm text-[var(--color-fg-muted)]">{error}</p>
            <Button variant="secondary" size="sm" className="mt-4" onClick={() => void load()}>
              {t('common:actions.tryAgain')}
            </Button>
          </div>
        ) : data && data.items.length > 0 ? (
          <div className={loading ? 'pointer-events-none opacity-60 transition-opacity' : 'transition-opacity'}>
            <AdminTable
              items={data.items}
              rowKey={(item) => item.id}
              label={t('admin:htmlPreviews.title')}
              embedded
              columns={[
                { id: 'link', header: t('admin:htmlPreviews.table.link'), width: 320, render: (item) => <code className="block truncate font-mono text-[12px]" title={previewUrl(item.id)}>{previewUrl(item.id)}</code> },
                { id: 'creator', header: t('admin:htmlPreviews.table.creator'), width: 240, render: (item) => <div className="min-w-0"><span className="block truncate font-medium" title={[item.user_name, item.user_email, item.user_id].filter(Boolean).join(' / ')}>{ownerLabel(item)}</span>{item.user_email && item.user_name ? <span className="block truncate text-[12px] text-[var(--color-fg-muted)]">{item.user_email}</span> : null}</div> },
                { id: 'created', header: t('admin:htmlPreviews.table.created'), width: 170, render: (item) => <time className="text-[12px] tabular-nums text-[var(--color-fg-muted)]">{formatDate(item.created_at)}</time> },
                { id: 'actions', header: t('admin:common.actions'), width: 168, align: 'right', render: (item) => <RowActions item={item} url={previewUrl(item.id)} copied={copiedID === item.id} onCopy={copyLink} onDelete={setDeleteTarget} t={t} /> },
              ]}
            />

            <Pagination page={page} pageCount={pageCount} onPage={setPage} className="pb-4" />
          </div>
        ) : (
          <EmptyState
            className="py-20"
            icon={<Link2 size={21} aria-hidden />}
            title={t(searchDebounced ? 'admin:htmlPreviews.emptyFiltered' : 'admin:htmlPreviews.empty')}
            description={searchDebounced ? undefined : t('admin:htmlPreviews.emptyLead')}
          />
        )}
      </section>

      <Dialog open={deleteTarget !== null} onOpenChange={(open) => !open && !deleting && setDeleteTarget(null)}>
        <DialogContent size="sm" closeDisabled={deleting}>
          <DialogHeader>
            <DialogTitle>{t('admin:htmlPreviews.deleteTitle')}</DialogTitle>
            <DialogDescription>{t('admin:htmlPreviews.deleteDescription')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <code className="block break-all rounded-[8px] bg-[var(--color-surface-sunken)] px-3 py-2.5 font-mono text-[12px] text-[var(--color-fg-muted)]">
              {deleteTarget ? previewUrl(deleteTarget.id) : ''}
            </code>
          </DialogBody>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setDeleteTarget(null)} disabled={deleting}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" leadingIcon={<Trash2 size={14} aria-hidden />} loading={deleting} onClick={() => void remove()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

interface RowActionsProps {
  item: ApiAdminHTMLPreviewShare
  url: string
  copied: boolean
  onCopy: (item: ApiAdminHTMLPreviewShare) => Promise<void>
  onDelete: (item: ApiAdminHTMLPreviewShare) => void
  t: ReturnType<typeof useTranslation>['t']
  mobile?: boolean
}

function RowActions({ item, url, copied, onCopy, onDelete, t, mobile = false }: RowActionsProps) {
  return (
    <>
      <Tooltip content={copied ? t('common:actions.copied') : t('common:actions.copy')}>
        <Button variant="ghost" size="icon-sm" className={mobile ? 'size-11' : undefined} aria-label={t('admin:htmlPreviews.copyLabel')} onClick={() => void onCopy(item)}>
          {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
        </Button>
      </Tooltip>
      <Tooltip content={t('admin:htmlPreviews.open')}>
        <Button asChild variant="ghost" size="icon-sm" className={mobile ? 'size-11' : undefined}>
          <a href={url} target="_blank" rel="noreferrer" aria-label={t('admin:htmlPreviews.open')}>
            <ExternalLink size={13} aria-hidden />
          </a>
        </Button>
      </Tooltip>
      <Tooltip content={t('common:actions.delete')}>
        <Button variant="ghost" size="icon-sm" className={`${mobile ? 'size-11 ' : ''}text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)]`} aria-label={t('admin:htmlPreviews.deleteLabel')} onClick={() => onDelete(item)}>
          <Trash2 size={13} aria-hidden />
        </Button>
      </Tooltip>
    </>
  )
}
