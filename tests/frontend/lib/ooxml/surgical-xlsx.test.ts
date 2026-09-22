// @vitest-environment jsdom
import JSZip from 'jszip'
import { describe, expect, it } from 'vitest'
import { loadOoxmlArchive, decodeXml, encodeXml } from '@/lib/ooxml/archive'
import {
  SPREADSHEET_NS,
  parseSheet,
  readSharedStrings,
  resolveSheets,
  writeSheetEdits,
  XLSX_MIME,
  type SheetEdit,
} from '@/lib/ooxml/xlsx'

const XML_HEAD = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
const NS = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'
const R_NS = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
const PKG_NS = 'http://schemas.openxmlformats.org/package/2006/relationships'

const SHEET_PATH = 'xl/worksheets/sheet1.xml'
/** Arbitrary bytes: proves non-XML entries survive a save bit-for-bit. */
const BINARY_ENTRY = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0x10, 0x7f, 0x42])

async function buildFixture(): Promise<ArrayBuffer> {
  const zip = new JSZip()
  zip.file(
    '[Content_Types].xml',
    `${XML_HEAD}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`,
  )
  zip.file(
    '_rels/.rels',
    `${XML_HEAD}<Relationships xmlns="${PKG_NS}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
  )
  zip.file(
    'xl/workbook.xml',
    `${XML_HEAD}<workbook xmlns="${NS}" xmlns:r="${R_NS}"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
  )
  zip.file(
    'xl/_rels/workbook.xml.rels',
    `${XML_HEAD}<Relationships xmlns="${PKG_NS}"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
  )
  // A1 carries style index 3 — it must survive an edit.
  zip.file(
    SHEET_PATH,
    `${XML_HEAD}<worksheet xmlns="${NS}"><sheetData><row r="1"><c r="A1" s="3"><v>10</v></c><c r="B1" t="s"><v>0</v></c><c r="C1"><f>SUM(A1:A3)</f><v>60</v></c></row><row r="2"><c r="A2"><v>20</v></c></row><row r="3"><c r="A3"><v>30</v></c></row></sheetData></worksheet>`,
  )
  zip.file(
    'xl/sharedStrings.xml',
    `${XML_HEAD}<sst xmlns="${NS}" count="1" uniqueCount="1"><si><t>Hello</t></si></sst>`,
  )
  zip.file('xl/styles.xml', `${XML_HEAD}<styleSheet xmlns="${NS}"/>`)
  zip.file('xl/media/image1.png', BINARY_ENTRY)
  return (await zip.generateAsync({ type: 'uint8array' })).buffer as ArrayBuffer
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
  for (let index = 0; index < a.length; index += 1) {
    if (a[index] !== b[index]) return `byte ${index}: ${a[index]} != ${b[index]}`
  }
  return null
}

async function loadBook(data: ArrayBuffer) {
  const archive = await loadOoxmlArchive(data)
  const shared = readSharedStrings(decodeXml(await archive.read('xl/sharedStrings.xml')))
  const sheets = resolveSheets(
    decodeXml(await archive.read('xl/workbook.xml')),
    decodeXml(await archive.read('xl/_rels/workbook.xml.rels')),
  )
  return { archive, shared, sheets }
}

describe('xlsx surgical editing', () => {
  it('reads sheet names, shared strings, values and formulas', async () => {
    const { shared, sheets } = await loadBook(await buildFixture())

    expect(sheets).toEqual([{ name: 'Data', path: SHEET_PATH }])
    expect(shared).toEqual(['Hello'])

    const source = decodeXml(await (await loadOoxmlArchive(await buildFixture())).read(SHEET_PATH))
    const sheet = parseSheet(sheets[0]!, source, shared)

    expect(sheet.cells.get('A1')).toEqual({ kind: 'number', text: '10', formula: null })
    expect(sheet.cells.get('B1')).toEqual({ kind: 'string', text: 'Hello', formula: null })
    expect(sheet.cells.get('C1')).toEqual({ kind: 'number', text: '60', formula: 'SUM(A1:A3)' })
    expect(sheet.rowCount).toBe(3)
    expect(sheet.columnCount).toBe(3)
  })

  it('preserves every unedited archive entry byte-for-byte', async () => {
    const original = await buildFixture()
    const { archive, shared, sheets } = await loadBook(original)

    const source = decodeXml(await archive.read(SHEET_PATH))
    const edits = new Map<string, SheetEdit>([
      ['A1', { value: '42' }],
      ['B1', { value: 'hi & <there> "quoted"' }],
      ['D5', { value: 'brand new' }],
      ['A2', { formula: 'A1*2' }],
    ])
    const cached = new Map([['A2', '84']])
    const { xml } = writeSheetEdits(source, edits, cached)
    archive.write(SHEET_PATH, encodeXml(xml))

    const before = await entriesOf(original)
    const after = await entriesOf(await (await archive.build(XLSX_MIME)).arrayBuffer())

    expect([...after.keys()].sort()).toEqual([...before.keys()].sort())

    for (const [name, bytes] of before) {
      if (name === SHEET_PATH) continue
      const difference = byteDiff(bytes, after.get(name)!)
      expect({ entry: name, difference }).toEqual({ entry: name, difference: null })
    }

    // The cell the editor touched is of course allowed to differ.
    expect(byteDiff(before.get(SHEET_PATH)!, after.get(SHEET_PATH)!)).not.toBeNull()

    // ...but the edited sheet must still describe the untouched cells as before.
    const rebuilt = parseSheet(sheets[0]!, decodeXml(after.get(SHEET_PATH)!), shared)
    expect(rebuilt.cells.get('C1')).toEqual({ kind: 'number', text: '60', formula: 'SUM(A1:A3)' })
    expect(rebuilt.cells.get('A3')).toEqual({ kind: 'number', text: '30', formula: null })
  })

  it('keeps a cell\u2019s style index and writes valid namespaced markup', async () => {
    const { archive, shared, sheets } = await loadBook(await buildFixture())
    const source = decodeXml(await archive.read(SHEET_PATH))

    const { xml } = writeSheetEdits(source, new Map([['A1', { value: '42' }]]), new Map())
    archive.write(SHEET_PATH, encodeXml(xml))
    const rebuiltXml = decodeXml((await entriesOf(await (await archive.build(XLSX_MIME)).arrayBuffer())).get(SHEET_PATH)!)

    // A null-namespace element would serialize as <v xmlns=""> and Excel rejects it.
    expect(rebuiltXml).not.toContain('xmlns=""')
    expect(rebuiltXml).toContain(`xmlns="${SPREADSHEET_NS}"`)

    const cell = parseXmlElement(rebuiltXml, 'A1')
    expect(cell.getAttribute('s')).toBe('3')

    const rebuilt = parseSheet(sheets[0]!, rebuiltXml, shared)
    expect(rebuilt.cells.get('A1')).toEqual({ kind: 'number', text: '42', formula: null })
  })

  it('writes strings as inline strings, escaping markup, without touching sharedStrings', async () => {
    const { archive, shared, sheets } = await loadBook(await buildFixture())
    const source = decodeXml(await archive.read(SHEET_PATH))

    const { xml } = writeSheetEdits(source, new Map([['E1', { value: 'a & b < c' }]]), new Map())
    archive.write(SHEET_PATH, encodeXml(xml))
    const out = await entriesOf(await (await archive.build(XLSX_MIME)).arrayBuffer())

    const rebuilt = parseSheet(sheets[0]!, decodeXml(out.get(SHEET_PATH)!), shared)
    expect(rebuilt.cells.get('E1')).toEqual({ kind: 'string', text: 'a & b < c', formula: null })
    // sharedStrings.xml is never rewritten, so its bytes are untouched.
    expect(byteDiff((await entriesOf(await buildFixture())).get('xl/sharedStrings.xml')!, out.get('xl/sharedStrings.xml')!)).toBeNull()
  })

  it('asks Excel to recalculate so an edited formula cannot show a stale result', async () => {
    const { archive } = await loadBook(await buildFixture())
    const source = decodeXml(await archive.read(SHEET_PATH))

    const unchanged = writeSheetEdits(source, new Map(), new Map())
    expect(unchanged.xml).not.toContain('fullCalcOnLoad')

    const edited = writeSheetEdits(source, new Map([['A1', { value: '1' }]]), new Map())
    expect(edited.xml).toContain('fullCalcOnLoad="1"')

    const workbookDoc = new DOMParser().parseFromString(edited.xml, 'application/xml')
    const calcPr = workbookDoc.getElementsByTagName('calcPr')[0]
    expect(calcPr?.getAttribute('fullCalcOnLoad')).toBe('1')
  })

  it('leaves untouched cells byte-identical inside the rewritten sheet', async () => {
    const { archive } = await loadBook(await buildFixture())
    const source = decodeXml(await archive.read(SHEET_PATH))

    const { xml } = writeSheetEdits(source, new Map([['A1', { value: '42' }]]), new Map())

    // Entry-level preservation is not enough: the edited SHEET is rebuilt, so
    // the surgical promise also has to hold cell by cell within it.
    expect(xml).toContain('<c r="B1" t="s"><v>0</v></c>')
    expect(xml).toContain('<c r="C1"><f>SUM(A1:A3)</f><v>60</v></c>')
    expect(xml).toContain('<c r="A2"><v>20</v></c>')
    expect(xml).toContain('<c r="A3"><v>30</v></c>')
    // Only A1's value moved, and its style attribute survived.
    expect(xml).toContain('<c r="A1" s="3"><v>42</v></c>')
  })

  it('keeps row and column order when filling empty cells', async () => {
    const { archive } = await loadBook(await buildFixture())
    const source = decodeXml(await archive.read(SHEET_PATH))

    const { xml } = writeSheetEdits(
      source,
      new Map<string, SheetEdit>([
        ['C1', { value: 'inserted' }],
        ['A1', { value: 'first' }],
        ['B1', { value: 'second' }],
      ]),
      new Map(),
    )

    const doc = new DOMParser().parseFromString(xml, 'application/xml')
    const refs = Array.from(doc.getElementsByTagName('c'), (cell) => cell.getAttribute('r'))
    const rowOne = refs.slice(0, 3)
    expect(rowOne).toEqual(['A1', 'B1', 'C1'])
    expect(Array.from(doc.getElementsByTagName('row'), (row) => row.getAttribute('r'))).toEqual([
      '1',
      '2',
      '3',
    ])
  })
})

function parseXmlElement(xml: string, cellRef: string): Element {
  const doc = new DOMParser().parseFromString(xml, 'application/xml')
  const found = Array.from(doc.getElementsByTagName('c')).find(
    (cell) => cell.getAttribute('r') === cellRef,
  )
  if (!found) throw new Error(`cell ${cellRef} not found`)
  return found
}
