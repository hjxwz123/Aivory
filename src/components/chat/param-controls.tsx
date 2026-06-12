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
import { useTranslation } from 'react-i18next'
import * as Icons from 'lucide-react'
import { Switch } from '@/components/ui/switch'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
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
  const { t } = useTranslation('chat')
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
        if (c.type === 'toggle') {
          const checked = Boolean(values[c.key] ?? c.default ?? false)
          return (
            <label
              key={c.key}
              className={cn(
                'inline-flex items-center gap-2 rounded-[8px] px-2 h-7 text-[12px] font-medium border interactive',
                checked
                  ? 'bg-[var(--color-accent-soft)] text-[var(--color-accent)] border-[var(--color-accent)]/30'
                  : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] border-[var(--color-border)]',
              )}
              title={t('paramControls.toggleHint', { defaultValue: label })}
            >
              <LucideIcon name={c.icon} />
              <span>{label}</span>
              <Switch checked={checked} onCheckedChange={(v) => onChange({ ...values, [c.key]: v })} />
            </label>
          )
        }
        if (c.type === 'select') {
          const value = String(values[c.key] ?? c.default ?? c.options?.[0]?.value ?? '')
          const current = c.options?.find((o) => o.value === value)
          return (
            <div key={c.key} className="inline-flex items-center">
              <Select value={value} onValueChange={(v) => onChange({ ...values, [c.key]: v })}>
                <SelectTrigger
                  className={cn(
                    'h-7 px-2 rounded-[8px] text-[12px] font-medium border',
                    'bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] border-[var(--color-border)]',
                    'hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)]',
                  )}
                  aria-label={label}
                >
                  <LucideIcon name={current?.icon ?? c.icon} />
                  <SelectValue placeholder={label} />
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
            </div>
          )
        }
        return null
      })}
    </div>
  )
}
