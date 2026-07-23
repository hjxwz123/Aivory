/**
 * ParamControlsEditor — a visual builder for a model's `param_controls` JSON
 * (§2.3-G), so admins don't hand-write the array. Each control is a toggle (二选一)
 * or a select (多个选项); every value maps to a fragment that's deep-merged into
 * the upstream request body. Map fragments are arbitrary provider JSON, so they
 * stay as small JSON text areas. Emits the serialized JSON string up to the
 * model-edit form; an "advanced" raw view stays in sync for power users.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { IconPicker } from '@/components/admin/icon-picker'

interface EditorOption {
  value: string
  label: string
  icon: string
  fragment: string
}
interface EditorControl {
  key: string
  type: 'toggle' | 'select'
  label: string
  icon: string
  def: string // toggle: "true"/"false"; select: an option value
  onFragment: string
  offFragment: string
  options: EditorOption[]
  showIfKey: string
  showIfValue: string
}

interface Props {
  /** The raw param_controls JSON text (source of truth in the parent form). */
  value: string
  onChange: (jsonText: string) => void
}

const pretty = (v: unknown) => JSON.stringify(v ?? {}, null, 2)
const parseFrag = (s: string): Record<string, unknown> => {
  try {
    const v = JSON.parse(s || '{}')
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
  } catch {
    return {}
  }
}
// "true"/"false" → boolean; otherwise the raw string (for show_if / defaults).
const coerce = (s: string): unknown => (s === 'true' ? true : s === 'false' ? false : s)

function parse(text: string): EditorControl[] {
  let arr: unknown
  try {
    arr = JSON.parse(text || '[]')
  } catch {
    return []
  }
  if (!Array.isArray(arr)) return []
  return arr.map((raw): EditorControl => {
    const c = (raw ?? {}) as Record<string, unknown>
    const map = (c.map ?? {}) as Record<string, unknown>
    const opts = Array.isArray(c.options) ? (c.options as Record<string, unknown>[]) : []
    const showIf = (c.show_if ?? {}) as Record<string, unknown>
    const showKey = Object.keys(showIf)[0] ?? ''
    return {
      key: String(c.key ?? ''),
      type: c.type === 'select' ? 'select' : 'toggle',
      label: String(c.label ?? ''),
      icon: String(c.icon ?? ''),
      def: c.default === undefined ? '' : String(c.default),
      onFragment: pretty(map.on),
      offFragment: pretty(map.off),
      options: opts.map((o) => ({
        value: String(o.value ?? ''),
        label: String(o.label ?? ''),
        icon: String(o.icon ?? ''),
        fragment: pretty(map[String(o.value ?? '')]),
      })),
      showIfKey: showKey,
      showIfValue: showKey ? String(showIf[showKey]) : '',
    }
  })
}

function serialize(controls: EditorControl[]): string {
  const out = controls
    .filter((c) => c.key.trim())
    .map((c) => {
      const o: Record<string, unknown> = { key: c.key.trim(), type: c.type }
      if (c.label.trim()) o.label = c.label.trim()
      if (c.icon.trim()) o.icon = c.icon.trim()
      if (c.def !== '') o.default = c.type === 'toggle' ? c.def === 'true' : c.def
      if (c.showIfKey.trim()) o.show_if = { [c.showIfKey.trim()]: coerce(c.showIfValue) }
      if (c.type === 'toggle') {
        o.map = { on: parseFrag(c.onFragment), off: parseFrag(c.offFragment) }
      } else {
        o.options = c.options.map((op) => {
          const oo: Record<string, unknown> = { value: op.value }
          if (op.label.trim()) oo.label = op.label.trim()
          if (op.icon.trim()) oo.icon = op.icon.trim()
          return oo
        })
        o.map = Object.fromEntries(c.options.map((op) => [op.value, parseFrag(op.fragment)]))
      }
      return o
    })
  return JSON.stringify(out, null, 2)
}

const blankControl = (): EditorControl => ({
  key: '', type: 'toggle', label: '', icon: '', def: 'false',
  onFragment: '{\n  \n}', offFragment: '{\n  \n}', options: [], showIfKey: '', showIfValue: '',
})

