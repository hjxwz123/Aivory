import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Copy, Check, Play, Square, AppWindow } from 'lucide-react'
import { useCopy } from '@/hooks/use-clipboard'
import {
  runPython,
  type PythonRunHandle,
  type PythonRunPhase,
  type PythonRunResult,
  type PythonStreamChunk,
} from '@/lib/pyodide-runner'
import { autoOpenPreview, useHtmlPreview } from '@/store/html-preview'
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

// Small per-language token map. Token names map to CSS classes that
// pick up theme tokens (`--color-syntax-*`). Order matters: regexes are tried
// left-to-right, first match wins.
type Token = { re: RegExp; cls: string }
const LANG_TOKENS: Record<string, Token[]> = {
  ts: [
    { re: /\/\/[^\n]*/g, cls: 'comment' },
    { re: /\/\*[\s\S]*?\*\//g, cls: 'comment' },
    { re: /(['"`])(?:\\.|(?!\1).)*\1/g, cls: 'string' },
    { re: /\b(import|export|from|as|const|let|var|function|return|class|extends|implements|interface|type|enum|if|else|for|while|do|switch|case|break|continue|new|this|super|in|of|typeof|instanceof|throw|try|catch|finally|async|await|public|private|protected|static|readonly|abstract|declare|default|null|undefined|true|false)\b/g, cls: 'keyword' },
    { re: /\b(0x[0-9a-fA-F]+|\d+(?:\.\d+)?(?:e[+-]?\d+)?)\b/g, cls: 'number' },
    { re: /\b([A-Z][A-Za-z0-9_]*)\b/g, cls: 'type' },
    { re: /\b([a-zA-Z_$][\w$]*)(?=\s*\()/g, cls: 'fn' },
  ],
  python: [
    { re: /#[^\n]*/g, cls: 'comment' },
    { re: /("""[\s\S]*?"""|'''[\s\S]*?''')/g, cls: 'string' },
    { re: /(['"])(?:\\.|(?!\1).)*\1/g, cls: 'string' },
    { re: /\b(def|class|return|if|elif|else|for|while|in|not|and|or|is|None|True|False|import|from|as|with|try|except|finally|raise|lambda|yield|pass|break|continue|global|nonlocal|async|await)\b/g, cls: 'keyword' },
    { re: /\b(\d+(?:\.\d+)?)\b/g, cls: 'number' },
    { re: /\b([a-zA-Z_][\w]*)(?=\s*\()/g, cls: 'fn' },
  ],
  go: [
    { re: /\/\/[^\n]*/g, cls: 'comment' },
    { re: /\/\*[\s\S]*?\*\//g, cls: 'comment' },
    { re: /`[\s\S]*?`|"(?:\\.|[^"])*"/g, cls: 'string' },
    { re: /\b(func|return|if|else|for|range|switch|case|default|type|struct|interface|map|chan|select|go|defer|var|const|package|import|nil|true|false|break|continue|fallthrough|new|make)\b/g, cls: 'keyword' },
    { re: /\b(\d+(?:\.\d+)?)\b/g, cls: 'number' },
    { re: /\b([A-Z][\w]*)\b/g, cls: 'type' },
  ],
  html: [
    { re: /<!--[\s\S]*?-->/g, cls: 'comment' },
    { re: /(["'])(?:\\.|(?!\1).)*\1/g, cls: 'string' },
    { re: /<\/?[a-zA-Z][\w-]*|\/?>/g, cls: 'keyword' },
    { re: /\b[a-zA-Z-]+(?==)/g, cls: 'fn' },
  ],
}
LANG_TOKENS.tsx = LANG_TOKENS.ts
LANG_TOKENS.javascript = LANG_TOKENS.ts
LANG_TOKENS.js = LANG_TOKENS.ts
LANG_TOKENS.jsx = LANG_TOKENS.ts
LANG_TOKENS.py = LANG_TOKENS.python
LANG_TOKENS.python3 = LANG_TOKENS.python
LANG_TOKENS.golang = LANG_TOKENS.go
LANG_TOKENS.htm = LANG_TOKENS.html
LANG_TOKENS.xhtml = LANG_TOKENS.html

const PYTHON_LANGS = new Set(['python', 'py', 'python3'])
const HTML_LANGS = new Set(['html', 'htm', 'xhtml'])

/** Fenced block is HTML when tagged so, or (untagged) when it *reads* like a document. */
function isHtmlSnippet(code: string, lang?: string): boolean {
  if (lang) return HTML_LANGS.has(lang.toLowerCase())
  return /^\s*(?:<!doctype\s+html|<html[\s>])/i.test(code)
}

function escapeHtml(s: string) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/**
 * highlight runs a tiny non-greedy token pass over `code` and returns an HTML
 * string with class-wrapped spans. Designed for the editorial, calm-monotone
 * look the project demands — colours come from `--color-syntax-*` tokens, so
 * the highlighter inherits theme + dark mode automatically. Not a full
 * tree-sitter, but for the languages the user pastes most (TS, Python, Go,
 * HTML) it gives the right "this is a keyword / string / comment" cue without
 * bringing shiki's 2 MB wasm payload onto the wire.
 */
function highlight(code: string, lang?: string): string {
  if (!lang) return escapeHtml(code)
  const tokens = LANG_TOKENS[lang.toLowerCase()]
  if (!tokens) return escapeHtml(code)
  // Replace each match with a placeholder so we can post-escape.
  type Match = { start: number; end: number; cls: string }
  const matches: Match[] = []
  for (const t of tokens) {
    t.re.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = t.re.exec(code)) !== null) {
      matches.push({ start: m.index, end: m.index + m[0].length, cls: t.cls })
      if (m.index === t.re.lastIndex) t.re.lastIndex++
    }
  }
  matches.sort((a, b) => a.start - b.start || b.end - a.end)
  // Drop overlapping later matches (first match wins per index).
  const filtered: Match[] = []
  let cursor = 0
  for (const m of matches) {
    if (m.start < cursor) continue
    filtered.push(m)
    cursor = m.end
  }
  let out = ''
  let i = 0
  for (const m of filtered) {
    out += escapeHtml(code.slice(i, m.start))
    out += `<span class="hl-${m.cls}">${escapeHtml(code.slice(m.start, m.end))}</span>`
    i = m.end
  }
  out += escapeHtml(code.slice(i))
  return out
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
  const [hovered, setHovered] = useState(false)
  const html = useMemo(() => highlight(code, lang), [code, lang])

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
