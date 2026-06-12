export interface ParamControlDef {
  key: string
  type: 'toggle' | 'select'
  label?: string
  icon?: string
  default?: boolean | string
  options?: Array<{ value: string; label?: string; icon?: string }>
  // map exists in the schema but is consumed server-side only; we don't render it.
  map?: Record<string, unknown>
  show_if?: Record<string, unknown>
}

export function parseControls(raw: unknown): ParamControlDef[] {
  if (Array.isArray(raw)) return raw as ParamControlDef[]
  if (typeof raw === 'string') {
    try {
      const v = JSON.parse(raw)
      return Array.isArray(v) ? v : []
    } catch {
      return []
    }
  }
  return []
}

/** filterVisibleParams strips keys whose `show_if` no longer matches before
 *  sending to the backend. */
export function filterVisibleParams(controls: unknown, values: Record<string, unknown>): Record<string, unknown> {
  const defs = parseControls(controls)
  if (defs.length === 0) return {}
  const out: Record<string, unknown> = {}
  for (const c of defs) {
    if (c.show_if) {
      let ok = true
      for (const [k, v] of Object.entries(c.show_if)) {
        if (values[k] !== v) {
          ok = false
          break
        }
      }
      if (!ok) continue
    }
    if (values[c.key] !== undefined) out[c.key] = values[c.key]
  }
  return out
}
