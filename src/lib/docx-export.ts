/**
 * Export one assistant reply (markdown string) as a .docx file.
 *
 * Pipeline: math is extracted FIRST with the exact same delimiters as
 * lib/markdown.ts#protectMath (keep the two in sync), then the remainder is
 * tokenized by marked's lexer and walked into docx paragraphs/tables/runs.
 * LaTeX becomes a native Word equation: KaTeX renders TeX to MathML and
 * mathml2omml converts that to OMML, embedded via ImportedXmlComponent.
 *
 * Every risky step degrades instead of throwing: a formula that fails to
 * convert is emitted as monospace italic TeX, unknown tokens fall back to
 * their plain text, so the export itself never crashes on odd content.
 */
import { marked, type Token, type Tokens } from 'marked'
import katex from 'katex'
import { mml2omml } from 'mathml2omml'
import {
  AlignmentType,
  BorderStyle,
  Document,
  ExternalHyperlink,
  HeadingLevel,
  ImportedXmlComponent,
  LevelFormat,
  Packer,
  Paragraph,
  ShadingType,
  Table,
  TableCell,
  TableRow,
  TextRun,
  WidthType,
} from 'docx'

// ---------------------------------------------------------------- palette
// Word documents are theme-independent; these mirror the brand without pure
// black/white (kept local on purpose: docx colors are hex-without-# strings).
const COLOR_FG = '2A241E'
const COLOR_FG_MUTED = '6B5F53'
const COLOR_ACCENT = 'B1552F'
const COLOR_BORDER = 'D8D0C6'
const FILL_CODE_BLOCK = 'F5F2EC'
const FILL_CODE_INLINE = 'F0EBE3'
const FILL_TABLE_HEAD = 'F2EDE6'
const FONT_MONO = 'Consolas'

// ------------------------------------------------------------ math slots

interface MathSlot {
  tex: string
  display: boolean
}

const MATH_TOKEN = /@@DXMATH(\d+)@@/g

/**
 * Same four delimiter rules as lib/markdown.ts#protectMath ($$…$$, \[…\],
 * \(…\), $…$ with the currency guard) — but stashing the raw TeX instead of
 * KaTeX HTML.
 */
function extractMath(md: string): { text: string; slots: MathSlot[] } {
  const slots: MathSlot[] = []
  const stash = (tex: string, display: boolean) => {
    slots.push({ tex: tex.trim(), display })
    return `@@DXMATH${slots.length - 1}@@`
  }
  let text = md
  text = text.replace(/\$\$([\s\S]+?)\$\$/g, (_, tex: string) => stash(tex, true))
  text = text.replace(/\\\[([\s\S]+?)\\\]/g, (_, tex: string) => stash(tex, true))
  text = text.replace(/\\\(([\s\S]+?)\\\)/g, (_, tex: string) => stash(tex, false))
  text = text.replace(/\$(?!\s)([^$\n]+?)(?<!\s)\$/g, (_, tex: string) => stash(tex, false))
  return { text, slots }
}

/** TeX → OMML string (`<m:oMath …>…</m:oMath>`), or null when unconvertible. */
function texToOmml(tex: string, display: boolean): string | null {
  try {
    const html = katex.renderToString(tex, {
      output: 'mathml',
      displayMode: display,
      throwOnError: false,
      strict: false,
    })
    let mathml = /<math[\s\S]*?<\/math>/.exec(html)?.[0]
    if (!mathml) return null
    // Drop KaTeX's TeX-source annotation node: mathml2omml can't map it and
    // logs a "Type not supported" warning for every formula otherwise.
    mathml = mathml.replace(/<annotation[\s\S]*?<\/annotation>/g, '')
    const omml = mml2omml(mathml)
    if (typeof omml !== 'string' || !omml.includes('oMath')) return null
    return omml
  } catch {
    return null
  }
}

type InlineChild = TextRun | ExternalHyperlink | ImportedXmlComponent

/** One math slot → native equation, or a monospace-italic TeX fallback. */
function mathChild(slot: MathSlot): InlineChild {
  const omml = texToOmml(slot.tex, slot.display)
  if (omml) {
    try {
      return ImportedXmlComponent.fromXmlString(omml)
    } catch {
      /* fall through to text */
    }
  }
  return new TextRun({
    text: slot.display ? ` ${slot.tex} ` : slot.tex,
    italics: true,
    font: FONT_MONO,
    color: COLOR_FG,
  })
}

