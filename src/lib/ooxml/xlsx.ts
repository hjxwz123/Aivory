/**
 * Minimal, surgical `.xlsx` reading and writing.
 *
 * Only the parts a user actually edits are rewritten; every other archive entry
 * is passed through untouched (see archive.ts). Cells keep their original
 * attributes — most importantly `s`, the style index — so restyling, number
 * formats, conditional formatting and anything else the editor does not model
 * survive a save.
 *
 * Namespace handling is the subtle part. SpreadsheetML parts declare a DEFAULT
 * namespace, so `document.createElement('v')` produces an element in the null
 * namespace that serializes as `<v xmlns="">…</v>` — which Excel rejects.
 * Every element this module creates therefore goes through `createElementNS`.
 */
import { xmlDeclarationOf } from '@/lib/ooxml/archive'

export const SPREADSHEET_NS = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'
export const DOCUMENT_REL_NS = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
export const PACKAGE_REL_NS = 'http://schemas.openxmlformats.org/package/2006/relationships'

export const XLSX_MIME = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'

export type CellKind = 'number' | 'string' | 'boolean' | 'empty'

export interface SheetCell {
  kind: CellKind
  /** Display/raw text of the cached value (`""` for empty). */
  text: string
  /** Formula body without the leading `=`, or null. */
  formula: string | null
}

export interface SheetRef {
  name: string
  path: string
}

export interface SheetEdit {
  /** Replacement value as typed. Ignored when `formula` is set. */
  value?: string
  /** Replacement formula body without `=`. `null` clears the formula. */
  formula?: string | null
}

// ------------------------------------------------------------------ refs

export function columnIndexOf(letters: string): number {
  let index = 0
  for (const char of letters.toUpperCase()) {
    index = index * 26 + (char.charCodeAt(0) - 64)
  }
  return index - 1
}

export function columnNameOf(index: number): string {
  let value = index + 1
  let name = ''
  while (value > 0) {
    value -= 1
    name = String.fromCharCode(65 + (value % 26)) + name
    value = Math.floor(value / 26)
  }
  return name
}

export function splitCellRef(ref: string): { column: number; row: number } | null {
  const match = /^([A-Za-z]+)(\d+)$/.exec(ref.trim())
  if (!match) return null
  return { column: columnIndexOf(match[1]!), row: Number(match[2]!) - 1 }
}

// --------------------------------------------------------------- parsing

export function parseXml(source: string): Document {
  const doc = new DOMParser().parseFromString(source, 'application/xml')
  const failure = doc.getElementsByTagName('parsererror')[0]
  if (failure) throw new Error(`Invalid OOXML part: ${failure.textContent?.slice(0, 200) ?? 'parse error'}`)
  return doc
}

function firstByTag(parent: Document | Element, tag: string): Element | null {
  return parent.getElementsByTagName(tag)[0] ?? null
}

/** Shared strings are indexed by `<si>` order; every `<t>` inside one joins. */
export function readSharedStrings(source: string | null): string[] {
  if (source === null) return []
  const doc = parseXml(source)
  return Array.from(doc.getElementsByTagName('si'), (si) =>
    Array.from(si.getElementsByTagName('t'), (t) => t.textContent ?? '').join(''),
  )
}

