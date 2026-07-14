import { marked } from 'marked'
import katex from 'katex'

/**
 * Lightweight markdown configuration.
 * - GFM tables. Soft-break-to-<br> is OFF by default here (standard rendering,
 *   used for assistant output) and opt-in per call via the `breaks` argument
 *   on the render helpers below — turned on for literal user input / thinking
 *   text where the author's own line breaks are meaningful.
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
  'small', 'sub', 'sup',
  'p', 'br', 'hr', 'span', 'div', 'img', 'figure', 'figcaption',
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
  img: new Set(['src', 'alt', 'title', 'width', 'height']),
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

// Visual / layout CSS properties admins may set via inline `style` in an
// announcement (the only sanitizeHtml consumer, all admin-authored). Behavioral
// or escape-prone properties are intentionally excluded: `position` (fixed
// overlays / clickjacking), `content`, `cursor`, `pointer-events`, `z-index`,
// `transform`, and any `-webkit-*`/`-moz-*` vendor prop.
const allowedStyleProps = new Set([
  'color', 'background', 'background-color',
  'font', 'font-size', 'font-weight', 'font-style', 'font-family', 'line-height',
  'letter-spacing', 'text-align', 'text-decoration', 'text-transform', 'text-shadow', 'text-indent',
  'white-space', 'word-break', 'overflow-wrap',
  'margin', 'margin-top', 'margin-right', 'margin-bottom', 'margin-left',
  'padding', 'padding-top', 'padding-right', 'padding-bottom', 'padding-left',
  'border', 'border-top', 'border-right', 'border-bottom', 'border-left',
  'border-color', 'border-width', 'border-style', 'border-radius',
  'width', 'max-width', 'min-width', 'height', 'max-height', 'min-height',
  'display', 'flex', 'flex-direction', 'flex-wrap', 'align-items', 'justify-content',
  'gap', 'row-gap', 'column-gap',
  'opacity', 'box-shadow', 'list-style', 'vertical-align',
])

// A declaration is dropped whole when its value carries a dangerous token:
// any external-resource fetch — url() / image-set() / -webkit-image-set()
// (trackers, legacy url(javascript:)), expression()/behavior/binding (old IE
// code exec), @import (external CSS), attr() (content pull), CSS comments
// (obfuscation), a backslash (CSS escape sequences like `\75rl(` that hide the
// tokens above), or angle brackets. Images go through <img>, never CSS. The
// backslash ban is the catch-all: legitimate visual/layout CSS never needs one,
// but every known escape-based bypass of the literal-token checks requires it.
function styleValueSafe(v: string): boolean {
  return !/url\(|image-set\(|expression\(|javascript:|vbscript:|@import|behavior\s*:|binding\s*:|attr\(|[<>\\]|\/\*/i.test(v)
}

// sanitizeStyle keeps only allow-listed, safe-valued declarations so admins get
// real CSS control (colors, fonts, spacing, borders, flex layout) without an
// XSS / tracking surface. Returns "" when nothing survives (caller drops the attr).
function sanitizeStyle(value: string): string {
  const out: string[] = []
  for (const decl of value.split(';')) {
    const i = decl.indexOf(':')
    if (i < 0) continue
    const prop = decl.slice(0, i).trim().toLowerCase()
    const val = decl.slice(i + 1).trim()
    if (!prop || !val || !allowedStyleProps.has(prop) || !styleValueSafe(val)) continue
    out.push(`${prop}: ${val}`)
  }
  return out.join('; ')
}

/**
 * sanitizeHtml runs in the browser. It re-parses the marked-emitted HTML in
 * a detached <template> (never connected to the live DOM), walks the tree,
 * strips disallowed tags / attributes / URLs, and serializes back.
 *
 * Performance: parseInline runs per paragraph; sanitize runs once per parsed
 * fragment. Both are linear in fragment size — fine for streaming.
 */
