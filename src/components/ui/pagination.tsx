/**
 * Pagination — a minimal prev/next pager for admin tables. Page-through over an
 * already-loaded list (client-side). Renders nothing when there's a single page.
 */
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'

interface PaginationProps {
  page: number
  pageCount: number
  onPage: (page: number) => void
  className?: string
}

export function Pagination({ page, pageCount, onPage, className }: PaginationProps) {
  const { t } = useTranslation('common')
  if (pageCount <= 1) return null
  const btn =
    'inline-flex items-center justify-center size-[var(--tap-min)] rounded-[8px] border border-[var(--color-border)] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] disabled:opacity-40 disabled:pointer-events-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:size-8'
  return (
    <div className={cn('flex items-center justify-center gap-3 pt-4', className)}>
      <button type="button" className={btn} disabled={page <= 1} onClick={() => onPage(page - 1)} aria-label={t('pagination.prev', { defaultValue: 'Previous' })}>
        <ChevronLeft size={15} aria-hidden />
      </button>
      <span className="text-[12.5px] tabular-nums text-[var(--color-fg-subtle)]">
        {t('pagination.page', { defaultValue: '{{page}} / {{total}}', page, total: pageCount })}
      </span>
      <button type="button" className={btn} disabled={page >= pageCount} onClick={() => onPage(page + 1)} aria-label={t('pagination.next', { defaultValue: 'Next' })}>
        <ChevronRight size={15} aria-hidden />
      </button>
    </div>
  )
}
