import { marked } from 'marked'
import katex from 'katex'

/**
 * Lightweight markdown configuration.
 * - GFM tables, breaks
 * - We keep raw HTML escaped (no dangerouslySetInnerHTML escape hatch)
 * - Custom renderer keeps code blocks as fenced blocks so our React
 *   <CodeBlock> component can detect them.
 *
 * §8.1 / project rule: model + tool output flows into dangerouslySetInnerHTML
 * via this module. marked v15 `parseInline` passes raw HTML through untouched;
 * we must therefore (a) strip incoming HTML from the markdown source AND
 * (b) sanitize the rendered HTML before it reaches React. Both layers are
 * defence in depth — either alone would block this attack class, both together
 * make the attack surface explicit.
 */
marked.setOptions({
  gfm: true,
  breaks: false,
})

/**
 * Strip raw HTML tags from the markdown source so marked can't pass them
 * through verbatim. Code fences are NOT touched (they end up in <CodeBlock>
 * which already escapes), so we only need to scrub paragraphs / headings /
 * lists / blockquotes / tables — i.e. everything that flows through
 * `inlineMarkdownToHtml`.
 *
 * Note: this runs BEFORE `protectMath` so KaTeX placeholders don't get eaten.
 */
function stripRawHtml(md: string): string {
  // Replace any `<tag ...>` / `</tag>` with their escaped form. We escape
  // rather than delete so a literal `<3` or `<your-name>` survives as text.
  return md.replace(/<[^>]+>/g, (m) => m.replace(/[<>]/g, (c) => (c === '<' ? '&lt;' : '&gt;')))
}

/**
 * Allowlist sanitizer for already-rendered HTML. We only allow the tags marked
 * itself emits (a/em/strong/code/pre/span/p/hN/ul/ol/li/table/.../katex spans).
 * Anything else is escaped. Dangerous attributes (`on*`, `style`, `srcdoc`)
 * and dangerous URL schemes (`javascript:`, `data:` outside images, `vbscript:`)
 * are stripped.
 */
const allowedTags = new Set([
  'a', 'b', 'em', 'i', 'strong', 'u', 's', 'del', 'code', 'pre', 'kbd', 'mark',
  'p', 'br', 'hr', 'span', 'div',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'ul', 'ol', 'li',
  'blockquote',
  'table', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td',
  // KaTeX
  'math', 'semantics', 'mrow', 'mi', 'mo', 'mn', 'msup', 'msub', 'mfrac',
  'msqrt', 'mtext', 'mspace', 'annotation', 'mstyle', 'msubsup',
])
const allowedAttrs: Record<string, Set<string>> = {
  a: new Set(['href', 'title']),
  span: new Set(['class', 'aria-hidden']),
  div: new Set(['class']),
  code: new Set(['class']),
  pre: new Set(['class']),
  th: new Set(['align', 'colspan', 'rowspan']),
  td: new Set(['align', 'colspan', 'rowspan']),
  // KaTeX uses class everywhere.
  math: new Set(['class', 'xmlns']),
  mrow: new Set(['class']),
  mi: new Set(['class', 'mathvariant']),
  mo: new Set(['class']),
  mn: new Set(['class']),
  mtext: new Set(['class']),
  mstyle: new Set(['class', 'displaystyle']),
  msup: new Set(['class']),
  msub: new Set(['class']),
  mfrac: new Set(['class']),
  msqrt: new Set(['class']),
  semantics: new Set(['class']),
  annotation: new Set(['encoding']),
}
const urlAttrs = new Set(['href', 'src'])

function isSafeUrl(url: string): boolean {
  const t = url.trim().toLowerCase()
  if (t.startsWith('javascript:') || t.startsWith('vbscript:') || t.startsWith('data:text/html')) return false
  return true
}

/**
 * sanitizeHtml runs in the browser. It re-parses the marked-emitted HTML in
 * a detached <template> (never connected to the live DOM), walks the tree,
 * strips disallowed tags / attributes / URLs, and serializes back.
 *
 * Performance: parseInline runs per paragraph; sanitize runs once per parsed
 * fragment. Both are linear in fragment size — fine for streaming.
 */
function sanitizeHtml(html: string): string {
  if (typeof document === 'undefined') return html
  const tpl = document.createElement('template')
  tpl.innerHTML = html
  const walk = (node: Node) => {
    if (node.nodeType === 1) {
      const el = node as HTMLElement
      const tag = el.tagName.toLowerCase()
      if (!allowedTags.has(tag)) {
        // Replace with its text contents so visible glyphs survive but the
        // tag itself disappears.
        const textNode = document.createTextNode(el.textContent ?? '')
        el.replaceWith(textNode)
        return
      }
      const allow = allowedAttrs[tag] ?? new Set<string>()
      for (const a of [...el.attributes]) {
        const name = a.name.toLowerCase()
        if (name.startsWith('on') || name === 'style' || name === 'srcdoc' || name === 'formaction') {
          el.removeAttribute(a.name)
          continue
        }
        if (!allow.has(name)) {
          el.removeAttribute(a.name)
          continue
        }
        if (urlAttrs.has(name) && !isSafeUrl(a.value)) {
          el.removeAttribute(a.name)
          continue
        }
      }
      // For external links, harden with rel="noreferrer noopener" + target="_blank".
      if (tag === 'a') {
        const href = el.getAttribute('href') ?? ''
        if (href.startsWith('http')) {
          el.setAttribute('rel', 'noreferrer noopener')
          el.setAttribute('target', '_blank')
        }
      }
      ;[...el.childNodes].forEach(walk)
    } else if (node.nodeType === 8) {
      // Comment node — strip.
      node.parentNode?.removeChild(node)
    }
  }
  ;[...tpl.content.childNodes].forEach(walk)
  // Serialize.
  const out = document.createElement('div')
  out.appendChild(tpl.content)
  return out.innerHTML
}

