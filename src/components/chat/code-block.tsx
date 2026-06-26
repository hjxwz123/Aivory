import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Check, Play, Square, AppWindow } from 'lucide-react'
import { useCopy } from '@/hooks/use-clipboard'
import { useCodeHighlight } from '@/lib/syntax/use-code-highlight'
import {
  runPython,
  type PythonRunHandle,
  type PythonRunPhase,
  type PythonRunResult,
  type PythonStreamChunk,
} from '@/lib/pyodide-runner'
import { autoOpenPreview, useHtmlPreview } from '@/store/html-preview'
import { useTheme } from '@/store/theme'
import { CodeRunOutput } from './code-run-output'
import { cn } from '@/lib/utils'

interface CodeBlockProps {
  code: string
  lang?: string
  className?: string
  /** True while the owning assistant message is still streaming. */
  live?: boolean
  /**
   * Stable identity for this block (message id + block index). Drives the
   * HTML preview panel ownership; falls back to useId when absent.
   */
  previewKey?: string
}

const PYTHON_LANGS = new Set(['python', 'py', 'python3'])
const HTML_LANGS = new Set(['html', 'htm', 'xhtml'])

/** Fenced block is HTML when tagged so, or (untagged) when it *reads* like a document. */
function isHtmlSnippet(code: string, lang?: string): boolean {
  if (lang) return HTML_LANGS.has(lang.toLowerCase())
  return /^\s*(?:<!doctype\s+html|<html[\s>])/i.test(code)
}

/**
 * Calm, sunken code block with a sticky header (language + actions).
 * Python blocks gain a Run button (Pyodide, in-browser, off-thread) with a
 * result well underneath; HTML blocks gain a Preview button and, while the
 * message streams, drive the live preview drawer automatically.
 */
export function CodeBlock({ code, lang, className, live = false, previewKey }: CodeBlockProps) {
  const { t } = useTranslation('chat')
  const { copied, copy } = useCopy()
  const theme = useTheme((s) => s.resolved)
  const [hovered, setHovered] = useState(false)
  const { html } = useCodeHighlight({ code, lang, live, theme })

  const fallbackKey = useId()
  const blockKey = previewKey ?? fallbackKey
  const isPython = PYTHON_LANGS.has((lang ?? '').toLowerCase())
  const isHtml = isHtmlSnippet(code, lang)

  // ---- Python execution -------------------------------------------------
  const [running, setRunning] = useState(false)
  const [phase, setPhase] = useState<PythonRunPhase>('queued')
  const [chunks, setChunks] = useState<PythonStreamChunk[]>([])
  const [outcome, setOutcome] = useState<PythonRunResult | null>(null)
  const handleRef = useRef<PythonRunHandle | null>(null)

  function startRun() {
    if (running) return
    setRunning(true)
    setPhase('queued')
    setChunks([])
    setOutcome(null)
    const handle = runPython(code, {
      onPhase: setPhase,
      onStream: (chunk) => setChunks((prev) => [...prev, chunk]),
    })
    handleRef.current = handle
    void handle.promise.then((result) => {
      handleRef.current = null
      setOutcome(result)
      setRunning(false)
    })
  }

  // Don't leave an orphaned run burning CPU after the block unmounts.
  useEffect(() => () => handleRef.current?.cancel(), [])

  // ---- HTML live preview --------------------------------------------------
  const ownsPreview = useHtmlPreview((s) => s.sourceKey === blockKey)
  useEffect(() => {
    if (!isHtml) return
    if (live && code.trim().length > 16) {
      // Streaming HTML pops the drawer once, then keeps it in sync.
      autoOpenPreview(blockKey, code)
    } else if (ownsPreview) {
      useHtmlPreview.getState().syncHtml(blockKey, code)
    }
  }, [isHtml, live, code, blockKey, ownsPreview])

  return (
    <div
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      className={cn(
        'group/code relative my-3.5 overflow-hidden',
        'rounded-[14px] border border-[var(--color-border)]',
        'bg-[var(--color-code-bg)] text-[var(--color-code-fg)]',
        className,
      )}
    >
      <div className="flex items-center justify-between gap-2 px-3 h-9 border-b border-[var(--color-border-subtle)] text-[var(--color-fg-subtle)]">
        <span className="font-mono text-[11px] uppercase tracking-wider">{lang || 'plain'}</span>
        <div className="flex items-center gap-1">
          {isPython && !live ? (
            running ? (
              <HeaderAction onClick={() => handleRef.current?.cancel()} label={t('code.stop')}>
                <Square size={11} aria-hidden />
              </HeaderAction>
            ) : (
              <HeaderAction onClick={startRun} label={outcome ? t('code.rerun') : t('code.run')}>
                <Play size={12} aria-hidden />
              </HeaderAction>
            )
          ) : null}
          {isHtml ? (
            <HeaderAction
              onClick={() => useHtmlPreview.getState().openPreview(blockKey, code)}
              label={t('code.preview')}
            >
              <AppWindow size={12} aria-hidden />
            </HeaderAction>
          ) : null}
          <button
            type="button"
            onClick={() => void copy(code)}
            className={cn(
              'inline-flex items-center gap-1.5 h-6 px-1.5 rounded-[6px]',
              'text-[11px] font-medium text-[var(--color-fg-muted)] interactive',
              'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              hovered || copied ? 'opacity-100' : 'opacity-0 sm:opacity-100',
            )}
            aria-label={copied ? t('actions.copied') : t('actions.copy')}
          >
            {copied ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
            <span>{copied ? t('actions.copied') : t('actions.copy')}</span>
          </button>
        </div>
      </div>
      <pre className="overflow-x-auto p-4 text-[13px] leading-[1.65]">
        <code className="font-mono whitespace-pre" dangerouslySetInnerHTML={{ __html: html }} />
      </pre>
      {isPython ? (
        <CodeRunOutput
          running={running}
          phase={phase}
          chunks={chunks}
          outcome={outcome}
          onClear={() => {
            setChunks([])
            setOutcome(null)
          }}
        />
      ) : null}
    </div>
  )
}

interface HeaderActionProps {
  onClick: () => void
  label: string
  children: ReactNode
}

function HeaderAction({ onClick, label, children }: HeaderActionProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 h-6 px-1.5 rounded-[6px]',
        'text-[11px] font-medium text-[var(--color-fg-muted)] interactive',
        'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
      )}
    >
      {children}
      <span>{label}</span>
    </button>
  )
}
