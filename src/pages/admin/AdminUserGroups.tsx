/**
 * AdminUserGroups — manage membership tiers (§ user groups). Each group has a
 * name, short description, USD/CNY price (display-only) and a feature list.
 * Per-model usage caps live on the model editor; this page also holds the global
 * "quota exceeded / upgrade" prompt shown when a user hits a model's limit.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUserGroup } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
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

type Draft = Partial<ApiUserGroup> & { featuresText?: string; researchEnabled?: boolean }

// Reserved functional feature flag (not a marketing bullet) — gates the Deep
// Research mode. Managed via a dedicated toggle and hidden from the free-text
// features editor + the subscription page's marketing list.
const RESEARCH_FEATURE = 'research'

export default function AdminUserGroups() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiUserGroup; draft: Draft }>({ open: false, draft: {} })
  const [confirmDelete, setConfirmDelete] = useState<ApiUserGroup | null>(null)
  // Global over-quota / upgrade prompt.
  const [quotaMsg, setQuotaMsg] = useState('')
  const [savingMsg, setSavingMsg] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const [groups, settings] = await Promise.all([adminApi.userGroups(), adminApi.settings()])
      setRows(groups)
      setQuotaMsg(typeof settings.quota_exceeded_message === 'string' ? settings.quota_exceeded_message : '')
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
    setEditor({ open: true, draft: { featuresText: '', price_usd: 0, price_cny: 0, researchEnabled: false } })
  }
  function openEdit(row: ApiUserGroup) {
    const feats = row.features ?? []
    setEditor({
      open: true,
      row,
      draft: {
        ...row,
        // Hide the reserved functional flag from the marketing free-text editor.
        featuresText: feats.filter((f) => f !== RESEARCH_FEATURE).join('\n'),
        researchEnabled: feats.includes(RESEARCH_FEATURE),
      },
    })
  }
  function setDraft(p: Partial<Draft>) {
    setEditor((ed) => ({ ...ed, draft: { ...ed.draft, ...p } }))
  }

  async function submit() {
    const d = editor.draft
    if (!d.name?.trim()) {
      toast.error(t('admin:groups.errors.nameRequired'))
      return
    }
    const marketing = (d.featuresText ?? '')
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean)
      .filter((f) => f !== RESEARCH_FEATURE)
    // Append the reserved functional flag when the research toggle is on.
    const features = d.researchEnabled ? [...marketing, RESEARCH_FEATURE] : marketing
    const body: Partial<ApiUserGroup> = {
      name: d.name,
      description: d.description ?? '',
      features,
      price_usd: Number(d.price_usd) || 0,
      price_cny: Number(d.price_cny) || 0,
      buy_url: (d.buy_url ?? '').trim(),
      max_projects: Math.max(0, Number(d.max_projects) || 0),
      max_kbs: Math.max(0, Number(d.max_kbs) || 0),
    }
    try {
      if (editor.row) {
        await adminApi.updateUserGroup(editor.row.id, body)
        toast.success(t('admin:groups.updated'))
      } else {
        await adminApi.createUserGroup(body)
        toast.success(t('admin:groups.created'))
      }
      setEditor({ ...editor, open: false })
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  async function remove(row: ApiUserGroup) {
    try {
      await adminApi.removeUserGroup(row.id)
      toast.success(t('admin:groups.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    }
  }

  async function saveMsg() {
    setSavingMsg(true)
    try {
      await adminApi.updateSettings({ quota_exceeded_message: quotaMsg })
      toast.success(t('admin:groups.msgSaved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSavingMsg(false)
    }
  }

  return (
    <div>
      <header className="flex items-end justify-between gap-4">
        <div>
          <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:groups.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:groups.lead')}</p>
        </div>
        <Button leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
          {t('admin:groups.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('admin:common.loading')}</div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((g) => (
              <li key={g.id} className="grid grid-cols-[1fr_auto_auto] gap-3 items-center px-5 py-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-[var(--color-fg)] truncate">{g.name}</span>
                    {g.is_default ? <Badge size="xs" variant="neutral">{t('admin:groups.default')}</Badge> : null}
                    <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">
                      ${g.price_usd} · ¥{g.price_cny}
                    </span>
                  </div>
                  {g.description ? (
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] line-clamp-1">{g.description}</div>
                  ) : null}
                </div>
                <Button variant="ghost" size="sm" leadingIcon={<Pencil size={13} aria-hidden />} onClick={() => openEdit(g)}>
                  {t('admin:common.edit')}
                </Button>
                {g.is_default ? (
                  <span className="w-[72px]" />
                ) : (
                  <Button variant="ghost" size="sm" leadingIcon={<Trash2 size={13} aria-hidden />} onClick={() => setConfirmDelete(g)}>
                    {t('admin:common.remove')}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* Global over-quota / upgrade prompt */}
      <section className="mt-8 max-w-xl">
        <Field label={t('admin:groups.quotaMsgLabel')} htmlFor="quota-msg" hint={t('admin:groups.quotaMsgHint')}>
          <Textarea
            id="quota-msg"
            rows={3}
            value={quotaMsg}
            onChange={(e) => setQuotaMsg(e.target.value)}
            placeholder={t('admin:groups.quotaMsgPlaceholder')}
          />
        </Field>
        <div className="mt-3 flex justify-end">
          <Button variant="secondary" loading={savingMsg} onClick={() => void saveMsg()}>
            {t('common:actions.save')}
          </Button>
        </div>
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => setEditor({ ...editor, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:groups.editorTitle') : t('admin:groups.newTitle')}</DialogTitle>
            <DialogDescription>{t('admin:groups.editorLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:groups.fields.name')} htmlFor="g-name">
                <Input id="g-name" value={editor.draft.name ?? ''} onChange={(e) => setDraft({ name: e.target.value })} placeholder="VIP" />
              </Field>
              <Field label={t('admin:groups.fields.description')} htmlFor="g-desc">
                <Input
                  id="g-desc"
                  value={editor.draft.description ?? ''}
                  onChange={(e) => setDraft({ description: e.target.value })}
                  placeholder={t('admin:groups.fields.descriptionPlaceholder')}
                />
              </Field>
              <div className="grid grid-cols-2 gap-4">
                <Field label={t('admin:groups.fields.priceUsd')} htmlFor="g-usd">
                  <Input
                    id="g-usd"
                    type="number"
                    value={String(editor.draft.price_usd ?? 0)}
                    onChange={(e) => setDraft({ price_usd: Number(e.target.value) })}
                  />
                </Field>
                <Field label={t('admin:groups.fields.priceCny')} htmlFor="g-cny">
                  <Input
                    id="g-cny"
                    type="number"
                    value={String(editor.draft.price_cny ?? 0)}
                    onChange={(e) => setDraft({ price_cny: Number(e.target.value) })}
                  />
                </Field>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <Field
                  label={t('admin:groups.fields.maxProjects')}
                  htmlFor="g-maxproj"
                  hint={t('admin:groups.fields.limitHint')}
                >
                  <Input
                    id="g-maxproj"
                    type="number"
                    min={0}
                    value={String(editor.draft.max_projects ?? 0)}
                    onChange={(e) => setDraft({ max_projects: Number(e.target.value) })}
                  />
                </Field>
                <Field
                  label={t('admin:groups.fields.maxKbs')}
                  htmlFor="g-maxkbs"
                  hint={t('admin:groups.fields.limitHint')}
                >
                  <Input
                    id="g-maxkbs"
                    type="number"
                    min={0}
                    value={String(editor.draft.max_kbs ?? 0)}
                    onChange={(e) => setDraft({ max_kbs: Number(e.target.value) })}
                  />
                </Field>
              </div>
              <Field label={t('admin:groups.fields.features')} htmlFor="g-feat" hint={t('admin:groups.fields.featuresHint')}>
                <Textarea
                  id="g-feat"
                  rows={5}
                  value={editor.draft.featuresText ?? ''}
                  onChange={(e) => setDraft({ featuresText: e.target.value })}
                  placeholder={t('admin:groups.fields.featuresPlaceholder')}
                />
              </Field>
              <div className="flex items-center justify-between gap-3 rounded-[10px] border border-[var(--color-border)] px-3 py-2.5">
                <div className="min-w-0">
                  <p className="text-sm text-[var(--color-fg)]">{t('admin:groups.fields.research', { defaultValue: 'Deep Research' })}</p>
                  <p className="text-[12px] text-[var(--color-fg-subtle)]">
                    {t('admin:groups.fields.researchHint', { defaultValue: 'Allow this group to use the Deep Research mode.' })}
                  </p>
                </div>
                <Switch
                  checked={Boolean(editor.draft.researchEnabled)}
                  onCheckedChange={(v) => setDraft({ researchEnabled: v })}
                  aria-label={t('admin:groups.fields.research', { defaultValue: 'Deep Research' })}
                />
              </div>
              <Field
                label={t('admin:groups.fields.buyUrl', { defaultValue: 'Purchase link' })}
                htmlFor="g-buy"
                hint={t('admin:groups.fields.buyUrlHint', { defaultValue: 'Optional. Shown as a “Buy / Upgrade” button on the subscription page.' })}
              >
                <Input
                  id="g-buy"
                  value={editor.draft.buy_url ?? ''}
                  onChange={(e) => setDraft({ buy_url: e.target.value })}
                  placeholder="https://…"
                />
              </Field>
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
            <DialogTitle>{t('admin:groups.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:groups.removeBody', { name: confirmDelete.name }) : ''}
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
