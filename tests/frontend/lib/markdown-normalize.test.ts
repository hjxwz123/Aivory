import { describe, expect, it } from 'vitest'
import { normalizeThinkingMarkdown, tokenizeMarkdown, inlineMarkdownToHtml } from '@/lib/markdown'

describe('normalizeThinkingMarkdown', () => {
  it('keeps inline **bold** inside a line intact', () => {
    const n = normalizeThinkingMarkdown('前文 **加粗** 尾')
    expect(n).toBe('前文 **加粗** 尾')
    const html = tokenizeMarkdown(n, true).map((b) =>
      b.type === 'paragraph' ? inlineMarkdownToHtml(b.content, undefined, true) : '',
    ).join('|')
    expect(html).toContain('<strong>加粗</strong>')
  })

  it('splits consecutive line-leading **bold** titles into paragraphs', () => {
    const n = normalizeThinkingMarkdown('**要点一** 摘要1\n**要点二** 摘要2\n**要点三** 摘要3')
    expect(n).toContain('\n\n')
    const htmls = tokenizeMarkdown(n, true)
      .filter((b) => b.type === 'paragraph')
      .map((b) => inlineMarkdownToHtml(b.content, undefined, true))
    expect(htmls).toHaveLength(3)
    for (const h of htmls) expect(h).toContain('<strong>')
  })

  it('keeps a two-block bold list as paragraphs (no stray ** leftovers)', () => {
    const n = normalizeThinkingMarkdown('**标题一**：摘要\n**标题二**：摘要2')
    const htmls = tokenizeMarkdown(n, true)
      .filter((b) => b.type === 'paragraph')
      .map((b) => inlineMarkdownToHtml(b.content, undefined, true))
    expect(htmls.join(' ')).not.toMatch(/\*\*/)
    for (const h of htmls) expect(h).toContain('<strong>')
  })

  it('separates a bold thought title glued directly after the prior sentence', () => {
    const n = normalizeThinkingMarkdown(
      '**Clarifying image edits**\n\nThe previous reasoning ends at the lower right.**Updating hardware visuals**\n\nThe next reasoning section starts here.',
    )

    expect(n).toContain('lower right.\n\n**Updating hardware visuals**')
    const paragraphs = tokenizeMarkdown(n, true)
      .filter((b) => b.type === 'paragraph')
      .map((b) => inlineMarkdownToHtml(b.content, undefined, true))

    expect(paragraphs).toHaveLength(4)
    expect(paragraphs[1]).not.toContain('Updating hardware visuals')
    expect(paragraphs[2]).toBe('<strong>Updating hardware visuals</strong>')
  })

  it('leaves ATX headings and blank-line text unchanged', () => {
    const n = normalizeThinkingMarkdown('### 标题一\n这是摘要\n\n### 标题二\n这是摘要2')
    expect(n).toBe('### 标题一\n这是摘要\n\n### 标题二\n这是摘要2')
  })

  it('leaves fenced code blocks untouched', () => {
    const n = normalizeThinkingMarkdown('前文\n```\n**不是标题**\n```')
    expect(n).toContain('```\n**不是标题**\n```')
  })
})

describe('copyable markdown formulae', () => {
  it('preserves inline LaTeX as safely escaped copy metadata', () => {
    const html = inlineMarkdownToHtml('Solve \\(x^2 + "quoted" & y < z\\).')

    expect(html).toContain('data-math-copy="true"')
    expect(html).toContain('data-latex="x^2 + &quot;quoted&quot; &amp; y &lt; z"')
    expect(html).toContain('data-display="false"')
    expect(html).toContain('aria-label="Copy LaTeX"')
  })

  it('keeps block LaTeX raw until the React renderer adds KaTeX', () => {
    const blocks = tokenizeMarkdown('Before\n\n$$\\sum_i x_i$$\n\nAfter')
    const math = blocks.find((block) => block.type === 'math')

    expect(math?.content).toBe('\\sum_i x_i')
    expect(math?.content).not.toContain('katex')
  })

  it('leaves math delimiters inside inline code literal', () => {
    const html = inlineMarkdownToHtml('Use `$x^2$` in the example.')

    expect(html).toContain('<code>$x^2$</code>')
    expect(html).not.toContain('data-math-copy="true"')
  })
})
