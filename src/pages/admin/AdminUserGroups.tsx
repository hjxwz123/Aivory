/**
 * AdminUserGroups — manage membership tiers (§ user groups). Each group has a
 * name, short description, settlement price and a feature list.
 * Per-model usage caps live on the model editor; this page also holds the global
 * "quota exceeded / upgrade" prompt shown when a user hits a model's limit.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiCreditPackage, ApiUserGroup } from '@/api/types'
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
import {
  currencyInputStep,
  formatCurrencyMinor,
  inputAmountToMinor,
  minorAmountToInput,
  normalizeSettlementCurrency,
} from '@/lib/currency'

type PeriodUnit = 'hour' | 'day' | 'week'
type Draft = Partial<ApiUserGroup> & {
  featuresText?: string
  researchEnabled?: boolean
  workspacesEnabled?: boolean
  creditPeriodValue?: number
  creditPeriodUnit?: PeriodUnit
  monthlyPriceInput?: string
  yearlyPriceInput?: string
}

type CreditPackageDraft = Partial<ApiCreditPackage> & {
  priceInput?: string
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
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiUserGroup[]>([])
  const [creditPackages, setCreditPackages] = useState<ApiCreditPackage[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiUserGroup; draft: Draft }>({ open: false, draft: {} })
  const [packageEditor, setPackageEditor] = useState<{
    open: boolean
    row?: ApiCreditPackage
    draft: CreditPackageDraft
  }>({ open: false, draft: {} })
  const [confirmDelete, setConfirmDelete] = useState<ApiUserGroup | null>(null)
  const [confirmPackageDelete, setConfirmPackageDelete] = useState<ApiCreditPackage | null>(null)
  // Global over-quota / purchase settings + the internal USD→credit rate.
  const [quotaMsg, setQuotaMsg] = useState('')
  const [creditsPerUsd, setCreditsPerUsd] = useState(0)
  const [settlementCurrency, setSettlementCurrency] = useState('USD')
  const [groupBuyUrl, setGroupBuyUrl] = useState('')
  const [creditBuyUrl, setCreditBuyUrl] = useState('')
  const [savingMsg, setSavingMsg] = useState(false)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [packageSaving, setPackageSaving] = useState(false)
  const packageSavingRef = useRef(false)
  const [packageBusyId, setPackageBusyId] = useState<string | null>(null)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const [packageDeleting, setPackageDeleting] = useState(false)
  const packageDeletingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      const [groups, settings, packages] = await Promise.all([
        adminApi.userGroups(),
        adminApi.settings(),
        adminApi.creditPackages(),
      ])
      setRows(groups)
      setCreditPackages(packages)
      const currency = normalizeSettlementCurrency(settings.settlement_currency)
      setSettlementCurrency(currency)
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
        monthlyPriceInput: minorAmountToInput(0, settlementCurrency, i18n.resolvedLanguage),
        yearlyPriceInput: minorAmountToInput(0, settlementCurrency, i18n.resolvedLanguage),
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
        monthlyPriceInput: minorAmountToInput(
          row.monthly_price_amount_minor ?? 0,
          settlementCurrency,
          i18n.resolvedLanguage,
        ),
        yearlyPriceInput: minorAmountToInput(
          row.yearly_price_amount_minor ?? 0,
          settlementCurrency,
          i18n.resolvedLanguage,
        ),
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

  function openNewPackage() {
    setPackageEditor({
      open: true,
      draft: {
        name: '',
        description: '',
        credits: 0,
        priceInput: minorAmountToInput(0, settlementCurrency, i18n.resolvedLanguage),
        enabled: true,
        sort_order: creditPackages.length,
      },
    })
  }

  function openEditPackage(row: ApiCreditPackage) {
    setPackageEditor({
      open: true,
      row,
      draft: {
        ...row,
        priceInput: minorAmountToInput(row.price_amount_minor, settlementCurrency, i18n.resolvedLanguage),
      },
    })
  }

  function setPackageDraft(patch: Partial<CreditPackageDraft>) {
    setPackageEditor((current) => ({ ...current, draft: { ...current.draft, ...patch } }))
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
    const monthlyPriceAmountMinor = inputAmountToMinor(
      d.monthlyPriceInput ?? '',
      settlementCurrency,
      i18n.resolvedLanguage,
    )
    const yearlyPriceAmountMinor = inputAmountToMinor(
      d.yearlyPriceInput ?? '',
      settlementCurrency,
      i18n.resolvedLanguage,
    )
    if (monthlyPriceAmountMinor === null || yearlyPriceAmountMinor === null) {
      toast.error(t('admin:groups.errors.priceInvalid'))
      return
    }
    const body: Partial<ApiUserGroup> = {
      name: d.name,
      description: d.description ?? '',
      features,
      monthly_price_amount_minor: monthlyPriceAmountMinor,
      yearly_price_amount_minor: yearlyPriceAmountMinor,
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

  async function submitPackage() {
    if (packageSavingRef.current) return
    const draft = packageEditor.draft
    const name = draft.name?.trim() ?? ''
    if (!name) {
      toast.error(t('admin:groups.creditPackages.errors.nameRequired'))
      return
    }
    const credits = Number(draft.credits)
    if (!Number.isFinite(credits) || credits <= 0) {
      toast.error(t('admin:groups.creditPackages.errors.creditsInvalid'))
      return
    }
    const priceAmountMinor = inputAmountToMinor(
      draft.priceInput ?? '',
      settlementCurrency,
      i18n.resolvedLanguage,
    )
    if (priceAmountMinor === null) {
      toast.error(t('admin:groups.creditPackages.errors.priceInvalid'))
      return
    }
    const body: Partial<ApiCreditPackage> = {
      name,
      description: draft.description?.trim() ?? '',
      credits,
      price_amount_minor: priceAmountMinor,
      enabled: draft.enabled !== false,
      sort_order: Math.max(0, Number(draft.sort_order) || 0),
    }

    packageSavingRef.current = true
    setPackageSaving(true)
    try {
      if (packageEditor.row) {
        const updated = await adminApi.updateCreditPackage(packageEditor.row.id, body)
        setCreditPackages((items) => items.map((item) => (item.id === updated.id ? updated : item)))
        toast.success(t('admin:groups.creditPackages.updated'))
      } else {
        const created = await adminApi.createCreditPackage(body)
        setCreditPackages((items) => [...items, created].sort((a, b) => a.sort_order - b.sort_order))
        toast.success(t('admin:groups.creditPackages.created'))
      }
      setPackageEditor({ open: false, draft: {} })
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      packageSavingRef.current = false
      setPackageSaving(false)
    }
  }

  async function togglePackage(row: ApiCreditPackage, enabled: boolean) {
    if (packageBusyId) return
    setPackageBusyId(row.id)
    try {
      const updated = await adminApi.updateCreditPackage(row.id, { enabled })
      setCreditPackages((items) => items.map((item) => (item.id === updated.id ? updated : item)))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setPackageBusyId(null)
    }
  }

  async function removePackage(row: ApiCreditPackage) {
    if (packageDeletingRef.current) return
    packageDeletingRef.current = true
    setPackageDeleting(true)
    try {
      await adminApi.removeCreditPackage(row.id)
      setCreditPackages((items) => items.filter((item) => item.id !== row.id))
      setConfirmPackageDelete(null)
      toast.success(t('admin:groups.creditPackages.removed'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      packageDeletingRef.current = false
      setPackageDeleting(false)
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

  function persistPackageOrder(next: ApiCreditPackage[], prev: ApiCreditPackage[]) {
    void adminApi.reorderCreditPackages(next.map((item) => item.id)).catch((e) => {
      setCreditPackages(prev)
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
                      {g.monthly_price_amount_minor > 0
                        ? `${formatCurrencyMinor(g.monthly_price_amount_minor, settlementCurrency, i18n.resolvedLanguage)} ${t('admin:groups.monthlyShort')}`
                        : null}
                      {g.monthly_price_amount_minor > 0 && g.yearly_price_amount_minor > 0 ? ' · ' : null}
                      {g.yearly_price_amount_minor > 0
                        ? `${formatCurrencyMinor(g.yearly_price_amount_minor, settlementCurrency, i18n.resolvedLanguage)} ${t('admin:groups.yearlyShort')}`
                        : null}
                      {g.monthly_price_amount_minor <= 0 && g.yearly_price_amount_minor <= 0
                        ? t('admin:groups.freePrice')
                        : null}
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

      <section className="mt-10">
        <div className="flex items-end justify-between gap-4">
          <div>
            <h2 className="font-serif text-xl tracking-tight text-[var(--color-fg)]">
              {t('admin:groups.creditPackages.title')}
            </h2>
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
              {t('admin:groups.creditPackages.lead')}
            </p>
          </div>
          <Button
            variant="secondary"
            size="sm"
            leadingIcon={<Plus size={14} aria-hidden />}
            onClick={openNewPackage}
          >
            {t('admin:groups.creditPackages.new')}
          </Button>
        </div>

        {creditPackages.length === 0 ? (
          <p className="mt-4 rounded-[10px] border border-[var(--color-border)] px-4 py-5 text-sm text-[var(--color-fg-muted)]">
            {t('admin:groups.creditPackages.empty')}
          </p>
        ) : (
          <AdminSortableList
            items={creditPackages}
            onItemsChange={setCreditPackages}
            onOrderCommit={persistPackageOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            listClassName="mt-4"
            rowClassName="grid grid-cols-[auto_auto_minmax(0,1fr)_auto_auto] gap-3 items-center px-4 py-3"
            renderItem={(item) => (
              <>
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium text-[var(--color-fg)]">{item.name}</span>
                    {!item.enabled ? (
                      <Badge size="xs" variant="neutral">{t('admin:groups.creditPackages.disabled')}</Badge>
                    ) : null}
                  </div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-2 text-[12px] text-[var(--color-fg-subtle)]">
                    <span>{t('admin:groups.creditPackages.creditCount', { count: item.credits.toLocaleString(i18n.resolvedLanguage) })}</span>
                    <span aria-hidden>·</span>
                    <span className="tabular-nums">
                      {formatCurrencyMinor(item.price_amount_minor, settlementCurrency, i18n.resolvedLanguage)}
                    </span>
                    {item.description ? <span className="basis-full truncate">{item.description}</span> : null}
                  </div>
                </div>
                <Switch
                  checked={item.enabled}
                  disabled={packageBusyId === item.id}
                  onCheckedChange={(enabled) => void togglePackage(item, enabled)}
                  aria-label={t('admin:groups.creditPackages.enabledLabel', { name: item.name })}
                />
                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    leadingIcon={<Pencil size={14} aria-hidden />}
                    onClick={() => openEditPackage(item)}
                    aria-label={`${t('admin:common.edit')}: ${item.name}`}
                  />
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    leadingIcon={<Trash2 size={14} aria-hidden />}
                    onClick={() => setConfirmPackageDelete(item)}
                    aria-label={`${t('admin:common.remove')}: ${item.name}`}
                    className="text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)]"
                  />
                </div>
              </>
            )}
          />
        )}
      </section>

      {/* Global credit settings + over-quota prompt */}
      <section className="mt-8 flex w-full flex-col gap-5">
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
              <div className="grid gap-4 sm:grid-cols-2">
                <Field
                  label={t('admin:groups.fields.monthlyPrice', { currency: settlementCurrency })}
                  htmlFor="g-monthly-price"
                  hint={t('admin:groups.fields.priceHint')}
                >
                  <Input
                    id="g-monthly-price"
                    type="number"
                    min={0}
                    step={currencyInputStep(settlementCurrency, i18n.resolvedLanguage)}
                    value={editor.draft.monthlyPriceInput ?? ''}
                    onChange={(e) => setDraft({ monthlyPriceInput: e.target.value })}
                  />
                </Field>
                <Field
                  label={t('admin:groups.fields.yearlyPrice', { currency: settlementCurrency })}
                  htmlFor="g-yearly-price"
                  hint={t('admin:groups.fields.priceHint')}
                >
                  <Input
                    id="g-yearly-price"
                    type="number"
                    min={0}
                    step={currencyInputStep(settlementCurrency, i18n.resolvedLanguage)}
                    value={editor.draft.yearlyPriceInput ?? ''}
                    onChange={(e) => setDraft({ yearlyPriceInput: e.target.value })}
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

      <Dialog
        open={packageEditor.open}
        onOpenChange={(open) => {
          if (!packageSavingRef.current) {
            setPackageEditor((current) => ({ ...current, open }))
          }
        }}
      >
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>
              {packageEditor.row
                ? t('admin:groups.creditPackages.editorTitle')
                : t('admin:groups.creditPackages.newTitle')}
            </DialogTitle>
            <DialogDescription>{t('admin:groups.creditPackages.editorLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('admin:groups.creditPackages.fields.name')} htmlFor="credit-package-name">
                <Input
                  id="credit-package-name"
                  value={packageEditor.draft.name ?? ''}
                  onChange={(event) => setPackageDraft({ name: event.target.value })}
                />
              </Field>
              <Field
                label={t('admin:groups.creditPackages.fields.description')}
                htmlFor="credit-package-description"
              >
                <Textarea
                  id="credit-package-description"
                  rows={3}
                  value={packageEditor.draft.description ?? ''}
                  onChange={(event) => setPackageDraft({ description: event.target.value })}
                  placeholder={t('admin:groups.creditPackages.fields.descriptionPlaceholder')}
                />
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field
                  label={t('admin:groups.creditPackages.fields.credits')}
                  htmlFor="credit-package-credits"
                  hint={t('admin:groups.creditPackages.fields.creditsHint')}
                >
                  <Input
                    id="credit-package-credits"
                    type="number"
                    min={0}
                    step="any"
                    value={String(packageEditor.draft.credits ?? 0)}
                    onChange={(event) => setPackageDraft({ credits: Number(event.target.value) })}
                  />
                </Field>
                <Field
                  label={t('admin:groups.creditPackages.fields.price', { currency: settlementCurrency })}
                  htmlFor="credit-package-price"
                  hint={t('admin:groups.creditPackages.fields.priceHint')}
                >
                  <Input
                    id="credit-package-price"
                    type="number"
                    min={0}
                    step={currencyInputStep(settlementCurrency, i18n.resolvedLanguage)}
                    value={packageEditor.draft.priceInput ?? ''}
                    onChange={(event) => setPackageDraft({ priceInput: event.target.value })}
                  />
                </Field>
              </div>
              <div className="flex items-center justify-between gap-3 rounded-[10px] border border-[var(--color-border)] px-3 py-2.5">
                <div className="min-w-0">
                  <p className="text-sm text-[var(--color-fg)]">
                    {t('admin:groups.creditPackages.fields.enabled')}
                  </p>
                  <p className="text-[12px] text-[var(--color-fg-subtle)]">
                    {t('admin:groups.creditPackages.fields.enabledHint')}
                  </p>
                </div>
                <Switch
                  checked={packageEditor.draft.enabled !== false}
                  onCheckedChange={(enabled) => setPackageDraft({ enabled })}
                  aria-label={t('admin:groups.creditPackages.fields.enabled')}
                />
              </div>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={packageSaving}
              onClick={() => setPackageEditor((current) => ({ ...current, open: false }))}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button loading={packageSaving} onClick={() => void submitPackage()}>
              {t('common:actions.save')}
            </Button>
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

      <Dialog
        open={Boolean(confirmPackageDelete)}
        onOpenChange={(open) => {
          if (!open && !packageDeletingRef.current) setConfirmPackageDelete(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:groups.creditPackages.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmPackageDelete
                ? t('admin:groups.creditPackages.removeBody', { name: confirmPackageDelete.name })
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="ghost"
              disabled={packageDeleting}
              onClick={() => setConfirmPackageDelete(null)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive"
              loading={packageDeleting}
              onClick={() => confirmPackageDelete && void removePackage(confirmPackageDelete)}
            >
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
