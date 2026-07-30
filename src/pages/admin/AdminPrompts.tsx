import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FileText, Pencil, Plus, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiPrompt } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'
import { AdminSortableList } from '@/components/admin/AdminSortableList'

type PromptDraft = Pick<ApiPrompt, 'name' | 'description' | 'content' | 'enabled' | 'sort_order'>

const emptyDraft: PromptDraft = {
  name: '',
  description: '',
  content: '',
  enabled: true,
  sort_order: 0,
}

export default function AdminPrompts() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiPrompt[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiPrompt; draft: PromptDraft }>({
    open: false,
    draft: emptyDraft,
  })
  const [toDelete, setToDelete] = useState<ApiPrompt | null>(null)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const savingRef = useRef(false)
  const deletingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      setRows(await adminApi.prompts())
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function openNew() {
    setEditor({ open: true, draft: { ...emptyDraft, sort_order: rows.length } })
  }

  function openEdit(row: ApiPrompt) {
    setEditor({
      open: true,
      row,
      draft: {
        name: row.name,
        description: row.description,
        content: row.content,
        enabled: row.enabled,
        sort_order: row.sort_order,
      },
    })
  }

  async function submit() {
    if (savingRef.current) return
    const draft = {
      ...editor.draft,
      name: editor.draft.name.trim(),
      description: editor.draft.description.trim(),
      content: editor.draft.content.trim(),
    }
    if (!draft.name || !draft.description || !draft.content) {
      toast.error(t('admin:prompts.errors.missingFields'))
      return
    }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updatePrompt(editor.row.id, draft)
        toast.success(t('admin:prompts.updated'))
      } else {
        await adminApi.createPrompt(draft)
        toast.success(t('admin:prompts.created'))
      }
      setEditor((current) => ({ ...current, open: false }))
      await load()
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('admin:prompts.errors.nameExists'))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
      }
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function remove() {
    if (!toDelete || deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removePrompt(toDelete.id)
      toast.success(t('admin:prompts.removed'))
      setToDelete(null)
      await load()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  function setOrderedRows(next: ApiPrompt[]) {
    setRows(next.map((row, sortOrder) => ({ ...row, sort_order: sortOrder })))
  }

  function persistOrder(next: ApiPrompt[], previous: ApiPrompt[]) {
    void adminApi.reorderPrompts(next.map((row) => row.id)).catch((error) => {
      setRows(previous)
      toast.error(error instanceof ApiError ? error.message : t('admin:common.reorderFailed'))
    })
  }

  return (
    <div>
      <header className="flex flex-col items-start gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
            {t('admin:prompts.title')}
          </h1>
          <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
            {t('admin:prompts.lead')}
          </p>
        </div>
        <Button
          leadingIcon={<Plus size={15} aria-hidden />}
          onClick={openNew}
          className="max-sm:min-h-[var(--tap-min)]"
        >
          {t('admin:prompts.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="flex flex-col items-center gap-3 rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center">
            <FileText size={20} className="text-[var(--color-fg-subtle)]" aria-hidden />
            <p className="text-sm text-[var(--color-fg-muted)]">{t('admin:prompts.empty')}</p>
          </div>
        ) : (
          <AdminSortableList
            items={rows}
            onItemsChange={setOrderedRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            mobileDragOnly
            rowClassName="grid grid-cols-[2.75rem_auto_minmax(0,1fr)_2.75rem_2.75rem] items-center gap-2 px-2 py-3.5 md:grid-cols-[auto_auto_auto_minmax(0,1fr)_auto_auto] md:gap-3 md:px-5"
            renderItem={(row) => (
              <>
                <span className="grid size-8 shrink-0 place-items-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                  <FileText size={15} aria-hidden />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span
                      className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--color-fg)]"
                      title={row.name}
                    >
                      {row.name}
                    </span>
                    {!row.enabled ? (
                      <span className="shrink-0">
                        <Badge size="xs" variant="neutral">
                          {t('admin:prompts.disabledTag')}
                        </Badge>
                      </span>
                    ) : null}
                  </div>
                  <p className="mt-0.5 truncate text-[12px] text-[var(--color-fg-subtle)]">{row.description}</p>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`${t('admin:common.edit')}: ${row.name}`}
                  onClick={() => openEdit(row)}
                  className="max-sm:size-[var(--tap-min)]"
                >
                  <Pencil size={14} aria-hidden />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`${t('admin:common.remove')}: ${row.name}`}
                  onClick={() => setToDelete(row)}
                  className="max-sm:size-[var(--tap-min)]"
                >
                  <Trash2 size={14} aria-hidden />
                </Button>
              </>
            )}
          />
        )}
      </section>

      <Dialog
        open={editor.open}
        onOpenChange={(open) => {
          if (!savingRef.current) setEditor((current) => ({ ...current, open }))
        }}
      >
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>
              {editor.row ? t('admin:prompts.editorTitle') : t('admin:prompts.newTitle')}
            </DialogTitle>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:prompts.fields.name')} htmlFor="admin-prompt-name">
                <Input
                  id="admin-prompt-name"
                  autoFocus
                  value={editor.draft.name}
                  onChange={(event) =>
                    setEditor((current) => ({
                      ...current,
                      draft: { ...current.draft, name: event.target.value },
                    }))
                  }
                />
              </Field>
              <Field label={t('admin:prompts.fields.description')} htmlFor="admin-prompt-description">
                <Input
                  id="admin-prompt-description"
                  value={editor.draft.description}
                  onChange={(event) =>
                    setEditor((current) => ({
                      ...current,
                      draft: { ...current.draft, description: event.target.value },
                    }))
                  }
                />
              </Field>
              <Field label={t('admin:prompts.fields.content')} htmlFor="admin-prompt-content">
                <Textarea
                  id="admin-prompt-content"
                  rows={12}
                  value={editor.draft.content}
                  onChange={(event) =>
                    setEditor((current) => ({
                      ...current,
                      draft: { ...current.draft, content: event.target.value },
                    }))
                  }
                />
              </Field>
              <label
                htmlFor="admin-prompt-enabled"
                className="flex items-center justify-between rounded-[10px] bg-[var(--color-bg-muted)] px-3 py-2.5"
              >
                <span className="text-sm text-[var(--color-fg)]">{t('admin:prompts.fields.enabled')}</span>
                <Switch
                  id="admin-prompt-enabled"
                  checked={editor.draft.enabled}
                  onCheckedChange={(enabled) =>
                    setEditor((current) => ({ ...current, draft: { ...current.draft, enabled } }))
                  }
                />
              </label>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" disabled={saving} onClick={() => setEditor((current) => ({ ...current, open: false }))}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submit()}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(toDelete)}
        onOpenChange={(open) => {
          if (!open && !deletingRef.current) setToDelete(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:prompts.removeTitle')}</DialogTitle>
            <DialogDescription>
              {toDelete ? t('admin:prompts.removeBody', { name: toDelete.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => setToDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void remove()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
