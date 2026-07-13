/**
 * MemoryManager — list / add / edit / delete the user's long-term memories.
 * Compact embed used inside the Personalization settings page (replaces the
 * standalone /memory route). Reuses the `memory` i18n namespace.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2, Pencil } from 'lucide-react'
import { ApiError, memoriesApi } from '@/api'
import type { ApiMemory } from '@/api/types'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
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
import { Field } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'

const STATUSES: ApiMemory['status'][] = ['ACTIVE', 'STALE', 'UNKNOWN_CURRENT', 'HISTORICAL_ONLY', 'QUERY_DEPENDENT']

function badgeVariant(s: ApiMemory['status']) {
  switch (s) {
    case 'ACTIVE':
      return 'sage' as const
    case 'STALE':
    case 'HISTORICAL_ONLY':
      return 'neutral' as const
    default:
      return 'accent' as const
  }
}

export function MemoryManager() {
  const { t } = useTranslation(['memory', 'common'])
  const [rows, setRows] = useState<ApiMemory[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiMemory; draft: Partial<ApiMemory> }>({
    open: false,
    draft: { status: 'ACTIVE' },
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiMemory | null>(null)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      setRows(await memoriesApi.list())
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function openNew() {
    setEditor({ open: true, draft: { status: 'ACTIVE', memory_text: '', slot: '', value: '' } })
  }
  function openEdit(row: ApiMemory) {
    setEditor({ open: true, row, draft: { ...row } })
  }

  async function submit() {
    const d = editor.draft
    if (!d.memory_text) {
      toast.error(t('memory:fields.text'))
      return
    }
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await memoriesApi.update(editor.row.id, { memory_text: d.memory_text, status: d.status, reason: d.reason })
        toast.success(t('memory:updated'))
      } else {
        await memoriesApi.create({ memory_text: d.memory_text, slot: d.slot, value: d.value })
        toast.success(t('memory:created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function remove(row: ApiMemory) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await memoriesApi.remove(row.id)
      toast.success(t('memory:deleted'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  return (
    <div>
      <div className="flex items-center justify-end mb-3">
        <Button size="sm" leadingIcon={<Plus size={14} aria-hidden />} onClick={openNew}>
          {t('memory:new')}
        </Button>
      </div>

      {loading ? (
        <div className="text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('memory:empty')} description={t('memory:emptyBody')} />
      ) : (
        <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
          {rows.map((m) => (
            <li key={m.id} className="grid grid-cols-[1fr_auto_auto] gap-2 items-center px-4 py-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-sm text-[var(--color-fg)]">{m.memory_text}</span>
                  <Badge size="xs" variant={badgeVariant(m.status)}>
                    {t(`memory:status.${m.status}`)}
                  </Badge>
                </div>
                {m.slot ? (
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono">
                    {m.slot}
                    {m.value ? ` = ${m.value}` : ''}
                  </div>
                ) : null}
              </div>
              <Button variant="ghost" size="icon-sm" aria-label={t('memory:actions.edit')} onClick={() => openEdit(m)}>
                <Pencil size={13} aria-hidden />
              </Button>
              <Button variant="ghost" size="icon-sm" aria-label={t('memory:actions.delete')} onClick={() => setConfirmDelete(m)}>
                <Trash2 size={13} aria-hidden />
              </Button>
            </li>
          ))}
        </ul>
      )}

      <Dialog open={editor.open} onOpenChange={(o) => setEditor({ ...editor, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('memory:actions.edit') : t('memory:addDialogTitle')}</DialogTitle>
            <DialogDescription>{t('memory:addDialogBody')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('memory:fields.text')} htmlFor="mm-txt">
                <Textarea
                  id="mm-txt"
                  rows={3}
                  value={editor.draft.memory_text ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, memory_text: e.target.value } })}
                />
              </Field>
              <div className="grid grid-cols-2 gap-4">
                <Field label={t('memory:fields.slot')} htmlFor="mm-slot">
                  <Input
                    id="mm-slot"
                    value={editor.draft.slot ?? ''}
                    onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, slot: e.target.value } })}
                    placeholder="current_city"
                  />
                </Field>
                <Field label={t('memory:fields.value')} htmlFor="mm-val">
                  <Input
                    id="mm-val"
                    value={editor.draft.value ?? ''}
                    onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, value: e.target.value } })}
                  />
                </Field>
              </div>
              {editor.row ? (
                <Field label={t('memory:status.ACTIVE')} htmlFor="mm-status">
                  <Select
                    value={editor.draft.status ?? 'ACTIVE'}
                    onValueChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, status: v as ApiMemory['status'] } })}
                  >
                    <SelectTrigger id="mm-status">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {STATUSES.map((s) => (
                        <SelectItem key={s} value={s}>{t(`memory:status.${s}`)}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              ) : null}
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
            <DialogTitle>{t('memory:deleteConfirm')}</DialogTitle>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('memory:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
