import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Copy, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'

import { adminApi, ApiError } from '@/api'
import type { ApiPaymentChannel, ApiPaymentEnvironment, ApiPaymentProvider } from '@/api/types'
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
import { formatDateTime } from '@/lib/utils'

type ChannelDraft = Pick<ApiPaymentChannel, 'name' | 'provider' | 'environment' | 'config' | 'enabled'>

const PROVIDERS: ApiPaymentProvider[] = ['stripe', 'epay', 'waffo']

function defaultEnvironment(provider: ApiPaymentProvider): ApiPaymentEnvironment {
  return provider === 'epay' ? 'live' : 'test'
}

function emptyConfig(provider: ApiPaymentProvider): Record<string, unknown> {
  if (provider === 'stripe') return { secret_key: '', webhook_secret: '' }
  if (provider === 'epay') {
    return { gateway_url: '', merchant_id: '', merchant_key: '', currency: 'CNY' }
  }
  return {
    merchant_id: '',
    private_key: '',
    store_id: '',
    product_id: '',
    mode: 'test',
    webhook_public_key: '',
  }
}

function configString(config: Record<string, unknown>, key: string): string {
  const value = config[key]
  return typeof value === 'string' ? value : ''
}

function editableConfig(provider: ApiPaymentProvider, config: Record<string, unknown>): Record<string, unknown> {
  if (provider !== 'waffo') return { ...config }
  return {
    merchant_id: configString(config, 'merchant_id'),
    private_key: configString(config, 'private_key'),
    store_id: configString(config, 'store_id'),
    product_id: configString(config, 'product_id'),
    mode: configString(config, 'mode') === 'prod' ? 'prod' : 'test',
    webhook_public_key: configString(config, 'webhook_public_key'),
  }
}

function submitConfig(provider: ApiPaymentProvider, config: Record<string, unknown>): Record<string, unknown> {
  if (provider === 'epay') {
    return {
      ...config,
      currency: configString(config, 'currency').trim().toUpperCase(),
    }
  }
  if (provider === 'waffo') {
    return {
      merchant_id: configString(config, 'merchant_id').trim(),
      private_key: configString(config, 'private_key'),
      store_id: configString(config, 'store_id').trim(),
      product_id: configString(config, 'product_id').trim(),
      mode: configString(config, 'mode').trim(),
      webhook_public_key: configString(config, 'webhook_public_key'),
    }
  }
  return config
}

function timestamp(value: number): Date {
  return new Date(value < 1_000_000_000_000 ? value * 1000 : value)
}

