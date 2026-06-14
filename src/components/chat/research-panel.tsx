/**
 * ResearchPanel — the live "deep research" surface (§ deep-research mode). Shows
 * the research plan as a checklist and the sources being read, while the report
 * streams below as the message body. Auto-expands while researching and
 * collapses once the report starts (same lifecycle as ReasoningTrace). Sage
 * accent is the AI-status colour (CLAUDE.md §2.4); no spinners (§7).
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Telescope, Globe, Check, Circle, ChevronRight, AlertTriangle } from 'lucide-react'
import type { ResearchSource, ResearchState } from '@/types/chat'
import { cn, safeHref } from '@/lib/utils'

interface ResearchPanelProps {
  research: ResearchState
  streaming?: boolean
  /** True once the report body has started (collapse the panel). */
  settled?: boolean
}

export function ResearchPanel({ research, streaming = false, settled = false }: ResearchPanelProps) {
  const { t } = useTranslation('chat')
  const tasks = research.tasks ?? []
  const sources = research.sources ?? []
  const active = streaming && !settled

  const [expanded, setExpanded] = useState(active)
  const userToggled = useRef(false)
  useEffect(() => {
    if (userToggled.current) return
    setExpanded(active)
  }, [active])

  if (tasks.length === 0 && sources.length === 0) return null

  const doneCount = tasks.filter((tk) => tk.status === 'done').length

  return (
    <div className="mb-3 overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-bg-subtle)]">
      <button
        type="button"
        onClick={() => {
          userToggled.current = true
          setExpanded((v) => !v)
        }}
        className="flex w-full items-center gap-2 px-3 py-2 text-left interactive hover:bg-[var(--color-bg-muted)]/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <Telescope
          size={13}
          aria-hidden
          className={cn(
            'shrink-0 text-[var(--color-secondary)]',
            active && 'animate-[streaming-pulse_1600ms_ease-in-out_infinite]',
          )}
        />
        <span className="min-w-0 flex-1 truncate text-[13px] text-[var(--color-fg-muted)]">
          {research.title?.trim() ? research.title : t('research.title')}
        </span>
        {tasks.length > 0 ? (
          <span className="shrink-0 text-[11px] tabular-nums text-[var(--color-fg-subtle)]">
            {t('research.steps', { done: doneCount, total: tasks.length })}
          </span>
        ) : null}
        <ChevronRight
          size={13}
          aria-hidden
          className={cn(
            'shrink-0 text-[var(--color-fg-subtle)] transition-transform duration-150',
            expanded && 'rotate-90',
          )}
        />
      </button>

      <div
        className={cn(
          'grid transition-[grid-template-rows] duration-300 ease-[var(--ease-out)]',
          expanded ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]',
        )}
      >
        <div className="overflow-hidden">
          <div className="space-y-4 px-3 pb-3 pt-1">
            {tasks.length > 0 ? (
              <div>
                <div className="mb-1.5 text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)]">
                  {t('research.planLabel')}
                </div>
                <ul className="space-y-1.5">
                  {tasks.map((tk) => (
                    <li key={tk.id} className="flex items-start gap-2 text-[12.5px]">
                      <TaskDot status={tk.status} />
                      <span
                        className={cn(
                          'leading-relaxed',
                          tk.status === 'done' ? 'text-[var(--color-fg-muted)]' : 'text-[var(--color-fg)]',
                        )}
                      >
                        {tk.question}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            {sources.length > 0 ? (
              <div>
                <div className="mb-1.5 text-[11px] uppercase tracking-wider text-[var(--color-fg-subtle)]">
                  {t('research.sourcesLabel', { count: sources.length })}
                </div>
                <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
                  {sources.map((s) => (
                    <SourceCard key={s.id} source={s} />
                  ))}
                </div>
              </div>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  )
}

function TaskDot({ status }: { status: ResearchState['tasks'][number]['status'] }) {
  if (status === 'done') {
    return <Check size={13} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-success)]" />
  }
  if (status === 'researching' || status === 'partial') {
    return (
      <span
        aria-hidden
        className="mt-1 inline-block size-2 shrink-0 rounded-full bg-[var(--color-secondary)] animate-[streaming-pulse_1600ms_ease-in-out_infinite]"
      />
    )
  }
  return <Circle size={11} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)]" />
}

function SourceCard({ source }: { source: ResearchSource }) {
  const failed = source.status === 'failed'
  const kept = source.status === 'kept' || source.status === 'read'
  return (
    <a
      href={safeHref(source.url)}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        'group flex items-start gap-2 overflow-hidden rounded-[10px] border px-2.5 py-2',
        'border-[var(--color-border-subtle)] bg-[var(--color-surface)]',
        'interactive hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
      )}
    >
      {failed ? (
        <AlertTriangle size={12} aria-hidden className="mt-0.5 shrink-0 text-[var(--color-warning)]" />
      ) : (
        <Globe
          size={12}
          aria-hidden
          className={cn('mt-0.5 shrink-0', kept ? 'text-[var(--color-secondary)]' : 'text-[var(--color-fg-subtle)]')}
        />
      )}
      <span className="min-w-0">
        <span className="block truncate text-[12px] font-medium text-[var(--color-fg)]">
          {source.title || source.domain || source.url}
        </span>
        <span className="block truncate text-[11px] text-[var(--color-fg-subtle)]">{source.domain}</span>
      </span>
    </a>
  )
}