/** Worksheet name → archive path, via workbook.xml and its relationships. */
export function resolveSheets(workbookXml: string, relsXml: string): SheetRef[] {
  const workbook = parseXml(workbookXml)
  const rels = parseXml(relsXml)

  const targets = new Map<string, string>()
  for (const rel of Array.from(rels.getElementsByTagName('Relationship'))) {
    const id = rel.getAttribute('Id')
    const target = rel.getAttribute('Target')
    if (id && target) targets.set(id, target)
  }

  const sheets: SheetRef[] = []
  for (const sheet of Array.from(workbook.getElementsByTagName('sheet'))) {
    const name = sheet.getAttribute('name') ?? `Sheet${sheets.length + 1}`
    const relId = sheet.getAttributeNS(DOCUMENT_REL_NS, 'id') ?? sheet.getAttribute('r:id')
    const target = relId ? targets.get(relId) : undefined
    if (!target) continue
    // Targets are relative to xl/ (e.g. "worksheets/sheet1.xml") but may be
    // absolute package paths ("/xl/worksheets/sheet1.xml").
    const path = target.startsWith('/') ? target.slice(1) : `xl/${target.replace(/^\.\//, '')}`
    sheets.push({ name, path: path.replace(/\/[^/]+\/\.\.\//g, '/') })
  }
  return sheets
}

function cellTextOf(cell: Element, shared: string[]): SheetCell {
  const type = cell.getAttribute('t')
  const formulaEl = firstByTag(cell, 'f')
  const formula = formulaEl?.textContent?.trim() ? formulaEl.textContent.trim().replace(/^=/, '') : null

  if (type === 'inlineStr') {
    const is = firstByTag(cell, 'is')
    const text = is ? Array.from(is.getElementsByTagName('t'), (t) => t.textContent ?? '').join('') : ''
    return { kind: 'string', text, formula }
  }

  const valueEl = firstByTag(cell, 'v')
  const raw = valueEl?.textContent ?? ''

  if (type === 's') {
    const index = Number(raw)
    return { kind: 'string', text: shared[index] ?? '', formula }
  }
  if (type === 'b') return { kind: 'boolean', text: raw === '1' ? 'TRUE' : 'FALSE', formula }
  if (type === 'str') return { kind: 'string', text: raw, formula }
  if (raw === '') return { kind: 'empty', text: '', formula }
  return { kind: 'number', text: raw, formula }
}

export interface ParsedSheet {
  ref: SheetRef
  cells: Map<string, SheetCell>
  /** Highest populated row/column (0-based), for sizing the grid. */
  rowCount: number
  columnCount: number
}

export function parseSheet(ref: SheetRef, source: string, shared: string[]): ParsedSheet {
  const doc = parseXml(source)
  const cells = new Map<string, SheetCell>()
  let rowCount = 0
  let columnCount = 0

  for (const cell of Array.from(doc.getElementsByTagName('c'))) {
    const cellRef = cell.getAttribute('r')
    if (!cellRef) continue
    const position = splitCellRef(cellRef)
    if (!position) continue
    cells.set(cellRef.toUpperCase(), cellTextOf(cell, shared))
    rowCount = Math.max(rowCount, position.row + 1)
    columnCount = Math.max(columnCount, position.column + 1)
  }

  return { ref, cells, rowCount, columnCount }
}

// --------------------------------------------------------------- writing

function typedValue(text: string): { kind: CellKind; text: string } {
  const trimmed = text.trim()
  if (trimmed === '') return { kind: 'empty', text: '' }
  if (/^(?:TRUE|FALSE)$/i.test(trimmed)) return { kind: 'boolean', text: trimmed.toUpperCase() }
  if (/^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$/.test(trimmed)) return { kind: 'number', text: trimmed }
  return { kind: 'string', text }
}

function clearChildren(element: Element): void {
  while (element.firstChild) element.removeChild(element.firstChild)
}

/**
 * Rewrite one `<c>` in place, preserving its attributes (notably the `s` style
 * index) and its position in the row.
 */
export function applyCellEdit(doc: Document, cell: Element, edit: SheetEdit, cached: string): void {
  const existingFormula = firstByTag(cell, 'f')
  const formula = edit.formula === undefined ? existingFormula?.textContent ?? null : edit.formula

  if (formula !== null && formula !== undefined && formula.trim() !== '') {
    const result = typedValue(cached)
    clearChildren(cell)
    // OOXML ordering: <f> before <v>.
    const formulaEl = doc.createElementNS(SPREADSHEET_NS, 'f')
    formulaEl.textContent = formula.trim().replace(/^=/, '')
    cell.appendChild(formulaEl)

    if (result.kind === 'empty') {
      cell.removeAttribute('t')
      return
    }
    const valueEl = doc.createElementNS(SPREADSHEET_NS, 'v')
    valueEl.textContent = result.kind === 'boolean' ? (result.text === 'TRUE' ? '1' : '0') : result.text
    cell.appendChild(valueEl)
    // A formula returning text needs t="str"; numbers keep the default (none).
    if (result.kind === 'string') cell.setAttribute('t', 'str')
    else if (result.kind === 'boolean') cell.setAttribute('t', 'b')
    else cell.removeAttribute('t')
    return
  }

  const value = typedValue(edit.value ?? cached)
  clearChildren(cell)

  if (value.kind === 'empty') {
    cell.removeAttribute('t')
    return
  }

  if (value.kind === 'string') {
    // Inline strings avoid rewriting sharedStrings.xml and every index into it.
    cell.setAttribute('t', 'inlineStr')
    const is = doc.createElementNS(SPREADSHEET_NS, 'is')
    const t = doc.createElementNS(SPREADSHEET_NS, 't')
    t.textContent = value.text
    is.appendChild(t)
    cell.appendChild(is)
    return
  }

  const valueEl = doc.createElementNS(SPREADSHEET_NS, 'v')
  if (value.kind === 'boolean') {
    cell.setAttribute('t', 'b')
    valueEl.textContent = value.text === 'TRUE' ? '1' : '0'
  } else {
    cell.removeAttribute('t')
    valueEl.textContent = value.text
  }
  cell.appendChild(valueEl)
}

/** Insert (or find) the `<row r="n">` for a 0-based row index, in order. */
function ensureRow(doc: Document, sheetData: Element, rowIndex: number): Element {
  const wanted = rowIndex + 1
  const rows = Array.from(sheetData.getElementsByTagName('row'))
  const existing = rows.find((row) => Number(row.getAttribute('r')) === wanted)
  if (existing) return existing

  const row = doc.createElementNS(SPREADSHEET_NS, 'row')
  row.setAttribute('r', String(wanted))
  const after = rows.find((candidate) => Number(candidate.getAttribute('r')) > wanted)
  sheetData.insertBefore(row, after ?? null)
  return row
}

/** Insert (or find) the `<c>` for a reference, keeping column order. */
function ensureCell(doc: Document, row: Element, ref: string, column: number): Element {
  const cells = Array.from(row.getElementsByTagName('c'))
  const existing = cells.find((cell) => cell.getAttribute('r')?.toUpperCase() === ref)
  if (existing) return existing

  const cell = doc.createElementNS(SPREADSHEET_NS, 'c')
  cell.setAttribute('r', ref)
  const after = cells.find((candidate) => {
    const position = splitCellRef(candidate.getAttribute('r') ?? '')
    return position !== null && position.column > column
  })
  row.insertBefore(cell, after ?? null)
  return cell
}

/**
 * Force Excel to recalculate on open. Formula results are cached in the file, so
 * without this an edited formula can display the previous value until something
 * else triggers a recalculation.
 */
export function markFullRecalc(doc: Document): void {
  const workbook = doc.documentElement
  if (!workbook) return
  let calcPr = firstByTag(workbook, 'calcPr')
  if (!calcPr) {
    calcPr = doc.createElementNS(SPREADSHEET_NS, 'calcPr')
    workbook.appendChild(calcPr)
  }
  calcPr.setAttribute('fullCalcOnLoad', '1')
}

export interface SheetWriteResult {
  xml: string
}

/**
 * Apply edits to one worksheet part and return its serialized XML.
 * `cached` supplies the value to store alongside a formula (the caller has
 * already evaluated it), keyed by upper-case cell reference.
 */
export function writeSheetEdits(
  source: string,
  edits: Map<string, SheetEdit>,
  cached: Map<string, string>,
): SheetWriteResult {
  const doc = parseXml(source)
  const sheetData = firstByTag(doc, 'sheetData')
  if (!sheetData) throw new Error('Worksheet has no sheetData element')

  for (const [rawRef, edit] of edits) {
    const ref = rawRef.toUpperCase()
    const position = splitCellRef(ref)
    if (!position) continue
    const row = ensureRow(doc, sheetData, position.row)
    const cell = ensureCell(doc, row, ref, position.column)
    applyCellEdit(doc, cell, edit, cached.get(ref) ?? '')
  }

  if (edits.size > 0) markFullRecalc(doc)

  // XMLSerializer emits its own declaration (and drops standalone="yes"), so
  // re-attach the source's to keep the part self-describing exactly as before.
  const serialized = new XMLSerializer()
    .serializeToString(doc)
    .replace(/^\s*<\?xml[^?]*\?>/, '')
    .replace(/^\s+/, '')
  return { xml: `${xmlDeclarationOf(source)}\n${serialized}` }
}
