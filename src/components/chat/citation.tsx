import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ExternalLink, FileText } from 'lucide-react'
import type { Citation } from '@/types/chat'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn, safeHref } from '@/lib/utils'

interface CitationChipProps {
  citation: Citation
  className?: string
}

/**
 * A citation points at one of the user's own indexed documents (RAG) rather
 * than a public web page when the backend marks it source:'kb' or hands back a
 * `doc://<id>` URL. Those have no browsable URL — `safeHref` rejects `doc:` and
 * `safeDomain` would surface the raw doc id — so we render them as a
 * non-clickable document chip instead of a dead link.
 */
function isDocCitation(c: Citation): boolean {
  return c.source === 'kb' || c.url.trim().toLowerCase().startsWith('doc:')
}

export function CitationChip({ citation, className }: CitationChipProps) {
  const { t } = useTranslation('chat')
  const isDoc = isDocCitation(citation)
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`Source ${citation.index}: ${citation.title}`}
          className={cn(
            'inline-flex items-center justify-center align-text-top',
            'h-[18px] min-w-[18px] px-1 mx-0.5',
            'text-[10px] font-medium rounded-[5px]',
            'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]',
            'border border-[var(--color-secondary)]/20',
            'hover:bg-[var(--color-accent-soft)] hover:text-[var(--color-accent)] hover:border-[var(--color-accent)]/25',
            'interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            className,
          )}
        >
          {citation.index}
        </button>
      </PopoverTrigger>
      <PopoverContent side="top" align="start" className="w-[320px]">
        <div className="px-2.5 pt-1.5 pb-2">
          {isDoc ? (
            <>
              <p className="inline-flex items-center gap-1.5 text-[11px] text-[var(--color-fg-subtle)]">
                <FileText size={11} aria-hidden />
                {t('sources.fromDocuments')}
              </p>
              <p className="mt-1 block text-sm font-medium text-[var(--color-fg)] leading-snug">
                {citation.title}
              </p>
              {citation.snippet ? (
                <p className="mt-2 text-xs text-[var(--color-fg-muted)] leading-relaxed">
                  {citation.snippet}
                </p>
              ) : null}
            </>
          ) : (
            <>
              <p className="text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)]">
                {citation.domain}
              </p>
              <a
                href={safeHref(citation.url)}
                target="_blank"
                rel="noopener noreferrer"
                className="mt-1 block text-sm font-medium text-[var(--color-fg)] hover:text-[var(--color-accent)] leading-snug"
              >
                {citation.title}
              </a>
              {citation.snippet ? (
                <p className="mt-2 text-xs text-[var(--color-fg-muted)] leading-relaxed">
                  {citation.snippet}
                </p>
              ) : null}
              <a
                href={safeHref(citation.url)}
                target="_blank"
                rel="noopener noreferrer"
                className="mt-3 inline-flex items-center gap-1.5 text-[11px] text-[var(--color-accent)] hover:text-[var(--color-accent-hover)]"
              >
                {t('sources.open')}
                <ExternalLink size={11} aria-hidden />
              </a>
            </>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}

interface CitationListProps {
  citations: Citation[]
}

export function CitationList({ citations }: CitationListProps) {
  const { t } = useTranslation('chat')
  const [open, setOpen] = useState(false)
  if (citations.length === 0) return null
  return (
    <div className="mt-5 border-t border-[var(--color-divider)] pt-3.5">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className={cn(
          'group flex items-center gap-1.5 text-[11px] uppercase tracking-wider',
          'text-[var(--color-fg-subtle)] hover:text-[var(--color-fg-muted)]',
          'interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[5px]',
        )}
      >
        <ChevronDown
          size={13}
          aria-hidden
          className={cn('transition-transform duration-200', open ? 'rotate-0' : '-rotate-90')}
        />
        {t('sources.label')}
        <span className="text-[var(--color-fg-subtle)]/70 normal-case">· {citations.length}</span>
      </button>
      {/* grid 0fr→1fr animates height without measuring; the global
          prefers-reduced-motion rule neutralises the transition automatically. */}
      <div
        className={cn(
          'grid transition-[grid-template-rows] duration-300 ease-[var(--ease-out)]',
          open ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]',
        )}
      >
        <div className="overflow-hidden">
          <ol className="space-y-1.5 pt-2.5">
            {citations.map((c) =>
              isDocCitation(c) ? (
                // KB document source — no browsable URL, so render a static row
                // (filename + "from your documents") instead of a dead link.
                <li key={c.id} className="flex items-start gap-2.5 text-xs">
                  <CitationChip citation={c} />
                  <span className="leading-relaxed text-[var(--color-fg-muted)]">
                    <span className="inline-flex items-center gap-1 font-medium text-[var(--color-fg)]">
                      <FileText size={11} aria-hidden className="text-[var(--color-fg-subtle)]" />
                      {c.title}
                    </span>
                    <span className="ml-1.5 text-[var(--color-fg-subtle)]">{t('sources.fromDocuments')}</span>
                  </span>
                </li>
              ) : (
                <li key={c.id} className="flex items-start gap-2.5 text-xs">
                  <CitationChip citation={c} />
                  <a
                    href={safeHref(c.url)}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-[var(--color-fg-muted)] hover:text-[var(--color-accent)] leading-relaxed"
                  >
                    <span className="font-medium text-[var(--color-fg)]">{c.title}</span>
                    <span className="ml-1.5">{c.domain}</span>
                  </a>
                </li>
              ),
            )}
          </ol>
        </div>
      </div>
    </div>
  )
}