// ------------------------------------------------------------ inline walk

interface RunStyle {
  bold?: boolean
  italics?: boolean
  strike?: boolean
}

function textRuns(raw: string, style: RunStyle, slots: MathSlot[]): InlineChild[] {
  const out: InlineChild[] = []
  let last = 0
  MATH_TOKEN.lastIndex = 0
  for (let m = MATH_TOKEN.exec(raw); m; m = MATH_TOKEN.exec(raw)) {
    if (m.index > last) out.push(plainRun(raw.slice(last, m.index), style))
    const slot = slots[Number(m[1])]
    if (slot) out.push(mathChild(slot))
    last = m.index + m[0].length
  }
  if (last < raw.length) out.push(plainRun(raw.slice(last), style))
  return out
}

function plainRun(text: string, style: RunStyle): TextRun {
  return new TextRun({
    text,
    bold: style.bold,
    italics: style.italics,
    strike: style.strike,
    color: COLOR_FG,
  })
}

function inlineChildren(tokens: Token[] | undefined, style: RunStyle, slots: MathSlot[]): InlineChild[] {
  if (!tokens || tokens.length === 0) return []
  const out: InlineChild[] = []
  for (const tk of tokens) {
    switch (tk.type) {
      case 'text': {
        const t = tk as Tokens.Text
        if (t.tokens && t.tokens.length > 0) out.push(...inlineChildren(t.tokens, style, slots))
        else out.push(...textRuns(decodeEntities(t.text), style, slots))
        break
      }
      case 'escape':
        out.push(...textRuns((tk as Tokens.Escape).text, style, slots))
        break
      case 'strong':
        out.push(...inlineChildren((tk as Tokens.Strong).tokens, { ...style, bold: true }, slots))
        break
      case 'em':
        out.push(...inlineChildren((tk as Tokens.Em).tokens, { ...style, italics: true }, slots))
        break
      case 'del':
        out.push(...inlineChildren((tk as Tokens.Del).tokens, { ...style, strike: true }, slots))
        break
      case 'codespan':
        out.push(
          new TextRun({
            text: decodeEntities((tk as Tokens.Codespan).text),
            font: FONT_MONO,
            size: 20, // 10pt
            color: COLOR_FG,
            shading: { type: ShadingType.CLEAR, fill: FILL_CODE_INLINE },
          }),
        )
        break
      case 'link': {
        const link = tk as Tokens.Link
        const inner = inlineChildren(link.tokens, style, slots).filter(
          (c): c is TextRun => c instanceof TextRun,
        )
        out.push(
          new ExternalHyperlink({
            link: link.href,
            children: inner.length > 0 ? inner : [plainRun(link.text || link.href, style)],
          }),
        )
        break
      }
      case 'image': {
        // Keep exports network-free and failure-free: images become links.
        const img = tk as Tokens.Image
        out.push(
          new ExternalHyperlink({
            link: img.href,
            children: [plainRun(img.text || img.title || img.href, { ...style, italics: true })],
          }),
        )
        break
      }
      case 'br':
        out.push(new TextRun({ break: 1 }))
        break
      default: {
        const text = 'text' in tk && typeof tk.text === 'string' ? tk.text : ''
        if (text) out.push(...textRuns(decodeEntities(text), style, slots))
      }
    }
  }
  return out
}

/** marked leaves &amp;/&lt;/&gt;/&quot;/&#39; entities in token text. */
function decodeEntities(s: string): string {
  return s
    .replace(/&amp;/g, '&')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
}

// ------------------------------------------------------------- block walk

interface BlockCtx {
  slots: MathSlot[]
  quoteDepth: number
  /** Every top-level ordered list gets its own numbering instance so the
   *  counter restarts at 1 per list. */
  orderedRefs: string[]
}

type DocBlock = Paragraph | Table

const HEADINGS = [
  HeadingLevel.HEADING_1,
  HeadingLevel.HEADING_2,
  HeadingLevel.HEADING_3,
  HeadingLevel.HEADING_4,
  HeadingLevel.HEADING_5,
  HeadingLevel.HEADING_6,
] as const

function quoteProps(depth: number): Partial<ConstructorParameters<typeof Paragraph>[0] & object> {
  if (depth <= 0) return {}
  return {
    indent: { left: 360 * depth },
    border: {
      left: { style: BorderStyle.SINGLE, size: 18, color: COLOR_BORDER, space: 8 },
    },
  }
}

