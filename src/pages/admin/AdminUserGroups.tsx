/**
 * AdminUserGroups — manage membership tiers (§ user groups). Each group has a
 * name, short description, USD/CNY price (display-only) and a feature list.
 * Per-model usage caps live on the model editor; this page also holds the global
 * "quota exceeded / upgrade" prompt shown when a user hits a model's limit.
 */
import { useEffect, useRef, useState } from 'react'
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
import { PanelFallback } from '@/components/ui/panel-fallback'

type PeriodUnit = 'hour' | 'day' | 'week'
type Draft = Partial<ApiUserGroup> & {
  featuresText?: string
  researchEnabled?: boolean
  workspacesEnabled?: boolean
  creditPeriodValue?: number
  creditPeriodUnit?: PeriodUnit
}

const UNIT_SECONDS: Record<PeriodUnit, number> = { hour: 3600, day: 86400, week: 604800 }

// Convert stored seconds into the largest whole unit for display, and back.
function splitPeriod(seconds: number): { value: number; unit: PeriodUnit } {
  if (!seconds || seconds <= 0) return { value: 0, unit: 'day' }
  for (const u of ['week', 'day', 'hour'] as const) {
    if (seconds % UNIT_SECONDS[u] === 0) return { value: seconds / UNIT_SECONDS[u], unit: u }
  }
  return { value: Math.round(seconds / 3600), unit: 'hour' }
}

// Reserved functional feature flag (not a marketing bullet) — gates the Deep
// Research mode. Managed via a dedicated toggle and hidden from the free-text
// features editor + the subscription page's marketing list.
const RESEARCH_FEATURE = 'research'
// Reserved functional flag: whether the group may CREATE workspaces (§workspaces).
const WORKSPACES_FEATURE = 'workspaces'

