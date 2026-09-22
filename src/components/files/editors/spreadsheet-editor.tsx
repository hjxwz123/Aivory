import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AllCommunityModule, ModuleRegistry, createGrid, themeQuartz, type GridApi } from 'ag-grid-community'
import { decodeXml, encodeXml, loadOoxmlArchive } from '@/lib/ooxml/archive'
import { evaluateFormula, FormulaUnsupported } from '@/lib/ooxml/formula'
import {
  columnNameOf,
  parseSheet,
  readSharedStrings,
  resolveSheets,
  writeSheetEdits,
  XLSX_MIME,
  type ParsedSheet,
  type SheetEdit,
} from '@/lib/ooxml/xlsx'
import type { DocumentEditorProps } from '@/components/files/editors/editor-types'

/**
 * Spreadsheet editor for `.xlsx`.
 *
 * Renders with AG Grid Community so the grid keeps real DOM/ARIA semantics and
 * keyboard navigation — the reason this library was chosen over a canvas grid.
 * Saving goes through the surgical OOXML writer, so everything the editor does
 * not model (styles, charts, pivot caches, other sheets) survives untouched.
 *
 * Formula evaluation is single-pass: a formula that references another formula
 * cell uses that cell's value from the file (or from earlier in this pass). A
 * full iterative calculation engine is deliberately out of scope, and cells
 * whose formulas fall outside the supported grammar are labelled rather than
 * shown with a wrong number.
 */

/** Editing is capped far above the read-only preview's 250×50. */
const MAX_EDIT_ROWS = 2_000
const MAX_EDIT_COLUMNS = 100
const EMPTY_MARKER = ''

/**
 * AG Grid v33+ ships its features as opt-in modules. Rendering works without
 * them, but an `editable` cell then silently refuses to open an editor and the
 * grid logs error #200 — cell editing is the entire point of this component, so
 * the community set is registered once at module load.
 *
 * This is registered here rather than in a global entry so the cost lands in the
 * editor's lazy chunk, which only loads when a spreadsheet is actually opened.
 */
ModuleRegistry.registerModules([AllCommunityModule])

interface SheetState {
  ref: { name: string; path: string }
  source: string
  shared: string[]
  cells: Map<string, { text: string; formula: string | null }>
  /** Refs the user actually changed. Only these are written back. */
  dirty: Set<string>
  rowCount: number
  columnCount: number
  /** Formulas the evaluator refused; surfaced in the UI instead of hidden. */
  unsupported: Set<string>
}

/** Splits "A1" into a column letter and 1-based row number. */
function refParts(ref: string): { column: string; row: number } {
  const match = /^([A-Z]+)(\d+)$/.exec(ref)
  return match ? { column: match[1]!, row: Number(match[2]!) } : { column: 'A', row: 1 }
}

function buildContext(cells: Map<string, { text: string }>) {
  return {
    valueOf: (ref: string) => {
      const cell = cells.get(ref)
      if (!cell || cell.text === EMPTY_MARKER) return null
      const parsed = Number(cell.text)
      return Number.isNaN(parsed) ? cell.text : parsed
    },
    rangeOf: (start: string, end: string) => {
      const from = refParts(start)
      const to = refParts(end)
      const values: Array<number | string | null> = []
      const fromCode = from.column.charCodeAt(0)
      const toCode = to.column.charCodeAt(0)
      for (let row = Math.min(from.row, to.row); row <= Math.max(from.row, to.row); row += 1) {
        for (let code = Math.min(fromCode, toCode); code <= Math.max(fromCode, toCode); code += 1) {
          const cell = cells.get(`${String.fromCharCode(code)}${row}`)
          if (!cell || cell.text === EMPTY_MARKER) {
            values.push(null)
            continue
          }
          const parsed = Number(cell.text)
          values.push(Number.isNaN(parsed) ? cell.text : parsed)
        }
      }
      return values
    },
  }
}

