import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Bug, ChevronRight, Image as ImageIcon, Search, X } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiAdminUserFeedback, ApiAdminUserFeedbackPage } from '@/api/types'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { Pagination } from '@/components/ui/pagination'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Sheet,
  SheetBody,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { cn } from '@/lib/utils'

const PAGE_SIZE = 50

function displayUser(item: ApiAdminUserFeedback): string {
  return item.user_name || item.user_email || item.user_id || '—'
}

function formatBytes(value: number): string {
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`
  if (value >= 1024) return `${Math.round(value / 1024)} KB`
  return `${value} B`
}

export default function AdminFeedback() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [search, setSearch] = useState('')
  const [searchDebounced, setSearchDebounced] = useState('')
  const [page, setPage] = useState(1)
  const [data, setData] = useState<ApiAdminUserFeedbackPage | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [selected, setSelected] = useState<ApiAdminUserFeedback | null>(null)
  const [screenshotUrl, setScreenshotUrl] = useState('')
  const [screenshotLoading, setScreenshotLoading] = useState(false)
  const [screenshotError, setScreenshotError] = useState('')
  const requestRef = useRef(0)
  const screenshotUrlRef = useRef('')
  const screenshotAbortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    const timer = window.setTimeout(() => setSearchDebounced(search.trim()), 300)
    return () => window.clearTimeout(timer)
  }, [search])

  const load = useCallback(async () => {
    const request = ++requestRef.current
    setLoading(true)
    setError('')
    try {
      const result = await adminApi.userFeedback({
        q: searchDebounced,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      })
      if (request === requestRef.current) setData(result)
    } catch (cause) {
      if (request === requestRef.current) {
        setData(null)
        setError(cause instanceof ApiError ? cause.message : t('admin:userFeedback.loadFailed'))
      }
    } finally {
      if (request === requestRef.current) setLoading(false)
    }
  }, [page, searchDebounced, t])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    setPage(1)
  }, [searchDebounced])

  const releaseScreenshot = useCallback(() => {
    screenshotAbortRef.current?.abort()
    screenshotAbortRef.current = null
    if (screenshotUrlRef.current) {
      URL.revokeObjectURL(screenshotUrlRef.current)
      screenshotUrlRef.current = ''
    }
    setScreenshotUrl('')
    setScreenshotLoading(false)
    setScreenshotError('')
  }, [])

  useEffect(() => () => releaseScreenshot(), [releaseScreenshot])

  useEffect(() => {
    releaseScreenshot()
    if (!selected?.has_screenshot) return
    const controller = new AbortController()
    screenshotAbortRef.current = controller
    setScreenshotLoading(true)
    void adminApi
      .userFeedbackScreenshotBlob(selected.id, controller.signal)
      .then((blob) => {
        if (controller.signal.aborted) return
        const url = URL.createObjectURL(blob)
        screenshotUrlRef.current = url
        setScreenshotUrl(url)
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setScreenshotError(cause instanceof ApiError ? cause.message : t('admin:userFeedback.loadFailed'))
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setScreenshotLoading(false)
      })
    return () => controller.abort()
  }, [releaseScreenshot, selected, t])

  const pageCount = Math.max(1, Math.ceil((data?.total ?? 0) / PAGE_SIZE))
  const dateFormatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language || undefined, { dateStyle: 'medium', timeStyle: 'short' }),
    [i18n.language],
  )

  function formatDate(value: number): string {
    return value ? dateFormatter.format(new Date(value > 1_000_000_000_000 ? value : value * 1000)) : '—'
  }

  function openDetails(item: ApiAdminUserFeedback) {
    setSelected(item)
  }

  return (
    <div className="min-w-0 pb-10">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl text-[var(--color-fg)] sm:text-3xl">{t('admin:userFeedback.title')}</h1>
          <p className="mt-2 max-w-2xl text-sm leading-relaxed text-[var(--color-fg-muted)]">
            {t('admin:userFeedback.lead')}
          </p>
        </div>
        <Input
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          leadingIcon={<Search size={15} aria-hidden />}
          placeholder={t('admin:userFeedback.searchPlaceholder')}
          aria-label={t('admin:userFeedback.searchPlaceholder')}
          wrapperClassName="w-full sm:w-80"
        />
      </div>

      <div className="mt-7 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)]">
        <div className="flex min-h-11 items-center justify-between gap-3 border-b border-[var(--color-divider)] px-4 py-2.5 sm:px-5">
          <span className="text-[12.5px] tabular-nums text-[var(--color-fg-subtle)]">
            {t('admin:userFeedback.total', { count: data?.total ?? 0 })}
          </span>
          {loading && data ? <span className="text-[12px] text-[var(--color-fg-subtle)]">{t('admin:userFeedback.loading')}</span> : null}
        </div>

        {loading && !data ? (
          <div className="space-y-1 p-2" aria-label={t('admin:userFeedback.loading')}>
            {Array.from({ length: 6 }, (_, index) => (
              <div key={index} className="flex min-h-24 items-center gap-3 px-3 py-3">
                <Skeleton className="size-9 shrink-0" />
                <div className="min-w-0 flex-1 space-y-2">
                  <Skeleton shape="line" className="w-1/2" />
                  <Skeleton shape="line" className="w-4/5" />
                  <Skeleton shape="line" className="w-2/5" />
                </div>
              </div>
            ))}
          </div>
        ) : error ? (
          <div className="flex flex-col items-center px-6 py-16 text-center" role="alert">
            <Bug size={24} className="text-[var(--color-danger)]" aria-hidden />
            <p className="mt-3 max-w-md text-sm text-[var(--color-fg-muted)]">{error}</p>
            <Button variant="secondary" size="sm" className="mt-4" onClick={() => void load()}>
              {t('admin:userFeedback.retry')}
            </Button>
          </div>
        ) : data && data.items.length > 0 ? (
          <div className={cn('transition-opacity', loading && 'pointer-events-none opacity-60')}>
            <ul aria-label={t('admin:userFeedback.title')} className="divide-y divide-[var(--color-divider)]">
              {data.items.map((item) => (
                <li key={item.id}>
                  <button
                    type="button"
                    onClick={() => openDetails(item)}
                    className="group flex min-h-24 w-full items-start gap-3 px-4 py-4 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)] sm:px-5"
                  >
                    <span className="mt-0.5 inline-flex size-9 shrink-0 items-center justify-center rounded-[8px] bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
                      <Bug size={16} aria-hidden />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-[var(--color-fg-subtle)]">
                        <span className="font-medium text-[var(--color-fg)]">{displayUser(item)}</span>
                        <span aria-hidden>·</span>
                        <span>{formatDate(item.created_at)}</span>
                        {item.has_screenshot ? (
                          <span className="inline-flex items-center gap-1 text-[var(--color-secondary)]">
                            <ImageIcon size={12} aria-hidden />
                            {formatBytes(item.screenshot_size)}
                          </span>
                        ) : null}
                      </span>
                      <span className="mt-1.5 block line-clamp-2 text-sm leading-relaxed text-[var(--color-fg-muted)]">
                        {item.description}
                      </span>
                      <span className="mt-2 block truncate text-[11px] text-[var(--color-fg-subtle)]">
                        {item.conversation_title || item.page_path || item.conversation_id || '—'}
                      </span>
                    </span>
                    <ChevronRight size={16} className="mt-2 shrink-0 text-[var(--color-fg-faint)] transition-transform group-hover:translate-x-0.5" aria-hidden />
                  </button>
                </li>
              ))}
            </ul>
            <Pagination page={page} pageCount={pageCount} onPage={setPage} className="pb-4" />
          </div>
        ) : (
          <EmptyState
            className="py-20"
            icon={<Bug size={21} aria-hidden />}
            title={t(searchDebounced ? 'admin:userFeedback.emptyFiltered' : 'admin:userFeedback.empty')}
            description={searchDebounced ? undefined : t('admin:userFeedback.lead')}
          />
        )}
      </div>

      <Sheet open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <SheetContent side="right" size="lg" label={t('admin:userFeedback.details')}>
          <SheetHeader className="relative border-b border-[var(--color-divider)] pr-14">
            <SheetTitle>{t('admin:userFeedback.details')}</SheetTitle>
            <SheetDescription>{selected ? formatDate(selected.created_at) : ''}</SheetDescription>
            <SheetClose asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label={t('common:actions.close')}
                className="absolute right-4 top-4"
              >
                <X size={17} aria-hidden />
              </Button>
            </SheetClose>
          </SheetHeader>
          <SheetBody className="space-y-6 py-5">
            {selected ? (
              <>
                <section>
                  <h3 className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">
                    {t('admin:userFeedback.description')}
                  </h3>
                  <p className="mt-2 whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-fg)]">
                    {selected.description}
                  </p>
                </section>

                <section>
                  <h3 className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">
                    {t('admin:userFeedback.screenshot')}
                  </h3>
                  <div className="mt-2 flex min-h-36 items-center justify-center overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)]">
                    {screenshotLoading ? (
                      <span className="flex items-center gap-2 text-sm text-[var(--color-fg-muted)]" role="status">
                        <span className="inline-block size-4 animate-spin rounded-full border-2 border-current border-r-transparent" aria-hidden />
                        {t('admin:userFeedback.loading')}
                      </span>
                    ) : screenshotUrl ? (
                      <img src={screenshotUrl} alt={t('admin:userFeedback.screenshot')} className="max-h-[60dvh] w-full object-contain" />
                    ) : selected.has_screenshot && screenshotError ? (
                      <p className="px-5 text-center text-sm text-[var(--color-danger)]">{screenshotError}</p>
                    ) : (
                      <span className="text-sm text-[var(--color-fg-muted)]">{t('admin:userFeedback.noScreenshot')}</span>
                    )}
                  </div>
                </section>

                <section>
                  <h3 className="text-xs font-medium uppercase tracking-[0.08em] text-[var(--color-fg-subtle)]">
                    {t('admin:userFeedback.metadata')}
                  </h3>
                  <dl className="mt-2 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)] text-[12.5px]">
                    <MetaRow label={t('admin:userFeedback.reportedBy')} value={[selected.user_name, selected.user_email].filter(Boolean).join(' / ') || selected.user_id} />
                    <MetaRow label={t('admin:userFeedback.page')} value={selected.page_path || '—'} />
                    <MetaRow label={t('admin:userFeedback.viewport')} value={selected.viewport_width && selected.viewport_height ? `${selected.viewport_width} × ${selected.viewport_height}` : '—'} />
                    <MetaRow label={t('admin:userFeedback.userAgent')} value={selected.user_agent || '—'} />
                    <MetaRow label={t('admin:userFeedback.message')} value={selected.message_id || '—'} mono />
                  </dl>
                </section>

                {selected.conversation_id ? (
                  <Link
                    to={`/admin/users/${encodeURIComponent(selected.user_id)}/conversations/${encodeURIComponent(selected.conversation_id)}`}
                    className="inline-flex min-h-9 items-center rounded-[8px] border border-[var(--color-border)] px-3 text-sm text-[var(--color-fg)] interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    {t('admin:userFeedback.conversation')}
                    <ChevronRight size={14} className="ml-1.5" aria-hidden />
                  </Link>
                ) : null}
              </>
            ) : null}
          </SheetBody>
        </SheetContent>
      </Sheet>
    </div>
  )
}

function MetaRow({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3 py-2.5">
      <dt className="text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className={cn('min-w-0 break-words text-right text-[var(--color-fg)]', mono && 'font-mono text-[11px]')}>{value}</dd>
    </div>
  )
}