export default function AdminUserGroups() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiUserGroup; draft: Draft }>({ open: false, draft: {} })
  const [confirmDelete, setConfirmDelete] = useState<ApiUserGroup | null>(null)
  // Global over-quota / upgrade prompt + the shared USD→credit rate.
  const [quotaMsg, setQuotaMsg] = useState('')
  const [creditsPerUsd, setCreditsPerUsd] = useState(0)
  const [groupBuyUrl, setGroupBuyUrl] = useState('')
  const [creditBuyUrl, setCreditBuyUrl] = useState('')
  const [savingMsg, setSavingMsg] = useState(false)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      const [groups, settings] = await Promise.all([adminApi.userGroups(), adminApi.settings()])
      setRows(groups)
      setQuotaMsg(typeof settings.quota_exceeded_message === 'string' ? settings.quota_exceeded_message : '')
      setCreditsPerUsd(Number(settings.credits_per_usd) || 0)
      setGroupBuyUrl(typeof settings.group_buy_url === 'string' ? settings.group_buy_url : '')
      setCreditBuyUrl(typeof settings.credit_buy_url === 'string' ? settings.credit_buy_url : '')
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
    setEditor({
      open: true,
      draft: {
        featuresText: '',
        price_usd: 0,
        price_cny: 0,
        researchEnabled: false,
        workspacesEnabled: false,
        is_public: true,
        max_workspaces: 0,
        max_storage_mb: 0,
        creditPeriodValue: 0,
        creditPeriodUnit: 'day',
      },
    })
  }
  function openEdit(row: ApiUserGroup) {
    const feats = row.features ?? []
    const period = splitPeriod(row.credit_period_seconds ?? 0)
    setEditor({
      open: true,
      row,
      draft: {
        ...row,
        // Hide the reserved functional flag from the marketing free-text editor.
        featuresText: feats.filter((f) => f !== RESEARCH_FEATURE && f !== WORKSPACES_FEATURE).join('\n'),
        researchEnabled: feats.includes(RESEARCH_FEATURE),
        workspacesEnabled: feats.includes(WORKSPACES_FEATURE),
        creditPeriodValue: period.value,
        creditPeriodUnit: period.unit,
      },
    })
  }
  function setDraft(p: Partial<Draft>) {
    setEditor((ed) => ({ ...ed, draft: { ...ed.draft, ...p } }))
  }

  async function submit() {
    if (savingRef.current) return
    const d = editor.draft
    if (!d.name?.trim()) {
      toast.error(t('admin:groups.errors.nameRequired'))
      return
    }
    const marketing = (d.featuresText ?? '')
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean)
      .filter((f) => f !== RESEARCH_FEATURE && f !== WORKSPACES_FEATURE)
    // Append the reserved functional flags for the enabled toggles.
    const features = [
      ...marketing,
      ...(d.researchEnabled ? [RESEARCH_FEATURE] : []),
      ...(d.workspacesEnabled ? [WORKSPACES_FEATURE] : []),
    ]
    const periodSeconds = Math.max(0, Number(d.creditPeriodValue) || 0) * UNIT_SECONDS[d.creditPeriodUnit ?? 'day']
    const body: Partial<ApiUserGroup> = {
      name: d.name,
      description: d.description ?? '',
      features,
      price_usd: Number(d.price_usd) || 0,
      price_cny: Number(d.price_cny) || 0,
      max_projects: Math.max(0, Number(d.max_projects) || 0),
      max_kbs: Math.max(0, Number(d.max_kbs) || 0),
      max_workspaces: Math.max(0, Number(d.max_workspaces) || 0),
      max_storage_mb: Math.max(0, Number(d.max_storage_mb) || 0),
      is_public: d.is_public !== false,
      credit_allowance: Math.max(0, Number(d.credit_allowance) || 0),
      credit_period_seconds: periodSeconds,
    }
    savingRef.current = true
    setSaving(true)
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

  async function remove(row: ApiUserGroup) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeUserGroup(row.id)
      toast.success(t('admin:groups.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  async function saveMsg() {
    setSavingMsg(true)
    try {
      await adminApi.updateSettings({
        quota_exceeded_message: quotaMsg,
        credits_per_usd: Math.max(0, Number(creditsPerUsd) || 0),
        group_buy_url: groupBuyUrl.trim(),
        credit_buy_url: creditBuyUrl.trim(),
      })
      toast.success(t('admin:groups.msgSaved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSavingMsg(false)
    }
  }

  function persistOrder(next: ApiUserGroup[], prev: ApiUserGroup[]) {
    void adminApi.reorderUserGroups(next.map((g) => g.id)).catch((e) => {
      setRows(prev)
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
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
          <PanelFallback />
        ) : (
          <AdminSortableList
            items={rows}
            onItemsChange={setRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            rowClassName="grid grid-cols-[auto_auto_1fr_auto_auto] gap-3 items-center px-5 py-4"
            renderItem={(g) => (
              <>
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
              </>
            )}
          />
        )}
      </section>

      {/* Global credit settings + over-quota prompt */}
      <section className="mt-8 max-w-xl flex flex-col gap-5">
        <Field
          label={t('admin:groups.creditsRatioLabel')}
          htmlFor="credits-per-usd"
          hint={t('admin:groups.creditsRatioHint')}
        >
          <Input
            id="credits-per-usd"
            type="number"
            min={0}
            value={String(creditsPerUsd)}
            onChange={(e) => setCreditsPerUsd(Math.max(0, Number(e.target.value) || 0))}
          />
        </Field>
        <Field
          label={t('admin:groups.groupBuyUrlLabel')}
          htmlFor="group-buy-url"
          hint={t('admin:groups.groupBuyUrlHint')}
        >
          <Input
            id="group-buy-url"
            value={groupBuyUrl}
            placeholder="https://…"
            onChange={(e) => setGroupBuyUrl(e.target.value)}
          />
        </Field>
        <Field
          label={t('admin:groups.creditBuyUrlLabel')}
          htmlFor="credit-buy-url"
          hint={t('admin:groups.creditBuyUrlHint')}
        >
          <Input
            id="credit-buy-url"
            value={creditBuyUrl}
            placeholder="https://…"
            onChange={(e) => setCreditBuyUrl(e.target.value)}
          />
        </Field>
        <Field label={t('admin:groups.quotaMsgLabel')} htmlFor="quota-msg" hint={t('admin:groups.quotaMsgHint')}>
          <Textarea
            id="quota-msg"
            rows={3}
            value={quotaMsg}
            onChange={(e) => setQuotaMsg(e.target.value)}
            placeholder={t('admin:groups.quotaMsgPlaceholder')}
          />
        </Field>
        <div className="flex justify-end">
          <Button variant="secondary" loading={savingMsg} onClick={() => void saveMsg()}>
            {t('common:actions.save')}
          </Button>
        </div>
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => !savingRef.current && setEditor({ ...editor, open: o })}>
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
                <Field
                  label={t('admin:groups.fields.maxWorkspaces', { defaultValue: 'Max workspaces' })}
                  htmlFor="g-maxws"
                  hint={t('admin:groups.fields.limitHint')}
                >
                  <Input
                    id="g-maxws"
                    type="number"
                    min={0}
                    value={String(editor.draft.max_workspaces ?? 0)}
                    onChange={(e) => setDraft({ max_workspaces: Number(e.target.value) })}
                  />
                </Field>
                <Field
                  label={t('admin:groups.maxStorage', { defaultValue: 'Storage (MB)' })}
                  htmlFor="g-maxstorage"
                  hint={t('admin:groups.maxStorageHint', { defaultValue: '0 = unlimited. Non-image uploads only.' })}
                >
                  <Input
                    id="g-maxstorage"
                    type="number"
                    min={0}
                    value={String(editor.draft.max_storage_mb ?? 0)}
                    onChange={(e) => setDraft({ max_storage_mb: Number(e.target.value) })}
                  />
                </Field>
              </div>
              {/* Credit system (§ credits). The USD→credit rate is a global
                  setting (below the group list); per-group: allowance + period. */}
              <div className="pt-1 border-t border-[var(--color-divider)]">
                <h2 className="pt-3 font-serif text-lg tracking-tight text-[var(--color-fg)]">{t('admin:groups.fields.creditsSection')}</h2>
                <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">{t('admin:groups.fields.creditsLead')}</p>
              </div>
              <Field label={t('admin:groups.fields.creditAllowance')} htmlFor="g-allow" hint={t('admin:groups.fields.creditAllowanceHint')}>
                <Input
                  id="g-allow"
                  type="number"
                  min={0}
                  value={String(editor.draft.credit_allowance ?? 0)}
                  onChange={(e) => setDraft({ credit_allowance: Number(e.target.value) })}
                />
              </Field>
              <Field label={t('admin:groups.fields.creditPeriod')} hint={t('admin:groups.fields.creditPeriodHint')}>
                <div className="flex items-stretch gap-2">
                  <Input
                    type="number"
                    min={0}
                    aria-label={t('admin:groups.fields.creditPeriod')}
                    value={String(editor.draft.creditPeriodValue ?? 0)}
                    onChange={(e) => setDraft({ creditPeriodValue: Number(e.target.value) })}
                    wrapperClassName="flex-1 min-w-0"
                  />
                  <Select
                    value={editor.draft.creditPeriodUnit ?? 'day'}
                    onValueChange={(v) => setDraft({ creditPeriodUnit: v as PeriodUnit })}
                  >
                    <SelectTrigger className="w-[120px] shrink-0">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="hour">{t('admin:groups.fields.unitHour')}</SelectItem>
                      <SelectItem value="day">{t('admin:groups.fields.unitDay')}</SelectItem>
                      <SelectItem value="week">{t('admin:groups.fields.unitWeek')}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </Field>

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
              <div className="flex items-center justify-between gap-3 rounded-[10px] border border-[var(--color-border)] px-3 py-2.5">
                <div className="min-w-0">
                  <p className="text-sm text-[var(--color-fg)]">{t('admin:groups.fields.workspaces', { defaultValue: 'Workspaces' })}</p>
                  <p className="text-[12px] text-[var(--color-fg-subtle)]">
                    {t('admin:groups.fields.workspacesHint', { defaultValue: 'Allow this group to create workspaces (max above; 0 = unlimited).' })}
                  </p>
                </div>
                <Switch
                  checked={Boolean(editor.draft.workspacesEnabled)}
                  onCheckedChange={(v) => setDraft({ workspacesEnabled: v })}
                  aria-label={t('admin:groups.fields.workspaces', { defaultValue: 'Workspaces' })}
                />
              </div>
              <div className="flex items-center justify-between gap-3 rounded-[10px] border border-[var(--color-border)] px-3 py-2.5">
                <div className="min-w-0">
                  <p className="text-sm text-[var(--color-fg)]">{t('admin:groups.fields.isPublic', { defaultValue: 'Show on subscription page' })}</p>
                  <p className="text-[12px] text-[var(--color-fg-subtle)]">
                    {t('admin:groups.fields.isPublicHint', { defaultValue: 'When off, this tier is hidden from the public subscription page (members keep their plan).' })}
                  </p>
                </div>
                <Switch
                  checked={editor.draft.is_public !== false}
                  onCheckedChange={(v) => setDraft({ is_public: v })}
                  aria-label={t('admin:groups.fields.isPublic', { defaultValue: 'Show on subscription page' })}
                />
              </div>
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
            <DialogTitle>{t('admin:groups.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:groups.removeBody', { name: confirmDelete.name }) : ''}
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
