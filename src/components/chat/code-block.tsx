import { useMemo, useState } from 'react'
import { Copy, Check } from 'lucide-react'
import { useCopy } from '@/hooks/use-clipboard'
import { cn } from '@/lib/utils'

interface CodeBlockProps {
  code: string
  lang?: string
  className?: string
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
}
LANG_TOKENS.tsx = LANG_TOKENS.ts
LANG_TOKENS.javascript = LANG_TOKENS.ts
LANG_TOKENS.js = LANG_TOKENS.ts
LANG_TOKENS.jsx = LANG_TOKENS.ts
LANG_TOKENS.py = LANG_TOKENS.python
LANG_TOKENS.golang = LANG_TOKENS.go

function escapeHtml(s: string) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/**
 * highlight runs a tiny non-greedy token pass over `code` and returns an HTML
 * string with class-wrapped spans. Designed for the editorial, calm-monotone
 * look the project demands — colours come from `--color-syntax-*` tokens, so
 * the highlighter inherits theme + dark mode automatically. Not a full
 * tree-sitter, but for the languages the user pastes most (TS, Python, Go)
 * it gives the right "this is a keyword / string / comment" cue without
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
 * Calm, sunken code block with a sticky header (language + copy). Highlighting
 * runs through the tiny in-repo tokenizer for TS/Python/Go and falls back to
 * plain monospace for everything else — keeps the look editorial and the
 * bundle small.
 */
export function CodeBlock({ code, lang, className }: CodeBlockProps) {
  const { copied, copy } = useCopy()
  const [hovered, setHovered] = useState(false)
  const html = useMemo(() => highlight(code, lang), [code, lang])

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
          aria-label={copied ? 'Copied' : 'Copy code'}
        >
          {copied ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
          <span>{copied ? 'Copied' : 'Copy'}</span>
        </button>
      </div>
      <pre className="overflow-x-auto p-4 text-[13px] leading-[1.65]">
        <code className="font-mono whitespace-pre" dangerouslySetInnerHTML={{ __html: html }} />
      </pre>
    </div>
  )
}
