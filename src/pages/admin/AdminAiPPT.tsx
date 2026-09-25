import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { DocmeeTemplateAdmin } from '@/components/admin/docmee-templates'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { docmeeSettingsPatch, storedDocmeeEnabled } from '@/lib/aippt-admin-settings'
import { useAiPPT } from '@/store/aippt'

type Settings = Record<string, unknown>

export default function AdminAiPPT() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [enabledTouched, setEnabledTouched] = useState(false)
  const [switchSaving, setSwitchSaving] = useState(false)
  const switchSavingRef = useRef(false)
  const [vendor, setVendor] = useState<{ available_count: number; used_count: number } | null>(null)
  const [vendorLoading, setVendorLoading] = useState(false)
  const [vendorError, setVendorError] = useState<string | null>(null)

  async function load() {
    setLoading(true)
    setLoadError(false)
    try {
      setDraft(await adminApi.settings())
      setEnabledTouched(false)
    } catch {
      setLoadError(true)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  function readString(key: string): string {
    return typeof draft[key] === 'string' ? draft[key] as string : ''
  }

  function readNumber(key: string, fallback = 0): number {
    const value = draft[key]
    const number = typeof value === 'number' ? value : typeof value === 'string' && value.trim() ? Number(value) : fallback
    return Number.isFinite(number) ? number : fallback
  }

  function setSetting(key: string, value: string | number) {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  async function toggleEnabled(enabled: boolean) {
    if (switchSavingRef.current || savingRef.current) return
    const previous = storedDocmeeEnabled(draft)
    switchSavingRef.current = true
    setSwitchSaving(true)
    setDraft((current) => ({ ...current, docmee_enabled: enabled }))
    try {
      await adminApi.updateSettings({ docmee_enabled: enabled })
      setEnabledTouched(true)
      void useAiPPT.getState().load(true)
      toast.success(t(enabled ? 'admin:creditSettings.docmee.enabledToast' : 'admin:creditSettings.docmee.disabledToast'))
    } catch (error) {
      setDraft((current) => ({ ...current, docmee_enabled: previous }))
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      switchSavingRef.current = false
      setSwitchSaving(false)
    }
  }

  async function save() {
    if (savingRef.current || switchSavingRef.current) return
    const patch = docmeeSettingsPatch(draft, enabledTouched)
    savingRef.current = true
    setSaving(true)
    try {
      await adminApi.updateSettings(patch)
      setDraft((current) => ({ ...current, ...patch }))
      setEnabledTouched(false)
      void useAiPPT.getState().load(true)
      toast.success(t('admin:settings.saved'))
    } catch (error) {
      toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

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

  const enabled = storedDocmeeEnabled(draft)
  const keyConfigured = readString('docmee_api_key').trim() !== ''

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl text-[var(--color-fg)] sm:text-3xl">
          {t('admin:creditSettings.docmee.title')}
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:creditSettings.docmee.lead')}
        </p>
      </header>

      {loading ? <PanelFallback /> : loadError ? (
        <div className="mt-8 flex flex-wrap items-center gap-3">
          <p className="text-sm text-[var(--color-danger)]">{t('admin:creditSettings.docmee.loadFailed')}</p>
          <Button variant="secondary" onClick={() => void load()}>{t('admin:creditSettings.docmee.retry')}</Button>
        </div>
      ) : (
        <>
          <section className="mt-8">
            <h2 className="font-serif text-xl text-[var(--color-fg)]">{t('admin:creditSettings.docmee.connection')}</h2>
            <div className="mt-5 flex items-center justify-between gap-4 border-y border-[var(--color-divider)] py-4">
              <label htmlFor="docmee-enabled" className="text-sm font-medium text-[var(--color-fg)]">
                {t('admin:creditSettings.docmee.enabled')}
              </label>
              <Switch
                id="docmee-enabled"
                checked={enabled ?? keyConfigured}
                disabled={switchSaving || saving}
                aria-busy={switchSaving || undefined}
                onCheckedChange={(value) => void toggleEnabled(value)}
              />
            </div>
            {enabled === false && keyConfigured ? (
              <p className="mt-4 text-sm text-[var(--color-fg-muted)]">{t('admin:creditSettings.docmee.offWithKeyHint')}</p>
            ) : null}
            <div className="mt-5 grid gap-5 lg:grid-cols-2">
              <Field label={t('admin:creditSettings.docmee.apiKey')} htmlFor="docmee-api-key"
                hint={t('admin:creditSettings.docmee.apiKeyHint')}>
                <Input id="docmee-api-key" type="password" autoComplete="off" spellCheck={false}
                  value={readString('docmee_api_key')} onChange={(event) => setSetting('docmee_api_key', event.target.value)}
                  placeholder="sk-…" />
              </Field>
              <Field label={t('admin:creditSettings.docmee.apiBaseUrl')} htmlFor="docmee-api-base"
                hint={t('admin:creditSettings.docmee.apiBaseUrlHint')}>
                <Input id="docmee-api-base" value={readString('docmee_api_base_url')}
                  onChange={(event) => setSetting('docmee_api_base_url', event.target.value)}
                  placeholder="https://docmee.cn" spellCheck={false} />
              </Field>
            </div>
            <div className="mt-5 flex flex-wrap items-center gap-3 border-t border-[var(--color-divider)] pt-4">
              <span className="text-sm text-[var(--color-fg-muted)]">{t('admin:creditSettings.docmee.vendorBalance')}</span>
              <Button size="sm" variant="secondary" loading={vendorLoading} disabled={vendorLoading}
                onClick={() => void loadVendor()}>
                {t('admin:creditSettings.docmee.vendorRefresh')}
              </Button>
              {vendor ? (
                <span className="text-sm text-[var(--color-fg)]">
                  {t('admin:creditSettings.docmee.vendorCounts', {
                    available: vendor.available_count, used: vendor.used_count,
                  })}
                </span>
              ) : vendorError ? <span className="text-sm text-[var(--color-danger)]">{vendorError}</span> : null}
            </div>
          </section>

          <section className="mt-10 border-t border-[var(--color-divider)] pt-8">
            <h2 className="font-serif text-xl text-[var(--color-fg)]">{t('admin:creditSettings.docmee.pricing')}</h2>
            {readNumber('docmee_credits_per_ppt', 10) > 0 && readNumber('credits_per_usd') === 0 ? (
              <p className="mt-4 text-sm text-[var(--color-warning)]">
                {t('admin:creditSettings.docmee.creditsOffHint', { price: readNumber('docmee_credits_per_ppt', 10) })}{' '}
                <Link to="/admin/credits" className="font-medium underline underline-offset-2">
                  {t('admin:creditSettings.docmee.openCredits')}
                </Link>
              </p>
            ) : null}
            <div className="mt-5 grid gap-5 lg:grid-cols-2">
              <Field label={t('admin:creditSettings.docmee.creditsPerPpt')} htmlFor="docmee-credits"
                hint={t('admin:creditSettings.docmee.creditsPerPptHint')}>
                <Input id="docmee-credits" type="number" min={0} step="any"
                  value={String(readNumber('docmee_credits_per_ppt', 10))}
                  onChange={(event) => setSetting('docmee_credits_per_ppt', Math.max(0, Number(event.target.value)))} />
              </Field>
              <Field label={t('admin:creditSettings.docmee.editCredits')} htmlFor="docmee-edit-credits"
                hint={t('admin:creditSettings.docmee.editCreditsHint')}>
                <Input id="docmee-edit-credits" type="number" min={0} step="any"
                  value={String(readNumber('docmee_edit_credits'))}
                  onChange={(event) => setSetting('docmee_edit_credits', Math.max(0, Number(event.target.value)))} />
              </Field>
            </div>
          </section>

          <section className="mt-10 border-t border-[var(--color-divider)] pt-8">
            <h2 className="font-serif text-xl text-[var(--color-fg)]">{t('admin:creditSettings.docmee.editor')}</h2>
            <div className="mt-5 grid gap-5 lg:grid-cols-2">
              <Field label={t('admin:creditSettings.docmee.defaultTemplate')} htmlFor="docmee-default-template"
                hint={t('admin:creditSettings.docmee.defaultTemplateHint')}>
                <Input id="docmee-default-template" value={readString('docmee_default_template_id')}
                  onChange={(event) => setSetting('docmee_default_template_id', event.target.value)}
                  placeholder="1940697631068151808" spellCheck={false} />
              </Field>
              <Field label={t('admin:creditSettings.docmee.maxUpload')} htmlFor="docmee-max-upload"
                hint={t('admin:creditSettings.docmee.maxUploadHint')}>
                <Input id="docmee-max-upload" type="number" min={0} step={1}
                  value={String(readNumber('docmee_max_upload_mb', 50))}
                  onChange={(event) => setSetting('docmee_max_upload_mb', Math.max(0, Number(event.target.value)))} />
              </Field>
              <Field label={t('admin:creditSettings.docmee.tokenHours')} htmlFor="docmee-token-hours"
                hint={t('admin:creditSettings.docmee.tokenHoursHint')}>
                <Input id="docmee-token-hours" type="number" min={0} step={1}
                  value={String(readNumber('docmee_token_hours', 2))}
                  onChange={(event) => setSetting('docmee_token_hours', Math.max(0, Number(event.target.value)))} />
              </Field>
              <Field label={t('admin:creditSettings.docmee.sdkUrl')} htmlFor="docmee-sdk-url"
                hint={t('admin:creditSettings.docmee.sdkUrlHint')}>
                <Input id="docmee-sdk-url" value={readString('docmee_sdk_url')}
                  onChange={(event) => setSetting('docmee_sdk_url', event.target.value)}
                  placeholder="https://cdn.jsdelivr.net/npm/@docmee/sdk-ui@1.6.47/dist/index.global.js" spellCheck={false} />
              </Field>
              <Field label={t('admin:creditSettings.docmee.domain')} htmlFor="docmee-domain"
                hint={t('admin:creditSettings.docmee.domainHint')}>
                <Input id="docmee-domain" value={readString('docmee_domain')}
                  onChange={(event) => setSetting('docmee_domain', event.target.value)}
                  placeholder="https://app.xpptx.com" spellCheck={false} />
              </Field>
            </div>
          </section>

          <section className="mt-10 border-t border-[var(--color-divider)] pt-8">
            <DocmeeTemplateAdmin enabled={keyConfigured} />
          </section>
          <div className="mt-8 flex justify-end">
            <Button onClick={() => void save()} loading={saving} disabled={saving || switchSaving}>
              {t('common:actions.save')}
            </Button>
          </div>
        </>
      )}
    </div>
  )
}
