/**
 * AdminSkills — manage the skill library.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2 } from 'lucide-react'
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

type Draft = Partial<ApiSkill>
const defaultDraft: Draft = { enabled: true }

/**
 * parseSkillMd reads an Anthropic-style SKILL.md: a YAML frontmatter block
 * (--- … ---) carrying `name` + `description` (the "when to use" line), followed
 * by the markdown body which becomes the instructions. With no frontmatter the
 * whole text is treated as instructions.
 */
function parseSkillMd(md: string): { name?: string; description?: string; instructions: string } {
  const m = md.match(/^\s*---\s*\n([\s\S]*?)\n---\s*\n?([\s\S]*)$/)
  if (!m) return { instructions: md.trim() }
  const fm = m[1]
  const body = m[2].trim()
  const unquote = (s: string) => s.trim().replace(/^["']|["']$/g, '').trim()
  const nameLine = fm.match(/^\s*name:\s*(.+)$/m)?.[1]
  const descLine = fm.match(/^\s*description:\s*(.+)$/m)?.[1]
  return {
    name: nameLine ? unquote(nameLine) : undefined,
    description: descLine ? unquote(descLine) : undefined,
    instructions: body,
  }
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
    setEditor({ open: true, draft: { ...defaultDraft } })
  }
  function openEdit(row: ApiSkill) {
    setImportMd('')
    setEditor({ open: true, row, draft: { ...row } })
  }

  // Parse a pasted SKILL.md and fill name / description / instructions.
  function applyImport() {
    const parsed = parseSkillMd(importMd)
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
    const d = editor.draft
    if (!d.name || !d.description || !d.instructions) {
      toast.error(t('admin:skills.errors.missingFields'))
      return
    }
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
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  async function remove(row: ApiSkill) {
    try {
      await adminApi.removeSkill(row.id)
      toast.success(t('admin:skills.removed'))
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
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:skills.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:skills.lead')}</p>
        </div>
        <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
          {t('admin:skills.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:skills.empty')}
          </div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((s) => (
              <li key={s.id} className="grid grid-cols-[1fr_auto_auto] gap-3 items-center px-5 py-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-[var(--color-fg)] truncate">{s.name}</span>
                    {!s.enabled ? <Badge size="xs" variant="neutral">disabled</Badge> : null}
                  </div>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] line-clamp-2">{s.description}</div>
                </div>
                <Button variant="ghost" size="sm" leadingIcon={<Pencil size={13} aria-hidden />} onClick={() => openEdit(s)}>
                  {t('admin:common.edit')}
                </Button>
                <Button variant="ghost" size="sm" leadingIcon={<Trash2 size={13} aria-hidden />} onClick={() => setConfirmDelete(s)}>
                  {t('admin:common.remove')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => setEditor({ ...editor, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:skills.editorTitle') : t('admin:skills.newTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:skills.fields.importMd')} hint={t('admin:skills.fields.importMdHint')}>
                <Textarea
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
              <label className="flex items-center justify-between rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
                <span className="text-sm">{t('admin:skills.fields.enabled')}</span>
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
            <DialogTitle>{t('admin:skills.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:skills.removeBody', { name: confirmDelete.name }) : ''}
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
