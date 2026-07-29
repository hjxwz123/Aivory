/**
 * AdminSkills — manage the skill library.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2, Upload, X, FileText } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiSkill } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
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
import { PanelFallback } from '@/components/ui/panel-fallback'
import { parseSkillDocument } from '@/lib/skill-document'
import { IconPicker } from '@/components/admin/icon-picker'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
import { SkillIcon } from '@/components/ui/skill-icon'

type Draft = Partial<ApiSkill>
const defaultDraft: Draft = { enabled: true, icon: '' }

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export default function AdminSkills() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiSkill[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiSkill; draft: Draft }>({
    open: false,
    draft: defaultDraft,
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiSkill | null>(null)
  const [importMd, setImportMd] = useState('')
  const [uploading, setUploading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const savingRef = useRef(false)
  const deletingRef = useRef(false)

  // Upload one asset (template / script / data); append the returned descriptor
  // to the draft's assets. Persisted with the skill on save (§4.17).
  async function onPickAsset(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    setUploading(true)
    try {
      const asset = await adminApi.uploadSkillAsset(file)
      setEditor((ed) => ({ ...ed, draft: { ...ed.draft, assets: [...(ed.draft.assets ?? []), asset] } }))
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t('admin:common.failed'))
    } finally {
      setUploading(false)
    }
  }

  function removeAsset(idx: number) {
    setEditor((ed) => ({
      ...ed,
      draft: { ...ed.draft, assets: (ed.draft.assets ?? []).filter((_, i) => i !== idx) },
    }))
  }

  async function load() {
    setLoading(true)
    try {
      setRows(await adminApi.skills())
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
    setImportMd('')
    setEditor({ open: true, draft: { ...defaultDraft, sort_order: rows.length } })
  }
  function openEdit(row: ApiSkill) {
    setImportMd('')
    setEditor({ open: true, row, draft: { ...row } })
  }

  // Parse a pasted SKILL.md and fill name / description / instructions.
  function applyImport() {
    const parsed = parseSkillDocument(importMd)
    if (!importMd.trim() || (!parsed.name && !parsed.description && !parsed.instructions)) {
      toast.error(t('admin:skills.importFailed'))
      return
    }
    setEditor((ed) => ({
      ...ed,
      draft: {
        ...ed.draft,
        name: parsed.name ?? ed.draft.name,
        description: parsed.description ?? ed.draft.description,
        instructions: parsed.instructions || ed.draft.instructions,
      },
    }))
    toast.success(t('admin:skills.importDone'))
  }

  async function submit() {
    if (savingRef.current) return
    const d = editor.draft
    if (!d.name || !d.description || !d.instructions) {
      toast.error(t('admin:skills.errors.missingFields'))
      return
    }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updateSkill(editor.row.id, d)
        toast.success(t('admin:skills.updated'))
      } else {
        await adminApi.createSkill(d)
        toast.success(t('admin:skills.created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:skills.errors.nameExists'))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function remove(row: ApiSkill) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeSkill(row.id)
      toast.success(t('admin:skills.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  function setOrderedRows(next: ApiSkill[]) {
    setRows(next.map((row, sortOrder) => ({ ...row, sort_order: sortOrder })))
  }

  function persistOrder(next: ApiSkill[], previous: ApiSkill[]) {
    void adminApi.reorderSkills(next.map((row) => row.id)).catch((error) => {
      setRows(previous)
      toast.error(error instanceof ApiError ? error.message : t('admin:common.reorderFailed'))
    })
  }

  return (
    <div>
      <header className="flex flex-col items-start gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:skills.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:skills.lead')}</p>
        </div>
        <Button
          leadingIcon={<Plus size={15} aria-hidden />}
          onClick={openNew}
          className="max-sm:min-h-[var(--tap-min)]"
        >
          {t('admin:skills.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:skills.empty')}
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
            rowClassName="grid grid-cols-[2.75rem_minmax(0,1fr)_auto] items-center gap-2 px-2 py-3.5 md:grid-cols-[auto_auto_minmax(0,1fr)_auto] md:gap-3 md:px-5 md:py-4"
            renderItem={(s) => (
              <>
                <div className="flex min-w-0 items-center gap-3">
                  <span className="grid size-9 shrink-0 place-items-center rounded-[9px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                    <SkillIcon name={s.icon} size={16} aria-hidden />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="min-w-0 flex-1 truncate font-medium text-[var(--color-fg)]" title={s.name}>
                        {s.name}
                      </span>
                      {!s.enabled ? <Badge size="xs" variant="neutral">{t('admin:skills.disabledTag')}</Badge> : null}
                    </div>
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] line-clamp-2">
                      {s.display_description || s.description}
                    </div>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    leadingIcon={<Pencil size={13} aria-hidden />}
                    aria-label={`${t('admin:common.edit')}: ${s.name}`}
                    onClick={() => openEdit(s)}
                    className="max-sm:size-[var(--tap-min)] max-sm:gap-0 max-sm:px-0"
                  >
                    <span className="max-sm:sr-only">{t('admin:common.edit')}</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    leadingIcon={<Trash2 size={13} aria-hidden />}
                    aria-label={`${t('admin:common.remove')}: ${s.name}`}
                    onClick={() => setConfirmDelete(s)}
                    className="max-sm:size-[var(--tap-min)] max-sm:gap-0 max-sm:px-0"
                  >
                    <span className="max-sm:sr-only">{t('admin:common.remove')}</span>
                  </Button>
                </div>
              </>
            )}
          />
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => !savingRef.current && setEditor({ ...editor, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:skills.editorTitle') : t('admin:skills.newTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field
                label={t('admin:skills.fields.importMd')}
                htmlFor="admin-skill-import"
                hint={t('admin:skills.fields.importMdHint')}
              >
                <Textarea
                  id="admin-skill-import"
                  rows={4}
                  value={importMd}
                  onChange={(e) => setImportMd(e.target.value)}
                  placeholder={'---\nname: make_ppt\ndescription: Use when the user asks for slides or a deck.\n---\n\n# How to build a deck\n…'}
                  className="font-mono text-[12px]"
                />
                <div className="mt-2 flex justify-end">
                  <Button size="sm" variant="secondary" onClick={applyImport}>
                    {t('admin:skills.importApply')}
                  </Button>
                </div>
              </Field>
              <Field label={t('admin:skills.fields.name')} htmlFor="s-name">
                <Input
                  id="s-name"
                  value={editor.draft.name ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, name: e.target.value } })}
                  placeholder="make_ppt"
                />
              </Field>
              <Field label={t('admin:skills.fields.icon')} htmlFor="s-icon">
                <IconPicker
                  id="s-icon"
                  value={editor.draft.icon ?? ''}
                  onChange={(icon) => setEditor({ ...editor, draft: { ...editor.draft, icon } })}
                  aria-label={t('admin:skills.fields.icon')}
                />
              </Field>
              <Field
                label={t('admin:skills.fields.displayDescription')}
                htmlFor="s-display-desc"
                hint={t('admin:skills.fields.displayDescriptionHint')}
              >
                <Input
                  id="s-display-desc"
                  value={editor.draft.display_description ?? ''}
                  onChange={(e) =>
                    setEditor({
                      ...editor,
                      draft: { ...editor.draft, display_description: e.target.value },
                    })
                  }
                />
              </Field>
              <Field
                label={t('admin:skills.fields.when')}
                htmlFor="s-desc"
                hint={t('admin:skills.fields.whenHint')}
              >
                <Input
                  id="s-desc"
                  value={editor.draft.description ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, description: e.target.value } })}
                  placeholder="Use when the user asks for slides or a deck."
                />
              </Field>
              <Field label={t('admin:skills.fields.instructions')} htmlFor="s-inst">
                <Textarea
                  id="s-inst"
                  rows={10}
                  value={editor.draft.instructions ?? ''}
                  onChange={(e) => setEditor({ ...editor, draft: { ...editor.draft, instructions: e.target.value } })}
                />
              </Field>
              <Field
                label={t('admin:skills.fields.assets', { defaultValue: 'Assets (templates / scripts)' })}
                hint={t('admin:skills.fields.assetsHint', {
                  defaultValue: 'Files staged into the sandbox at /workspace/skills/<name>/ when use_skill loads this skill.',
                })}
              >
                <div className="grid gap-2">
                  {(editor.draft.assets ?? []).length > 0 ? (
                    <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[10px] border border-[var(--color-border)]">
                      {(editor.draft.assets ?? []).map((a, i) => (
                        <li key={`${a.storage_path}-${i}`} className="flex items-center gap-2 px-3 py-2">
                          <FileText size={14} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
                          <span className="min-w-0 flex-1 truncate text-[13px] text-[var(--color-fg)]">{a.filename}</span>
                          {typeof a.size_bytes === 'number' ? (
                            <span className="text-[11px] text-[var(--color-fg-subtle)] tabular-nums">{formatBytes(a.size_bytes)}</span>
                          ) : null}
                          <button
                            type="button"
                            onClick={() => removeAsset(i)}
                            aria-label={t('admin:common.remove')}
                            className="inline-flex size-6 items-center justify-center rounded-[6px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-[var(--tap-min)]"
                          >
                            <X size={13} aria-hidden />
                          </button>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                  <div>
                    <input ref={fileRef} type="file" className="hidden" onChange={onPickAsset} />
                    <Button
                      size="sm"
                      variant="secondary"
                      leadingIcon={<Upload size={14} aria-hidden />}
                      loading={uploading}
                      onClick={() => fileRef.current?.click()}
                    >
                      {t('admin:skills.fields.assetsUpload', { defaultValue: 'Upload asset' })}
                    </Button>
                  </div>
                </div>
              </Field>
              <label
                htmlFor="admin-skill-enabled"
                className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5"
              >
                <span className="text-sm">{t('admin:skills.fields.enabled')}</span>
                <Switch
                  id="admin-skill-enabled"
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
            <Button loading={saving} disabled={uploading} onClick={() => void submit()}>{t('common:actions.save')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={Boolean(confirmDelete)}
        onOpenChange={(open) => {
          if (!open && !deletingRef.current) setConfirmDelete(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:skills.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:skills.removeBody', { name: confirmDelete.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => setConfirmDelete(null)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => confirmDelete && void remove(confirmDelete)}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