export default function AdminPaymentChannels() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiPaymentChannel[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [editor, setEditor] = useState<{
    open: boolean
    row?: ApiPaymentChannel
    draft: ChannelDraft
  }>({
    open: false,
    draft: { name: '', provider: 'stripe', environment: 'test', config: emptyConfig('stripe'), enabled: false },
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiPaymentChannel | null>(null)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const [copiedWebhook, setCopiedWebhook] = useState('')

  async function load() {
    setLoading(true)
    setLoadError('')
    try {
      setRows(await adminApi.paymentChannels())
    } catch (error) {
      const message = error instanceof ApiError ? error.message : t('admin:paymentChannels.loadFailed')
      setLoadError(message)
      toast.error(message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function openNew() {
    setCopiedWebhook('')
    setEditor({
      open: true,
      draft: { name: '', provider: 'stripe', environment: 'test', config: emptyConfig('stripe'), enabled: false },
    })
  }

  function openEdit(row: ApiPaymentChannel) {
    setCopiedWebhook('')
    setEditor({
      open: true,
      row,
      draft: { name: row.name, provider: row.provider, environment: row.environment, config: editableConfig(row.provider, row.config), enabled: row.enabled },
    })
  }

  function setDraft(patch: Partial<ChannelDraft>) {
    setEditor((current) => ({ ...current, draft: { ...current.draft, ...patch } }))
  }

  function setConfig(key: string, value: unknown) {
    setEditor((current) => ({
      ...current,
      draft: {
        ...current.draft,
        config: { ...current.draft.config, [key]: value },
      },
    }))
  }

  function setEnvironment(environment: ApiPaymentEnvironment) {
    setEditor((current) => ({
      ...current,
      draft: {
        ...current.draft,
        environment,
        config: current.draft.provider === 'waffo'
          ? { ...current.draft.config, mode: environment === 'test' ? 'test' : 'prod' }
          : current.draft.config,
      },
    }))
  }

  function validateDraft(): string | null {
    const { draft } = editor
    if (!draft.name.trim()) return t('admin:paymentChannels.errors.nameRequired')
    const required = draft.provider === 'stripe'
      ? ['secret_key', ...(draft.enabled ? ['webhook_secret'] : [])]
      : draft.provider === 'epay'
        ? ['gateway_url', 'merchant_id', 'merchant_key', 'currency']
        : ['merchant_id', 'private_key', 'store_id', 'product_id', 'mode']
    if (required.some((key) => !configString(draft.config, key).trim())) {
      return t('admin:paymentChannels.errors.configRequired')
    }
    if (draft.provider === 'epay' && !/^[A-Z]{3}$/.test(configString(draft.config, 'currency').trim().toUpperCase())) {
      return t('admin:paymentChannels.errors.currencyInvalid')
    }
    if (draft.provider === 'waffo' && !['test', 'prod'].includes(configString(draft.config, 'mode').trim())) {
      return t('admin:paymentChannels.errors.modeInvalid')
    }
    return null
  }

  async function submit() {
    if (savingRef.current) return
    const validationError = validateDraft()
    if (validationError) {
      toast.error(validationError)
      return
    }

    savingRef.current = true
    setSaving(true)
    try {
      const body: ChannelDraft = {
        ...editor.draft,
        name: editor.draft.name.trim(),
        config: submitConfig(editor.draft.provider, editor.draft.config),
      }
      if (editor.row) {
        await adminApi.updatePaymentChannel(editor.row.id, body)
        toast.success(t('admin:paymentChannels.updated'))
      } else {
        await adminApi.createPaymentChannel(body)
        toast.success(t('admin:paymentChannels.created'))
      }
      setEditor((current) => ({ ...current, open: false }))
      await load()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function remove(row: ApiPaymentChannel) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removePaymentChannel(row.id)
      setConfirmDelete(null)
      toast.success(t('admin:paymentChannels.removed'))
      await load()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  async function copyWebhook(url: string, key: string) {
    try {
      await navigator.clipboard.writeText(url)
      setCopiedWebhook(key)
      window.setTimeout(() => setCopiedWebhook((current) => current === key ? '' : current), 1800)
    } catch {
      toast.error(t('admin:paymentChannels.copyFailed'))
    }
  }

  function channelSummary(row: ApiPaymentChannel): string {
    if (row.provider === 'stripe') return t('admin:paymentChannels.summary.stripe')
    if (row.provider === 'epay') {
      return [configString(row.config, 'gateway_url'), configString(row.config, 'currency')]
        .filter(Boolean)
        .join(' · ')
    }
    return [
      configString(row.config, 'merchant_id'),
      configString(row.config, 'store_id'),
      configString(row.config, 'product_id'),
    ]
      .filter(Boolean)
      .join(' · ') || t('admin:paymentChannels.summary.waffo')
  }

  return (
    <div className="min-w-0 max-w-full font-sans">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-normal text-[var(--color-fg)]">
            {t('admin:paymentChannels.title')}
          </h1>
          <p className="mt-1 max-w-2xl text-[13px] leading-5 text-[var(--color-fg-muted)]">
            {t('admin:paymentChannels.lead')}
          </p>
        </div>
        <Button className="rounded-[8px] self-start max-sm:h-11 sm:self-auto" size="sm" leadingIcon={<Plus size={14} aria-hidden />} onClick={openNew}>
          {t('admin:paymentChannels.new')}
        </Button>
      </header>

      <section className="mt-5">
        {loading ? (
          <PanelFallback />
        ) : loadError ? (
          <div className="flex flex-col items-start gap-3 rounded-[8px] border border-[var(--color-danger)]/25 bg-[var(--color-danger-soft)] px-4 py-3 text-[13px] text-[var(--color-danger)] sm:flex-row sm:items-center sm:justify-between">
            <span>{loadError}</span>
            <Button className="rounded-[8px]" variant="secondary" size="sm" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => void load()}>
              {t('admin:paymentChannels.retry')}
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <div className="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center">
            <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:paymentChannels.emptyTitle')}</p>
            <p className="mx-auto mt-1 max-w-lg text-[13px] text-[var(--color-fg-muted)]">{t('admin:paymentChannels.empty')}</p>
            <Button className="mt-4 rounded-[8px]" size="sm" onClick={openNew}>{t('admin:paymentChannels.new')}</Button>
          </div>
        ) : (
          <ul className="divide-y divide-[var(--color-divider)] overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((row) => (
              <li key={row.id} className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-3 py-3 sm:px-4">
                <div className="min-w-0">
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <span className="min-w-0 break-words text-[13px] font-medium text-[var(--color-fg)] [overflow-wrap:anywhere]">{row.name}</span>
                    <Badge size="xs" variant="info">{t(`admin:paymentProviders.${row.provider}`)}</Badge>
                    {row.environment === 'test' ? <Badge size="xs" variant="warning">{t('admin:paymentChannels.testEnvironment')}</Badge> : null}
                    {!row.enabled ? <Badge size="xs">{t('admin:paymentChannels.disabled')}</Badge> : null}
                  </div>
                  <p className="mt-1 truncate text-[12px] text-[var(--color-fg-subtle)]">
                    {channelSummary(row)} · {t('admin:paymentChannels.updatedAt', { date: formatDateTime(timestamp(row.updated_at)) })}
                  </p>
                  {row.webhook_url ? (
                    <div className="mt-1 flex min-w-0 items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
                      <code className="min-w-0 flex-1 truncate" title={row.webhook_url}>{row.webhook_url}</code>
                      <button
                        type="button"
                        className="inline-flex size-7 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-11"
                        title={copiedWebhook === `list:${row.id}` ? t('admin:paymentChannels.copied') : t('admin:paymentChannels.copyWebhook')}
                        aria-label={copiedWebhook === `list:${row.id}` ? t('admin:paymentChannels.copied') : t('admin:paymentChannels.copyWebhook')}
                        onClick={() => void copyWebhook(row.webhook_url!, `list:${row.id}`)}
                      >
                        {copiedWebhook === `list:${row.id}` ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
                      </button>
                    </div>
                  ) : null}
                </div>
                <div className="flex items-center gap-1">
                  <Button
                    className="rounded-[8px] max-sm:size-11"
                    variant="ghost"
                    size="icon"
                    title={t('admin:common.edit')}
                    aria-label={t('admin:common.edit')}
                    onClick={() => openEdit(row)}
                  >
                    <Pencil size={14} aria-hidden />
                  </Button>
                  <Button
                    className="rounded-[8px] max-sm:size-11"
                    variant="ghost"
                    size="icon"
                    title={t('admin:common.remove')}
                    aria-label={t('admin:common.remove')}
                    onClick={() => setConfirmDelete(row)}
                  >
                    <Trash2 size={14} aria-hidden />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(open) => !savingRef.current && setEditor((current) => ({ ...current, open }))}>
        <DialogContent size="lg" className="rounded-[8px] font-sans max-sm:[&>button]:size-11">
          <form
            className="flex min-h-0 flex-1 flex-col"
            onSubmit={(event) => {
              event.preventDefault()
              void submit()
            }}
          >
            <DialogHeader className="px-5 pt-5 pb-3 max-sm:pr-16">
              <DialogTitle>{editor.row ? t('admin:paymentChannels.editTitle') : t('admin:paymentChannels.newTitle')}</DialogTitle>
              <DialogDescription className="mt-1 text-[13px]">
                {t('admin:paymentChannels.editorDescription')}
              </DialogDescription>
            </DialogHeader>
            <DialogBody className="px-5 pb-4">
              <div className="grid min-w-0 gap-3.5">
              <div className="grid gap-3 sm:grid-cols-3">
                <Field className="min-w-0" label={t('admin:paymentChannels.fields.name')} htmlFor="payment-channel-name">
                  <Input
                    id="payment-channel-name"
                    required
                    wrapperClassName="rounded-[8px] max-sm:h-11"
                    value={editor.draft.name}
                    onChange={(event) => setDraft({ name: event.target.value })}
                    placeholder={t('admin:paymentChannels.fields.namePlaceholder')}
                  />
                </Field>
                <Field className="min-w-0" label={t('admin:paymentChannels.fields.provider')} htmlFor="payment-channel-provider">
                  <Select
                    name="payment-channel-provider"
                    required
                    value={editor.draft.provider}
                    onValueChange={(value) => {
                      const provider = value as ApiPaymentProvider
                      setDraft({ provider, environment: defaultEnvironment(provider), config: emptyConfig(provider) })
                    }}
                  >
                    <SelectTrigger id="payment-channel-provider" className="rounded-[8px] max-sm:h-11">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {PROVIDERS.map((provider) => (
                        <SelectItem key={provider} value={provider}>{t(`admin:paymentProviders.${provider}`)}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field className="min-w-0" label={t('admin:paymentChannels.fields.environment')} htmlFor="payment-channel-environment" hint={t('admin:paymentChannels.fields.environmentHint')}>
                  <Select
                    name="payment-channel-environment"
                    required
                    value={editor.draft.environment}
                    onValueChange={(value) => setEnvironment(value as ApiPaymentEnvironment)}
                  >
                    <SelectTrigger id="payment-channel-environment" className="rounded-[8px] max-sm:h-11">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="live">{t('admin:paymentChannels.fields.environmentLive')}</SelectItem>
                      <SelectItem value="test">{t('admin:paymentChannels.fields.environmentTest')}</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>

              <Field label={t('admin:paymentChannels.fields.webhookUrl')} hint={editor.row ? undefined : t('admin:paymentChannels.fields.webhookUrlNew')}>
                {editor.row?.webhook_url ? (
                  <div className="flex min-w-0 gap-1.5">
                    <Input
                      readOnly
                      wrapperClassName="min-w-0 flex-1 rounded-[8px] max-sm:h-11"
                      className="font-mono text-[12px]"
                      value={editor.row.webhook_url}
                      onFocus={(event) => event.currentTarget.select()}
                    />
                    <Button
                      className="rounded-[8px] max-sm:size-11"
                      variant="secondary"
                      size="icon"
                      title={copiedWebhook === 'editor' ? t('admin:paymentChannels.copied') : t('admin:paymentChannels.copyWebhook')}
                      aria-label={copiedWebhook === 'editor' ? t('admin:paymentChannels.copied') : t('admin:paymentChannels.copyWebhook')}
                      onClick={() => void copyWebhook(editor.row!.webhook_url!, 'editor')}
                    >
                      {copiedWebhook === 'editor' ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
                    </Button>
                  </div>
                ) : (
                  <div className="min-h-10 rounded-[8px] border border-dashed border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2 text-[12px] text-[var(--color-fg-subtle)]">
                    {t('admin:paymentChannels.fields.webhookUrlNew')}
                  </div>
                )}
              </Field>

              {editor.draft.provider === 'stripe' ? (
                <>
                  <Field label={t('admin:paymentChannels.fields.secretKey')} htmlFor="payment-channel-stripe-secret" hint={t('admin:paymentChannels.secretHint')}>
                    <Input
                      id="payment-channel-stripe-secret"
                      type="password"
                      required
                      autoComplete="new-password"
                      wrapperClassName="rounded-[8px] max-sm:h-11"
                      value={configString(editor.draft.config, 'secret_key')}
                      onChange={(event) => setConfig('secret_key', event.target.value)}
                      placeholder="sk_live_…"
                    />
                  </Field>
                  <Field
                    label={t('admin:paymentChannels.fields.webhookSecret')}
                    htmlFor="payment-channel-stripe-webhook"
                    hint={t('admin:paymentChannels.fields.webhookSecretHint')}
                  >
                    <Input
                      id="payment-channel-stripe-webhook"
                      type="password"
                      required={editor.draft.enabled}
                      autoComplete="new-password"
                      wrapperClassName="rounded-[8px] max-sm:h-11"
                      value={configString(editor.draft.config, 'webhook_secret')}
                      onChange={(event) => setConfig('webhook_secret', event.target.value)}
                      placeholder="whsec_…"
                    />
                  </Field>
                </>
              ) : null}

              {editor.draft.provider === 'epay' ? (
                <>
                  <Field label={t('admin:paymentChannels.fields.gatewayUrl')} htmlFor="payment-channel-epay-url">
                    <Input
                      id="payment-channel-epay-url"
                      type="url"
                      required
                      wrapperClassName="rounded-[8px] max-sm:h-11"
                      value={configString(editor.draft.config, 'gateway_url')}
                      onChange={(event) => setConfig('gateway_url', event.target.value)}
                      placeholder="https://pay.example.com"
                    />
                  </Field>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label={t('admin:paymentChannels.fields.merchantId')} htmlFor="payment-channel-epay-merchant">
                      <Input
                        id="payment-channel-epay-merchant"
                        required
                        wrapperClassName="rounded-[8px] max-sm:h-11"
                        value={configString(editor.draft.config, 'merchant_id')}
                        onChange={(event) => setConfig('merchant_id', event.target.value)}
                      />
                    </Field>
                    <Field label={t('admin:paymentChannels.fields.currency')} htmlFor="payment-channel-epay-currency" hint={t('admin:paymentChannels.fields.currencyHint')}>
                      <Input
                        id="payment-channel-epay-currency"
                        required
                        wrapperClassName="rounded-[8px] max-sm:h-11"
                        className="uppercase"
                        maxLength={3}
                        value={configString(editor.draft.config, 'currency')}
                        onChange={(event) => setConfig('currency', event.target.value.toUpperCase())}
                        placeholder="CNY"
                      />
                    </Field>
                  </div>
                  <Field label={t('admin:paymentChannels.fields.merchantKey')} htmlFor="payment-channel-epay-key" hint={t('admin:paymentChannels.secretHint')}>
                    <Input
                      id="payment-channel-epay-key"
                      type="password"
                      required
                      autoComplete="new-password"
                      wrapperClassName="rounded-[8px] max-sm:h-11"
                      value={configString(editor.draft.config, 'merchant_key')}
                      onChange={(event) => setConfig('merchant_key', event.target.value)}
                    />
                  </Field>
                </>
              ) : null}

              {editor.draft.provider === 'waffo' ? (
                <>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label={t('admin:paymentChannels.fields.merchantId')} htmlFor="payment-channel-waffo-merchant">
                      <Input
                        id="payment-channel-waffo-merchant"
                        required
                        wrapperClassName="rounded-[8px] max-sm:h-11"
                        value={configString(editor.draft.config, 'merchant_id')}
                        onChange={(event) => setConfig('merchant_id', event.target.value)}
                      />
                    </Field>
                    <Field label={t('admin:paymentChannels.fields.storeId')} htmlFor="payment-channel-waffo-store" hint={t('admin:paymentChannels.fields.storeIdHint')}>
                      <Input
                        id="payment-channel-waffo-store"
                        required
                        wrapperClassName="rounded-[8px] max-sm:h-11"
                        className="font-mono text-[13px]"
                        value={configString(editor.draft.config, 'store_id')}
                        onChange={(event) => setConfig('store_id', event.target.value)}
                        placeholder="STO_…"
                      />
                    </Field>
                  </div>
                  <Field label={t('admin:paymentChannels.fields.productId')} htmlFor="payment-channel-waffo-product" hint={t('admin:paymentChannels.fields.productIdHint')}>
                      <Input
                        id="payment-channel-waffo-product"
                        required
                        wrapperClassName="rounded-[8px] max-sm:h-11"
                        className="font-mono text-[13px]"
                        value={configString(editor.draft.config, 'product_id')}
                        onChange={(event) => setConfig('product_id', event.target.value)}
                        placeholder="PROD_…"
                      />
                  </Field>
                  <div className="grid gap-3 md:grid-cols-2">
                    <Field label={t('admin:paymentChannels.fields.privateKey')} htmlFor="payment-channel-waffo-private" hint={t('admin:paymentChannels.secretHint')}>
                      <Textarea
                        id="payment-channel-waffo-private"
                        required
                        rows={4}
                        className="rounded-[8px] font-mono text-[12px]"
                        value={configString(editor.draft.config, 'private_key')}
                        onChange={(event) => setConfig('private_key', event.target.value)}
                      />
                    </Field>
                    <Field label={t('admin:paymentChannels.fields.webhookPublicKey')} htmlFor="payment-channel-waffo-webhook-public" hint={t('admin:paymentChannels.fields.webhookPublicKeyHint')}>
                      <Textarea
                        id="payment-channel-waffo-webhook-public"
                        rows={4}
                        className="rounded-[8px] font-mono text-[12px]"
                        value={configString(editor.draft.config, 'webhook_public_key')}
                        onChange={(event) => setConfig('webhook_public_key', event.target.value)}
                      />
                    </Field>
                  </div>
                </>
              ) : null}

              <label className="flex min-h-11 items-center justify-between gap-4 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2">
                <span className="min-w-0">
                  <span className="block text-[13px] font-medium text-[var(--color-fg)]">{t('admin:paymentChannels.fields.enabled')}</span>
                  <span className="block text-[12px] text-[var(--color-fg-subtle)]">{t('admin:paymentChannels.fields.enabledHint')}</span>
                </span>
                <Switch checked={editor.draft.enabled} onCheckedChange={(enabled) => setDraft({ enabled })} />
              </label>
              </div>
            </DialogBody>
            <DialogFooter className="max-sm:[&_button]:!h-11">
              <Button className="rounded-[8px]" variant="ghost" disabled={saving} onClick={() => setEditor((current) => ({ ...current, open: false }))}>
                {t('common:actions.cancel')}
              </Button>
              <Button type="submit" className="rounded-[8px]" loading={saving}>
                {t('common:actions.save')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(open) => !open && !deletingRef.current && setConfirmDelete(null)}>
        <DialogContent size="sm" className="rounded-[8px] font-sans max-sm:[&>button]:size-11">
          <DialogHeader className="px-5 pt-5 pb-3 max-sm:pr-16">
            <DialogTitle>{t('admin:paymentChannels.removeTitle')}</DialogTitle>
            <DialogDescription className="mt-1 break-words text-[13px] [overflow-wrap:anywhere]">
              {confirmDelete ? t('admin:paymentChannels.removeBody', { name: confirmDelete.name }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter className="max-sm:[&_button]:!h-11">
            <Button className="rounded-[8px]" variant="ghost" disabled={deleting} onClick={() => setConfirmDelete(null)}>{t('common:actions.cancel')}</Button>
            <Button className="rounded-[8px]" variant="destructive" loading={deleting} onClick={() => confirmDelete && void remove(confirmDelete)}>{t('common:actions.delete')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
