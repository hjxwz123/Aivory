import { ExternalLink } from 'lucide-react'
import type { Citation } from '@/types/chat'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'

interface CitationChipProps {
  citation: Citation
  className?: string
}

export function CitationChip({ citation, className }: CitationChipProps) {
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
          <p className="text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)]">
            {citation.domain}
          </p>
          <a
            href={citation.url}
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
            href={citation.url}
            target="_blank"
            rel="noopener noreferrer"
            className="mt-3 inline-flex items-center gap-1.5 text-[11px] text-[var(--color-accent)] hover:text-[var(--color-accent-hover)]"
          >
            Open source
            <ExternalLink size={11} aria-hidden />
          </a>
        </div>
      </PopoverContent>
    </Popover>
  )
}

interface CitationListProps {
  citations: Citation[]
}

export function CitationList({ citations }: CitationListProps) {
  if (citations.length === 0) return null
  return (
    <div className="mt-5 border-t border-[var(--color-divider)] pt-3.5">
      <p className="text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)] mb-2.5">
        Sources
      </p>
      <ol className="space-y-1.5">
        {citations.map((c) => (
          <li key={c.id} className="flex items-start gap-2.5 text-xs">
            <CitationChip citation={c} />
            <a
              href={c.url}
              target="_blank"
              rel="noopener noreferrer"
              className="text-[var(--color-fg-muted)] hover:text-[var(--color-accent)] leading-relaxed"
            >
              <span className="font-medium text-[var(--color-fg)]">{c.title}</span>
              <span className="ml-1.5">{c.domain}</span>
            </a>
          </li>
        ))}
      </ol>
    </div>
  )
}
