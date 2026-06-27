import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Check, Play, Square, AppWindow, CodeXml } from 'lucide-react'
import { Tooltip } from '@/components/ui/tooltip'
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

// Friendly, properly-cased display names for the header. Anything not listed
// falls back to a simple capitalisation ("java" → "Java", "ruby" → "Ruby").
const LANG_LABELS: Record<string, string> = {
  js: 'JavaScript', javascript: 'JavaScript', mjs: 'JavaScript', cjs: 'JavaScript',
  ts: 'TypeScript', typescript: 'TypeScript', jsx: 'JSX', tsx: 'TSX',
  py: 'Python', python: 'Python', python3: 'Python', rb: 'Ruby', go: 'Go', golang: 'Go',
  rs: 'Rust', rust: 'Rust', java: 'Java', kt: 'Kotlin', kotlin: 'Kotlin',
  cs: 'C#', csharp: 'C#', cpp: 'C++', 'c++': 'C++', c: 'C', objc: 'Objective-C',
  php: 'PHP', sh: 'Shell', bash: 'Bash', zsh: 'Zsh', shell: 'Shell', ps1: 'PowerShell',
  html: 'HTML', htm: 'HTML', xml: 'XML', css: 'CSS', scss: 'SCSS', sass: 'Sass', less: 'Less',
  json: 'JSON', jsonc: 'JSON', yaml: 'YAML', yml: 'YAML', toml: 'TOML',
  sql: 'SQL', md: 'Markdown', markdown: 'Markdown', diff: 'Diff', graphql: 'GraphQL',
  swift: 'Swift', dart: 'Dart', scala: 'Scala', r: 'R', lua: 'Lua', dockerfile: 'Dockerfile',
}

function langLabel(lang?: string): string {
  if (!lang) return 'Plain'
  const key = lang.toLowerCase()
  return LANG_LABELS[key] ?? key.charAt(0).toUpperCase() + key.slice(1)
}

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
      className={cn(
        'group/code relative my-3.5 overflow-hidden',
        'rounded-[14px] border border-[var(--color-border)]',
        'bg-[var(--color-code-bg)] text-[var(--color-code-fg)]',
        className,
      )}
    >
      <div className="flex items-center justify-between gap-2 px-4 h-10">
        <span className="inline-flex items-center gap-1.5 text-[12.5px] font-medium text-[var(--color-fg-muted)]">
          <CodeXml size={14} strokeWidth={1.5} aria-hidden className="text-[var(--color-fg-subtle)]" />
          {langLabel(lang)}
        </span>
        <div className="flex items-center gap-0.5">
          {isPython && !live ? (
            running ? (
              <IconAction onClick={() => handleRef.current?.cancel()} label={t('code.stop')}>
                <Square size={13} aria-hidden />
              </IconAction>
            ) : (
              <IconAction onClick={startRun} label={outcome ? t('code.rerun') : t('code.run')}>
                <Play size={13} aria-hidden />
              </IconAction>
            )
          ) : null}
          {isHtml ? (
            <IconAction
              onClick={() => useHtmlPreview.getState().openPreview(blockKey, code)}
              label={t('code.preview')}
            >
              <AppWindow size={13} aria-hidden />
            </IconAction>
          ) : null}
          <IconAction onClick={() => void copy(code)} label={copied ? t('actions.copied') : t('actions.copy')}>
            {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
          </IconAction>
        </div>
      </div>
      <pre className="overflow-x-auto px-4 pb-4 pt-1 text-[13px] leading-[1.65]">
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

interface IconActionProps {
  onClick: () => void
  label: string
  children: ReactNode
}

// Icon-only header action (Run / Preview / Copy). The label rides in a tooltip
// + aria-label so the header stays minimal while keeping the action discoverable.
function IconAction({ onClick, label, children }: IconActionProps) {
  return (
    <Tooltip content={label}>
      <button
        type="button"
        onClick={onClick}
        aria-label={label}
        className={cn(
          'inline-flex items-center justify-center size-7 rounded-[7px]',
          'text-[var(--color-fg-subtle)] interactive',
          'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        )}
      >
        {children}
      </button>
    </Tooltip>
  )
}
