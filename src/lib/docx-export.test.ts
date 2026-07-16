import JSZip from 'jszip'
import { Packer } from 'docx'
import { describe, expect, it } from 'vitest'

import { buildDocxDocument } from './docx-export'

async function packMarkdown(markdown: string): Promise<{ files: string[]; documentXml: string }> {
  const buffer = await Packer.toBuffer(buildDocxDocument(markdown))
  const zip = await JSZip.loadAsync(buffer)
  const documentXml = await zip.file('word/document.xml')?.async('string')

  expect(documentXml).toBeDefined()
  return { files: Object.keys(zip.files), documentXml: documentXml! }
}

function hasXmlInvalidCharacter(value: string): boolean {
  return [...value].some((character) => {
    const codePoint = character.codePointAt(0)!
    return (
      (codePoint >= 0x00 && codePoint <= 0x08) ||
      codePoint === 0x0b ||
      codePoint === 0x0c ||
      (codePoint >= 0x0e && codePoint <= 0x1f) ||
      codePoint === 0xfffe ||
      codePoint === 0xffff
    )
  })
}

describe('buildDocxDocument', () => {
  it.each([
    ['plain text', 'A normal paragraph.'],
    ['inline and display equations', 'Inline $x^2$ and display:\n\n$$\\frac{a}{b}$$'],
    ['table', '| Name | Value |\n| --- | ---: |\n| Alpha | 1 |'],
    [
      'equations in a table',
      '| Formula | Meaning |\n| --- | --- |\n| $E=mc^2$ | Energy |\n| $$\\frac{a}{b}$$ | Ratio |',
    ],
  ])('packs a complete document for %s', async (_name, markdown) => {
    const { files, documentXml } = await packMarkdown(markdown)

    expect(files).toEqual(
      expect.arrayContaining([
        '[Content_Types].xml',
        '_rels/.rels',
        'word/document.xml',
        'word/styles.xml',
      ]),
    )
    expect(documentXml).toContain('<w:document')
    expect(documentXml).toContain('<w:body>')
    expect(hasXmlInvalidCharacter(documentXml)).toBe(false)
  })

  it('emits equations as OMML directly inside paragraphs and table cells', async () => {
    const { documentXml } = await packMarkdown(
      'Before $x^2$ after.\n\n| Formula |\n| --- |\n| $\\frac{a}{b}$ |',
    )

    expect(documentXml).toContain('<m:oMath')
    expect(documentXml).toContain('<m:sSup>')
    expect(documentXml).toContain('<m:f>')
    expect(documentXml).not.toContain('<undefined>')
    expect(documentXml).not.toContain('</undefined>')
    expect(documentXml).not.toMatch(/<w:r[^>]*>[^<]*<m:oMath/)
  })

  it('removes XML 1.0-invalid characters from all exported content', async () => {
    const { documentXml } = await packMarkdown('| Cell |\n| --- |\n| bad\u0000\u000Bvalue |')

    expect(documentXml).toContain('badvalue')
  })
})