export function sanitizeHtml(html: string): string {
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
        if (name.startsWith('on') || name === 'srcdoc' || name === 'formaction') {
          el.removeAttribute(a.name)
          continue
        }
        // Inline style is allowed on every allowed tag but sanitized to
        // visual/layout CSS (admin announcements author real CSS this way).
        if (name === 'style') {
          const safe = sanitizeStyle(a.value)
          if (safe) el.setAttribute('style', safe)
          else el.removeAttribute(a.name)
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
  type: 'paragraph' | 'heading' | 'list' | 'ordered-list' | 'code' | 'blockquote' | 'hr' | 'table' | 'html' | 'raw' | 'math'
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

function escapeAttr(s: string): string {
  return escapeHtml(s).replace(/"/g, '&quot;')
}

/** Minimal citation descriptor used to turn inline `[n]` markers into links. */
export interface CiteRef {
  index: number
  url: string
  title: string
  domain: string
  isDoc: boolean
}

// Skip protected regions so a `[n]` inside code, a code block, or an existing
// link is never rewritten (e.g. `arr[1]` in a code span stays literal).
const PROTECTED_HTML = /<(code|pre|a)\b[^>]*>[\s\S]*?<\/\1>|<[^>]+>/gi

/**
 * linkifyCitations turns `[n]` markers in sanitized inline HTML into small
 * superscript citation references pointing at the numbered sources (§ citations).
 * Web sources become links; KB documents (no browsable URL) become a
 * non-clickable marker with the document name as a tooltip. Only `[n]` where n
 * is a valid 1-based source index is rewritten, and never inside code/links.
 */
export function linkifyCitations(html: string, cites: CiteRef[]): string {
  if (!cites || cites.length === 0) return html
  const byIndex = new Map<number, CiteRef>()
  for (const c of cites) byIndex.set(c.index, c)

  const transform = (text: string): string =>
    text.replace(/\[(\d{1,3})\]/g, (full, d) => {
      const n = Number(d)
      const c = byIndex.get(n)
      if (!c) return full
      const isWeb = !c.isDoc && /^https?:\/\//i.test(c.url)
      if (isWeb) {
        const tip = escapeAttr((c.domain ? c.domain + ' — ' : '') + c.title)
        return `<sup class="cite-marker"><a href="${escapeAttr(c.url)}" target="_blank" rel="noopener noreferrer" title="${tip}">${n}</a></sup>`
      }
      return `<sup class="cite-marker cite-doc" title="${escapeAttr(c.title)}">${n}</sup>`
    })

  let out = ''
  let last = 0
  PROTECTED_HTML.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = PROTECTED_HTML.exec(html)) !== null) {
    out += transform(html.slice(last, m.index))
    out += m[0] // keep protected/tag chunk verbatim
    last = m.index + m[0].length
  }
  out += transform(html.slice(last))
  return out
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

export function inlineMarkdownToHtml(md: string, cites?: CiteRef[], breaks = false): string {
  // Layer 1: strip raw HTML tags from markdown source before marked sees it.
  const cleaned = stripRawHtml(md)
  const { text, map } = protectMath(cleaned)
  // `breaks` turns a single "\n" into <br> (soft break). Off for assistant
  // markdown (standard rendering); on for literal user input / thinking where
  // the author's line breaks are meaningful.
  let out = marked.parseInline(text, { async: false, breaks })
  if (typeof out !== 'string') {
    // Should never happen with async:false; fall back to safe escape.
    return escapeHtml(md)
  }
  // Layer 2: allowlist-sanitize the marked output FIRST (the @@MATH@@
  // placeholders are inert text and survive), THEN splice in the KaTeX HTML.
  // KaTeX positions glyphs with inline `style` which the sanitizer strips — and
  // the math HTML is trusted (KaTeX-generated from the TeX we extracted, not
  // model-supplied HTML), so it must bypass the sanitizer to render correctly.
  out = sanitizeHtml(out)
  out = out.replace(/@@MATH(\d+)@@/g, (_, i) => map[Number(i)] ?? '')
  if (cites && cites.length) out = linkifyCitations(out, cites)
  return out
}

/**
 * Block-level variant of inlineMarkdownToHtml. Uses marked's full block parser
 * (not parseInline) so GFM tables become real `<table>` markup. Used for the
 * `table` block — paragraphs/headings stay on the inline path. Same two-layer
 * defence (strip raw HTML → sanitize) and math protection.
 */
export function blockMarkdownToHtml(md: string, cites?: CiteRef[], breaks = false): string {
  const cleaned = stripRawHtml(md)
  const { text, map } = protectMath(cleaned)
  let out = marked.parse(text, { async: false, breaks })
  if (typeof out !== 'string') {
    return escapeHtml(md)
  }
  // Sanitize first (placeholders survive as text), then splice in the trusted
  // KaTeX HTML so its inline positioning styles aren't stripped — see
  // inlineMarkdownToHtml for the rationale.
  out = sanitizeHtml(out)
  out = out.replace(/@@MATH(\d+)@@/g, (_, i) => map[Number(i)] ?? '')
  if (cites && cites.length) out = linkifyCitations(out, cites)
  return out
}

/**
 * Tokenize markdown into a flat block list our React renderer can map over.
 *
 * Display math (`$$…$$` / `\[…\]`) is extracted FIRST, before `marked` ever
 * sees it, and emitted as standalone `math` blocks rendered straight by KaTeX.
 * Otherwise marked splits multi-line display math across paragraphs (or escapes
 * `\[` → `[`), which left raw LaTeX like `[ \frac{…} ]` on screen. Inline math
 * (`$…$` / `\(…\)`) stays on the per-block path in inlineMarkdownToHtml.
 */
export function tokenizeMarkdown(md: string, breaks = false): MarkdownBlock[] {
  const blocks: MarkdownBlock[] = []
  const displayMath = /\$\$([\s\S]+?)\$\$|\\\[([\s\S]+?)\\\]/g
  let lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = displayMath.exec(md)) !== null) {
    lexInto(blocks, md.slice(lastIndex, m.index), breaks)
    const tex = (m[1] ?? m[2] ?? '').trim()
    if (tex) blocks.push({ type: 'math', content: renderTex(tex, true) })
    lastIndex = m.index + m[0].length
  }
  lexInto(blocks, md.slice(lastIndex), breaks)
  return blocks
}

// lexInto runs marked's block lexer over a text segment and appends the mapped
// blocks. Split out so tokenizeMarkdown can interleave extracted math blocks.
function lexInto(blocks: MarkdownBlock[], md: string, breaks = false): void {
  if (!md.trim()) return
  const tokens = marked.lexer(md, { gfm: true, breaks })

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
}
