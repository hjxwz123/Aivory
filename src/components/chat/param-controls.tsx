/**
 * ParamControls — renders the per-model `param_controls` JSON (design.md
 * §2.3-G) as toggle / select controls above the composer.
 *
 * The schema:
 *   [{
 *     key: "thinking", type: "toggle", label: "Deep thinking", icon: "brain",
 *     default: false,
 *     map: { on: {...upstream}, off: {...upstream} },
 *     show_if: { otherKey: value }  // optional gate
 *   }, {
 *     key: "effort", type: "select", label: "Effort", icon: "gauge",
 *     default: "high",
 *     options: [{value: "low", label: "Low", icon: "..."}, ...],
 *     map: { low: {...}, high: {...} }
 *   }]
 *
 * What the user picks is captured in `values` and sent up as the `params`
 * field on the POST /api/conversations/:id/messages body — the backend then
 * deep-merges the matching map fragments into the provider request.
 *
 * Display rules:
 * - If a control is hidden by show_if, we drop the value silently so the
 *   backend doesn't apply it (a hidden toggle should never affect upstream).
 * - Default values are seeded once on mount.
 * - Both toggle and select show their icon (lucide-react) when set.
 */
import { useEffect, useMemo, useRef } from 'react'
import * as Icons from 'lucide-react'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'
import { parseControls, type ParamControlDef } from './param-controls.utils'

interface ParamControlsProps {
  controls: ParamControlDef[] | unknown
  values: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  className?: string
}

function LucideIcon({ name, size = 13 }: { name?: string; size?: number }) {
  if (!name) return null
  // Iconify name → PascalCase lucide-react export
  const key = name
    .split(/[-_]/)
    .map((p) => p.charAt(0).toUpperCase() + p.slice(1))
    .join('') as keyof typeof Icons
  const C = Icons[key] as React.ComponentType<{ size?: number; 'aria-hidden'?: boolean }> | undefined
  if (!C) return null
  return <C size={size} aria-hidden />
}

export function ParamControls({ controls, values, onChange, className }: ParamControlsProps) {
  const defs = useMemo(() => parseControls(controls), [controls])
  const seeded = useRef(false)

  // Seed defaults once per controls signature.
  useEffect(() => {
    if (seeded.current) return
    if (defs.length === 0) {
      seeded.current = true
      return
    }
    const next = { ...values }
    let changed = false
    for (const c of defs) {
      if (next[c.key] === undefined && c.default !== undefined) {
        next[c.key] = c.default
        changed = true
      }
    }
    if (changed) onChange(next)
    seeded.current = true
    // We deliberately depend on the controls signature only — re-seed when the
    // model changes (callers reset values + remount this component).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [defs])

  if (defs.length === 0) return null

  function shouldShow(c: ParamControlDef): boolean {
    if (!c.show_if) return true
    for (const [k, v] of Object.entries(c.show_if)) {
      if (values[k] !== v) return false
    }
    return true
  }

  return (
    <div className={cn('flex flex-wrap items-center gap-2', className)}>
      {defs.map((c) => {
        if (!shouldShow(c)) return null
        const label = c.label ?? c.key
        // Shared pill look, matching the composer's "research" mode button: a
        // single clickable chip; active = sage-soft, idle = muted.
        const pill = (active: boolean) =>
          cn(
            'inline-flex items-center gap-1.5 h-8 px-2.5 rounded-[8px] text-[12px] font-medium interactive',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            active
              ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
              : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
          )
        if (c.type === 'toggle') {
          // 二选一 → a single pill that toggles on click (like "research").
          const checked = Boolean(values[c.key] ?? c.default ?? false)
          return (
            <button
              key={c.key}
              type="button"
              aria-pressed={checked}
              onClick={() => onChange({ ...values, [c.key]: !checked })}
              className={pill(checked)}
              title={label}
            >
              <LucideIcon name={c.icon} />
              <span>{label}</span>
            </button>
          )
        }
        if (c.type === 'select') {
          // 多个调节 → same pill, click pops a dropdown of the options.
          const value = String(values[c.key] ?? c.default ?? c.options?.[0]?.value ?? '')
          const current = c.options?.find((o) => o.value === value)
          const active = c.default !== undefined ? value !== String(c.default) : Boolean(value)
          return (
            <Select key={c.key} value={value} onValueChange={(v) => onChange({ ...values, [c.key]: v })}>
              <SelectTrigger className={cn(pill(active), 'w-auto justify-start border-0')} aria-label={label} hideChevron>
                <LucideIcon name={current?.icon ?? c.icon} />
                <span>
                  {label}
                  {current ? <span className="opacity-70">：{current.label ?? current.value}</span> : null}
                </span>
              </SelectTrigger>
              <SelectContent>
                {c.options?.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    <span className="inline-flex items-center gap-2">
                      <LucideIcon name={o.icon} />
                      {o.label ?? o.value}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )
        }
        return null
      })}
    </div>
  )
}
