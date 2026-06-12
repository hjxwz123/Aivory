/**
 * MemoryView — list/edit/delete the user's memories (design.md §4.16).
 */
import { useEffect, useMemo, useState } from 'react'
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
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/hooks/use-toast'

const STATUSES: ApiMemory['status'][] = ['ACTIVE', 'STALE', 'UNKNOWN_CURRENT', 'HISTORICAL_ONLY', 'QUERY_DEPENDENT']

export default function MemoryView() {
  const { t } = useTranslation(['memory', 'common'])
  const [rows, setRows] = useState<ApiMemory[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState<'all' | 'ACTIVE' | 'STALE'>('all')
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiMemory; draft: Partial<ApiMemory> }>({
    open: false,
    draft: { status: 'ACTIVE' },
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiMemory | null>(null)

  const filtered = useMemo(
    () => (filter === 'all' ? rows : rows.filter((r) => r.status === filter)),
    [rows, filter],
  )

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
    try {
      if (editor.row) {
        await memoriesApi.update(editor.row.id, {
          memory_text: d.memory_text,
          status: d.status,
          reason: d.reason,
        })
        toast.success(t('memory:updated'))
      } else {
        await memoriesApi.create({ memory_text: d.memory_text, slot: d.slot, value: d.value })
        toast.success(t('memory:created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    }
  }

  async function remove(row: ApiMemory) {
    try {
      await memoriesApi.remove(row.id)
      toast.success(t('memory:deleted'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common:common.error'))
    }
  }

  return (
    <div className="flex-1 min-h-0 overflow-y-auto">
      <div className="mx-auto w-full max-w-[68rem] px-5 sm:px-10 lg:px-14 pt-10 sm:pt-16 pb-24">
        <header className="flex items-end justify-between gap-4">
          <div className="max-w-[36ch]">
            <h1 className="font-serif text-[2.5rem] sm:text-[3.25rem] leading-[1.02] tracking-[-0.02em] text-[var(--color-fg)]">
              {t('memory:title')}
            </h1>
            <p className="mt-4 text-[var(--color-fg-muted)] text-[15px] leading-relaxed">
              {t('memory:lead')}
            </p>
          </div>
          <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
            {t('memory:new')}
          </Button>
        </header>

        <Tabs value={filter} onValueChange={(v) => setFilter(v as 'all' | 'ACTIVE' | 'STALE')} className="mt-8">
          <TabsList>
            <TabsTrigger value="all">{t('memory:filters.all')} ({rows.length})</TabsTrigger>
            <TabsTrigger value="ACTIVE">
              {t('memory:filters.active')} ({rows.filter((r) => r.status === 'ACTIVE').length})
            </TabsTrigger>
            <TabsTrigger value="STALE">
              {t('memory:filters.stale')} ({rows.filter((r) => r.status === 'STALE').length})
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <section className="mt-6">
          {loading ? (
            <div className="text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</div>
          ) : filtered.length === 0 ? (
            <EmptyState title={t('memory:empty')} description={t('memory:emptyBody')} />
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
              {filtered.map((m) => (
                <li key={m.id} className="grid grid-cols-[1fr_auto_auto] gap-3 items-center px-5 py-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-[var(--color-fg)]">{m.memory_text}</span>
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
                  <Button variant="ghost" size="sm" leadingIcon={<Pencil size={13} aria-hidden />} onClick={() => openEdit(m)}>
                    {t('memory:actions.edit')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    leadingIcon={<Trash2 size={13} aria-hidden />}
                    onClick={() => setConfirmDelete(m)}
                  >
                    {t('memory:actions.delete')}
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </section>
      </div>

      <Dialog open={editor.open} onOpenChange={(o) => setEditor({ ...editor, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('memory:actions.edit') : t('memory:addDialogTitle')}</DialogTitle>
            <DialogDescription>{t('memory:addDialogBody')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('memory:fields.text')} htmlFor="m-txt">
                <Textarea
                  id="m-txt"
                  rows={3}
                  value={editor.draft.memory_text ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, memory_text: e.target.value } })}
                />
              </Field>
              <div className="grid grid-cols-2 gap-4">
                <Field label={t('memory:fields.slot')} htmlFor="m-slot">
                  <Input
                    id="m-slot"
                    value={editor.draft.slot ?? ''}
                    onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, slot: e.target.value } })}
                    placeholder="current_city"
                  />
                </Field>
                <Field label={t('memory:fields.value')} htmlFor="m-val">
                  <Input
                    id="m-val"
                    value={editor.draft.value ?? ''}
                    onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, value: e.target.value } })}
                  />
                </Field>
              </div>
              {editor.row ? (
                <Field label={t('memory:status.ACTIVE')} htmlFor="m-status">
                  <Select
                    value={editor.draft.status ?? 'ACTIVE'}
                    onValueChange={(v) => setEditor({ ...editor, draft: { ...editor.draft, status: v as ApiMemory['status'] } })}
                  >
                    <SelectTrigger id="m-status">
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
            <DialogTitle>{t('memory:deleteConfirm')}</DialogTitle>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('memory:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

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