function walkBlocks(tokens: Token[], ctx: BlockCtx): DocBlock[] {
  const out: DocBlock[] = []
  for (const tk of tokens) {
    switch (tk.type) {
      case 'space':
        break
      case 'heading': {
        const h = tk as Tokens.Heading
        out.push(
          new Paragraph({
            heading: HEADINGS[Math.min(h.depth, 6) - 1],
            children: inlineChildren(h.tokens, {}, ctx.slots),
            ...quoteProps(ctx.quoteDepth),
          }),
        )
        break
      }
      case 'paragraph': {
        const p = tk as Tokens.Paragraph
        const children = inlineChildren(p.tokens, {}, ctx.slots)
        // A display formula alone in a paragraph gets centered.
        const loneMath =
          children.length === 1 && /^\s*@@DXMATH(\d+)@@\s*$/.exec(p.text ?? '')
        out.push(
          new Paragraph({
            children,
            alignment: loneMath && ctx.slots[Number(loneMath[1])]?.display ? AlignmentType.CENTER : undefined,
            spacing: { after: 160 },
            ...quoteProps(ctx.quoteDepth),
          }),
        )
        break
      }
      case 'code': {
        const c = tk as Tokens.Code
        // marked entity-escapes code text the same way it does inline text.
        const lines = decodeEntities(c.text ?? '').split('\n')
        lines.forEach((line, i) => {
          out.push(
            new Paragraph({
              children: [
                new TextRun({ text: line.length > 0 ? line : ' ', font: FONT_MONO, size: 19, color: COLOR_FG }),
              ],
              shading: { type: ShadingType.CLEAR, fill: FILL_CODE_BLOCK },
              spacing: { before: i === 0 ? 120 : 0, after: i === lines.length - 1 ? 160 : 0, line: 252 },
              ...quoteProps(ctx.quoteDepth),
            }),
          )
        })
        break
      }
      case 'blockquote': {
        const q = tk as Tokens.Blockquote
        out.push(...walkBlocks(q.tokens, { ...ctx, quoteDepth: ctx.quoteDepth + 1 }))
        break
      }
      case 'list':
        out.push(...listBlocks(tk as Tokens.List, 0, ctx))
        break
      case 'table':
        out.push(tableBlock(tk as Tokens.Table, ctx))
        break
      case 'hr':
        out.push(
          new Paragraph({
            children: [],
            spacing: { before: 160, after: 160 },
            border: { bottom: { style: BorderStyle.SINGLE, size: 6, color: COLOR_BORDER } },
          }),
        )
        break
      case 'html': {
        const text = (tk as Tokens.HTML).text.replace(/<[^>]*>/g, '').trim()
        if (text.length > 0) {
          out.push(
            new Paragraph({
              children: textRuns(decodeEntities(text), {}, ctx.slots),
              spacing: { after: 160 },
              ...quoteProps(ctx.quoteDepth),
            }),
          )
        }
        break
      }
      case 'text': {
        const t = tk as Tokens.Text
        out.push(
          new Paragraph({
            children: t.tokens
              ? inlineChildren(t.tokens, {}, ctx.slots)
              : textRuns(decodeEntities(t.text), {}, ctx.slots),
            spacing: { after: 160 },
            ...quoteProps(ctx.quoteDepth),
          }),
        )
        break
      }
      default:
        break
    }
  }
  return out
}

function listBlocks(list: Tokens.List, level: number, ctx: BlockCtx, orderedRef?: string): DocBlock[] {
  const out: DocBlock[] = []
  let ref = orderedRef
  if (list.ordered && !ref) {
    ref = `ol-${ctx.orderedRefs.length}`
    ctx.orderedRefs.push(ref)
  }
  for (const item of list.items) {
    const checkbox = item.task ? (item.checked ? '☑ ' : '☐ ') : ''
    let firstParagraphDone = false
    for (const child of item.tokens) {
      if (child.type === 'list') {
        out.push(...listBlocks(child as Tokens.List, level + 1, ctx))
        continue
      }
      if (child.type === 'text' || child.type === 'paragraph') {
        const tokens = (child as Tokens.Text | Tokens.Paragraph).tokens
        const children = [
          ...(checkbox && !firstParagraphDone ? [plainRun(checkbox, {})] : []),
          ...(tokens
            ? inlineChildren(tokens, {}, ctx.slots)
            : textRuns(decodeEntities((child as Tokens.Text).text), {}, ctx.slots)),
        ]
        out.push(
          new Paragraph({
            children,
            spacing: { after: 60 },
            ...(list.ordered && ref
              ? { numbering: { reference: ref, level: Math.min(level, 5) } }
              : { bullet: { level: Math.min(level, 5) } }),
            ...quoteProps(ctx.quoteDepth),
          }),
        )
        firstParagraphDone = true
        continue
      }
      // Nested code blocks/quotes/tables inside a list item keep their own shape.
      out.push(...walkBlocks([child], ctx))
    }
  }
  return out
}

