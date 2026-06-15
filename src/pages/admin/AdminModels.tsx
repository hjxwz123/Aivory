/**
 * AdminModels — list, quick-create, and entry to per-model settings.
 *
 * The list is shallow on purpose: the New-model dialog asks for only the
 * fields needed to register a row (channel, kind, label, request_id, icon,
 * description). Behaviour, system prompt, param_controls and pricing live on
 * the per-model settings page (/admin/models/:id) — reachable via the gear
 * icon on each row. This avoids a 15-field overflow modal on small screens
 * and matches the editorial-feel "one job per surface" rule.
 */
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Settings as SettingsIcon, Trash2, ArrowUp, ArrowDown, GripVertical } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiChannel, ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Tooltip } from '@/components/ui/tooltip'
import { IconUploader } from '@/components/admin/icon-uploader'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'

const KINDS = ['chat', 'image', 'embedding'] as const

type CreateDraft = {
  channel_id: string
  kind: ApiModel['kind']
  label: string
  request_id: string
  icon: string
  description: string
}

const emptyCreate: CreateDraft = {
  channel_id: '',
  kind: 'chat',
  label: '',
  request_id: '',
  icon: '',
  description: '',
}

export default function AdminModels() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const [channels, setChannels] = useState<ApiChannel[]>([])
  const [models, setModels] = useState<ApiModel[]>([])
  const [loading, setLoading] = useState(true)
  const [creator, setCreator] = useState<{ open: boolean; draft: CreateDraft }>({
    open: false,
    draft: emptyCreate,
  })
  const [submitting, setSubmitting] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState<ApiModel | null>(null)

  async function load() {
    setLoading(true)
    try {
      const [c, m] = await Promise.all([adminApi.channels(), adminApi.models()])
      setChannels(c)
      setModels(m)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function openNew() {
    setCreator({
      open: true,
      draft: { ...emptyCreate, channel_id: channels[0]?.id ?? '' },
    })
  }

  async function submitCreate() {
    const d = creator.draft
    if (!d.channel_id || !d.label.trim() || !d.request_id.trim()) {
      toast.error(t('admin:models.errors.missingFields'))
      return
    }
    setSubmitting(true)
    try {
      // Sensible defaults so the row is immediately usable; user fine-tunes on
      // the settings page. param_controls stays empty list — the editor
      // accepts JSON text and parses on save.
      const created = await adminApi.createModel({
        channel_id: d.channel_id,
        kind: d.kind,
        label: d.label.trim(),
        request_id: d.request_id.trim(),
        icon: d.icon.trim(),
        description: d.description.trim(),
        enabled: true,
        tool_mode: 'native',
        vision: true,
        stream: true,
        param_controls: [],
        currency: 'USD',
      })
      toast.success(t('admin:models.created'))
      setCreator({ open: false, draft: emptyCreate })
      await load()
      // Take the user straight to the full settings page so the next action
      // (pricing, system prompt, tool mode) is one click away.
      navigate(`/admin/models/${encodeURIComponent(created.id)}`)
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSubmitting(false)
    }
  }

  async function remove(row: ApiModel) {
    try {
      await adminApi.removeModel(row.id)
      toast.success(t('admin:models.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  // Reordering is optimistic: the list updates instantly (no refetch / loading
  // flash) and the new order is persisted in one PATCH. On failure we revert.
  const [dragIdx, setDragIdx] = useState<number | null>(null)
  const dragStart = useRef<ApiModel[] | null>(null)

  function persistOrder(next: ApiModel[], prev: ApiModel[]) {
    void adminApi.reorderModels(next.map((m) => m.id)).catch((e) => {
      setModels(prev)
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  // Quick show/hide: flip `enabled` inline (optimistic + revert on error).
  async function toggleEnabled(m: ApiModel) {
    const next = !m.enabled
    setModels((list) => list.map((x) => (x.id === m.id ? { ...x, enabled: next } : x)))
    try {
      await adminApi.updateModel(m.id, { enabled: next })
    } catch (e) {
      setModels((list) => list.map((x) => (x.id === m.id ? { ...x, enabled: m.enabled } : x)))
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  function moveBy(idx: number, dir: -1 | 1) {
    const to = idx + dir
    if (to < 0 || to >= models.length) return
    const prev = models
    const next = [...models]
    const [item] = next.splice(idx, 1)
    next.splice(to, 0, item)
    setModels(next)
    persistOrder(next, prev)
  }

  function onDragStart(idx: number) {
    dragStart.current = models
    setDragIdx(idx)
  }
  function onDragOverRow(idx: number) {
    if (dragIdx === null || dragIdx === idx) return
    const next = [...models]
    const [item] = next.splice(dragIdx, 1)
    next.splice(idx, 0, item)
    setModels(next)
    setDragIdx(idx)
  }
  function onDragEnd() {
    const prev = dragStart.current
    dragStart.current = null
    setDragIdx(null)
    if (prev && prev.some((m, i) => m.id !== models[i]?.id)) {
      persistOrder(models, prev)
    }
  }

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:models.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:models.lead')}</p>
        </div>
        <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
          {t('admin:models.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : models.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:models.empty')}
          </div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {models.map((m, idx) => {
              const ch = channels.find((c) => c.id === m.channel_id)
              return (
                <li
                  key={m.id}
                  onDragOver={(e) => {
                    if (dragIdx !== null) {
                      e.preventDefault()
                      onDragOverRow(idx)
                    }
                  }}
                  className={cn(
                    'grid grid-cols-[auto_auto_1fr_auto_auto] gap-2 items-center px-5 py-4 transition-[background-color,opacity] duration-150',
                    dragIdx === idx && 'bg-[var(--color-bg-muted)] opacity-90',
                  )}
                >
                  {/* Drag handle — grab to reorder; arrows remain for keyboard/touch. */}
                  <button
                    type="button"
                    draggable
                    onDragStart={() => onDragStart(idx)}
                    onDragEnd={onDragEnd}
                    aria-label={t('admin:models.dragHandle', { defaultValue: 'Drag to reorder' })}
                    className="cursor-grab active:cursor-grabbing rounded p-1 text-[var(--color-fg-faint)] hover:text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <GripVertical size={15} strokeWidth={1.5} aria-hidden />
                  </button>
                  <div className="flex flex-col gap-0.5">
                    <button
                      type="button"
                      className="rounded p-0.5 text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] disabled:opacity-30 disabled:pointer-events-none transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      disabled={idx === 0}
                      onClick={() => moveBy(idx, -1)}
                      aria-label={t('admin:models.moveUp')}
                    >
                      <ArrowUp size={14} strokeWidth={1.5} aria-hidden />
                    </button>
                    <button
                      type="button"
                      className="rounded p-0.5 text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] disabled:opacity-30 disabled:pointer-events-none transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      disabled={idx === models.length - 1}
                      onClick={() => moveBy(idx, 1)}
                      aria-label={t('admin:models.moveDown')}
                    >
                      <ArrowDown size={14} strokeWidth={1.5} aria-hidden />
                    </button>
                  </div>
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="font-medium text-[var(--color-fg)] truncate">{m.label}</span>
                      <Badge size="xs">{m.kind}</Badge>
                      <Badge size="xs" variant="neutral">{m.tool_mode}</Badge>
                      {!m.enabled ? <Badge size="xs" variant="neutral">disabled</Badge> : null}
                    </div>
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">
                      {ch?.name ?? '(unknown channel)'} · {m.request_id}
                      {m.kind === 'chat' ? ` · in $${m.price_input}/M · out $${m.price_output}/M` : ''}
                      {m.kind === 'image' ? ` · $${m.price_per_image}/img` : ''}
                      {m.kind === 'embedding' ? ` · dim ${m.dim}` : ''}
                    </div>
                  </div>
                  <Tooltip content={t('admin:models.visibleToggle', { defaultValue: m.enabled ? 'Visible to users' : 'Hidden from users' })}>
                    <span className="shrink-0">
                      <Switch checked={m.enabled} onCheckedChange={() => void toggleEnabled(m)} aria-label={t('admin:models.visibleToggle', { defaultValue: 'Show in app' })} />
                    </span>
                  </Tooltip>
                  <Button
                    variant="ghost"
                    size="sm"
                    leadingIcon={<SettingsIcon size={13} aria-hidden />}
                    onClick={() => navigate(`/admin/models/${encodeURIComponent(m.id)}`)}
                  >
                    {t('admin:models.settings')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    leadingIcon={<Trash2 size={13} aria-hidden />}
                    onClick={() => setConfirmDelete(m)}
                  >
                    {t('admin:common.remove')}
                  </Button>
                </li>
              )
            })}
          </ul>
        )}
      </section>

      {/* Quick-create dialog — only the six fields needed to register a row.
          Everything else lives on /admin/models/:id. */}
      <Dialog open={creator.open} onOpenChange={(o) => setCreator({ ...creator, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('admin:models.newTitle')}</DialogTitle>
            <DialogDescription>{t('admin:models.newDialogLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid grid-cols-2 gap-4">
              <Field label={t('admin:models.fields.channel')} htmlFor="m-new-ch">
                <Select
                  value={creator.draft.channel_id}
                  onValueChange={(v) => setCreator({ ...creator, draft: { ...creator.draft, channel_id: v } })}
                >
                  <SelectTrigger id="m-new-ch">
                    <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
                  </SelectTrigger>
                  <SelectContent>
                    {channels.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name} ({c.type})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('admin:models.fields.kind')} htmlFor="m-new-kind">
                <Select
                  value={creator.draft.kind}
                  onValueChange={(v) =>
                    setCreator({ ...creator, draft: { ...creator.draft, kind: v as ApiModel['kind'] } })
                  }
                >
                  <SelectTrigger id="m-new-kind">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {KINDS.map((k) => (
                      <SelectItem key={k} value={k}>
                        {k}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('admin:models.fields.label')} htmlFor="m-new-label">
                <Input
                  id="m-new-label"
                  value={creator.draft.label}
                  onChange={(e) => setCreator({ ...creator, draft: { ...creator.draft, label: e.target.value } })}
                  placeholder="Claude Opus 4.8"
                />
              </Field>
              <Field label={t('admin:models.fields.requestId')} htmlFor="m-new-req">
                <Input
                  id="m-new-req"
                  value={creator.draft.request_id}
                  onChange={(e) =>
                    setCreator({ ...creator, draft: { ...creator.draft, request_id: e.target.value } })
                  }
                  placeholder="claude-opus-4-8"
                />
              </Field>
              <Field label={t('admin:models.fields.icon')} htmlFor="m-new-icon" className="col-span-2">
                <IconUploader
                  id="m-new-icon"
                  value={creator.draft.icon}
                  onChange={(v) => setCreator({ ...creator, draft: { ...creator.draft, icon: v } })}
                  placeholder="🌟 or https://example.com/icon.png"
                />
              </Field>
              <Field label={t('admin:models.fields.description')} htmlFor="m-new-desc" className="col-span-2">
                <Input
                  id="m-new-desc"
                  value={creator.draft.description}
                  onChange={(e) =>
                    setCreator({ ...creator, draft: { ...creator.draft, description: e.target.value } })
                  }
                />
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreator({ ...creator, open: false })} disabled={submitting}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void submitCreate()} loading={submitting}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:models.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:models.removeBody', { label: confirmDelete.label }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
