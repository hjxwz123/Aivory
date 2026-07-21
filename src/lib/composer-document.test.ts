import type { JSONContent } from '@tiptap/core'
import { describe, expect, it } from 'vitest'
import { composerDocumentToValue, composerValueToDocument } from './composer-document'

const BLOCK_X = '\\[x^2\\]'
const BLOCK_Y = '\\[y+1\\]'
const textBlockBoundaryCases = [0, 1, 2].flatMap((beforeNewlines) => (
  [0, 1, 2].map((afterNewlines) => {
    const before = '\n'.repeat(beforeNewlines)
    const after = '\n'.repeat(afterNewlines)
    return {
      name: `${beforeNewlines} before and ${afterNewlines} after`,
      source: `before${before}${BLOCK_X}${after}after`,
    }
  })
))

function paragraph(text?: string): JSONContent {
  return {
    type: 'paragraph',
    content: text === undefined ? undefined : [{ type: 'text', text }],
  }
}

function blockMath(latex: string): JSONContent {
  return { type: 'blockMath', attrs: { latex } }
}

const canonicalDocuments: Array<{ name: string; document: JSONContent }> = [
  {
    name: 'text around a block formula',
    document: {
      type: 'doc',
      content: [paragraph('before'), blockMath('x^2'), paragraph('after')],
    },
  },
  {
    name: 'explicit text newlines around a block formula',
    document: {
      type: 'doc',
      content: [paragraph('before\n'), blockMath('x^2'), paragraph('\nafter')],
    },
  },
  {
    name: 'a leading newline before a terminal block formula',
    document: {
      type: 'doc',
      content: [paragraph('\n'), blockMath('x^2'), paragraph()],
    },
  },
  {
    name: 'a terminal block formula and its structural caret',
    document: {
      type: 'doc',
      content: [blockMath('x^2'), paragraph()],
    },
  },
  {
    name: 'a terminal block formula with one user-authored trailing newline',
    document: {
      type: 'doc',
      content: [blockMath('x^2'), paragraph(), paragraph()],
    },
  },
  {
    name: 'a terminal block formula with two user-authored trailing newlines',
    document: {
      type: 'doc',
      content: [blockMath('x^2'), paragraph(), paragraph(), paragraph()],
    },
  },
  {
    name: 'adjacent block formulas and a terminal structural caret',
    document: {
      type: 'doc',
      content: [blockMath('x^2'), blockMath('y+1'), paragraph()],
    },
  },
  {
    name: 'an empty paragraph between consecutive block formulas',
    document: {
      type: 'doc',
      content: [
        blockMath('x^2'),
        paragraph(),
        blockMath('y+1'),
        paragraph(),
      ],
    },
  },
]

describe('composer math document', () => {
  it('round-trips plain text without interpreting Markdown', () => {
    const source = '*literal* # heading\nsecond line'
    expect(composerDocumentToValue(composerValueToDocument(source))).toBe(source)
  })

  it('hydrates inline and block formulas as atomic math nodes', () => {
    const source = 'Area \\(\\pi r^2\\)\n\\[\\sum_{i=1}^{n} i\\]'
    const document = composerValueToDocument(source)

    expect(document.content?.[0]?.content?.[1]).toMatchObject({
      type: 'inlineMath',
      attrs: { latex: '\\pi r^2' },
    })
    expect(document.content?.[1]).toMatchObject({
      type: 'blockMath',
      attrs: { latex: '\\sum_{i=1}^{n} i' },
    })
    expect(composerDocumentToValue(document)).toBe(source)
  })

  it('normalizes legacy dollar formulas to unambiguous delimiters', () => {
    expect(composerDocumentToValue(composerValueToDocument('Solve $x^2=4$'))).toBe(
      'Solve \\(x^2=4\\)',
    )
  })

  it('keeps a formula-only draft non-empty', () => {
    const source = '\\(E=mc^2\\)'
    expect(composerDocumentToValue(composerValueToDocument(source))).toBe(source)
  })

  it.each(textBlockBoundaryCases)(
    'keeps $name newlines at text/block boundaries',
    ({ source }) => {
      expect(composerDocumentToValue(composerValueToDocument(source))).toBe(source)
    },
  )

  it.each([
    {
      name: 'keeps one leading newline before a block formula',
      source: `\n${BLOCK_X}`,
      expected: `\n${BLOCK_X}`,
    },
    {
      name: 'keeps two leading newlines before a block formula',
      source: `\n\n${BLOCK_X}`,
      expected: `\n\n${BLOCK_X}`,
    },
    {
      name: 'keeps one trailing newline after a block formula',
      source: `${BLOCK_X}\n`,
      expected: `${BLOCK_X}\n`,
    },
    {
      name: 'keeps two trailing newlines after a block formula',
      source: `${BLOCK_X}\n\n`,
      expected: `${BLOCK_X}\n\n`,
    },
    {
      name: 'adds a boundary between adjacent block formulas',
      source: `${BLOCK_X}${BLOCK_Y}`,
      expected: `${BLOCK_X}\n${BLOCK_Y}`,
    },
    {
      name: 'keeps one newline between adjacent block formulas',
      source: `${BLOCK_X}\n${BLOCK_Y}`,
      expected: `${BLOCK_X}\n${BLOCK_Y}`,
    },
    {
      name: 'keeps an empty line between block formulas',
      source: `${BLOCK_X}\n\n${BLOCK_Y}`,
      expected: `${BLOCK_X}\n\n${BLOCK_Y}`,
    },
    {
      name: 'normalizes CRLF around block formulas to LF',
      source: `before\r\n${BLOCK_X}\r\nafter`,
      expected: `before\n${BLOCK_X}\nafter`,
    },
    {
      name: 'normalizes lone carriage returns to LF',
      source: `before\r${BLOCK_X}\rafter`,
      expected: `before\n${BLOCK_X}\nafter`,
    },
  ])('$name', ({ source, expected }) => {
    expect(composerDocumentToValue(composerValueToDocument(source))).toBe(expected)
  })

  it('preserves empty paragraphs while omitting the structural caret after block math', () => {
    expect(composerDocumentToValue({
      type: 'doc',
      content: [
        { type: 'paragraph', content: [{ type: 'text', text: 'first' }] },
        { type: 'paragraph' },
        { type: 'paragraph', content: [{ type: 'text', text: 'third' }] },
        { type: 'paragraph' },
      ],
    })).toBe('first\n\nthird\n')

    expect(composerDocumentToValue({
      type: 'doc',
      content: [
        { type: 'blockMath', attrs: { latex: 'x^2' } },
        { type: 'paragraph' },
      ],
    })).toBe('\\[x^2\\]')
  })

  it('preserves an empty paragraph between block formulas', () => {
    expect(composerDocumentToValue({
      type: 'doc',
      content: [
        blockMath('x^2'),
        paragraph(),
        blockMath('y+1'),
        paragraph(),
      ],
    })).toBe(`${BLOCK_X}\n\n${BLOCK_Y}`)
  })

  it.each(canonicalDocuments)('round-trips the canonical document for $name', ({ document }) => {
    const value = composerDocumentToValue(document)
    expect(composerValueToDocument(value)).toEqual(document)
  })
})
