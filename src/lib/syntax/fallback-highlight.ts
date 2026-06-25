function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

type Token = { re: RegExp; cls: string }

const LANG_TOKENS: Record<string, Token[]> = {
  typescript: [
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

LANG_TOKENS.ts = LANG_TOKENS.typescript
LANG_TOKENS.tsx = LANG_TOKENS.typescript
LANG_TOKENS.javascript = LANG_TOKENS.typescript
LANG_TOKENS.js = LANG_TOKENS.typescript
LANG_TOKENS.jsx = LANG_TOKENS.typescript
LANG_TOKENS.py = LANG_TOKENS.python
LANG_TOKENS.python3 = LANG_TOKENS.python
LANG_TOKENS.golang = LANG_TOKENS.go
LANG_TOKENS.htm = LANG_TOKENS.html
LANG_TOKENS.xhtml = LANG_TOKENS.html

/**
 * Lightweight streaming fallback. It intentionally covers only cheap common
 * tokens; final code blocks upgrade to Shiki when the owning message stops
 * streaming.
 */
export function fallbackHighlight(code: string, lang?: string): string {
  if (!lang) return escapeHtml(code)
  const tokens = LANG_TOKENS[lang.toLowerCase()]
  if (!tokens) return escapeHtml(code)

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

