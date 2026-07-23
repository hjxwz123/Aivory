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
import { SlidersHorizontal } from 'lucide-react'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from '@/components/ui/select'
import { LucideGlyph } from '@/components/ui/lucide-icon'
import { Tooltip } from '@/components/ui/tooltip'
import { resolveLucideIcon } from '@/lib/lucide-icons'
import { cn } from '@/lib/utils'
import { parseControls, type ParamControlDef } from './param-controls.utils'

interface ParamControlsProps {
  controls: ParamControlDef[] | unknown
  values: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  className?: string
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
        // Match the adjacent knowledge-base control: the toolbar stays compact
        // and the control name moves into a hover/focus tooltip.
        const iconButton = (active: boolean) =>
          cn(
            'inline-flex size-8 shrink-0 items-center justify-center rounded-[8px] interactive',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            active
              ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
              : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
          )
        if (c.type === 'toggle') {
          const checked = Boolean(values[c.key] ?? c.default ?? false)
          const ControlIcon = resolveLucideIcon(c.icon) ?? SlidersHorizontal
          return (
            <Tooltip key={c.key} content={label}>
              <button
                type="button"
                aria-label={label}
                aria-pressed={checked}
                onClick={() => onChange({ ...values, [c.key]: !checked })}
                className={iconButton(checked)}
              >
                <ControlIcon size={14} aria-hidden />
              </button>
            </Tooltip>
          )
        }
        if (c.type === 'select') {
          const value = String(values[c.key] ?? c.default ?? c.options?.[0]?.value ?? '')
          const current = c.options?.find((o) => o.value === value)
          const active = c.default !== undefined ? value !== String(c.default) : Boolean(value)
          const currentLabel = current?.label ?? current?.value
          const ControlIcon = resolveLucideIcon(current?.icon ?? c.icon) ?? SlidersHorizontal
          return (
            <Select key={c.key} value={value} onValueChange={(v) => onChange({ ...values, [c.key]: v })}>
              <Tooltip content={label}>
                <SelectTrigger
                  className={cn(iconButton(active), 'border-0 px-0')}
                  aria-label={currentLabel ? `${label}: ${currentLabel}` : label}
                  hideChevron
                >
                  <ControlIcon size={14} aria-hidden />
                </SelectTrigger>
              </Tooltip>
              <SelectContent>
                {c.options?.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    <span className="inline-flex items-center gap-2">
                      <LucideGlyph name={o.icon} size={13} aria-hidden />
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
