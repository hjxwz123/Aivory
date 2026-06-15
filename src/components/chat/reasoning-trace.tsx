import { type ComponentType, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Brain,
  Search,
  Globe,
  Terminal,
  BookOpen,
  Image as ImageIcon,
  Sparkles,
  ChevronRight,
  Check,
  AlertTriangle,
} from 'lucide-react'
import type { ReasoningItem, ToolCall } from '@/types/chat'
import { cn } from '@/lib/utils'

interface ReasoningTraceProps {
  /** Ordered, interleaved trace — thinking runs + tool rounds in the exact
   *  order they happened (§7.1-4). */
  reasoning?: ReasoningItem[]
  /** True while the assistant message is still streaming. */
  streaming?: boolean
  /** True once the visible answer text has started — the reasoning phase has
   *  given way to the final answer, so the panel auto-collapses. */
  settled?: boolean
}

const TOOL_ICON: Record<string, ComponentType<{ size?: number; className?: string }>> = {
  web_search: Search,
  web_fetch: Globe,
  python_execute: Terminal,
  search_knowledge_base: BookOpen,
  use_skill: BookOpen,
  image_generate: ImageIcon,
  save_memory: Sparkles,
}

/**
 * ReasoningTrace — the unified "thinking" panel (§1.1 / §7.1-4). It folds the
 * model's extended-thinking text AND its tool rounds (web_search, python_execute,
 * search_knowledge_base, image_generate, …) into ONE collapsible trace instead
 * of a separate thinking block + a stack of tool cards.
 *
 * Live feedback (the user's ask): while streaming, the panel is open, the brain
 * glyph pulses, and every running tool shows a pulsing status dot plus a
 * per-second elapsed counter — so a long search / sandbox run never looks like
 * a frozen blank. No spinning loaders (CLAUDE.md §7 — shimmer/pulse only). Once
 * the answer text starts, the panel collapses to a one-line summary the user
 * can reopen.
 */
export function ReasoningTrace({ reasoning, streaming = false, settled = false }: ReasoningTraceProps) {
  const { t } = useTranslation(['chat', 'common'])
  const items = reasoning ?? []

  // Active = the reasoning phase is live (streaming and the answer hasn't
  // started). Drives the pulse + auto-expand.
  const active = streaming && !settled

  const [expanded, setExpanded] = useState(active)
  const userToggled = useRef(false)
  useEffect(() => {
    // Auto open while reasoning, auto close once it settles — unless the user
    // has taken manual control of the disclosure.
    if (userToggled.current) return
    setExpanded(active)
  }, [active])

  if (items.length === 0) return null

  const headline = active ? t('thinking') : t('reasoning.title')

  return (
    <div className="mb-3">
      {/* Minimal, box-free disclosure — just an icon + "thinking" label + caret. */}
      <button
        type="button"
        onClick={() => {
          userToggled.current = true
          setExpanded((v) => !v)
        }}
        className="flex items-center gap-1.5 -ml-1 px-1 py-0.5 text-left interactive rounded-[6px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <Brain
          size={13}
          aria-hidden
          className={cn(
            'shrink-0 text-[var(--color-secondary)]',
            active && 'animate-[streaming-pulse_1600ms_ease-in-out_infinite]',
          )}
        />
        <span className="min-w-0 truncate text-[12.5px]">{headline}</span>
        <ChevronRight
          size={12}
          aria-hidden
          className={cn('shrink-0 transition-transform duration-150', expanded && 'rotate-90')}
        />
      </button>

      {/* grid 0fr→1fr animates height without measuring; the global
          prefers-reduced-motion rule neutralises the transition automatically. */}
      <div
        className={cn(
          'grid transition-[grid-template-rows] duration-300 ease-[var(--ease-out)]',
          expanded ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]',
        )}
      >
        <div className="overflow-hidden">
          <div className="space-y-2 ml-[6px] mt-1.5 pl-3.5 border-l border-[var(--color-divider)]">
            {items.map((it) => {
              if (it.kind === 'thinking') {
                return (
                  <div
                    key={it.id}
                    className="whitespace-pre-wrap font-mono text-[12px] leading-relaxed text-[var(--color-fg-faint)]"
                  >
                    {it.text}
                  </div>
                )
              }
              if (it.kind === 'narration') {
                // Pre-tool narration: render as prose (distinct from mono
                // chain-of-thought) so it reads like the aside it is.
                return (
                  <div
                    key={it.id}
                    className="whitespace-pre-wrap text-[12.5px] leading-relaxed text-[var(--color-fg-muted)]"
                  >
                    {it.text}
                  </div>
                )
              }
              return <ToolStep key={it.id} toolCall={it.tool} />
            })}
          </div>
        </div>
      </div>
    </div>
  )
}

