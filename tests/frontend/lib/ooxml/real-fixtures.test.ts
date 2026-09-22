// @vitest-environment jsdom
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import JSZip from 'jszip'
import { describe, expect, it } from 'vitest'
import { decodeXml, encodeXml, loadOoxmlArchive } from '@/lib/ooxml/archive'
import {
  PPTX_MIME,
  parseSlideText,
  resolveSlides,
  writeSlideText,
} from '@/lib/ooxml/pptx'
import {
  XLSX_MIME,
  parseSheet,
  readSharedStrings,
  resolveSheets,
  writeSheetEdits,
} from '@/lib/ooxml/xlsx'

/**
 * Round-trip against files produced by the REFERENCE implementations
 * (openpyxl / python-pptx), not the hand-written XML used elsewhere.
 *
 * That matters because the synthetic fixtures encode my assumptions about the
 * format. openpyxl, for instance, writes inline strings and no sharedStrings
 * part at all — a shape the hand-written fixture never exercised.
 *
 * The edited output is written to `tests/fixtures/ooxml/out/` so the companion
 * `verify_roundtrip.py` can re-open it with openpyxl / python-pptx and confirm
 * an independent parser accepts what the surgical writer produced.
 *
 * Regenerate the inputs with: python tests/fixtures/ooxml/make_fixtures.py
 */

// `import.meta.url` is not a file: URL under the jsdom environment, so locate
// the fixtures by walking up from the working directory instead.
function findFixtures(): string {
  let dir = process.cwd()
  for (let depth = 0; depth < 6; depth += 1) {
    const candidate = join(dir, 'tests', 'fixtures', 'ooxml')
    if (existsSync(candidate)) return `${candidate}/`
    const parent = join(dir, '..')
    if (parent === dir) break
    dir = parent
  }
  throw new Error('tests/fixtures/ooxml not found — run from the repository root')
}

const FIXTURES = findFixtures()
const OUT = `${FIXTURES}out/`

function fixture(name: string): ArrayBuffer {
  const bytes = readFileSync(`${FIXTURES}${name}`)
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}

async function entriesOf(data: ArrayBuffer): Promise<Map<string, Uint8Array>> {
  const zip = await JSZip.loadAsync(data)
  const out = new Map<string, Uint8Array>()
  for (const [name, entry] of Object.entries(zip.files)) {
    if (entry.dir) continue
    out.set(name, await entry.async('uint8array'))
  }
  return out
}

function byteDiff(a: Uint8Array, b: Uint8Array): string | null {
  if (a.length !== b.length) return `length ${a.length} != ${b.length}`
  for (let i = 0; i < a.length; i += 1) if (a[i] !== b[i]) return `byte ${i}`
  return null
}

describe('round-trip against reference-library fixtures', () => {
  it('reads a real openpyxl workbook, including its inline-string cells', async () => {
    const archive = await loadOoxmlArchive(fixture('sample.xlsx'))
    const shared = readSharedStrings(
      archive.has('xl/sharedStrings.xml') ? decodeXml(await archive.read('xl/sharedStrings.xml')) : null,
    )
    const sheets = resolveSheets(
      decodeXml(await archive.read('xl/workbook.xml')),
      decodeXml(await archive.read('xl/_rels/workbook.xml.rels')),
    )

    expect(sheets.map((sheet) => sheet.name)).toEqual(['Report', 'Notes'])

    const report = parseSheet(sheets[0]!, decodeXml(await archive.read(sheets[0]!.path)), shared)
    expect(report.cells.get('A1')?.text).toBe('Quarterly Report')
    expect(report.cells.get('A3')?.text).toBe('Widgets')
    expect(report.cells.get('B3')?.text).toBe('120')
    expect(report.cells.get('C3')?.text).toBe('2400')
    expect(report.cells.get('B6')?.formula).toBe('SUM(B3:B5)')

    // The Notes sheet carries a boolean and a percentage, neither of which the
    // synthetic fixture covered.
    const notes = parseSheet(sheets[1]!, decodeXml(await archive.read(sheets[1]!.path)), shared)
    expect(notes.cells.get('B1')?.kind).toBe('boolean')
    expect(notes.cells.get('A1')?.text).toBe('Shared string alpha')
  })

  it('edits a real workbook and preserves every other part byte-for-byte', async () => {
    const original = fixture('sample.xlsx')
    const archive = await loadOoxmlArchive(original)
    const sheets = resolveSheets(
      decodeXml(await archive.read('xl/workbook.xml')),
      decodeXml(await archive.read('xl/_rels/workbook.xml.rels')),
    )
    const reportPath = sheets[0]!.path
    const source = decodeXml(await archive.read(reportPath))

    const edits = new Map([
      ['B3', { value: '999' }],
      ['C3', { value: '19980' }],
      ['A9', { value: 'Added by the editor' }],
    ])
    const cached = new Map([
      ['B3', '999'],
      ['C3', '19980'],
      ['A9', 'Added by the editor'],
    ])
    const { xml } = writeSheetEdits(source, edits, cached)
    archive.write(reportPath, encodeXml(xml))

    const out = await archive.build(XLSX_MIME)
    const before = await entriesOf(original)
    const after = await entriesOf(await out.arrayBuffer())

    for (const [name, bytes] of before) {
      if (name === reportPath) continue
      expect({ entry: name, difference: byteDiff(bytes, after.get(name)!) }).toEqual({
        entry: name,
        difference: null,
      })
    }

    mkdirSync(OUT, { recursive: true })
    writeFileSync(`${OUT}sample-edited.xlsx`, Buffer.from(await out.arrayBuffer()))
  })

  it('edits a real deck and preserves every other part byte-for-byte', async () => {
    const original = fixture('sample.pptx')
    const archive = await loadOoxmlArchive(original)
    const slides = resolveSlides(
      decodeXml(await archive.read('ppt/presentation.xml')),
      decodeXml(await archive.read('ppt/_rels/presentation.xml.rels')),
    )

    const titleIndex = parseSlideText(decodeXml(await archive.read(slides[0]!.path))).findIndex(
      (text) => text === 'Quarterly Review',
    )
    expect(titleIndex).toBeGreaterThanOrEqual(0)

    const source = decodeXml(await archive.read(slides[0]!.path))
    const { xml, changed } = writeSlideText(source, new Map([[titleIndex, 'Q4 Review — edited']]))
    expect(changed).toBe(1)
    archive.write(slides[0]!.path, encodeXml(xml))

    const out = await archive.build(PPTX_MIME)
    const before = await entriesOf(original)
    const after = await entriesOf(await out.arrayBuffer())

    for (const [name, bytes] of before) {
      if (name === slides[0]!.path) continue
      // Masters, layouts, theme, tableStyles, props: all untouched.
      expect({ entry: name, difference: byteDiff(bytes, after.get(name)!) }).toEqual({
        entry: name,
        difference: null,
      })
    }

    mkdirSync(OUT, { recursive: true })
    writeFileSync(`${OUT}sample-edited.pptx`, Buffer.from(await out.arrayBuffer()))
  })
})
