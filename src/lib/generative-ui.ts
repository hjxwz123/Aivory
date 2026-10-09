/** Versioned, deliberately small protocol. Model output is untrusted input. */
type PrimitiveCell = string | number | boolean | null
export type UITableCell =
  | PrimitiveCell
  | { type: 'text'; text: string; description?: string }
  | { type: 'image'; url: string; alt?: string; caption?: string }
  | { type: 'link'; url: string; text: string }
  | { type: 'list'; items: string[] }
export type UITableColumn = string | {
  label: string
  align?: 'left' | 'center' | 'right'
  width?: 'compact' | 'normal' | 'wide'
}

export type UIBlock =
  | { type: 'text'; text: string }
  | { type: 'metrics'; items: { label: string; value: string | number; hint?: string }[] }
  | { type: 'chart'; kind: 'line' | 'bar'; title?: string; labels: string[]; series: { name: string; values: number[] }[] }
  | { type: 'table'; title?: string; columns: UITableColumn[]; rows: UITableCell[][] }
  | { type: 'steps'; items: { title: string; description?: string }[] }
  | { type: 'tabs' | 'accordion'; items: { title: string; blocks: UIBlock[] }[] }

export interface UIDocument { version: 1; title?: string; description?: string; blocks: UIBlock[] }
export const MAX_GENERATIVE_SOURCE = 128 * 1024

function record(v: unknown): Record<string, unknown> {
  if (!v || typeof v !== 'object' || Array.isArray(v)) throw new Error('object')
  return v as Record<string, unknown>
}
function str(v: unknown, max = 4000): string {
  if (typeof v !== 'string' || v.length > max) throw new Error('text')
  return v
}
function optional(v: unknown): string | undefined { return v === undefined ? undefined : str(v) }
function array(v: unknown, max: number): unknown[] {
  if (!Array.isArray(v) || !v.length || v.length > max) throw new Error('array')
  return v
}
function cell(v: unknown): PrimitiveCell {
  if (v === null || typeof v === 'boolean') return v
  if (typeof v === 'number' && Number.isFinite(v) && Math.abs(v) <= 1e15) return v
  return str(v)
}
function webURL(v: unknown): string {
  const url = new URL(str(v))
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) throw new Error('url')
  return url.href
}
function tableCell(v: unknown): UITableCell {
  if (v === null || typeof v !== 'object') return cell(v)
  const c = record(v)
  switch (c.type) {
    case 'text': return { type: c.type, text: str(c.text), description: optional(c.description) }
    case 'image': return { type: c.type, url: webURL(c.url), alt: optional(c.alt), caption: optional(c.caption) }
    case 'link': return { type: c.type, url: webURL(c.url), text: str(c.text) }
    case 'list': return { type: c.type, items: array(c.items, 20).map(v => str(v)) }
    default: throw new Error('cell')
  }
}
function tableColumn(v: unknown): UITableColumn {
  if (typeof v === 'string') return str(v, 160)
  const c = record(v)
  const { align, width } = c
  if (align !== undefined && align !== 'left' && align !== 'center' && align !== 'right') throw new Error('align')
  if (width !== undefined && width !== 'compact' && width !== 'normal' && width !== 'wide') throw new Error('width')
  return { label: str(c.label, 160), align, width }
}
export function parseUIDocument(source: string): UIDocument | null {
  if (source.length > MAX_GENERATIVE_SOURCE) return null
  try {
    const root = record(JSON.parse(source))
    if (root.version !== 1) return null
    let count = 0
    function blocks(v: unknown, depth = 0): UIBlock[] {
      if (depth > 4) throw new Error('depth')
      return array(v, 32).map((raw): UIBlock => {
        if (++count > 100) throw new Error('blocks')
        const b = record(raw)
        switch (b.type) {
          case 'text': return { type: b.type, text: str(b.text, 12000) }
          case 'metrics': return { type: b.type, items: array(b.items, 12).map(v => {
            const i = record(v)
            const value = cell(i.value)
            if (value === null || typeof value === 'boolean') throw new Error('value')
            return { label: str(i.label, 160), value, hint: optional(i.hint) }
          }) }
          case 'chart': {
            if (b.kind !== 'line' && b.kind !== 'bar') throw new Error('chart')
            const labels = array(b.labels, 120).map(v => str(v, 160))
            const series = array(b.series, 6).map(v => {
              const s = record(v)
              const values = array(s.values, 120).map(v => {
                if (typeof v !== 'number' || !Number.isFinite(v) || Math.abs(v) > 1e15) throw new Error('number')
                return v
              })
              if (values.length !== labels.length) throw new Error('length')
              return { name: str(s.name, 160), values }
            })
            return { type: b.type, kind: b.kind, title: optional(b.title), labels, series }
          }
          case 'table': {
            const columns = array(b.columns, 16).map(tableColumn)
            const rows = Array.isArray(b.rows) && b.rows.length <= 200 ? b.rows : null
            if (!rows) throw new Error('rows')
            return { type: b.type, title: optional(b.title), columns, rows: rows.map(v => {
              const row = array(v, 16).map(tableCell)
              if (row.length !== columns.length) throw new Error('columns')
              return row
            }) }
          }
          case 'steps': return { type: b.type, items: array(b.items, 40).map(v => {
            const i = record(v)
            return { title: str(i.title, 240), description: optional(i.description) }
          }) }
          case 'tabs': case 'accordion': return { type: b.type, items: array(b.items, 8).map(v => {
            const i = record(v)
            return { title: str(i.title, 160), blocks: blocks(i.blocks, depth + 1) }
          }) }
          default: throw new Error('type')
        }
      })
    }
    return { version: 1, title: optional(root.title), description: optional(root.description), blocks: blocks(root.blocks) }
  } catch { return null }
}

export function generativeLanguage(lang?: string): 'aivory-ui' | 'aivory-html' | null {
  const value = lang?.trim().split(/\s+/)[0].toLowerCase()
  return value === 'aivory-ui' || value === 'aivory-html' ? value : null
}
