/**
 * AdminModels — list, create, edit chat/image/embedding models attached to a
 * channel.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiChannel, ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
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
const TOOL_MODES = ['native', 'prompt', 'none'] as const

type Draft = Partial<ApiModel>

const defaultDraft: Draft = {
  kind: 'chat',
  enabled: true,
  tool_mode: 'native',
  vision: true,
  stream: true,
  param_controls: '[]',
  currency: 'USD',
}

export default function AdminModels() {
  const { t } = useTranslation(['admin', 'common'])
  const [channels, setChannels] = useState<ApiChannel[]>([])
  const [models, setModels] = useState<ApiModel[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiModel; draft: Draft }>({
    open: false,
    draft: defaultDraft,
  })
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
    setEditor({ open: true, draft: { ...defaultDraft, channel_id: channels[0]?.id } })
  }

  function openEdit(row: ApiModel) {
    setEditor({
      open: true,
      row,
      draft: {
        ...row,
        param_controls:
          typeof row.param_controls === 'string'
            ? row.param_controls
            : JSON.stringify(row.param_controls ?? [], null, 2),
      },
    })
  }

  async function submit() {
    const d = editor.draft
    if (!d.channel_id || !d.label || !d.request_id) {
      toast.error(t('admin:models.errors.missingFields'))
      return
    }
    let parsedPC: unknown = []
    try {
      parsedPC = typeof d.param_controls === 'string' ? JSON.parse(d.param_controls || '[]') : d.param_controls ?? []
    } catch {
      toast.error(t('admin:models.errors.invalidJSON'))
      return
    }
    const payload: Partial<ApiModel> = {
      ...d,
      param_controls: parsedPC,
    }
    try {
      if (editor.row) {
        await adminApi.updateModel(editor.row.id, payload)
        toast.success(t('admin:models.updated'))
      } else {
        await adminApi.createModel(payload)
        toast.success(t('admin:models.created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
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
            {models.map((m) => {
              const ch = channels.find((c) => c.id === m.channel_id)
              return (
                <li key={m.id} className="grid grid-cols-[1fr_auto_auto] gap-3 items-center px-5 py-4">
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
                  <Button variant="ghost" size="sm" leadingIcon={<Pencil size={13} aria-hidden />} onClick={() => openEdit(m)}>
                    {t('admin:common.edit')}
                  </Button>
                  <Button variant="ghost" size="sm" leadingIcon={<Trash2 size={13} aria-hidden />} onClick={() => setConfirmDelete(m)}>
                    {t('admin:common.remove')}
                  </Button>
                </li>
              )
            })}
          </ul>
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => setEditor({ ...editor, open: o })}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:models.editorTitle') : t('admin:models.newTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <div className="grid grid-cols-2 gap-4">
              <Field label={t('admin:models.fields.channel')} htmlFor="m-ch">
                <Select
                  value={editor.draft.channel_id ?? ''}
                  onValueChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, channel_id: v } })}
                >
                  <SelectTrigger id="m-ch">
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
              <Field label={t('admin:models.fields.kind')} htmlFor="m-kind">
                <Select
                  value={editor.draft.kind ?? 'chat'}
                  onValueChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, kind: v as ApiModel['kind'] } })}
                >
                  <SelectTrigger id="m-kind">
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
              <Field label={t('admin:models.fields.label')} htmlFor="m-label">
                <Input
                  id="m-label"
                  value={editor.draft.label ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, label: e.target.value } })}
                  placeholder="Claude Opus 4.8"
                />
              </Field>
              <Field label={t('admin:models.fields.requestId')} htmlFor="m-req">
                <Input
                  id="m-req"
                  value={editor.draft.request_id ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, request_id: e.target.value } })}
                  placeholder="claude-opus-4-8"
                />
              </Field>
              <Field label={t('admin:models.fields.description')} htmlFor="m-desc" className="col-span-2">
                <Input
                  id="m-desc"
                  value={editor.draft.description ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, description: e.target.value } })}
                />
              </Field>
              {/* §2.3 model icon — emoji or remote URL the model-picker renders */}
              <Field label={t('admin:models.fields.icon', { defaultValue: 'Icon' })} htmlFor="m-icon" className="col-span-2">
                <Input
                  id="m-icon"
                  value={editor.draft.icon ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, icon: e.target.value } })}
                  placeholder="🌟 or https://example.com/icon.png"
                />
              </Field>
              {editor.draft.kind === 'chat' && (
                <>
                  <Field label={t('admin:models.fields.toolMode')} htmlFor="m-tool">
                    <Select
                      value={editor.draft.tool_mode ?? 'native'}
                      onValueChange={(v) =>
                        setEditor({ ...editor, draft: { ...editor.draft, tool_mode: v as ApiModel['tool_mode'] } })
                      }
                    >
                      <SelectTrigger id="m-tool">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {TOOL_MODES.map((tm) => (
                          <SelectItem key={tm} value={tm}>
                            {tm}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                  <div className="grid grid-cols-2 gap-3 items-end">
                    <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                      <span className="text-sm">{t('admin:models.fields.vision')}</span>
                      <Switch
                        checked={editor.draft.vision ?? true}
                        onCheckedChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, vision: v } })}
                      />
                    </label>
                    <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                      <span className="text-sm">{t('admin:models.fields.stream')}</span>
                      <Switch
                        checked={editor.draft.stream ?? true}
                        onCheckedChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, stream: v } })}
                      />
                    </label>
                  </div>
                  <Field label={t('admin:models.fields.systemPrompt')} htmlFor="m-sys" className="col-span-2">
                    <Textarea
                      id="m-sys"
                      rows={4}
                      value={editor.draft.system_prompt ?? ''}
                      onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, system_prompt: e.target.value } })}
                    />
                  </Field>
                  <Field label={t('admin:models.fields.paramControls')} htmlFor="m-pc" className="col-span-2">
                    <Textarea
                      id="m-pc"
                      rows={6}
                      value={typeof editor.draft.param_controls === 'string' ? editor.draft.param_controls : JSON.stringify(editor.draft.param_controls ?? [], null, 2)}
                      onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, param_controls: e.target.value as unknown as ApiModel['param_controls'] } })}
                    />
                  </Field>
                </>
              )}
              {editor.draft.kind === 'embedding' && (
                <Field label={t('admin:models.fields.dim')} htmlFor="m-dim">
                  <Input
                    id="m-dim"
                    type="number"
                    value={String(editor.draft.dim ?? 0)}
                    onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, dim: Number(e.target.value) } })}
                  />
                </Field>
              )}
              {editor.draft.kind !== 'image' && (
                <>
                  <Field label={t('admin:models.fields.priceIn')} htmlFor="m-pi">
                    <Input
                      id="m-pi"
                      type="number"
                      step="0.0001"
                      value={String(editor.draft.price_input ?? 0)}
                      onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, price_input: Number(e.target.value) } })}
                    />
                  </Field>
                  <Field label={t('admin:models.fields.priceOut')} htmlFor="m-po">
                    <Input
                      id="m-po"
                      type="number"
                      step="0.0001"
                      value={String(editor.draft.price_output ?? 0)}
                      onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, price_output: Number(e.target.value) } })}
                    />
                  </Field>
                  {/* §4.9 cache pricing — separate per-1M rates so accurate cost
                     accounting is possible when the provider returns cache
                     read/write hits in usage. */}
                  <Field label={t('admin:models.fields.priceCacheRead', { defaultValue: 'Cache read $/1M' })} htmlFor="m-pcr">
                    <Input
                      id="m-pcr"
                      type="number"
                      step="0.0001"
                      value={String(editor.draft.price_cache_read ?? 0)}
                      onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, price_cache_read: Number(e.target.value) } })}
                    />
                  </Field>
                  <Field label={t('admin:models.fields.priceCacheWrite', { defaultValue: 'Cache write $/1M' })} htmlFor="m-pcw">
                    <Input
                      id="m-pcw"
                      type="number"
                      step="0.0001"
                      value={String(editor.draft.price_cache_write ?? 0)}
                      onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, price_cache_write: Number(e.target.value) } })}
                    />
                  </Field>
                </>
              )}
              {editor.draft.kind === 'image' && (
                <Field label={t('admin:models.fields.priceImage')}>
                  <Input
                    type="number"
                    step="0.001"
                    value={String(editor.draft.price_per_image ?? 0)}
                    onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, price_per_image: Number(e.target.value) } })}
                  />
                </Field>
              )}
              <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5 col-span-2">
                <span className="text-sm">{t('admin:models.fields.enabled')}</span>
                <Switch
                  checked={editor.draft.enabled ?? true}
                  onCheckedChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, enabled: v } })}
                />
              </label>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setEditor({ ...editor, open: false })}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void submit()}>{t('common:actions.save')}</Button>
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
