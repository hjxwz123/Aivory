/**
 * AdminChannels — list, create and edit upstream channels.
 * Channels carry the (type + base_url + api_key + api_format) tuple from
 * design.md §2.3-B. The api_key column is never re-displayed; admins can leave
 * the field blank when editing to keep the existing secret.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiChannel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
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
import { Badge } from '@/components/ui/badge'

type Editable = Partial<ApiChannel> & { api_key?: string }

const TYPES = ['openai', 'claude', 'gemini'] as const

export default function AdminChannels() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiChannel[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiChannel; draft: Editable }>(
    { open: false, draft: { type: 'openai', api_format: 'chat', enabled: true } },
  )
  const [confirmDelete, setConfirmDelete] = useState<ApiChannel | null>(null)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      setRows(await adminApi.channels())
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
    setEditor({ open: true, draft: { type: 'openai', api_format: 'chat', enabled: true, name: '', base_url: '' } })
  }

  function openEdit(row: ApiChannel) {
    setEditor({ open: true, row, draft: { ...row, api_key: '' } })
  }

  async function submit() {
    if (savingRef.current) return
    const d = editor.draft
    if (!d.name) {
      toast.error(t('admin:channels.errors.nameRequired'))
      return
    }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updateChannel(editor.row.id, d)
        toast.success(t('admin:channels.updated'))
      } else {
        await adminApi.createChannel(d)
        toast.success(t('admin:channels.created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function remove(row: ApiChannel) {
    try {
      await adminApi.removeChannel(row.id)
      toast.success(t('admin:channels.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  function persistOrder(next: ApiChannel[], prev: ApiChannel[]) {
    void adminApi.reorderChannels(next.map((r) => r.id)).catch((e) => {
      setRows(prev)
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:channels.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:channels.lead')}</p>
        </div>
        <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
          {t('admin:channels.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center">
            <p className="text-[var(--color-fg-muted)] text-sm">{t('admin:channels.empty')}</p>
            <div className="mt-4">
              <Button onClick={openNew}>{t('admin:common.createFirst', { kind: t('admin:channels.title').toLowerCase() })}</Button>
            </div>
          </div>
        ) : (
          <AdminSortableList
            items={rows}
            onItemsChange={setRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            rowClassName="grid grid-cols-[auto_auto_1fr_auto_auto] items-center gap-4 px-5 py-4"
            renderItem={(r) => (
              <>
                <div className="min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-[var(--color-fg)] truncate">{r.name}</span>
                    <Badge size="xs">{r.type}</Badge>
                    {r.type === 'openai' && r.api_format ? <Badge size="xs">{r.api_format}</Badge> : null}
                    {r.enabled ? null : <Badge size="xs" variant="neutral">{t('admin:channels.labels.disabled')}</Badge>}
                  </div>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">
                    {r.base_url || t('admin:channels.labels.defaultEndpoint')} · {r.has_api_key ? t('admin:channels.labels.keySet') : t('admin:channels.labels.noKey')}
                  </div>
                </div>
                <Button variant="ghost" size="sm" leadingIcon={<Pencil size={13} aria-hidden />} onClick={() => openEdit(r)}>
                  {t('admin:common.edit')}
                </Button>
                <Button variant="ghost" size="sm" leadingIcon={<Trash2 size={13} aria-hidden />} onClick={() => setConfirmDelete(r)}>
                  {t('admin:common.remove')}
                </Button>
              </>
            )}
          />
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => !savingRef.current && setEditor({ ...editor, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:channels.editorTitle') : t('admin:channels.newTitle')}</DialogTitle>
            <DialogDescription>
              {editor.row ? t('admin:channels.editorDescriptionEdit') : t('admin:channels.editorDescriptionNew')}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:channels.fields.name')} htmlFor="ch-name">
                <Input
                  id="ch-name"
                  value={editor.draft.name ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, name: e.target.value } })}
                  placeholder="Anthropic production"
                />
              </Field>
              <div className="grid grid-cols-2 gap-4">
                <Field label={t('admin:channels.fields.type')} htmlFor="ch-type">
                  <Select
                    value={editor.draft.type ?? 'openai'}
                    onValueChange={(v) => {
                      const type = v as ApiChannel['type']
                      // api_format only applies to OpenAI; clear it for others.
                      setEditor({
                        ...editor,
                        draft: {
                          ...editor.draft,
                          type,
                          api_format: type === 'openai' ? (editor.draft.api_format ?? 'chat') : '',
                        },
                      })
                    }}
                  >
                    <SelectTrigger id="ch-type">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {TYPES.map((tp) => (
                        <SelectItem key={tp} value={tp}>
                          {tp}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                {editor.draft.type === 'openai' ? (
                  <Field label={t('admin:channels.fields.apiFormat')} htmlFor="ch-fmt" hint={t('admin:channels.fields.apiFormatHint')}>
                    <Select
                      value={editor.draft.api_format ?? 'chat'}
                      onValueChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, api_format: v as ApiChannel['api_format'] } })}
                    >
                      <SelectTrigger id="ch-fmt">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="chat">chat</SelectItem>
                        <SelectItem value="responses">responses</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                ) : null}
              </div>
              <Field label={t('admin:channels.fields.baseUrl')} htmlFor="ch-url" hint={t('admin:channels.fields.baseUrlHint')}>
                <Input
                  id="ch-url"
                  value={editor.draft.base_url ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, base_url: e.target.value } })}
                  placeholder="https://api.openai.com"
                />
              </Field>
              <Field
                label={t('admin:channels.fields.apiKey')}
                htmlFor="ch-key"
                hint={editor.row ? t('admin:channels.fields.apiKeyHintEdit') : t('admin:channels.fields.apiKeyHintNew')}
              >
                <Input
                  id="ch-key"
                  type="password"
                  value={editor.draft.api_key ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, api_key: e.target.value } })}
                  placeholder="sk-…"
                />
              </Field>
              <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                <span className="text-sm text-[var(--color-fg)]">{t('admin:channels.fields.enabled')}</span>
                <Switch
                  checked={editor.draft.enabled ?? true}
                  onCheckedChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, enabled: v } })}
                />
              </label>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" disabled={saving} onClick={() => setEditor({ ...editor, open: false })}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submit()}>{t('common:actions.save')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:channels.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:channels.removeBody', { name: confirmDelete.name }) : ''}
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
