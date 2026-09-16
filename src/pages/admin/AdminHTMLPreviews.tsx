import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertCircle, Check, Copy, ExternalLink, Link2, Search, Trash2 } from 'lucide-react'

import { adminApi, apiUrl, ApiError } from '@/api'
import type { ApiAdminHTMLPreviewShare, ApiAdminHTMLPreviewSharePage } from '@/api/types'
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
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { toast } from '@/hooks/use-toast'
import { copyText } from '@/lib/utils'

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
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl text-[var(--color-fg)] sm:text-3xl">
            {t('admin:htmlPreviews.title')}
          </h1>
          <p className="mt-2 max-w-2xl text-sm leading-relaxed text-[var(--color-fg-muted)]">
            {t('admin:htmlPreviews.lead')}
          </p>
        </div>
        <Input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          leadingIcon={<Search size={15} aria-hidden />}
          placeholder={t('admin:htmlPreviews.searchPlaceholder')}
          aria-label={t('admin:htmlPreviews.searchPlaceholder')}
          wrapperClassName="w-full sm:w-80"
        />
      </div>

      <section className="mt-7 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]" aria-label={t('admin:htmlPreviews.title')}>
        <div className="flex min-h-11 items-center justify-between gap-3 border-b border-[var(--color-divider)] px-4 py-2.5 sm:px-5">
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
            <div role="table" className="hidden md:block" aria-label={t('admin:htmlPreviews.title')}>
              <div role="row" className="grid grid-cols-[minmax(12rem,1.3fr)_minmax(13rem,1fr)_minmax(10rem,.7fr)_7rem] gap-4 border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)] px-5 py-2.5 text-[11.5px] font-medium text-[var(--color-fg-muted)]">
                <span role="columnheader">{t('admin:htmlPreviews.table.link')}</span>
                <span role="columnheader">{t('admin:htmlPreviews.table.creator')}</span>
                <span role="columnheader">{t('admin:htmlPreviews.table.created')}</span>
                <span role="columnheader" className="text-right">{t('admin:htmlPreviews.table.actions')}</span>
              </div>
              <div role="rowgroup" className="divide-y divide-[var(--color-divider)]">
                {data.items.map((item) => {
                  const url = previewUrl(item.id)
                  return (
                    <div key={item.id} role="row" className="grid min-h-16 grid-cols-[minmax(12rem,1.3fr)_minmax(13rem,1fr)_minmax(10rem,.7fr)_7rem] items-center gap-4 px-5 py-3">
                      <span role="cell" className="min-w-0">
                        <code className="block truncate font-mono text-[12px] text-[var(--color-fg)]" title={url}>{url}</code>
                      </span>
                      <span role="cell" className="min-w-0">
                        <span className="block truncate text-[12.5px] font-medium text-[var(--color-fg)]" title={[item.user_name, item.user_email, item.user_id].filter(Boolean).join(' / ')}>{ownerLabel(item)}</span>
                        {item.user_email && item.user_name ? <span className="mt-0.5 block truncate text-[11px] text-[var(--color-fg-subtle)]">{item.user_email}</span> : null}
                      </span>
                      <time role="cell" className="text-[12px] tabular-nums text-[var(--color-fg-muted)]">{formatDate(item.created_at)}</time>
                      <span role="cell" className="flex items-center justify-end gap-0.5">
                        <RowActions item={item} url={url} copied={copiedID === item.id} onCopy={copyLink} onDelete={setDeleteTarget} t={t} />
                      </span>
                    </div>
                  )
                })}
              </div>
            </div>

            <ul className="divide-y divide-[var(--color-divider)] md:hidden">
              {data.items.map((item) => {
                const url = previewUrl(item.id)
                return (
                  <li key={item.id} className="px-4 py-4">
                    <code className="block truncate font-mono text-[12px] text-[var(--color-fg)]" title={url}>{url}</code>
                    <div className="mt-2 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[11.5px] text-[var(--color-fg-subtle)]">
                      <span className="truncate font-medium text-[var(--color-fg-muted)]">{ownerLabel(item)}</span>
                      <span aria-hidden>·</span>
                      <time>{formatDate(item.created_at)}</time>
                    </div>
                    <div className="mt-3 flex items-center justify-end gap-1 border-t border-[var(--color-divider)] pt-2">
                      <RowActions item={item} url={url} copied={copiedID === item.id} onCopy={copyLink} onDelete={setDeleteTarget} t={t} mobile />
                    </div>
                  </li>
                )
              })}
            </ul>
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
