import { useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Eraser } from 'lucide-react'
import type { PythonRunPhase, PythonRunResult, PythonStreamChunk } from '@/lib/pyodide-runner'
import { cn } from '@/lib/utils'

interface CodeRunOutputProps {
  running: boolean
  phase: PythonRunPhase
  chunks: PythonStreamChunk[]
  outcome: PythonRunResult | null
  onClear: () => void
}

/**
 * CodeRunOutput — the result well under a Python code block: live
 * stdout/stderr stream, traceback, final-expression repr, matplotlib
 * figures, and a quiet footer (duration + engine note + clear).
 */
export function CodeRunOutput({ running, phase, chunks, outcome, onClear }: CodeRunOutputProps) {
  const { t } = useTranslation('chat')

  // Follow the stream like a terminal would.
  const streamRef = useRef<HTMLPreElement>(null)
  useEffect(() => {
    const el = streamRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [chunks])

  if (!running && !outcome) return null

  const phaseLabel: Record<PythonRunPhase, string> = {
    queued: t('code.phaseQueued'),
    boot: t('code.phaseBoot'),
    packages: t('code.phasePackages'),
    running: t('code.phaseRunning'),
  }

  const failureNote = outcome?.aborted
    ? { tone: 'muted' as const, text: t('code.stopped') }
    : outcome?.timedOut
      ? { tone: 'danger' as const, text: t('code.timeout', { seconds: 120 }) }
      : outcome?.engineFailed
        ? { tone: 'danger' as const, text: t('code.engineFailed') }
        : null

  const quiet =
    outcome?.ok === true &&
    chunks.length === 0 &&
    outcome.result === undefined &&
    outcome.images.length === 0

  return (
    <div className="border-t border-[var(--color-border-subtle)] divide-y divide-[var(--color-border-subtle)]">
      {running ? (
        <div className="flex items-center gap-2 px-3.5 h-9 text-[11px] text-[var(--color-fg-subtle)]">
          <span
            aria-hidden
            className="inline-block size-1.5 rounded-full bg-[var(--color-secondary)] animate-[streaming-pulse_1600ms_ease-in-out_infinite]"
          />
          {phaseLabel[phase]}
        </div>
      ) : null}

      {chunks.length > 0 ? (
        <pre
          ref={streamRef}
          className="max-h-72 overflow-auto px-4 py-3 font-mono text-[12.5px] leading-[1.6] whitespace-pre-wrap break-words text-[var(--color-code-fg)]"
        >
          {chunks.map((chunk, i) => (
            <span key={i} className={chunk.kind === 'stderr' ? 'text-[var(--color-warning)]' : undefined}>
              {chunk.text}
            </span>
          ))}
        </pre>
      ) : null}

      {outcome && !running ? (
        <>
          {failureNote ? <Note tone={failureNote.tone}>{failureNote.text}</Note> : null}

          {outcome.error && !failureNote ? (
            <pre className="max-h-72 overflow-auto px-4 py-3 font-mono text-[12px] leading-[1.6] whitespace-pre-wrap break-words text-[var(--color-danger)]">
              {outcome.error}
            </pre>
          ) : null}

          {outcome.result !== undefined ? (
            <div className="px-4 py-3">
              <div className="mb-1 text-[10.5px] uppercase tracking-wider text-[var(--color-fg-subtle)]">
                {t('code.result')}
              </div>
              <pre className="max-h-48 overflow-auto font-mono text-[12.5px] leading-[1.6] whitespace-pre-wrap break-words text-[var(--color-code-fg)]">
                {outcome.result}
              </pre>
            </div>
          ) : null}

          {outcome.images.length > 0 ? (
            <div className="flex flex-col gap-3 px-4 py-3">
              {outcome.images.map((b64, i) => (
                <img
                  key={i}
                  src={`data:image/png;base64,${b64}`}
                  alt={t('code.figureAlt', { index: i + 1 })}
                  className="max-w-full rounded-[10px] border border-[var(--color-border)] bg-[var(--color-preview-canvas)]"
                />
              ))}
            </div>
          ) : null}

          {quiet ? <Note tone="muted">{t('code.noOutput')}</Note> : null}

          <div className="flex items-center gap-2 px-3.5 h-8 text-[10.5px] text-[var(--color-fg-subtle)]">
            <span className="tabular-nums">{formatDuration(outcome.durationMs)}</span>
            <span aria-hidden>·</span>
            <span className="flex-1 truncate">{t('code.poweredBy')}</span>
            <button
              type="button"
              onClick={onClear}
              className="inline-flex items-center gap-1 h-6 px-1.5 rounded-[6px] font-medium text-[var(--color-fg-subtle)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <Eraser size={11} aria-hidden />
              {t('code.clear')}
            </button>
          </div>
        </>
      ) : null}
    </div>
  )
}

function Note({ tone, children }: { tone: 'muted' | 'danger'; children: ReactNode }) {
  return (
    <div
      className={cn(
        'px-4 py-2.5 text-[12px]',
        tone === 'danger' ? 'text-[var(--color-danger)]' : 'text-[var(--color-fg-subtle)]',
      )}
    >
      {children}
    </div>
  )
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(1)} s`
}
