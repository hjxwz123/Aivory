import { type ReactNode, useState } from 'react'
import { Sparkles, Loader2, CheckCircle2, AlertTriangle, ChevronDown, Search, Terminal, BookOpen, Image as ImageIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { ToolCall } from '@/types/chat'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

interface ToolCallCardProps {
  toolCall: ToolCall
  children?: ReactNode
}

/**
 * ToolCallCard — collapses a tool round into one compact row that shows what
 * the model is doing (e.g. "Searching the web: 'best espresso machines 2026'")
 * and expands to the full output on click. Per tool we render a richer view:
 *
 *   - web_search → magnifying-glass icon + the query string
 *   - python_execute → terminal icon + a 6-line preview of the code (mono)
 *   - search_knowledge_base → book icon + the query
 *   - image_generate → image icon + the prompt
 *   - use_skill → sparkles icon + the skill name
 *
 * The result body uses a monospace terminal look when the tool is
 * python_execute so stdout reads like a real shell.
 */
export function ToolCallCard({ toolCall }: ToolCallCardProps) {
  const { status, label, name, output, input } = toolCall
  const { t } = useTranslation(['chat', 'common'])
  const displayName = label || t(`tools.${name}`, { defaultValue: name })
  const [expanded, setExpanded] = useState(false)

  // Per-tool richer preview drawn from input args (search query / code / etc).
  const subtitle = (() => {
    const inp = (input ?? {}) as Record<string, unknown>
    if (name === 'web_search' && typeof inp.query === 'string') return inp.query
    if (name === 'search_knowledge_base' && typeof inp.query === 'string') return inp.query
    if (name === 'use_skill' && typeof inp.name === 'string') return inp.name as string
    if (name === 'image_generate' && typeof inp.prompt === 'string') return inp.prompt as string
    return null
  })()
  const code = name === 'python_execute' && typeof (input as Record<string, unknown>)?.code === 'string'
    ? ((input as Record<string, unknown>).code as string)
    : null
  const PreviewIcon = (() => {
    switch (name) {
      case 'web_search':
      case 'web_fetch':
        return Search
      case 'python_execute':
        return Terminal
      case 'search_knowledge_base':
      case 'use_skill':
        return BookOpen
      case 'image_generate':
        return ImageIcon
      default:
        return Sparkles
    }
  })()
  const isPython = name === 'python_execute'
  const hasBody = Boolean(output || code)

  return (
    <div
      className={cn(
        'group/tool my-3 rounded-[14px] border border-[var(--color-border)]',
        'bg-[var(--color-surface-sunken)] overflow-hidden',
      )}
    >
      <button
        type="button"
        onClick={() => hasBody && setExpanded((v) => !v)}
        className={cn(
          'flex w-full items-center gap-2.5 px-3.5 py-2.5 text-left interactive',
          hasBody ? 'cursor-pointer hover:bg-[var(--color-bg-muted)]/40' : 'cursor-default',
        )}
      >
        <span
          className={cn(
            'inline-flex shrink-0 size-6 items-center justify-center rounded-full',
            status === 'running' && 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]',
            status === 'complete' && 'bg-[var(--color-success-soft)] text-[var(--color-success)]',
            status === 'error' && 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]',
          )}
        >
          {status === 'running' ? (
            <Loader2 size={12} className="animate-[spin_900ms_linear_infinite]" aria-hidden />
          ) : status === 'complete' ? (
            <CheckCircle2 size={12} aria-hidden />
          ) : (
            <AlertTriangle size={12} aria-hidden />
          )}
        </span>
        <div className="flex-1 min-w-0 flex items-baseline gap-2">
          <span className="text-sm font-medium text-[var(--color-fg)] truncate inline-flex items-center gap-1.5">
            <PreviewIcon size={12} className="text-[var(--color-fg-subtle)]" aria-hidden />
            {displayName}
          </span>
          {subtitle ? (
            <span className="text-[12px] text-[var(--color-fg-muted)] font-mono truncate">{subtitle}</span>
          ) : null}
        </div>
        <Badge
          variant={status === 'running' ? 'sage' : status === 'complete' ? 'success' : 'danger'}
          size="xs"
          className="ml-auto"
        >
          {status === 'running'
            ? t('common:common.running')
            : status === 'complete'
              ? t('common:common.done')
              : t('common:common.error')}
        </Badge>
        {hasBody ? (
          <ChevronDown
            size={13}
            className={cn(
              'shrink-0 text-[var(--color-fg-subtle)] transition-transform duration-150',
              expanded && 'rotate-180',
            )}
            aria-hidden
          />
        ) : null}
      </button>
      {expanded && code ? (
        <pre className="border-t border-[var(--color-border-subtle)] bg-[var(--color-bg-sunken)] px-3.5 py-2.5 text-[11px] leading-relaxed text-[var(--color-fg)] font-mono whitespace-pre-wrap overflow-auto max-h-[300px]">
          {code}
        </pre>
      ) : null}
      {expanded && output ? (
        <div
          className={cn(
            'px-3.5 py-2.5 text-xs leading-relaxed border-t border-[var(--color-border-subtle)]',
            isPython
              ? 'bg-[var(--color-bg-sunken)] text-[var(--color-fg)] font-mono whitespace-pre-wrap max-h-[320px] overflow-auto'
              : 'text-[var(--color-fg-muted)]',
          )}
        >
          {output}
        </div>
      ) : null}
    </div>
  )
}
