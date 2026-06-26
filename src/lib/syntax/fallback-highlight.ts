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
    { re: /\b[a-zA-Z-]+(?==)/g, cls: 'attr' },
  ],
  json: [
    { re: /\/\/[^\n]*/g, cls: 'comment' },
    { re: /\/\*[\s\S]*?\*\//g, cls: 'comment' },
    { re: /"(?:\\.|[^"\\])*"(?=\s*:)/g, cls: 'type' },
    { re: /"(?:\\.|[^"\\])*"/g, cls: 'string' },
    { re: /\b(true|false|null)\b/g, cls: 'keyword' },
    { re: /-?\b(?:0|[1-9]\d*)(?:\.\d+)?(?:e[+-]?\d+)?\b/gi, cls: 'number' },
  ],
  bash: [
    { re: /^[ \t]*#[^\n]*/gm, cls: 'comment' },
    { re: /(["'])(?:\\.|(?!\1).)*\1/g, cls: 'string' },
    { re: /\$\{?[A-Za-z_][\w]*\}?|\$[@*#?$!-]/g, cls: 'variable' },
    { re: /\b(if|then|else|elif|fi|for|while|until|do|done|case|esac|function|select|in|time|coproc|return|exit|export|local|readonly|declare|source|alias|unalias|trap|shift|break|continue|eval|exec|test)\b/g, cls: 'keyword' },
    { re: /(^|[;&|]\s*)[A-Za-z_][\w.-]*(?=\s|$)/gm, cls: 'fn' },
  ],
  css: [
    { re: /\/\*[\s\S]*?\*\//g, cls: 'comment' },
    { re: /(["'])(?:\\.|(?!\1).)*\1/g, cls: 'string' },
    { re: /@[a-zA-Z-]+/g, cls: 'keyword' },
    { re: /--[A-Za-z_][\w-]*(?=\s*:)/g, cls: 'variable' },
    { re: /\b-?[A-Za-z_][\w-]*(?=\s*:)/g, cls: 'attr' },
    { re: /#[\da-fA-F]{3,8}\b/g, cls: 'number' },
    { re: /\b\d+(?:\.\d+)?(?:%|[a-zA-Z]+)?\b/g, cls: 'number' },
    { re: /\b[A-Za-z_][\w-]*(?=\()/g, cls: 'fn' },
  ],
  markdown: [
    { re: /```[\s\S]*?```/g, cls: 'string' },
    { re: /^#{1,6}\s.+$/gm, cls: 'keyword' },
    { re: /^>\s.*$/gm, cls: 'comment' },
    { re: /`[^`\n]+`/g, cls: 'string' },
    { re: /\[[^\]\n]+\]\([^)]+\)/g, cls: 'fn' },
    { re: /(?:\*\*|__)[\s\S]+?(?:\*\*|__)/g, cls: 'keyword' },
    { re: /^[ \t]*(?:[-*+]|\d+\.)\s/gm, cls: 'keyword' },
  ],
  yaml: [
    { re: /#[^\n]*/g, cls: 'comment' },
    { re: /(["'])(?:\\.|(?!\1).)*\1/g, cls: 'string' },
    { re: /\b[A-Za-z_][\w.-]*(?=\s*:)/g, cls: 'attr' },
    { re: /\b(true|false|null|yes|no|on|off)\b/gi, cls: 'keyword' },
    { re: /-?\b\d+(?:\.\d+)?\b/g, cls: 'number' },
  ],
  sql: [
    { re: /--[^\n]*/g, cls: 'comment' },
    { re: /\/\*[\s\S]*?\*\//g, cls: 'comment' },
    { re: /'(?:''|[^'])*'/g, cls: 'string' },
    { re: /\b(select|from|where|join|inner|left|right|full|outer|on|group|by|order|having|limit|offset|insert|into|values|update|set|delete|create|alter|drop|table|view|index|primary|foreign|key|references|constraint|with|as|distinct|union|all|case|when|then|else|end|and|or|not|null|is|in|exists|between|like|returning)\b/gi, cls: 'keyword' },
    { re: /\b(count|sum|avg|min|max|coalesce|nullif|cast|date_trunc|lower|upper|json_extract|jsonb_build_object)(?=\s*\()/gi, cls: 'fn' },
    { re: /-?\b\d+(?:\.\d+)?\b/g, cls: 'number' },
  ],
  diff: [
    { re: /^diff --git .+$/gm, cls: 'keyword' },
    { re: /^@@[\s\S]*?@@.*$/gm, cls: 'type' },
    { re: /^\+.*$/gm, cls: 'inserted' },
    { re: /^-.*$/gm, cls: 'deleted' },
  ],
  dockerfile: [
    { re: /^[ \t]*#[^\n]*/gm, cls: 'comment' },
    { re: /(["'])(?:\\.|(?!\1).)*\1/g, cls: 'string' },
    { re: /^\s*(FROM|RUN|CMD|LABEL|MAINTAINER|EXPOSE|ENV|ADD|COPY|ENTRYPOINT|VOLUME|USER|WORKDIR|ARG|ONBUILD|STOPSIGNAL|HEALTHCHECK|SHELL)\b/gim, cls: 'keyword' },
    { re: /\$\{?[A-Za-z_][\w]*\}?/g, cls: 'variable' },
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
LANG_TOKENS.xml = LANG_TOKENS.html
LANG_TOKENS.jsonc = LANG_TOKENS.json
LANG_TOKENS.json5 = LANG_TOKENS.json
LANG_TOKENS.jsonl = LANG_TOKENS.json
LANG_TOKENS.shell = LANG_TOKENS.bash
LANG_TOKENS.shellscript = LANG_TOKENS.bash
LANG_TOKENS.sh = LANG_TOKENS.bash
LANG_TOKENS.zsh = LANG_TOKENS.bash
LANG_TOKENS.scss = LANG_TOKENS.css
LANG_TOKENS.sass = LANG_TOKENS.css
LANG_TOKENS.md = LANG_TOKENS.markdown
LANG_TOKENS.yml = LANG_TOKENS.yaml
LANG_TOKENS.docker = LANG_TOKENS.dockerfile

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
