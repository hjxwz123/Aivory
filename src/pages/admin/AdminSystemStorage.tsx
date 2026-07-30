/**
 * AdminSystemStorage — platform object storage, archive retention, and upload
 * policy. Sensitive values are returned as "••••••" and may be PATCHed back
 * unchanged; the backend treats that sentinel as "keep the existing secret".
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

type Settings = Record<string, unknown>

const OWNED_KEYS = [
  'storage_provider',
  'storage_prefix',
  'storage_archive_ttl_days',
  'storage_s3_bucket',
  'storage_s3_region',
  'storage_s3_endpoint',
  'storage_s3_access_key',
  'storage_s3_secret_key',
  'storage_aliyun_bucket',
  'storage_aliyun_endpoint',
  'storage_aliyun_access_key_id',
  'storage_aliyun_access_key_secret',
  'upload_allowed_extensions',
  'max_image_upload_mb',
  'max_file_upload_mb',
] as const

export default function AdminSystemStorage() {
  const { t } = useTranslation(['admin', 'common'])
  const [draft, setDraft] = useState<Settings>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    adminApi
      .settings()
      .then((settings) => setDraft(settings))
      .catch((e) => toast.error(e instanceof ApiError ? e.message : t('admin:common.failed')))
      .finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function save() {
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const key of OWNED_KEYS) {
        if (key in draft) patch[key] = draft[key]
      }
      const archiveTtl = Number(readNumericString('storage_archive_ttl_days').trim())
      const normalizedArchiveTtl = Number.isFinite(archiveTtl) ? Math.max(0, Math.floor(archiveTtl)) : 0
      patch.storage_archive_ttl_days = normalizedArchiveTtl
      await adminApi.updateSettings(patch)
      setDraft((current) => ({ ...current, storage_archive_ttl_days: normalizedArchiveTtl }))
      toast.success(t('admin:settings.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  function readString(key: string, fallback = ''): string {
    const value = draft[key]
    return typeof value === 'string' ? value : fallback
  }

  function readNumber(key: string, fallback = 0): number {
    const value = draft[key]
    return typeof value === 'number' ? value : fallback
  }

  function readNumericString(key: string, fallback = ''): string {
    const value = draft[key]
    if (typeof value === 'string') return value
    return typeof value === 'number' && Number.isFinite(value) ? String(value) : fallback
  }

  const storageProvider = readString('storage_provider')

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">
          {t('admin:systemStorage.title', { defaultValue: 'Storage & uploads' })}
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-[var(--color-fg-muted)]">
          {t('admin:systemStorage.lead', {
            defaultValue: 'Configure platform object storage, archive retention, and upload restrictions.',
          })}
        </p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-6 flex flex-col gap-4 sm:mt-8 sm:gap-5">
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-4 sm:px-6 sm:py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:settings.fields.storageSection')}</h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:settings.fields.storageLead')}</p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.storageProvider')}
                htmlFor="storage-provider"
                hint={t('admin:settings.fields.storageProviderHint')}
              >
                <Select
                  value={storageProvider || 'none'}
                  onValueChange={(value) =>
                    setDraft({ ...draft, storage_provider: value === 'none' ? '' : value })
                  }
                >
                  <SelectTrigger id="storage-provider">
                    <SelectValue placeholder={t('admin:settings.fields.storageProviderPlaceholder')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{t('admin:settings.fields.storageNone')}</SelectItem>
                    <SelectItem value="local">{t('admin:settings.fields.storageLocal')}</SelectItem>
                    <SelectItem value="s3">{t('admin:settings.fields.storageS3')}</SelectItem>
                    <SelectItem value="aliyun_oss">{t('admin:settings.fields.storageAliyun')}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>

              <Field
                label={t('admin:settings.fields.storagePrefix')}
                htmlFor="storage-prefix"
                hint={t('admin:settings.fields.storagePrefixHint')}
              >
                <Input
                  id="storage-prefix"
                  placeholder="workspaces/"
                  value={readString('storage_prefix', 'workspaces/')}
                  onChange={(e) => setDraft({ ...draft, storage_prefix: e.target.value })}
                />
              </Field>

              {storageProvider !== '' && (
                <Field
                  label={t('admin:settings.fields.storageArchiveTtl')}
                  htmlFor="storage-archive-ttl"
                  hint={t('admin:settings.fields.storageArchiveTtlHint')}
                >
                  <Input
                    id="storage-archive-ttl"
                    type="number"
                    min={0}
                    step={1}
                    placeholder="0"
                    value={readNumericString('storage_archive_ttl_days')}
                    onChange={(e) => setDraft({ ...draft, storage_archive_ttl_days: e.target.value })}
                  />
                </Field>
              )}

              {storageProvider === 'local' && (
                <p className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-3 text-xs leading-relaxed text-[var(--color-fg-muted)] sm:p-4">
                  {t('admin:settings.fields.storageLocalNote')}
                </p>
              )}

              {storageProvider === 's3' && (
                <div className="flex flex-col gap-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-3 sm:gap-5 sm:p-4">
                  <Field label={t('admin:settings.fields.s3Bucket')} htmlFor="s3-bucket">
                    <Input
                      id="s3-bucket"
                      value={readString('storage_s3_bucket')}
                      onChange={(e) => setDraft({ ...draft, storage_s3_bucket: e.target.value })}
                    />
                  </Field>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={t('admin:settings.fields.s3Region')} htmlFor="s3-region">
                      <Input
                        id="s3-region"
                        placeholder="us-east-1"
                        value={readString('storage_s3_region')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_region: e.target.value })}
                      />
                    </Field>
                    <Field
                      label={t('admin:settings.fields.s3Endpoint')}
                      htmlFor="s3-endpoint"
                      hint={t('admin:settings.fields.s3EndpointHint')}
                    >
                      <Input
                        id="s3-endpoint"
                        placeholder="https://s3.amazonaws.com"
                        value={readString('storage_s3_endpoint')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_endpoint: e.target.value })}
                      />
                    </Field>
                  </div>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={t('admin:settings.fields.s3AccessKey')} htmlFor="s3-ak">
                      <Input
                        id="s3-ak"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_s3_access_key')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_access_key: e.target.value })}
                      />
                    </Field>
                    <Field label={t('admin:settings.fields.s3SecretKey')} htmlFor="s3-sk">
                      <Input
                        id="s3-sk"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_s3_secret_key')}
                        onChange={(e) => setDraft({ ...draft, storage_s3_secret_key: e.target.value })}
                      />
                    </Field>
                  </div>
                </div>
              )}

              {storageProvider === 'aliyun_oss' && (
                <div className="flex flex-col gap-4 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-3 sm:gap-5 sm:p-4">
                  <Field label={t('admin:settings.fields.ossBucket')} htmlFor="oss-bucket">
                    <Input
                      id="oss-bucket"
                      value={readString('storage_aliyun_bucket')}
                      onChange={(e) => setDraft({ ...draft, storage_aliyun_bucket: e.target.value })}
                    />
                  </Field>
                  <Field
                    label={t('admin:settings.fields.ossEndpoint')}
                    htmlFor="oss-endpoint"
                    hint={t('admin:settings.fields.ossEndpointHint')}
                  >
                    <Input
                      id="oss-endpoint"
                      placeholder="https://oss-cn-hangzhou.aliyuncs.com"
                      value={readString('storage_aliyun_endpoint')}
                      onChange={(e) => setDraft({ ...draft, storage_aliyun_endpoint: e.target.value })}
                    />
                  </Field>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={t('admin:settings.fields.ossAccessKeyId')} htmlFor="oss-akid">
                      <Input
                        id="oss-akid"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_aliyun_access_key_id')}
                        onChange={(e) => setDraft({ ...draft, storage_aliyun_access_key_id: e.target.value })}
                      />
                    </Field>
                    <Field label={t('admin:settings.fields.ossAccessKeySecret')} htmlFor="oss-aks">
                      <Input
                        id="oss-aks"
                        type="password"
                        autoComplete="off"
                        value={readString('storage_aliyun_access_key_secret')}
                        onChange={(e) => setDraft({ ...draft, storage_aliyun_access_key_secret: e.target.value })}
                      />
                    </Field>
                  </div>
                </div>
              )}
            </div>
          </div>

          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-4 sm:px-6 sm:py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:settings.fields.uploadsSection')}</h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:settings.fields.uploadsLead')}</p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.uploadAllowedExt')}
                htmlFor="upload-ext"
                hint={t('admin:settings.fields.uploadAllowedExtHint')}
              >
                <Input
                  id="upload-ext"
                  placeholder="pdf, docx, txt, png, jpg"
                  value={readString('upload_allowed_extensions')}
                  onChange={(e) => setDraft({ ...draft, upload_allowed_extensions: e.target.value })}
                />
              </Field>
              <Field
                label={t('admin:settings.fields.maxImageUploadMb', { defaultValue: 'Max image size (MB)' })}
                htmlFor="max-image-mb"
                hint={t('admin:settings.fields.maxImageUploadMbHint', {
                  defaultValue:
                    'Images larger than this are rejected at upload. 0 = default (5 MB). Cannot exceed the server upload ceiling.',
                })}
              >
                <Input
                  id="max-image-mb"
                  type="number"
                  min={0}
                  placeholder="5"
                  value={String(readNumber('max_image_upload_mb', 5))}
                  onChange={(e) =>
                    setDraft({ ...draft, max_image_upload_mb: Math.max(0, Number(e.target.value) || 0) })
                  }
                />
              </Field>
              <Field
                label={t('admin:settings.fields.maxFileUploadMb', { defaultValue: 'Max file size (MB, non-image)' })}
                htmlFor="max-file-mb"
                hint={t('admin:settings.fields.maxFileUploadMbHint', {
                  defaultValue:
                    'Non-image files (PDF, DOCX, CSV, …) larger than this are rejected. 0 = default (server upload ceiling).',
                })}
              >
                <Input
                  id="max-file-mb"
                  type="number"
                  min={0}
                  placeholder="0"
                  value={String(readNumber('max_file_upload_mb', 0))}
                  onChange={(e) =>
                    setDraft({ ...draft, max_file_upload_mb: Math.max(0, Number(e.target.value) || 0) })
                  }
                />
              </Field>
            </div>
          </div>

          <div className="flex justify-end">
            <Button className="w-full sm:w-auto" loading={saving} onClick={() => void save()}>
              {t('common:actions.save')}
            </Button>
          </div>
        </section>
      )}
    </div>
  )
}