function ToolStep({ toolCall }: { toolCall: ToolCall }) {
  const { t } = useTranslation(['chat', 'common'])
  const { status, label, name, input, output } = toolCall
  const [expanded, setExpanded] = useState(false)
  const elapsed = useElapsed(toolCall)
  const Icon = TOOL_ICON[name] ?? Sparkles
  const displayName = label || t(`tools.${name}`, { defaultValue: name })
  const subtitle = pickSubtitle(name, input)
  const code =
    name === 'python_execute' && typeof (input as Record<string, unknown>)?.code === 'string'
      ? ((input as Record<string, unknown>).code as string)
      : null
  const isPython = name === 'python_execute'
  const hasBody = Boolean(output || code)

  return (
    <div className="overflow-hidden rounded-[10px] border border-[var(--color-border-subtle)] bg-[var(--color-surface)]">
      <button
        type="button"
        onClick={() => hasBody && setExpanded((v) => !v)}
        className={cn(
          'flex w-full items-center gap-2 px-2.5 py-1.5 text-left',
          hasBody ? 'interactive cursor-pointer hover:bg-[var(--color-bg-muted)]/40' : 'cursor-default',
        )}
      >
        <StatusDot status={status} />
        <Icon size={12} className="shrink-0 text-[var(--color-fg-subtle)]" />
        <span className="shrink-0 text-[12.5px] font-medium text-[var(--color-fg)]">{displayName}</span>
        {subtitle ? (
          <span className="min-w-0 truncate font-mono text-[11.5px] text-[var(--color-fg-muted)]">{subtitle}</span>
        ) : null}
        <span className="ml-auto shrink-0 tabular-nums text-[10.5px] text-[var(--color-fg-subtle)]">{elapsed}</span>
        {hasBody ? (
          <ChevronRight
            size={12}
            aria-hidden
            className={cn(
              'shrink-0 text-[var(--color-fg-subtle)] transition-transform duration-150',
              expanded && 'rotate-90',
            )}
          />
        ) : null}
      </button>
      {expanded && code ? (
        <pre className="max-h-[300px] overflow-auto border-t border-[var(--color-border-subtle)] bg-[var(--color-code-bg)] px-3 py-2.5 font-mono text-[11px] leading-relaxed whitespace-pre-wrap text-[var(--color-code-fg)]">
          {code}
        </pre>
      ) : null}
      {expanded && output ? (
        <div
          className={cn(
            'border-t border-[var(--color-border-subtle)] px-3 py-2.5 text-[11.5px] leading-relaxed',
            isPython
              ? 'max-h-[320px] overflow-auto bg-[var(--color-code-bg)] font-mono whitespace-pre-wrap text-[var(--color-code-fg)]'
              : 'text-[var(--color-fg-muted)]',
          )}
        >
          {output}
        </div>
      ) : null}
    </div>
  )
}

function StatusDot({ status }: { status: ToolCall['status'] }) {
  if (status === 'running') {
    return (
      <span
        aria-hidden
        className="inline-block size-2 shrink-0 rounded-full bg-[var(--color-secondary)] animate-[streaming-pulse_1600ms_ease-in-out_infinite]"
      />
    )
  }
  if (status === 'complete') {
    return <Check size={13} aria-hidden className="shrink-0 text-[var(--color-success)]" />
  }
  return <AlertTriangle size={12} aria-hidden className="shrink-0 text-[var(--color-danger)]" />
}

/**
 * useElapsed returns a live "3s" / "1m05s" counter. While the tool runs it
 * ticks once per second (the key "we're still working" signal); once finished
 * it freezes at the total duration.
 */
function useElapsed(tc: ToolCall): string {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (tc.status !== 'running') return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [tc.status])
  const end = tc.status === 'running' ? now : tc.endedAt ?? tc.startedAt
  return formatElapsed(Math.max(0, end - tc.startedAt))
}

function formatElapsed(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m${String(s % 60).padStart(2, '0')}s`
}

function pickSubtitle(name: string, input: ToolCall['input']): string | null {
  const inp = (input ?? {}) as Record<string, unknown>
  if ((name === 'web_search' || name === 'search_knowledge_base') && typeof inp.query === 'string') {
    return inp.query
  }
  if (name === 'web_fetch' && typeof inp.url === 'string') return inp.url
  if (name === 'use_skill' && typeof inp.name === 'string') return inp.name
  if (name === 'image_generate' && typeof inp.prompt === 'string') return inp.prompt
  return null
}