export function ParamControlsEditor({ value, onChange }: Props) {
  const { t } = useTranslation('admin')
  const [controls, setControls] = useState<EditorControl[]>(() => parse(value))
  const [showRaw, setShowRaw] = useState(false)
  // Track our own last emit so an async load / raw-edit re-parses, but our own
  // edits don't bounce back and clobber in-progress typing.
  const lastEmitted = useRef(value)

  useEffect(() => {
    if (value !== lastEmitted.current) {
      setControls(parse(value))
      lastEmitted.current = value
    }
  }, [value])

  function commit(next: EditorControl[]) {
    setControls(next)
    const json = serialize(next)
    lastEmitted.current = json
    onChange(json)
  }
  const setAt = (i: number, patch: Partial<EditorControl>) =>
    commit(controls.map((c, idx) => (idx === i ? { ...c, ...patch } : c)))

  const tt = (k: string) => t(`models.pc.${k}`)
  const inputCls = 'h-8 text-[13px]'

  return (
    <div className="flex flex-col gap-3">
      {controls.map((c, i) => (
        <div key={i} className="min-w-0 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 sm:p-3.5 flex flex-col gap-3">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <div className="inline-flex items-center gap-1 p-0.5 rounded-[9px] bg-[var(--color-bg-muted)] border border-[var(--color-border-subtle)]">
              {(['toggle', 'select'] as const).map((ty) => (
                <button
                  key={ty}
                  type="button"
                  onClick={() => setAt(i, { type: ty })}
                  className={
                    'px-2.5 h-7 rounded-[7px] text-[12px] font-medium interactive ' +
                    (c.type === ty
                      ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]'
                      : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]')
                  }
                >
                  {ty === 'toggle' ? tt('toggle') : tt('select')}
                </button>
              ))}
            </div>
            <div className="ml-auto" />
            <Button
              variant="ghost"
              size="sm"
              leadingIcon={<Trash2 size={13} aria-hidden />}
              className="text-[var(--color-danger)]"
              onClick={() => commit(controls.filter((_, idx) => idx !== i))}
            >
              {tt('removeControl')}
            </Button>
          </div>

          <div className="grid min-w-0 grid-cols-1 gap-2.5 sm:grid-cols-2">
            <LabeledInput label={tt('key')} value={c.key} onChange={(v) => setAt(i, { key: v })} placeholder="thinking" cls={inputCls} mono />
            <LabeledInput label={tt('label')} value={c.label} onChange={(v) => setAt(i, { label: v })} placeholder={t('models.pc.labelPlaceholder')} cls={inputCls} />
            <LabeledIcon label={tt('icon')} value={c.icon} onChange={(v) => setAt(i, { icon: v })} />
            <LabeledInput label={tt('default')} value={c.def} onChange={(v) => setAt(i, { def: v })} placeholder={c.type === 'toggle' ? 'true / false' : 'medium'} cls={inputCls} mono />
          </div>

          {c.type === 'toggle' ? (
            <div className="grid min-w-0 grid-cols-1 gap-2.5 sm:grid-cols-2">
              <LabeledArea label={tt('onFragment')} value={c.onFragment} onChange={(v) => setAt(i, { onFragment: v })} />
              <LabeledArea label={tt('offFragment')} value={c.offFragment} onChange={(v) => setAt(i, { offFragment: v })} />
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <span className="text-[12px] text-[var(--color-fg-subtle)]">{tt('options')}</span>
              {c.options.map((op, j) => (
                <div key={j} className="rounded-[10px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] p-2.5 flex flex-col gap-2">
                  <div className="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] lg:items-end">
                    <LabeledInput label={tt('optValue')} value={op.value} onChange={(v) => setAt(i, { options: c.options.map((x, k) => (k === j ? { ...x, value: v } : x)) })} placeholder="high" cls={inputCls} mono />
                    <LabeledInput label={tt('optLabel')} value={op.label} onChange={(v) => setAt(i, { options: c.options.map((x, k) => (k === j ? { ...x, label: v } : x)) })} placeholder={t('models.pc.labelPlaceholder')} cls={inputCls} />
                    <LabeledIcon label={tt('icon')} value={op.icon} onChange={(v) => setAt(i, { options: c.options.map((x, k) => (k === j ? { ...x, icon: v } : x)) })} />
                    <Button variant="ghost" size="sm" className="h-8 justify-self-end text-[var(--color-danger)]" onClick={() => setAt(i, { options: c.options.filter((_, k) => k !== j) })}>
                      <Trash2 size={13} aria-hidden />
                    </Button>
                  </div>
                  <LabeledArea label={tt('fragment')} value={op.fragment} onChange={(v) => setAt(i, { options: c.options.map((x, k) => (k === j ? { ...x, fragment: v } : x)) })} />
                </div>
              ))}
              <Button
                variant="secondary"
                size="sm"
                leadingIcon={<Plus size={13} aria-hidden />}
                onClick={() => setAt(i, { options: [...c.options, { value: '', label: '', icon: '', fragment: '{\n  \n}' }] })}
              >
                {tt('addOption')}
              </Button>
            </div>
          )}

          <div className="grid min-w-0 grid-cols-1 gap-2.5 sm:grid-cols-2">
            <LabeledInput label={tt('showIfKey')} value={c.showIfKey} onChange={(v) => setAt(i, { showIfKey: v })} placeholder="thinking" cls={inputCls} mono />
            <LabeledInput label={tt('showIfValue')} value={c.showIfValue} onChange={(v) => setAt(i, { showIfValue: v })} placeholder="true" cls={inputCls} mono />
          </div>
        </div>
      ))}

      <div className="flex items-center gap-2">
        <Button variant="secondary" size="sm" leadingIcon={<Plus size={14} aria-hidden />} onClick={() => commit([...controls, blankControl()])}>
          {tt('addControl')}
        </Button>
        <button type="button" className="ml-auto text-[12px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] interactive" onClick={() => setShowRaw((s) => !s)}>
          {showRaw ? tt('hideRaw') : tt('showRaw')}
        </button>
      </div>

      {showRaw ? (
        <Textarea
          rows={8}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="font-mono text-[12px]"
        />
      ) : null}
    </div>
  )
}

function LabeledInput({ label, value, onChange, placeholder, cls, mono }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string; cls?: string; mono?: boolean }) {
  return (
    <label className="flex min-w-0 flex-col gap-1">
      <span className="text-[11.5px] text-[var(--color-fg-subtle)]">{label}</span>
      <Input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} className={(cls ?? '') + (mono ? ' font-mono' : '')} />
    </label>
  )
}

function LabeledIcon({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="flex min-w-0 flex-col gap-1">
      <span className="text-[11.5px] text-[var(--color-fg-subtle)]">{label}</span>
      <IconPicker value={value} onChange={onChange} />
    </label>
  )
}

function LabeledArea({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="flex min-w-0 flex-col gap-1">
      <span className="text-[11.5px] text-[var(--color-fg-subtle)]">{label}</span>
      <Textarea rows={4} value={value} onChange={(e) => onChange(e.target.value)} className="font-mono text-[12px]" />
    </label>
  )
}
