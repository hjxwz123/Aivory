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
import { DocmeeTemplateAdmin } from '@/components/admin/docmee-templates'
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/hooks/use-toast'
import {
  docmeeEnabledPatch as resolveDocmeeEnabledPatch,
  storedDocmeeEnabled,
} from '@/lib/aippt-admin-settings'
import { useAiPPT } from '@/store/aippt'
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
  // § AI PPT (Docmee, API mode): the API key is used server-side only and comes
  // back masked; credits_per_ppt is the flat price per generated deck, and
  // edit_credits prices an edit (AI rewrite / template change; 0 = free).
  'docmee_enabled',
  'docmee_api_key',
  'docmee_api_base_url',
  'docmee_credits_per_ppt',
  'docmee_edit_credits',
  'docmee_default_template_id',
  'docmee_max_upload_mb',
  // Editor surface only: the vendor iframe is used just for slide-level editing.
  'docmee_sdk_url',
  'docmee_domain',
  'docmee_token_hours',
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
  /**
   * True once the admin has flipped the AI PPT switch in this session. The switch
   * writes immediately, so the general Save must not overwrite it with a derived
   * value — and must not persist a derived `false` at all (see docmeeEnabledPatch).
   */
  const [docmeeEnabledTouched, setDocmeeEnabledTouched] = useState(false)
  const [docmeeSwitchSaving, setDocmeeSwitchSaving] = useState(false)
  /** Deployment-side Docmee balance (vendor credits), fetched on demand. */
  const [vendor, setVendor] = useState<{ available_count: number; used_count: number } | null>(null)
  const [vendorLoading, setVendorLoading] = useState(false)
  const [vendorError, setVendorError] = useState<string | null>(null)
  const [packageEditor, setPackageEditor] = useState<{
    open: boolean
    row?: ApiCreditPackage
    draft: CreditPackageDraft
  }>({ open: false, draft: {} })
  const [packageSaving, setPackageSaving] = useState(false)
  const packageSavingRef = useRef(false)
  const [packageBusyIds, setPackageBusyIds] = useState<Set<string>>(() => new Set())
  const packageBusyIdsRef = useRef(new Set<string>())
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

  /**
   * The vendor's own credit balance. Not part of `load()`: it is an extra
   * upstream call, and a failure must not block the settings page.
   */
  async function loadVendor() {
    if (vendorLoading) return
    setVendorLoading(true)
    setVendorError(null)
    try {
      setVendor(await adminApi.aipptVendor())
    } catch (error) {
      setVendorError(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setVendorLoading(false)
    }
  }

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

  /** The stored AI PPT enable flag (null = never written; the server follows the key). */
  function docmeeEnabled(): boolean | null {
    return storedDocmeeEnabled(draft)
  }

  /**
   * The value a general Save should send for `docmee_enabled`, or undefined to
   * leave the stored value untouched. See src/lib/aippt-admin-settings.ts for the
   * regression this guards (a derived `false` used to survive refreshes and keep
   * an otherwise configured integration switched off).
   */
  function docmeeEnabledPatch(): boolean | undefined {
    return resolveDocmeeEnabledPatch({
      stored: docmeeEnabled(),
      keyInput: readString('docmee_api_key'),
      touched: docmeeEnabledTouched,
    })
  }

  /**
   * The AI PPT switch persists on flip. A switch is a stateful control: requiring
   * a separate Save click meant "I turned it on, refreshed, and it was off again".
   */
  async function toggleDocmeeEnabled(enabled: boolean) {
    setSetting('docmee_enabled', enabled)
    setDocmeeEnabledTouched(true)
    if (docmeeSwitchSaving) return
    setDocmeeSwitchSaving(true)
    try {
      await adminApi.updateSettings({ docmee_enabled: enabled })
      // The sidebar entry and the /ppt page read this cached config.
      void useAiPPT.getState().load(true)
      toast.success(
        t(enabled ? 'admin:creditSettings.docmee.enabledToast' : 'admin:creditSettings.docmee.disabledToast', {
          defaultValue: enabled ? 'AI PPT is enabled' : 'AI PPT is disabled',
        }),
      )
    } catch (error) {
      // Roll the draft back so the switch never shows a state the server rejected.
      setSetting('docmee_enabled', !enabled)
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      setDocmeeSwitchSaving(false)
    }
  }

  async function saveSettings() {
    if (settingsSavingRef.current) return

    const currencyInput = readString('settlement_currency', packageCurrency).trim().toUpperCase()
    if (!isSettlementCurrencyCode(currencyInput)) {
      toast.error(t('admin:settings.fields.settlementCurrencyInvalid'))
      return
    }

    type OwnedKey = (typeof OWNED_KEYS)[number]
    // `docmee_enabled` is deliberately absent here: it is written only when the
    // admin expressed an intent this session (see docmeeEnabledPatch).
    const values: Record<Exclude<OwnedKey, 'docmee_enabled'>, unknown> = {
      settlement_currency: normalizeSettlementCurrency(currencyInput),
      credits_per_usd: nonNegativeNumber(readNumber('credits_per_usd')),
      daily_message_limit: nonNegativeNumber(readNumber('daily_message_limit', 200), true),
      daily_image_limit: nonNegativeNumber(readNumber('daily_image_limit', 30), true),
      daily_token_limit: nonNegativeNumber(readNumber('daily_token_limit'), true),
      max_concurrent_generations: nonNegativeNumber(readNumber('max_concurrent_generations', 3), true),
      credit_preflight_enabled: readBool('credit_preflight_enabled', true),
      quota_exceeded_message: readString('quota_exceeded_message'),
      // § AI PPT. A masked key ("••••••") is echoed back unchanged — the server
      // treats the display mask as "keep the stored value".
      docmee_api_key: readString('docmee_api_key'),
      docmee_api_base_url: readString('docmee_api_base_url').trim(),
      docmee_credits_per_ppt: nonNegativeNumber(readNumber('docmee_credits_per_ppt', 10)),
      docmee_edit_credits: nonNegativeNumber(readNumber('docmee_edit_credits', 0)),
      docmee_default_template_id: readString('docmee_default_template_id').trim(),
      docmee_max_upload_mb: nonNegativeNumber(readNumber('docmee_max_upload_mb', 50), true),
      docmee_sdk_url: readString('docmee_sdk_url').trim(),
      docmee_domain: readString('docmee_domain').trim(),
      docmee_token_hours: nonNegativeNumber(readNumber('docmee_token_hours', 2), true),
    }
    const patch: Settings = {}
    for (const key of OWNED_KEYS) {
      if (key === 'docmee_enabled') continue
      patch[key] = values[key]
    }    // Only ever send the enable flag deliberately (see docmeeEnabledPatch).
    const enabledPatch = docmeeEnabledPatch()
    if (enabledPatch !== undefined) patch.docmee_enabled = enabledPatch

    settingsSavingRef.current = true
    setSavingSettings(true)
    try {
      await adminApi.updateSettings(patch)
      setDraft((current) => ({
        ...current,
        ...values,
        ...(enabledPatch === undefined ? {} : { docmee_enabled: enabledPatch }),
      }))
      setPackageCurrency(values.settlement_currency as string)
      // Reflect the new state in the sidebar / /ppt page without a manual reload.
      void useAiPPT.getState().load(true)
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
    if (packageBusyIdsRef.current.has(row.id)) return
    packageBusyIdsRef.current.add(row.id)
    setPackageBusyIds(new Set(packageBusyIdsRef.current))
    setCreditPackages((items) => items.map((item) => (
      item.id === row.id ? { ...item, enabled } : item
    )))
    try {
      const updated = await adminApi.updateCreditPackage(row.id, { enabled })
      setCreditPackages((items) => items.map((item) => (item.id === updated.id ? updated : item)))
    } catch (error) {
      setCreditPackages((items) => items.map((item) => (
        item.id === row.id ? { ...item, enabled: row.enabled } : item
      )))
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      packageBusyIdsRef.current.delete(row.id)
      setPackageBusyIds(new Set(packageBusyIdsRef.current))
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
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
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
            <div>
              <h2 className="font-serif text-xl tracking-tight text-[var(--color-fg)]">
                {t('admin:creditSettings.docmee.title', { defaultValue: 'AI PPT (文多多)' })}
              </h2>
              <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
                {t('admin:creditSettings.docmee.lead', {
                  defaultValue:
                    'Embeds the Docmee presentation workbench and charges a flat credit price per generated deck. Leave the API key empty to turn the feature off.',
                })}
              </p>
            </div>

            <div className="mt-5 flex flex-col gap-5">
              <ToggleRow
                label={t('admin:creditSettings.docmee.enabled', { defaultValue: 'Enable AI PPT' })}
                checked={docmeeEnabled() ?? readString('docmee_api_key').trim() !== ''}
                onChange={(value) => void toggleDocmeeEnabled(value)}
              />
              {/* Ambiguous states are called out instead of silently looking off:
                  a stored key with the switch off is the exact combination that
                  made an enabled integration appear to disable itself. */}
              {docmeeEnabled() === false && readString('docmee_api_key').trim() !== '' && (
                <p className="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2 text-xs text-[var(--color-fg-muted)]">
                  {t('admin:creditSettings.docmee.offWithKeyHint', {
                    defaultValue:
                      'A Docmee API key is configured but AI PPT is currently switched off. Turn the switch on to activate it.',
                  })}
                </p>
              )}
              {/* A price that cannot take effect is the other half of the same
                  confusion: ppt billing shares the platform-wide credit switch
                  (credits_per_usd), so a zero rate silently makes it free. */}
              {readNumber('docmee_credits_per_ppt') > 0 && readNumber('credits_per_usd') === 0 && (
                <p className="rounded-[8px] border border-[var(--color-warning)] px-3 py-2 text-xs text-[var(--color-warning)]">
                  {t('admin:creditSettings.docmee.creditsOffHint', {
                    price: readNumber('docmee_credits_per_ppt'),
                    defaultValue:
                      'A price of {{price}} credits per deck is set, but the platform-wide credit system is off (Model cost conversion (per USD) = 0): generations are free, no charge is recorded in Usage, and users see the feature as free. Set that conversion rate above zero to charge this price — the same switch also enables credit billing for chat.',
                  })}
                </p>
              )}

              <div className="grid gap-5 lg:grid-cols-2">
                <Field
                  label={t('admin:creditSettings.docmee.apiKey', { defaultValue: 'Docmee API key' })}
                  htmlFor="docmee-api-key"
                  hint={t('admin:creditSettings.docmee.apiKeyHint', {
                    defaultValue: 'Created on the Docmee open platform. Stored server-side and shown masked.',
                  })}
                >
                  <Input
                    id="docmee-api-key"
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    value={readString('docmee_api_key')}
                    onChange={(event) => setSetting('docmee_api_key', event.target.value)}
                    placeholder="sk-…"
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.docmee.creditsPerPpt', {
                    defaultValue: 'Credits per generated deck',
                  })}
                  htmlFor="docmee-credits"
                  hint={t('admin:creditSettings.docmee.creditsPerPptHint', {
                    defaultValue:
                      '0 = free. Charging also requires a credits-per-USD rate above (otherwise credits are off platform-wide).',
                  })}
                >
                  <Input
                    id="docmee-credits"
                    type="number"
                    min={0}
                    step="any"
                    value={String(readNumber('docmee_credits_per_ppt', 10))}
                    onChange={(event) =>
                      setSetting('docmee_credits_per_ppt', nonNegativeNumber(Number(event.target.value)))
                    }
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.docmee.editCredits', { defaultValue: 'Credits per edit' })}
                  htmlFor="docmee-edit-credits"
                  hint={t('admin:creditSettings.docmee.editCreditsHint', {
                    defaultValue: 'Charged for an AI rewrite or a template change. 0 = free. Docmee bills 1 credit per such call.',
                  })}
                >
                  <Input
                    id="docmee-edit-credits"
                    type="number"
                    min={0}
                    step="any"
                    value={String(readNumber('docmee_edit_credits'))}
                    onChange={(event) =>
                      setSetting('docmee_edit_credits', nonNegativeNumber(Number(event.target.value)))
                    }
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.docmee.defaultTemplate', { defaultValue: 'Default template id' })}
                  htmlFor="docmee-default-template"
                  hint={t('admin:creditSettings.docmee.defaultTemplateHint', {
                    defaultValue: 'Optional. Used when a render request arrives without a template.',
                  })}
                >
                  <Input
                    id="docmee-default-template"
                    value={readString('docmee_default_template_id')}
                    onChange={(event) => setSetting('docmee_default_template_id', event.target.value)}
                    placeholder="1940697631068151808"
                    spellCheck={false}
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.docmee.maxUpload', { defaultValue: 'Upload cap (MB)' })}
                  htmlFor="docmee-max-upload"
                  hint={t('admin:creditSettings.docmee.maxUploadHint', {
                    defaultValue: 'Largest file accepted by the upload input. Docmee recommends ≤ 50 MB.',
                  })}
                >
                  <Input
                    id="docmee-max-upload"
                    type="number"
                    min={0}
                    step={1}
                    value={String(readNumber('docmee_max_upload_mb', 50))}
                    onChange={(event) =>
                      setSetting('docmee_max_upload_mb', nonNegativeNumber(Number(event.target.value), true))
                    }
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.docmee.tokenHours', { defaultValue: 'Token lifetime (hours)' })}
                  htmlFor="docmee-token-hours"
                  hint={t('admin:creditSettings.docmee.tokenHoursHint', { defaultValue: '0 = Docmee default.' })}
                >
                  <Input
                    id="docmee-token-hours"
                    type="number"
                    min={0}
                    step={1}
                    value={String(readNumber('docmee_token_hours', 2))}
                    onChange={(event) =>
                      setSetting('docmee_token_hours', nonNegativeNumber(Number(event.target.value), true))
                    }
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.docmee.apiBaseUrl', { defaultValue: 'Docmee API base URL' })}
                  htmlFor="docmee-api-base"
                  hint={t('admin:creditSettings.docmee.apiBaseUrlHint', {
                    defaultValue:
                      'Server-side API endpoint. Change only for the international build or a self-hosted proxy.',
                  })}
                >
                  <Input
                    id="docmee-api-base"
                    value={readString('docmee_api_base_url')}
                    onChange={(event) => setSetting('docmee_api_base_url', event.target.value)}
                    placeholder="https://docmee.cn"
                    spellCheck={false}
                  />
                </Field>
                {/* Editor-only surface: creation runs on our own UI, so these two
                    matter only for the "Edit slides" hand-off. */}
                <Field
                  label={t('admin:creditSettings.docmee.sdkUrl', { defaultValue: 'Editor SDK script URL' })}
                  htmlFor="docmee-sdk-url"
                  hint={t('admin:creditSettings.docmee.sdkUrlHint', {
                    defaultValue:
                      'Loaded only when a user opens the Docmee editor. Point it at a self-hosted copy or an internal mirror if needed.',
                  })}
                >
                  <Input
                    id="docmee-sdk-url"
                    value={readString('docmee_sdk_url')}
                    onChange={(event) => setSetting('docmee_sdk_url', event.target.value)}
                    placeholder="https://cdn.jsdelivr.net/npm/@docmee/sdk-ui@1.6.47/dist/index.global.js"
                    spellCheck={false}
                  />
                </Field>
                <Field
                  label={t('admin:creditSettings.docmee.domain', { defaultValue: 'Editor origin (international)' })}
                  htmlFor="docmee-domain"
                  hint={t('admin:creditSettings.docmee.domainHint', {
                    defaultValue:
                      'Leave empty for the China build. The international build uses https://app.xpptx.com.',
                  })}
                >
                  <Input
                    id="docmee-domain"
                    value={readString('docmee_domain')}
                    onChange={(event) => setSetting('docmee_domain', event.target.value)}
                    placeholder="https://app.xpptx.com"
                    spellCheck={false}
                  />
                </Field>
              </div>
            </div>

            {/* Deployment-side vendor balance: Docmee charges its own credits per
                deck, so an admin needs to see when the account runs dry. */}
            <div className="mt-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs text-[var(--color-fg-muted)]">
                  {t('admin:creditSettings.docmee.vendorBalance', { defaultValue: 'Docmee account balance' })}
                </span>
                <Button
                  size="sm"
                  variant="ghost"
                  loading={vendorLoading}
                  disabled={vendorLoading}
                  onClick={() => void loadVendor()}
                >
                  {t('admin:creditSettings.docmee.vendorRefresh', { defaultValue: 'Check' })}
                </Button>
                {vendor ? (
                  <span className="text-xs text-[var(--color-fg)]">
                    {t('admin:creditSettings.docmee.vendorCounts', {
                      available: vendor.available_count,
                      used: vendor.used_count,
                      defaultValue: '{{available}} credits left · {{used}} used',
                    })}
                  </span>
                ) : vendorError ? (
                  <span className="text-xs text-[var(--color-danger)]">{vendorError}</span>
                ) : null}
              </div>
            </div>

            {/* Deployment templates: uploading with the Api-Key is not enough —
                the vendor keeps it account-owned until it is published, so the
                panel pairs the upload with a share switch. */}
            <DocmeeTemplateAdmin enabled={readString('docmee_api_key').trim() !== ''} />

            <div className="mt-6 flex justify-end">
              <Button onClick={() => void saveSettings()} loading={savingSettings} disabled={savingSettings}>
                {t('common:actions.save')}
              </Button>
            </div>
          </section>

          <section className="mt-10 border-t border-[var(--color-divider)] pt-8">
            <div className="flex flex-col items-stretch gap-4 sm:flex-row sm:items-end sm:justify-between">
              <div className="min-w-0">
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
                className="w-full sm:w-auto"
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
                mobileDragOnly
                listClassName="mt-4"
                rowClassName="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-2 gap-y-1.5 px-3 py-3 md:grid-cols-[auto_auto_minmax(0,1fr)_auto] md:gap-3 md:px-4"
                renderItem={(item) => {
                  const toggling = packageBusyIds.has(item.id)
                  return (
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
                      <div className="flex items-center gap-1 max-md:col-start-2 max-md:w-full max-md:justify-between">
                        <Switch
                          checked={item.enabled}
                          disabled={toggling}
                          aria-busy={toggling || undefined}
                          onCheckedChange={(enabled) => void togglePackage(item, enabled)}
                          aria-label={t('admin:groups.creditPackages.enabledLabel', { name: item.name })}
                        />
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            className="max-md:size-11"
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
                            className="text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] max-md:size-11"
                          />
                        </div>
                      </div>
                    </>
                  )
                }}
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
