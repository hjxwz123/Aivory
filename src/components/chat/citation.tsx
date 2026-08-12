import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ExternalLink, FileSearch, FileText } from 'lucide-react'
import type { Citation } from '@/types/chat'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn, safeHref } from '@/lib/utils'
import {
  boundedCitationSnippet,
  citationsInDisplayOrder,
  documentCitationContentUrl,
  isDocumentCitation,
  isKnowledgeBaseCitation,
} from '@/lib/citations'

interface CitationChipProps {
  citation: Citation
  className?: string
  onOpenDocument?: (citation: Citation) => void
}

export function CitationChip({ citation, className, onOpenDocument }: CitationChipProps) {
  const { t } = useTranslation('chat')
  const [open, setOpen] = useState(false)
  const isDoc = isDocumentCitation(citation)
  const isKnowledgeBase = isKnowledgeBaseCitation(citation)
  const canOpenDocument = Boolean(onOpenDocument && documentCitationContentUrl(citation))
  return (
    <Popover open={open} onOpenChange={setOpen}>
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
      {/* Long RAG snippets (full parent sections) can exceed the viewport, and
          locally-extracted PDF text can be one giant unbroken "word" — so the
          panel is height-capped to Radix's collision-aware available height
          (scrolls inside) and everything wraps with overflow-wrap:anywhere. */}
      <PopoverContent
        side="top"
        align="start"
        collisionPadding={12}
        className="w-[min(320px,calc(100vw-24px))] max-h-[min(var(--radix-popover-content-available-height),480px)] overflow-y-auto scrollbar-thin"
      >
        <div className="px-2.5 pt-1.5 pb-2">
          {isDoc ? (
            <>
              <p className="inline-flex items-center gap-1.5 text-[11px] text-[var(--color-fg-subtle)]">
                <FileText size={11} aria-hidden />
                {t('sources.fromDocuments')}
              </p>
              <p className="mt-1 block text-sm font-medium text-[var(--color-fg)] leading-snug [overflow-wrap:anywhere]">
                {citation.title}
              </p>
              {citation.snippet ? (
                <p className="mt-2 text-xs text-[var(--color-fg-muted)] leading-relaxed [overflow-wrap:anywhere]">
                  {isKnowledgeBase
                    ? boundedCitationSnippet(citation.snippet)
                    : citation.snippet}
                </p>
              ) : null}
              {canOpenDocument ? (
                <button
                  type="button"
                  onClick={() => {
                    setOpen(false)
                    onOpenDocument?.(citation)
                  }}
                  className="mt-3 inline-flex items-center gap-1.5 text-[11px] text-[var(--color-accent)] interactive hover:text-[var(--color-accent-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[4px]"
                >
                  {t('sources.openDocument')}
                  <FileSearch size={11} aria-hidden />
                </button>
              ) : null}
            </>
          ) : (
            <>
              <p className="text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)] [overflow-wrap:anywhere]">
                {citation.domain}
              </p>
              <a
                href={safeHref(citation.url)}
                target="_blank"
                rel="noopener noreferrer"
                className="mt-1 block text-sm font-medium text-[var(--color-fg)] hover:text-[var(--color-accent)] leading-snug [overflow-wrap:anywhere]"
              >
                {citation.title}
              </a>
              {citation.snippet ? (
                <p className="mt-2 text-xs text-[var(--color-fg-muted)] leading-relaxed [overflow-wrap:anywhere]">
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
  onOpenDocument?: (citation: Citation) => void
}

export function CitationList({ citations, onOpenDocument }: CitationListProps) {
  const { t } = useTranslation('chat')
  const [open, setOpen] = useState(false)
  if (citations.length === 0) return null
  const orderedCitations = citationsInDisplayOrder(citations)
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
            {orderedCitations.map((c) => {
              if (isKnowledgeBaseCitation(c)) {
                const snippet = boundedCitationSnippet(c.snippet)
                const canOpen = Boolean(onOpenDocument && documentCitationContentUrl(c))
                const content = (
                  <>
                    <span className="block leading-relaxed text-[var(--color-fg-muted)] [overflow-wrap:anywhere]">
                      <span className="inline-flex max-w-full items-center gap-1 font-medium text-[var(--color-fg)]">
                        <FileText size={11} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
                        <span className="[overflow-wrap:anywhere]">{c.title}</span>
                      </span>
                      <span className="ml-1.5 text-[var(--color-fg-subtle)]">{t('sources.fromDocuments')}</span>
                    </span>
                    {snippet ? (
                      <span className="mt-1 block line-clamp-3 text-[11.5px] leading-relaxed text-[var(--color-fg-subtle)] [overflow-wrap:anywhere]">
                        {snippet}
                      </span>
                    ) : null}
                  </>
                )
                return (
                  <li key={c.id} className="flex items-start gap-2.5 text-xs">
                    <CitationChip citation={c} onOpenDocument={onOpenDocument} />
                    {canOpen ? (
                      <button
                        type="button"
                        onClick={() => onOpenDocument?.(c)}
                        className="min-w-0 flex-1 rounded-[5px] text-left interactive hover:text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      >
                        {content}
                      </button>
                    ) : (
                      <span className="min-w-0 flex-1">{content}</span>
                    )}
                  </li>
                )
              }

              if (isDocumentCitation(c)) {
                return (
                  <li key={c.id} className="flex items-start gap-2.5 text-xs">
                    <CitationChip citation={c} />
                    <span className="min-w-0 flex-1 leading-relaxed text-[var(--color-fg-muted)] [overflow-wrap:anywhere]">
                      <span className="inline-flex max-w-full items-center gap-1 font-medium text-[var(--color-fg)]">
                        <FileText size={11} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
                        <span className="[overflow-wrap:anywhere]">{c.title}</span>
                      </span>
                      <span className="ml-1.5 text-[var(--color-fg-subtle)]">{t('sources.fromDocuments')}</span>
                    </span>
                  </li>
                )
              }

              return (
                <li key={c.id} className="flex items-start gap-2.5 text-xs">
                  <CitationChip citation={c} />
                  <a
                    href={safeHref(c.url)}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="min-w-0 flex-1 text-[var(--color-fg-muted)] hover:text-[var(--color-accent)] leading-relaxed [overflow-wrap:anywhere]"
                  >
                    <span className="font-medium text-[var(--color-fg)]">{c.title}</span>
                    <span className="ml-1.5">{c.domain}</span>
                  </a>
                </li>
              )
            })}
          </ol>
        </div>
      </div>
    </div>
  )
}