export interface MarkdownBlock {
  type: 'paragraph' | 'heading' | 'list' | 'ordered-list' | 'code' | 'blockquote' | 'hr' | 'table' | 'html' | 'raw'
  /** Raw inner content (markdown for paragraph; code text for code). */
  content: string
  /** For headings */
  depth?: number
  /** For code blocks */
  lang?: string
}

/**
 * Render an inline markdown string to HTML (used by paragraph blocks).
 * marked v15 types this as `string | Promise<string>` — `async: false`
 * guarantees a string in practice; we runtime-assert to avoid silent
 * `[object Promise]` injection if marked extensions change behavior.
 */
/**
 * Render a LaTeX fragment via KaTeX (§1.1 P0 — math output). Errors degrade to
 * the original delimited source rather than throwing.
 */
function renderTex(tex: string, display: boolean): string {
  try {
    return katex.renderToString(tex.trim(), { displayMode: display, throwOnError: false })
  } catch {
    return escapeHtml(display ? `$$${tex}$$` : `$${tex}$`)
  }
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/**
 * Extract `$$…$$` / `\[…\]` (display) and `$…$` / `\(…\)` (inline) math, render
 * each with KaTeX, and replace with placeholders so `marked` doesn't mangle the
 * LaTeX (underscores, backslashes). Placeholders are restored afterwards.
 * Currency like `$5 and $10` is left alone (lookbehind requires a non-space
 * before the closing `$`, and a single `$…$` span can't straddle two amounts).
 */
function protectMath(md: string): { text: string; map: string[] } {
  const map: string[] = []
  const stash = (html: string) => {
    const i = map.length
    map.push(html)
    return `@@MATH${i}@@`
  }
  let text = md
  text = text.replace(/\$\$([\s\S]+?)\$\$/g, (_, tex) => stash(renderTex(tex, true)))
  text = text.replace(/\\\[([\s\S]+?)\\\]/g, (_, tex) => stash(renderTex(tex, true)))
  text = text.replace(/\\\(([\s\S]+?)\\\)/g, (_, tex) => stash(renderTex(tex, false)))
  text = text.replace(/\$(?!\s)([^$\n]+?)(?<!\s)\$/g, (_, tex) => stash(renderTex(tex, false)))
  return { text, map }
}

export function inlineMarkdownToHtml(md: string): string {
  // Layer 1: strip raw HTML tags from markdown source before marked sees it.
  const cleaned = stripRawHtml(md)
  const { text, map } = protectMath(cleaned)
  let out = marked.parseInline(text, { async: false })
  if (typeof out !== 'string') {
    // Should never happen with async:false; fall back to safe escape.
    return escapeHtml(md)
  }
  out = out.replace(/@@MATH(\d+)@@/g, (_, i) => map[Number(i)] ?? '')
  // Layer 2: allowlist-sanitize the rendered HTML.
  return sanitizeHtml(out)
}

/**
 * Tokenize markdown into a flat block list our React renderer can map over.
 */
export function tokenizeMarkdown(md: string): MarkdownBlock[] {
  const tokens = marked.lexer(md, { gfm: true })
  const blocks: MarkdownBlock[] = []

  for (const t of tokens) {
    switch (t.type) {
      case 'heading':
        blocks.push({ type: 'heading', content: t.text, depth: t.depth })
        break
      case 'paragraph':
        blocks.push({ type: 'paragraph', content: t.text })
        break
      case 'list':
        blocks.push({
          type: t.ordered ? 'ordered-list' : 'list',
          content: t.raw,
        })
        break
      case 'code':
        blocks.push({ type: 'code', content: t.text, lang: t.lang })
        break
      case 'blockquote':
        blocks.push({ type: 'blockquote', content: t.text })
        break
      case 'hr':
        blocks.push({ type: 'hr', content: '' })
        break
      case 'table':
        blocks.push({ type: 'table', content: t.raw })
        break
      case 'space':
        break
      case 'html':
        blocks.push({ type: 'html', content: t.raw })
        break
      default:
        blocks.push({ type: 'raw', content: 'raw' in t ? (t as { raw: string }).raw : '' })
    }
  }
  return blocks
}
