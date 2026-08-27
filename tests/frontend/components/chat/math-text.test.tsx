import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { MathText } from '@/components/chat/math-text'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (_key: string, options?: { defaultValue?: string }) => options?.defaultValue ?? _key,
  }),
}))

describe('MathText', () => {
  const renderMathText = (content: string) =>
    renderToStaticMarkup(createElement(MathText, { content }))

  it('falls back to literal text when KaTeX throws a non-parse error', () => {
    const hostile = `\\(${'{'.repeat(4_500)}x${'}'.repeat(4_500)}\\)`
    expect(() => renderMathText(hostile)).not.toThrow()
    expect(renderMathText(hostile)).toContain('x')
    expect(renderMathText(hostile)).not.toContain('data-math-copy="true"')
  })

  it('keeps the original inline LaTeX on an accessible copy button', () => {
    const html = renderMathText('Before \\(x^2 + "quoted" & y < z\\) after')

    expect(html).toContain('type="button"')
    expect(html).toContain('data-math-copy="true"')
    expect(html).toContain('data-latex="x^2 + &quot;quoted&quot; &amp; y &lt; z"')
    expect(html).toContain('data-display="false"')
    expect(html).toContain('aria-label="Copy LaTeX"')
  })

  it('marks block formulae as display copy targets', () => {
    const html = renderMathText('\\[\\sum_i x_i\\]')

    expect(html).toContain('class="math-text-block"')
    expect(html).toContain('data-latex="\\sum_i x_i"')
    expect(html).toContain('data-display="true"')
  })

  it('consumes structural line breaks around a block formula between text', () => {
    const html = renderMathText('Before\n\\[x^2\\]\nAfter')

    expect(html).toContain('<span>Before</span><div class="math-text-block"')
    expect(html).toContain('</div><span>After</span>')
  })

  it('does not leave a structural line break between consecutive block formulas', () => {
    const html = renderMathText('\\[x\\]\n\\[y\\]')

    expect(html.match(/class="math-text-block"/g)).toHaveLength(2)
    expect(html).not.toContain('<span>\n</span>')
  })

  it('preserves user-authored double line breaks around block formulas', () => {
    const html = renderMathText('Before\n\n\\[x\\]\n\nAfter')

    expect(html).toContain('<span>Before\n</span><div class="math-text-block"')
    expect(html).toContain('</div><span>\nAfter</span>')
  })
})
