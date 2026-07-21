import { describe, expect, it } from 'vitest'
import { hasMathContent, mathContentToPlainText, splitMathContent } from './math-content'

describe('splitMathContent', () => {
  it('splits canonical inline and block math while preserving the source', () => {
    const source = 'Area is \\(\\pi r^2\\).\n\\[\\sum_{i=1}^n i\\]'
    const segments = splitMathContent(source)

    expect(segments.map((segment) => segment.type)).toEqual([
      'text',
      'inline-math',
      'text',
      'block-math',
    ])
    expect(segments.map((segment) => segment.raw).join('')).toBe(source)
    expect(segments[1]?.value).toBe('\\pi r^2')
  })

  it('supports legacy dollar math without treating currency as math', () => {
    const source = 'The price is $100 and tax is $20, while $x^2$ is math.'
    const segments = splitMathContent(source)

    expect(segments.filter((segment) => segment.type !== 'text')).toEqual([
      { type: 'inline-math', value: 'x^2', raw: '$x^2$' },
    ])
  })

  it('keeps shell variables and embedded dollar pairs literal', () => {
    for (const source of [
      'Use $PATH:$HOME to configure the command.',
      'Run echo $HOME/$USER now.',
      'Keep prefix$x$ and $y$suffix literal.',
    ]) {
      expect(splitMathContent(source)).toEqual([{ type: 'text', value: source, raw: source }])
      expect(hasMathContent(source)).toBe(false)
    }
  })

  it('keeps formulas inside inline and fenced code literal', () => {
    const source = '`\\(x\\)`\n```txt\n\\[y\\]\n```\n~~~txt\n\\(w\\)\n~~~\n    \\(q\\)\nOutside \\(z\\)'
    const segments = splitMathContent(source)

    expect(segments.filter((segment) => segment.type !== 'text')).toEqual([
      { type: 'inline-math', value: 'z', raw: '\\(z\\)' },
    ])
  })

  it('handles deeply repeated unmatched openers without recursively rescanning them', () => {
    const source = `literal ${'\\('.repeat(6_000)}`
    expect(splitMathContent(source)).toEqual([{ type: 'text', value: source, raw: source }])
  })

  it('leaves unmatched or empty delimiters as text', () => {
    const source = 'literal \\( and \\(  \\)'
    expect(splitMathContent(source)).toEqual([{ type: 'text', value: source, raw: source }])
    expect(hasMathContent(source)).toBe(false)
  })

  it('produces readable plain text for titles and previews', () => {
    expect(mathContentToPlainText('Solve \\(x^2=4\\) now')).toBe('Solve x^2=4 now')
  })
})