const gridTheme = themeQuartz.withParams({
  backgroundColor: 'var(--color-surface)',
  foregroundColor: 'var(--color-fg)',
  borderColor: 'var(--color-divider)',
  headerBackgroundColor: 'var(--color-bg-muted)',
  headerTextColor: 'var(--color-fg-muted)',
  oddRowBackgroundColor: 'var(--color-surface)',
  rowHoverColor: 'var(--color-bg-muted)',
  selectedRowBackgroundColor: 'var(--color-accent-soft)',
  fontFamily: 'inherit',
  fontSize: '12.5px',
  cellHorizontalPadding: 8,
})

export default function SpreadsheetEditor({ data, onChange, flushRef }: DocumentEditorProps) {
  const { t } = useTranslation('chat')
  const hostRef = useRef<HTMLDivElement>(null)
  const gridRef = useRef<GridApi | null>(null)
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange

  const [sheets, setSheets] = useState<SheetState[]>([])
  const [activeIndex, setActiveIndex] = useState(0)
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [selectedRef, setSelectedRef] = useState<string | null>(null)
  const [formulaDraft, setFormulaDraft] = useState('')
  const [dirty, setDirty] = useState(false)

  /**
   * The grid's callbacks are created once per sheet and then kept, so anything
   * they read from the render they were born in goes stale. Both the edit path
   * and the panel's flush read the current sheets through this ref — without it
   * a second edit on the same sheet would rebuild from the pre-first-edit state
   * and silently drop the first one.
   */
  const sheetsRef = useRef<SheetState[]>([])
  sheetsRef.current = sheets

  // ------------------------------------------------------------ load
  useEffect(() => {
    let cancelled = false

    void (async () => {
      try {
        const archive = await loadOoxmlArchive(data)
        const shared = readSharedStrings(
          archive.has('xl/sharedStrings.xml') ? decodeXml(await archive.read('xl/sharedStrings.xml')) : null,
        )
        const refs = resolveSheets(
          decodeXml(await archive.read('xl/workbook.xml')),
          decodeXml(await archive.read('xl/_rels/workbook.xml.rels')),
        )

        const loaded: SheetState[] = []
        for (const ref of refs) {
          if (!archive.has(ref.path)) continue
          const source = decodeXml(await archive.read(ref.path))
          const parsed: ParsedSheet = parseSheet(ref, source, shared)
          const cells = new Map<string, { text: string; formula: string | null }>()
          for (const [key, cell] of parsed.cells) {
            cells.set(key, { text: cell.text, formula: cell.formula })
          }
          loaded.push({
            ref,
            source,
            shared,
            cells,
            dirty: new Set<string>(),
            rowCount: parsed.rowCount,
            columnCount: parsed.columnCount,
            unsupported: new Set(),
          })
        }

        if (cancelled) return
        setSheets(loaded)
        setActiveIndex(0)
        setStatus(loaded.length > 0 ? 'ready' : 'error')
      } catch {
        if (!cancelled) setStatus('error')
      }
    })()

    return () => {
      cancelled = true
    }
  }, [data])

  const active = sheets[activeIndex]

  // ------------------------------------------------------- serialization
  /**
   * Write back ONLY the cells the user changed. Rewriting every cell would turn
   * each shared-string reference into an inline string and churn the whole
   * sheet part, which is exactly the "untouched content must be identical"
   * guarantee this editor is supposed to keep. Formula cells the user did not
   * touch are left alone: `writeSheetEdits` marks the workbook for full
   * recalculation, so Excel refreshes their cached values on open.
   */
  const buildBlob = async (nextSheets: SheetState[]): Promise<Blob | null> => {
    try {
      const archive = await loadOoxmlArchive(data)
      let touched = false
      for (const sheet of nextSheets) {
        if (sheet.dirty.size === 0) continue
        const edits = new Map<string, SheetEdit>()
        const cached = new Map<string, string>()
        for (const ref of sheet.dirty) {
          const cell = sheet.cells.get(ref)
          if (!cell) continue
          edits.set(ref, { value: cell.text, formula: cell.formula })
          cached.set(ref, cell.text)
        }
        if (edits.size === 0) continue
        const { xml } = writeSheetEdits(sheet.source, edits, cached)
        archive.write(sheet.ref.path, encodeXml(xml))
        touched = true
      }
      if (!touched) return null
      return await archive.build(XLSX_MIME)
    } catch {
      return null
    }
  }

  const emit = (nextSheets: SheetState[]) => {
    void (async () => {
      const blob = await buildBlob(nextSheets)
      onChangeRef.current(blob ? { bytes: blob } : null)
      if (blob) setDirty(true)
    })()
  }

  // Let the panel serialize on demand, so "save a copy" reflects edits the
  // debounced emit has not produced yet.
  useEffect(() => {
    if (!flushRef) return
    flushRef.current = () => buildBlob(sheetsRef.current)
    return () => {
      flushRef.current = null
    }
  })

  // ------------------------------------------------------------- grid
  const rowData = useMemo(() => {
    if (!active) return []
    const rows = Math.min(Math.max(active.rowCount + 20, 50), MAX_EDIT_ROWS)
    const columns = Math.min(Math.max(active.columnCount, 8), MAX_EDIT_COLUMNS)
    return Array.from({ length: rows }, (_, rowIndex) => {
      const record: Record<string, string> = { __row: String(rowIndex + 1) }
      for (let column = 0; column < columns; column += 1) {
        const ref = `${columnNameOf(column)}${rowIndex + 1}`
        record[columnNameOf(column)] = active.cells.get(ref)?.formula
          ? `=${active.cells.get(ref)!.formula}`
          : active.cells.get(ref)?.text ?? ''
      }
      return record
    })
  }, [active])

  const columnDefs = useMemo(() => {
    const columns = Math.min(Math.max(active?.columnCount ?? 8, 8), MAX_EDIT_COLUMNS)
    return [
      { field: '__row', headerName: '#', width: 56, editable: false, pinned: 'left' as const },
      ...Array.from({ length: columns }, (_, index) => {
        const name = columnNameOf(index)
        return { field: name, headerName: name, width: 120, editable: true }
      }),
    ]
  }, [active?.columnCount])

  useEffect(() => {
    const host = hostRef.current
    if (!host || status !== 'ready' || !active) return

    const applyEdit = (ref: string, rawValue: string) => {
      const current = sheetsRef.current
      const sheet = current[activeIndex]
      if (!sheet) return
      const cells = new Map(sheet.cells)
      const trimmed = rawValue.trim()
      const formula = trimmed.startsWith('=') ? trimmed.slice(1) : null
      cells.set(ref, { text: formula ? '' : rawValue, formula })
      const dirtyRefs = new Set(sheet.dirty)
      dirtyRefs.add(ref)
      const next = current.map((item, index) =>
        index === activeIndex ? { ...item, cells, dirty: dirtyRefs } : item,
      )

      // Re-evaluate every formula cell in this sheet once, so dependants of the
      // edited cell pick up the new value.
      const unsupported = new Set<string>()
      const context = buildContext(cells)
      for (const [key, cell] of cells) {
        if (!cell.formula) continue
        try {
          const value = evaluateFormula(cell.formula, context)
          cells.set(key, { ...cell, text: value === null ? '' : String(value) })
        } catch (error) {
          if (error instanceof FormulaUnsupported) unsupported.add(key)
        }
      }
      next[activeIndex] = { ...next[activeIndex]!, cells, unsupported }
      setSheets(next)
      emit(next)
    }

    const api = createGrid(host, {
      theme: gridTheme,
      columnDefs,
      rowData,
      defaultColDef: { resizable: true, sortable: false, cellDataType: 'text' },
      rowHeight: 28,
      headerHeight: 30,
      onCellValueChanged: (event) => {
        if (!event.colDef.field || event.colDef.field === '__row') return
        const row = Number((event.data as Record<string, string>).__row)
        applyEdit(`${event.colDef.field}${row}`, String(event.newValue ?? ''))
      },
      onCellFocused: (event) => {
        const field = typeof event.column === 'string' ? event.column : event.column?.getColId()
        if (!field || field === '__row' || event.rowIndex === null) return
        const row = event.rowIndex + 1
        const ref = `${field}${row}`
        setSelectedRef(ref)
        const cell = sheets[activeIndex]?.cells.get(ref)
        setFormulaDraft(cell?.formula ? `=${cell.formula}` : cell?.text ?? '')
      },
    })
    gridRef.current = api

    return () => {
      api.destroy()
      gridRef.current = null
    }
    // `rowData`/`columnDefs` are intentionally excluded: the grid is rebuilt per
    // sheet, and re-creating it on every keystroke would drop the edit state.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, activeIndex, active?.ref.path])

  // -------------------------------------------------------------- render
  if (status === 'loading') {
    return (
      <div className="grid h-full place-items-center p-6 text-sm text-[var(--color-fg-muted)]" role="status">
        {t('filePreview.sheetLoading', { defaultValue: 'Loading spreadsheet…' })}
      </div>
    )
  }

  if (status === 'error' || !active) {
    return (
      <div className="grid h-full place-items-center p-6 text-center text-sm text-[var(--color-fg-muted)]" role="alert">
        {t('filePreview.sheetFailed', { defaultValue: "This spreadsheet couldn't be opened for editing." })}
      </div>
    )
  }

  const unsupported = selectedRef ? active.unsupported.has(selectedRef) : false

  return (
    <div className="flex h-full min-h-0 flex-col bg-[var(--color-surface)]">
      {sheets.length > 1 ? (
        <div className="flex shrink-0 gap-1 overflow-x-auto border-b border-[var(--color-divider)] px-2 py-1" role="tablist">
          {sheets.map((sheet, index) => (
            <button
              key={sheet.ref.path}
              type="button"
              role="tab"
              aria-selected={index === activeIndex}
              onClick={() => setActiveIndex(index)}
              className={
                index === activeIndex
                  ? 'interactive shrink-0 rounded-[6px] bg-[var(--color-bg-muted)] px-2.5 py-1 text-[12px] font-medium text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]'
                  : 'interactive shrink-0 rounded-[6px] px-2.5 py-1 text-[12px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]'
              }
            >
              {sheet.ref.name}
            </button>
          ))}
        </div>
      ) : null}

      <div className="flex shrink-0 items-center gap-2 border-b border-[var(--color-divider)] px-2 py-1.5">
        <span className="w-16 shrink-0 truncate font-mono text-[11px] text-[var(--color-fg-subtle)]" aria-hidden>
          {selectedRef ?? '—'}
        </span>
        <label className="sr-only" htmlFor="aivory-sheet-formula">
          {t('filePreview.formulaBar', { defaultValue: 'Formula' })}
        </label>
        <input
          id="aivory-sheet-formula"
          value={formulaDraft}
          onChange={(event) => setFormulaDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' || !selectedRef) return
            event.currentTarget.blur()
          }}
          onBlur={() => {
            if (!selectedRef) return
            const sheet = sheets[activeIndex]
            const current = sheet?.cells.get(selectedRef)
            const unchanged = (current?.formula ? `=${current.formula}` : current?.text ?? '') === formulaDraft
            if (unchanged) return
            // Reuse the grid's own edit path so recalc + serialization match.
            const row = Number(/^[A-Z]+(\d+)$/.exec(selectedRef)?.[1] ?? 0)
            gridRef.current?.getRowNode(String(row - 1))?.setDataValue(
              selectedRef.replace(/\d+$/, ''),
              formulaDraft,
            )
          }}
          className="min-w-0 flex-1 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1 font-mono text-[12px] text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        />
      </div>

      {unsupported ? (
        <p className="shrink-0 bg-[var(--color-warning-soft)] px-3 py-1.5 text-[11px] text-[var(--color-fg-muted)]" role="status">
          {t('filePreview.formulaUnsupported', {
            defaultValue: 'This formula is kept exactly as written, but is outside the supported set and was not calculated.',
          })}
        </p>
      ) : null}

      <div ref={hostRef} className="min-h-0 flex-1" data-dirty={dirty || undefined} />
    </div>
  )
}
