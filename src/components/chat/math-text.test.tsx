import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { MathText } from './math-text'

describe('MathText', () => {
  const renderMathText = (content: string) =>
    renderToStaticMarkup(createElement(MathText, { content }))

  it('falls back to literal text when KaTeX throws a non-parse error', () => {
    const hostile = `\\(${'{'.repeat(4_500)}x${'}'.repeat(4_500)}\\)`
    expect(() => renderMathText(hostile)).not.toThrow()
    expect(renderMathText(hostile)).toContain('x')
  })

  it('consumes structural line breaks around a block formula between text', () => {
    const html = renderMathText('Before\n\\[x^2\\]\nAfter')

    expect(html).toContain('<span>Before</span><div class="math-text-block"')
    expect(html).toContain('</div><span>After</span></div>')
  })

  it('does not leave a structural line break between consecutive block formulas', () => {
    const html = renderMathText('\\[x\\]\n\\[y\\]')

    expect(html.match(/class="math-text-block"/g)).toHaveLength(2)
    expect(html).not.toContain('<span>\n</span>')
  })

  it('preserves user-authored double line breaks around block formulas', () => {
    const html = renderMathText('Before\n\n\\[x\\]\n\nAfter')

    expect(html).toContain('<span>Before\n</span><div class="math-text-block"')
    expect(html).toContain('</div><span>\nAfter</span></div>')
  })
})
