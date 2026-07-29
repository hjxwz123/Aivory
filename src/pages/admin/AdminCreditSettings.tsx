/**
 * AdminCreditSettings owns deployment-wide credit, quota and settlement policy,
 * plus the purchasable permanent-credit packages. Membership tier benefits stay
 * on AdminUserGroups.
 */
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Pencil, Plus, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiCreditPackage } from '@/api/types'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
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
import {
  currencyInputStep,
  formatCurrencyMinor,
  inputAmountToMinor,
  isSettlementCurrencyCode,
  minorAmountToInput,
  normalizeSettlementCurrency,
} from '@/lib/currency'

type Settings = Record<string, unknown>

type CreditPackageDraft = Partial<ApiCreditPackage> & {
  priceInput?: string
}

const OWNED_KEYS = [
  'settlement_currency',
  'credits_per_usd',
  'daily_message_limit',
  'daily_image_limit',
  'daily_token_limit',
  'max_concurrent_generations',
  'credit_preflight_enabled',
  'quota_exceeded_message',
] as const

function nonNegativeNumber(value: number, integer = false): number {
  if (!Number.isFinite(value)) return 0
  const normalized = integer ? Math.floor(value) : value
  return Math.max(0, normalized)
}

export default function AdminCreditSettings() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [creditPackages, setCreditPackages] = useState<ApiCreditPackage[]>([])
  const [packageCurrency, setPackageCurrency] = useState('USD')
  const [loading, setLoading] = useState(true)
  const [savingSettings, setSavingSettings] = useState(false)
  const settingsSavingRef = useRef(false)
  const [packageEditor, setPackageEditor] = useState<{
    open: boolean
    row?: ApiCreditPackage
    draft: CreditPackageDraft
  }>({ open: false, draft: {} })
  const [packageSaving, setPackageSaving] = useState(false)
  const packageSavingRef = useRef(false)
  const [packageBusyId, setPackageBusyId] = useState<string | null>(null)
  const [confirmPackageDelete, setConfirmPackageDelete] = useState<ApiCreditPackage | null>(null)
  const [packageDeleting, setPackageDeleting] = useState(false)
  const packageDeletingRef = useRef(false)

  async function load() {
    setLoading(true)
    try {
      const [settings, packages] = await Promise.all([
        adminApi.settings(),
        adminApi.creditPackages(),
      ])
      const currency = normalizeSettlementCurrency(settings.settlement_currency)
      setDraft({ ...settings, settlement_currency: currency })
      setPackageCurrency(currency)
      setCreditPackages(packages)
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

  function readString(key: string, fallback = ''): string {
    const value = draft[key]
    return typeof value === 'string' ? value : fallback
  }

  function readNumber(key: string, fallback = 0): number {
    const value = draft[key]
    if (typeof value === 'number' && Number.isFinite(value)) return value
    if (typeof value === 'string' && value.trim() !== '') {
      const parsed = Number(value)
      if (Number.isFinite(parsed)) return parsed
    }
    return fallback
  }

  function readBool(key: string, fallback = false): boolean {
    const value = draft[key]
    if (typeof value === 'boolean') return value
    if (value === 'true') return true
    if (value === 'false') return false
    return fallback
  }

  function setSetting(key: string, value: unknown) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  async function saveSettings() {
    if (settingsSavingRef.current) return

    const currencyInput = readString('settlement_currency', packageCurrency).trim().toUpperCase()
    if (!isSettlementCurrencyCode(currencyInput)) {
      toast.error(t('admin:settings.fields.settlementCurrencyInvalid'))
      return
    }

    const values: Record<(typeof OWNED_KEYS)[number], unknown> = {
      settlement_currency: normalizeSettlementCurrency(currencyInput),
      credits_per_usd: nonNegativeNumber(readNumber('credits_per_usd')),
      daily_message_limit: nonNegativeNumber(readNumber('daily_message_limit', 200), true),
      daily_image_limit: nonNegativeNumber(readNumber('daily_image_limit', 30), true),
      daily_token_limit: nonNegativeNumber(readNumber('daily_token_limit'), true),
      max_concurrent_generations: nonNegativeNumber(readNumber('max_concurrent_generations', 3), true),
      credit_preflight_enabled: readBool('credit_preflight_enabled', true),
      quota_exceeded_message: readString('quota_exceeded_message'),
    }
    const patch: Settings = {}
    for (const key of OWNED_KEYS) patch[key] = values[key]

    settingsSavingRef.current = true
    setSavingSettings(true)
    try {
      await adminApi.updateSettings(patch)
      setDraft((current) => ({ ...current, ...values }))
      setPackageCurrency(values.settlement_currency as string)
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      settingsSavingRef.current = false
      setSavingSettings(false)
    }
  }

  function openNewPackage() {
    setPackageEditor({
      open: true,
      draft: {
        name: '',
        description: '',
        credits: 0,
        priceInput: minorAmountToInput(0, packageCurrency, i18n.resolvedLanguage),
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
        priceInput: minorAmountToInput(row.price_amount_minor, packageCurrency, i18n.resolvedLanguage),
      },
    })
  }

  function setPackageDraft(patch: Partial<CreditPackageDraft>) {
    setPackageEditor((current) => ({ ...current, draft: { ...current.draft, ...patch } }))
  }

  async function submitPackage() {
    if (packageSavingRef.current) return
    const packageDraft = packageEditor.draft
    const name = packageDraft.name?.trim() ?? ''
    if (!name) {
      toast.error(t('admin:groups.creditPackages.errors.nameRequired'))
      return
    }

    const credits = Number(packageDraft.credits)
    if (!Number.isFinite(credits) || credits <= 0) {
      toast.error(t('admin:groups.creditPackages.errors.creditsInvalid'))
      return
    }

    const priceAmountMinor = inputAmountToMinor(
      packageDraft.priceInput ?? '',
      packageCurrency,
      i18n.resolvedLanguage,
    )
    if (priceAmountMinor === null) {
      toast.error(t('admin:groups.creditPackages.errors.priceInvalid'))
      return
    }

    const body: Partial<ApiCreditPackage> = {
      name,
      description: packageDraft.description?.trim() ?? '',
      credits,
      price_amount_minor: priceAmountMinor,
      enabled: packageDraft.enabled !== false,
      sort_order: nonNegativeNumber(Number(packageDraft.sort_order), true),
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
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
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
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
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
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      packageDeletingRef.current = false
      setPackageDeleting(false)
    }
  }

  function persistPackageOrder(next: ApiCreditPackage[], previous: ApiCreditPackage[]) {
    void adminApi.reorderCreditPackages(next.map((item) => item.id)).catch((error) => {
      setCreditPackages(previous)
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    })
  }

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">
          {t('admin:creditSettings.title', { defaultValue: 'Credits and quotas' })}
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:creditSettings.lead', {
            defaultValue: 'Configure billing conversion, platform-wide usage limits and permanent-credit packages.',
          })}
        </p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <>
          <section className="mt-8">
            <div>
              <h2 className="font-serif text-xl tracking-tight text-[var(--color-fg)]">
                {t('admin:creditSettings.policyTitle', { defaultValue: 'Billing policy' })}
              </h2>
              <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
                {t('admin:creditSettings.policyLead', {
                  defaultValue: 'These settings apply to every member and every credit-charged model.',
                })}
              </p>
            </div>

            <div className="mt-5 grid gap-5 lg:grid-cols-2">
              <Field
                label={t('admin:settings.fields.settlementCurrency')}
                htmlFor="settlement-currency"
                hint={t('admin:settings.fields.settlementCurrencyHint')}
              >
                <Input
                  id="settlement-currency"
                  value={readString('settlement_currency', packageCurrency).toUpperCase()}
                  maxLength={3}
                  autoCapitalize="characters"
                  spellCheck={false}
                  className="font-mono uppercase"
                  onChange={(event) => setSetting('settlement_currency', event.target.value.toUpperCase())}
                />
              </Field>
              <Field
                label={t('admin:groups.creditsRatioLabel')}
                htmlFor="credits-per-usd"
                hint={t('admin:groups.creditsRatioHint')}
              >
                <Input
                  id="credits-per-usd"
                  type="number"
                  min={0}
                  step="any"
                  value={String(readNumber('credits_per_usd'))}
                  onChange={(event) => setSetting('credits_per_usd', nonNegativeNumber(Number(event.target.value)))}
                />
              </Field>
            </div>

            <div className="mt-8 border-t border-[var(--color-divider)] pt-6">
              <h2 className="font-serif text-xl tracking-tight text-[var(--color-fg)]">
                {t('admin:creditSettings.limitsTitle', { defaultValue: 'Platform limits' })}
              </h2>
              <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
                {t('admin:creditSettings.limitsLead', {
                  defaultValue: 'Per-user limits. Set a numeric limit to 0 to leave that limit unrestricted.',
                })}
              </p>

              <div className="mt-5 grid gap-5 sm:grid-cols-2">
                <Field
                  label={t('admin:settings.fields.dailyMessageLimit')}
                  htmlFor="daily-message-limit"
                  hint={t('admin:creditSettings.zeroUnlimited', { defaultValue: '0 = unlimited.' })}
                >
                  <Input
                    id="daily-message-limit"
                    type="number"
                    min={0}
                    step={1}
                    value={String(readNumber('daily_message_limit', 200))}
                    onChange={(event) => setSetting('daily_message_limit', nonNegativeNumber(Number(event.target.value), true))}
                  />
                </Field>
                <Field
                  label={t('admin:settings.fields.dailyImageLimit')}
                  htmlFor="daily-image-limit"
                  hint={t('admin:creditSettings.zeroUnlimited', { defaultValue: '0 = unlimited.' })}
                >
                  <Input
                    id="daily-image-limit"
                    type="number"
                    min={0}
                    step={1}
                    value={String(readNumber('daily_image_limit', 30))}
                    onChange={(event) => setSetting('daily_image_limit', nonNegativeNumber(Number(event.target.value), true))}
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.dailyTokenLimit', { defaultValue: 'Daily token limit' })}
                  htmlFor="daily-token-limit"
                  hint={t('admin:creditSettings.dailyTokenLimitHint', {
                    defaultValue: 'Maximum input plus output tokens per user per UTC day. 0 = unlimited.',
                  })}
                >
                  <Input
                    id="daily-token-limit"
                    type="number"
                    min={0}
                    step={1}
                    value={String(readNumber('daily_token_limit'))}
                    onChange={(event) => setSetting('daily_token_limit', nonNegativeNumber(Number(event.target.value), true))}
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.maxConcurrentGenerations', {
                    defaultValue: 'Concurrent generations per user',
                  })}
                  htmlFor="max-concurrent-generations"
                  hint={t('admin:creditSettings.maxConcurrentGenerationsHint', {
                    defaultValue: 'Maximum active response streams per user. 0 = unlimited.',
                  })}
                >
                  <Input
                    id="max-concurrent-generations"
                    type="number"
                    min={0}
                    step={1}
                    value={String(readNumber('max_concurrent_generations', 3))}
                    onChange={(event) => setSetting('max_concurrent_generations', nonNegativeNumber(Number(event.target.value), true))}
                  />
                </Field>
              </div>

              <div className="mt-5">
                <ToggleRow
                  label={t('admin:settings.fields.preflightEnabled')}
                  checked={readBool('credit_preflight_enabled', true)}
                  onChange={(enabled) => setSetting('credit_preflight_enabled', enabled)}
                />
                <p className="mt-2 pl-1 text-xs text-[var(--color-fg-subtle)]">
                  {t('admin:settings.fields.preflightLead')}
                </p>
              </div>
            </div>

            <div className="mt-8 border-t border-[var(--color-divider)] pt-6">
              <Field
                label={t('admin:groups.quotaMsgLabel')}
                htmlFor="quota-message"
                hint={t('admin:groups.quotaMsgHint')}
              >
                <Textarea
                  id="quota-message"
                  rows={3}
                  value={readString('quota_exceeded_message')}
                  onChange={(event) => setSetting('quota_exceeded_message', event.target.value)}
                  placeholder={t('admin:groups.quotaMsgPlaceholder')}
                />
              </Field>
            </div>

            <div className="mt-6 flex justify-end">
              <Button loading={savingSettings} onClick={() => void saveSettings()}>
                {t('common:actions.save')}
              </Button>
            </div>
          </section>

          <section className="mt-10 border-t border-[var(--color-divider)] pt-8">
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
              <p className="mt-4 rounded-[8px] border border-[var(--color-border)] px-4 py-5 text-sm text-[var(--color-fg-muted)]">
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
                        <span>
                          {t('admin:groups.creditPackages.creditCount', {
                            count: item.credits.toLocaleString(i18n.resolvedLanguage),
                          })}
                        </span>
                        <span aria-hidden>·</span>
                        <span className="tabular-nums">
                          {formatCurrencyMinor(item.price_amount_minor, packageCurrency, i18n.resolvedLanguage)}
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
        </>
      )}

      <Dialog
        open={packageEditor.open}
        onOpenChange={(open) => {
          if (!packageSavingRef.current) setPackageEditor((current) => ({ ...current, open }))
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
                  label={t('admin:groups.creditPackages.fields.price', { currency: packageCurrency })}
                  htmlFor="credit-package-price"
                  hint={t('admin:groups.creditPackages.fields.priceHint')}
                >
                  <Input
                    id="credit-package-price"
                    type="number"
                    min={0}
                    step={currencyInputStep(packageCurrency, i18n.resolvedLanguage)}
                    value={packageEditor.draft.priceInput ?? ''}
                    onChange={(event) => setPackageDraft({ priceInput: event.target.value })}
                  />
                </Field>
              </div>
              <div className="flex items-center justify-between gap-3 rounded-[8px] border border-[var(--color-border)] px-3 py-2.5">
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

function ToggleRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (value: boolean) => void }) {
  return (
    <label className="flex items-center justify-between rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
      <span className="text-sm">{label}</span>
      <Switch checked={checked} onCheckedChange={onChange} />
    </label>
  )
}
