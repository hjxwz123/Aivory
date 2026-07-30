import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-react'

import { adminApi, ApiError } from '@/api'
import type { ApiPaymentChannel, ApiPaymentMethod, ApiPaymentProvider } from '@/api/types'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
import { IconUploader } from '@/components/admin/icon-uploader'
import { PaymentMethodIcon } from '@/components/payment/payment-method-icon'
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
import { toast } from '@/hooks/use-toast'

interface MethodDraft {
  name: string
  icon: string
  channel_id: string
  provider_method_config: Record<string, unknown>
  enabled: boolean
  sort_order: number
}

function configString(config: Record<string, unknown>, key: string): string {
  const value = config[key]
  return typeof value === 'string' ? value : ''
}

function emptyMethodConfig(provider: ApiPaymentProvider): Record<string, unknown> {
  if (provider === 'epay') return { type: '' }
  return {}
}

function validCardPurchaseUrl(value: string): boolean {
  const input = value.trim()
  if (input.startsWith('/') && !input.startsWith('//')) {
    return !['\\', '\r', '\n', '\0'].some((character) => input.includes(character))
  }
  try {
    const parsed = new URL(input)
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !parsed.username && !parsed.password
  } catch {
    return false
  }
}

export default function AdminPaymentMethods() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiPaymentMethod[]>([])
  const [channels, setChannels] = useState<ApiPaymentChannel[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [cardPurchaseUrl, setCardPurchaseUrl] = useState('')
  const [savedCardPurchaseUrl, setSavedCardPurchaseUrl] = useState('')
  const [savingCardPurchaseUrl, setSavingCardPurchaseUrl] = useState(false)
  const savingCardPurchaseUrlRef = useRef(false)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiPaymentMethod; draft: MethodDraft }>({
    open: false,
    draft: {
      name: '',
      icon: 'CreditCard',
      channel_id: '',
      provider_method_config: {},
      enabled: true,
      sort_order: 0,
    },
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiPaymentMethod | null>(null)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)

  async function load() {
    setLoading(true)
    setLoadError('')
    try {
      const [methods, paymentChannels, settings] = await Promise.all([
        adminApi.paymentMethods(),
        adminApi.paymentChannels(),
        adminApi.settings(),
      ])
      setRows([...methods].sort((a, b) => a.sort_order - b.sort_order))
      setChannels(paymentChannels)
      const purchaseUrl = typeof settings.card_purchase_url === 'string' ? settings.card_purchase_url : ''
      setCardPurchaseUrl(purchaseUrl)
      setSavedCardPurchaseUrl(purchaseUrl)
    } catch (error) {
      const message = error instanceof ApiError ? error.message : t('admin:paymentMethods.loadFailed')
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

  function channelFor(id: string): ApiPaymentChannel | undefined {
    return channels.find((channel) => channel.id === id)
  }

  function providerForDraft(): ApiPaymentProvider | undefined {
    return channelFor(editor.draft.channel_id)?.provider ?? editor.row?.provider
  }

  function openNew() {
    const channel = channels.find((item) => item.enabled) ?? channels[0]
    setEditor({
      open: true,
      draft: {
        name: '',
        icon: 'CreditCard',
        channel_id: channel?.id ?? '',
        provider_method_config: channel ? emptyMethodConfig(channel.provider) : {},
        enabled: true,
        sort_order: rows.length,
      },
    })
  }

  function openEdit(row: ApiPaymentMethod) {
    setEditor({
      open: true,
      row,
      draft: {
        name: row.name,
        icon: row.icon,
        channel_id: row.channel_id,
        provider_method_config: row.provider === 'waffo' ? {} : { ...row.provider_method_config },
        enabled: row.enabled,
        sort_order: row.sort_order,
      },
    })
  }

  function setDraft(patch: Partial<MethodDraft>) {
    setEditor((current) => ({ ...current, draft: { ...current.draft, ...patch } }))
  }

  function setMethodConfig(key: string, value: unknown) {
    setEditor((current) => ({
      ...current,
      draft: {
        ...current.draft,
        provider_method_config: { ...current.draft.provider_method_config, [key]: value },
      },
    }))
  }

  function buildMethodConfig(): Record<string, unknown> {
    const provider = providerForDraft()
    if (provider === 'epay') {
      return { type: configString(editor.draft.provider_method_config, 'type').trim() }
    }
    return {}
  }

  async function submit() {
    if (savingRef.current) return
    const provider = providerForDraft()
    if (!editor.draft.name.trim()) {
      toast.error(t('admin:paymentMethods.errors.nameRequired'))
      return
    }
    if (!editor.draft.channel_id || !provider) {
      toast.error(t('admin:paymentMethods.errors.channelRequired'))
      return
    }
    const providerMethodConfig = buildMethodConfig()
    if (provider === 'epay' && !configString(providerMethodConfig, 'type')) {
      toast.error(t('admin:paymentMethods.errors.epayTypeRequired'))
      return
    }
    savingRef.current = true
    setSaving(true)
    try {
      const body = {
        name: editor.draft.name.trim(),
        icon: editor.draft.icon,
        channel_id: editor.draft.channel_id,
        provider_method_config: providerMethodConfig,
        enabled: editor.draft.enabled,
        sort_order: editor.draft.sort_order,
      }
      if (editor.row) {
        await adminApi.updatePaymentMethod(editor.row.id, body)
        toast.success(t('admin:paymentMethods.updated'))
      } else {
        await adminApi.createPaymentMethod(body)
        toast.success(t('admin:paymentMethods.created'))
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

  async function remove(row: ApiPaymentMethod) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removePaymentMethod(row.id)
      setConfirmDelete(null)
      toast.success(t('admin:paymentMethods.removed'))
      await load()
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  function setOrderedRows(next: ApiPaymentMethod[]) {
    setRows(next.map((row, sortOrder) => row.sort_order === sortOrder ? row : { ...row, sort_order: sortOrder }))
  }

  function persistOrder(next: ApiPaymentMethod[], prev: ApiPaymentMethod[]) {
    void adminApi.reorderPaymentMethods(next.map((row) => row.id)).catch((error) => {
      setRows(prev)
      toast.error(error instanceof ApiError ? error.message : t('admin:paymentMethods.reorderFailed'))
    })
  }

  async function saveCardPurchaseUrl() {
    if (savingCardPurchaseUrlRef.current) return
    const value = cardPurchaseUrl.trim()
    if (value && !validCardPurchaseUrl(value)) {
      toast.error(t('admin:paymentMethods.cardPurchase.invalid'))
      return
    }
    savingCardPurchaseUrlRef.current = true
    setSavingCardPurchaseUrl(true)
    try {
      await adminApi.updateSettings({ card_purchase_url: value })
      setCardPurchaseUrl(value)
      setSavedCardPurchaseUrl(value)
      toast.success(t('admin:paymentMethods.cardPurchase.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:paymentMethods.cardPurchase.saveFailed'))
    } finally {
      savingCardPurchaseUrlRef.current = false
      setSavingCardPurchaseUrl(false)
    }
  }

  const provider = providerForDraft()

  return (
    <div className="min-w-0 max-w-full font-sans">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:paymentMethods.title')}</h1>
          <p className="mt-1 max-w-2xl text-[13px] leading-5 text-[var(--color-fg-muted)]">{t('admin:paymentMethods.lead')}</p>
        </div>
        <Button
          className="rounded-[8px] self-start max-sm:h-11 sm:self-auto"
          size="sm"
          leadingIcon={<Plus size={14} aria-hidden />}
          disabled={!loading && channels.length === 0}
          onClick={openNew}
        >
          {t('admin:paymentMethods.new')}
        </Button>
      </header>

      {!loading && !loadError ? (
        <div className="mt-5 flex flex-col gap-2 border-y border-[var(--color-divider)] py-3 md:flex-row md:items-center md:justify-between md:gap-5">
          <div className="min-w-0 md:max-w-md">
            <label htmlFor="card-purchase-url" className="text-[13px] font-medium text-[var(--color-fg)]">{t('admin:paymentMethods.cardPurchase.label')}</label>
            <p className="mt-0.5 text-[12px] leading-4 text-[var(--color-fg-subtle)]">{t('admin:paymentMethods.cardPurchase.hint')}</p>
          </div>
          <div className="flex min-w-0 flex-1 gap-2 md:max-w-xl">
            <Input
              id="card-purchase-url"
              type="text"
              inputMode="url"
              wrapperClassName="min-w-0 flex-1 rounded-[8px] max-md:h-11"
              value={cardPurchaseUrl}
              onChange={(event) => setCardPurchaseUrl(event.target.value)}
              placeholder={t('admin:paymentMethods.cardPurchase.placeholder')}
            />
            <Button
              className="rounded-[8px] max-md:h-11"
              variant="secondary"
              size="sm"
              loading={savingCardPurchaseUrl}
              disabled={cardPurchaseUrl.trim() === savedCardPurchaseUrl}
              onClick={() => void saveCardPurchaseUrl()}
            >
              {t('admin:paymentMethods.cardPurchase.save')}
            </Button>
          </div>
        </div>
      ) : null}

      <section className="mt-5">
        {loading ? (
          <PanelFallback />
        ) : loadError ? (
          <div className="flex flex-col items-start gap-3 rounded-xl border border-[var(--color-danger)]/25 bg-[var(--color-danger-soft)] px-4 py-3 text-[13px] text-[var(--color-danger)] sm:flex-row sm:items-center sm:justify-between">
            <span>{loadError}</span>
            <Button className="rounded-[8px]" variant="secondary" size="sm" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => void load()}>{t('admin:paymentMethods.retry')}</Button>
          </div>
        ) : channels.length === 0 ? (
          <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center">
            <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:paymentMethods.noChannelsTitle')}</p>
            <p className="mx-auto mt-1 max-w-lg text-[13px] text-[var(--color-fg-muted)]">{t('admin:paymentMethods.noChannels')}</p>
            <Button asChild className="mt-4 rounded-[8px]" size="sm" variant="secondary">
              <Link to="/admin/payment-channels">{t('admin:paymentMethods.configureChannels')}</Link>
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <div className="rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-8 text-center">
            <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:paymentMethods.emptyTitle')}</p>
            <p className="mx-auto mt-1 max-w-lg text-[13px] text-[var(--color-fg-muted)]">{t('admin:paymentMethods.empty')}</p>
            <Button className="mt-4 rounded-[8px]" size="sm" onClick={openNew}>{t('admin:paymentMethods.new')}</Button>
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
            listClassName="overflow-hidden"
            rowClassName="grid grid-cols-[2.75rem_auto_minmax(0,1fr)] items-center gap-x-1 gap-y-2 px-2 py-3 sm:grid-cols-[auto_auto_auto_minmax(0,1fr)_auto] sm:gap-3 sm:px-4"
            renderItem={(row) => {
              const channel = channelFor(row.channel_id)
              return (
                <>
                  <span className="col-start-2 row-start-1 inline-flex size-8 items-center justify-center rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)] sm:col-start-auto sm:row-start-auto">
                    <PaymentMethodIcon icon={row.icon} size={16} />
                  </span>
                  <div className="col-start-3 row-start-1 min-w-0 sm:col-start-auto sm:row-start-auto">
                    <div className="flex min-w-0 flex-wrap items-center gap-2">
                      <span className="min-w-0 break-words text-[13px] font-medium text-[var(--color-fg)] [overflow-wrap:anywhere]">{row.name}</span>
                      <Badge size="xs" variant="info">{t(`admin:paymentProviders.${row.provider}`)}</Badge>
                      {!row.enabled ? <Badge size="xs">{t('admin:paymentMethods.disabled')}</Badge> : null}
                      {channel && !channel.enabled ? <Badge size="xs" variant="warning">{t('admin:paymentMethods.channelDisabled')}</Badge> : null}
                      {!channel ? <Badge size="xs" variant="danger">{t('admin:paymentMethods.channelMissing')}</Badge> : null}
                    </div>
                    <p className="mt-1 break-words text-[12px] text-[var(--color-fg-subtle)] [overflow-wrap:anywhere]">
                      {t('admin:paymentMethods.boundChannel', { name: channel?.name ?? row.channel_id })}
                    </p>
                  </div>
                  <div className="col-span-3 row-start-2 flex items-center justify-end gap-1 sm:col-span-1 sm:col-start-auto sm:row-start-auto">
                    <Button className="rounded-[8px] max-sm:size-11" variant="ghost" size="icon-sm" title={t('admin:common.edit')} aria-label={t('admin:common.edit')} onClick={() => openEdit(row)}>
                      <Pencil size={14} aria-hidden />
                    </Button>
                    <Button className="rounded-[8px] max-sm:size-11" variant="ghost" size="icon-sm" title={t('admin:common.remove')} aria-label={t('admin:common.remove')} onClick={() => setConfirmDelete(row)}>
                      <Trash2 size={14} aria-hidden />
                    </Button>
                  </div>
                </>
              )
            }}
          />
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(open) => !savingRef.current && setEditor((current) => ({ ...current, open }))}>
        <DialogContent size="md" className="rounded-[8px] font-sans max-sm:[&>button]:size-11">
          <form
            className="flex min-h-0 flex-1 flex-col"
            onSubmit={(event) => {
              event.preventDefault()
              void submit()
            }}
          >
            <DialogHeader className="px-5 pt-5 pb-3 max-sm:pr-16">
              <DialogTitle>{editor.row ? t('admin:paymentMethods.editTitle') : t('admin:paymentMethods.newTitle')}</DialogTitle>
              <DialogDescription className="mt-1 text-[13px]">{t('admin:paymentMethods.editorDescription')}</DialogDescription>
            </DialogHeader>
            <DialogBody className="px-5 pb-4">
              <div className="grid min-w-0 gap-3.5">
              <Field className="min-w-0" label={t('admin:paymentMethods.fields.name')} htmlFor="payment-method-name">
                <Input id="payment-method-name" required wrapperClassName="rounded-[8px] max-sm:h-11" value={editor.draft.name} onChange={(event) => setDraft({ name: event.target.value })} placeholder={t('admin:paymentMethods.fields.namePlaceholder')} />
              </Field>
              <Field label={t('admin:paymentMethods.fields.icon')} htmlFor="payment-method-icon">
                <IconUploader
                  id="payment-method-icon"
                  value={editor.draft.icon}
                  onChange={(icon) => setDraft({ icon })}
                  placeholder="CreditCard / https://..."
                  preview={<PaymentMethodIcon icon={editor.draft.icon} size={18} />}
                />
              </Field>
              <Field className="min-w-0" label={t('admin:paymentMethods.fields.channel')} htmlFor="payment-method-channel" hint={t('admin:paymentMethods.fields.channelHint')}>
                <Select
                  name="payment-method-channel"
                  required
                  value={editor.draft.channel_id}
                  onValueChange={(channelId) => {
                    const channel = channelFor(channelId)
                    setDraft({
                      channel_id: channelId,
                      provider_method_config: channel ? emptyMethodConfig(channel.provider) : {},
                    })
                  }}
                >
                  <SelectTrigger id="payment-method-channel" className="min-w-0 max-w-full overflow-hidden rounded-[8px] max-sm:h-11 [&>span:first-child]:min-w-0 [&>span:first-child]:flex-1 [&>span:first-child]:truncate"><SelectValue /></SelectTrigger>
                  <SelectContent className="max-w-[calc(100vw-2rem)] sm:max-w-md">
                    {channels.map((channel) => (
                      <SelectItem key={channel.id} value={channel.id} className="max-w-full min-w-0 [&>span:last-child]:min-w-0 [&>span:last-child]:overflow-hidden">
                        <span className="block whitespace-normal break-words [overflow-wrap:anywhere]">
                          {channel.name} · {t(`admin:paymentProviders.${channel.provider}`)}{channel.enabled ? '' : ` · ${t('admin:paymentMethods.disabled')}`}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              {provider === 'epay' ? (
                <Field label={t('admin:paymentMethods.fields.epayType')} htmlFor="payment-method-epay-type" hint={t('admin:paymentMethods.fields.epayTypeHint')}>
                  <Input id="payment-method-epay-type" required wrapperClassName="rounded-[8px] max-sm:h-11" className="font-mono text-[13px]" value={configString(editor.draft.provider_method_config, 'type')} onChange={(event) => setMethodConfig('type', event.target.value)} placeholder="alipay" />
                </Field>
              ) : null}

              <label className="flex min-h-11 items-center justify-between gap-4 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-3 py-2">
                <span className="min-w-0">
                  <span className="block text-[13px] font-medium text-[var(--color-fg)]">{t('admin:paymentMethods.fields.enabled')}</span>
                  <span className="block text-[12px] text-[var(--color-fg-subtle)]">{t('admin:paymentMethods.fields.enabledHint')}</span>
                </span>
                <Switch checked={editor.draft.enabled} onCheckedChange={(enabled) => setDraft({ enabled })} />
              </label>
              </div>
            </DialogBody>
            <DialogFooter className="max-sm:[&_button]:!h-11">
              <Button className="rounded-[8px]" variant="ghost" disabled={saving} onClick={() => setEditor((current) => ({ ...current, open: false }))}>{t('common:actions.cancel')}</Button>
              <Button type="submit" className="rounded-[8px]" loading={saving}>{t('common:actions.save')}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(open) => !open && !deletingRef.current && setConfirmDelete(null)}>
        <DialogContent size="sm" className="rounded-[8px] font-sans max-sm:[&>button]:size-11">
          <DialogHeader className="px-5 pt-5 pb-3 max-sm:pr-16">
            <DialogTitle>{t('admin:paymentMethods.removeTitle')}</DialogTitle>
            <DialogDescription className="mt-1 break-words text-[13px] [overflow-wrap:anywhere]">{confirmDelete ? t('admin:paymentMethods.removeBody', { name: confirmDelete.name }) : ''}</DialogDescription>
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