function tableBlock(table: Tokens.Table, ctx: BlockCtx): Table {
  const alignOf = (i: number) => {
    const a = table.align?.[i]
    if (a === 'center') return AlignmentType.CENTER
    if (a === 'right') return AlignmentType.RIGHT
    return AlignmentType.LEFT
  }
  const cell = (tokens: Token[] | undefined, text: string, i: number, header: boolean) =>
    new TableCell({
      shading: header ? { type: ShadingType.CLEAR, fill: FILL_TABLE_HEAD } : undefined,
      margins: { top: 60, bottom: 60, left: 100, right: 100 },
      children: [
        new Paragraph({
          alignment: alignOf(i),
          children: tokens
            ? inlineChildren(tokens, header ? { bold: true } : {}, ctx.slots)
            : textRuns(decodeEntities(text), header ? { bold: true } : {}, ctx.slots),
        }),
      ],
    })
  const rows: TableRow[] = [
    new TableRow({
      tableHeader: true,
      children: table.header.map((c, i) => cell(c.tokens, c.text, i, true)),
    }),
    ...table.rows.map(
      (r) => new TableRow({ children: r.map((c, i) => cell(c.tokens, c.text, i, false)) }),
    ),
  ]
  return new Table({
    width: { size: 100, type: WidthType.PERCENTAGE },
    rows,
  })
}

// ------------------------------------------------------------- document

/** Build the docx Document from a markdown string (pure; also used by tests). */
export function buildDocxDocument(markdown: string): Document {
  const { text, slots } = extractMath(markdown ?? '')
  const tokens = marked.lexer(text, { gfm: true, breaks: true })
  const ctx: BlockCtx = { slots, quoteDepth: 0, orderedRefs: [] }
  let blocks = walkBlocks(tokens, ctx)
  if (blocks.length === 0) blocks = [new Paragraph({ children: [] })]

  return new Document({
    numbering: {
      config: ctx.orderedRefs.map((reference) => ({
        reference,
        levels: [0, 1, 2, 3, 4, 5].map((level) => ({
          level,
          format: LevelFormat.DECIMAL,
          text: `%${level + 1}.`,
          alignment: AlignmentType.START,
          style: { paragraph: { indent: { left: 720 + 360 * level, hanging: 360 } } },
        })),
      })),
    },
    styles: {
      default: {
        document: {
          run: { font: 'Calibri', size: 22, color: COLOR_FG },
          paragraph: { spacing: { line: 300 } },
        },
      },
      paragraphStyles: [1, 2, 3, 4, 5, 6].map((h) => ({
        id: `Heading${h}`,
        name: `heading ${h}`,
        basedOn: 'Normal',
        next: 'Normal',
        quickFormat: true,
        run: { size: [36, 30, 26, 24, 23, 22][h - 1], bold: true, color: COLOR_FG },
        paragraph: { spacing: { before: [280, 240, 200, 160, 160, 160][h - 1], after: 120 } },
      })),
      characterStyles: [
        {
          id: 'Hyperlink',
          name: 'Hyperlink',
          basedOn: 'DefaultParagraphFont',
          run: { color: COLOR_ACCENT, underline: {} },
        },
      ],
    },
    sections: [{ children: blocks }],
  })
}

function sanitizeFilename(name: string): string {
  const cleaned = name.replace(/[\\/:*?"<>| -]/g, ' ').replace(/\s+/g, ' ').trim()
  return (cleaned || 'message').slice(0, 80)
}

/** Browser entry: build, pack and trigger the download. */
export async function exportMarkdownAsDocx(markdown: string, baseName: string): Promise<void> {
  const doc = buildDocxDocument(markdown)
  const blob = await Packer.toBlob(doc)
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = `${sanitizeFilename(baseName)}.docx`
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    // Give the click a tick before revoking so the download starts reliably.
    window.setTimeout(() => URL.revokeObjectURL(url), 4000)
  }
}
