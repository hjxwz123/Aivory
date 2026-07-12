/**
 * AdminDocuments — every knob related to turning an upload into searchable
 * context: KB embedding model, MinerU credentials, source-file storage, and
 * the upload extension allowlist.
 *
 * All keys are part of the shared `/admin/settings` endpoint — this page
 * PATCHes a focused subset of `settingsKeys` (admin_handlers.go) so saves
 * from /admin/settings, /admin/tools and /admin/documents don't stomp on
 * each other.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { adminApi, ApiError } from '@/api'
import type { ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

type Settings = Record<string, unknown>

// Keys this page owns. Used to PATCH only the relevant subset, so concurrent
// edits on other admin pages aren't clobbered.
const OWNED_KEYS = [
  'embedding_model_id',
  'rag_full_text_threshold',
  'rag_top_k',
  'rag_dynamic_topk',
  'rag_similarity_threshold',
  'mineru_api_url',
  'mineru_api_token',
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

export default function AdminDocuments() {
  const { t } = useTranslation(['admin', 'common'])
  const [embeddingModels, setEmbeddingModels] = useState<ApiModel[]>([])
  const [draft, setDraft] = useState<Settings>({})
  const [lockedEmbeddingModelID, setLockedEmbeddingModelID] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const [s, em] = await Promise.all([adminApi.settings(), adminApi.models('embedding')])
      setDraft(s)
      setLockedEmbeddingModelID(typeof s.embedding_model_id === 'string' ? s.embedding_model_id : '')
      setEmbeddingModels(em)
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

  async function save() {
    setSaving(true)
    try {
      const patch: Settings = {}
      for (const k of OWNED_KEYS) {
        if (k in draft) patch[k] = draft[k]
      }
      await adminApi.updateSettings(patch)
      const nextEmbedding = draft.embedding_model_id
      if (typeof nextEmbedding === 'string' && nextEmbedding) {
        setLockedEmbeddingModelID(nextEmbedding)
      }
      toast.success(t('admin:settings.saved'))
    } catch (e) {
      toast.error(e instanceof ApiError && e.message === 'embedding_model_locked'
        ? t('admin:documents.embeddingModelLockedError')
        : e instanceof ApiError
          ? e.message
          : t('admin:common.failed'))
    } finally {
      setSaving(false)
    }
  }

  function readString(key: string, fallback = ''): string {
    const v = draft[key]
    return typeof v === 'string' ? v : fallback
  }
  function readNumber(key: string, fallback = 0): number {
    const v = draft[key]
    return typeof v === 'number' ? v : fallback
  }
  function readBool(key: string, fallback = false): boolean {
    const v = draft[key]
    return typeof v === 'boolean' ? v : fallback
  }

  const storageProvider = readString('storage_provider')
  const embeddingModelLocked = lockedEmbeddingModelID !== ''

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('admin:documents.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:documents.lead')}</p>
      </header>

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="mt-8 flex flex-col gap-5">
          {/* Embedding model ------------------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:documents.embeddingSection')}</h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:documents.embeddingLead')}</p>
            <div className="mt-4">
              <Field
                label={t('admin:documents.embeddingModel')}
                htmlFor="embed-model"
                hint={embeddingModelLocked
                  ? t('admin:documents.embeddingModelLockedHint')
                  : t('admin:documents.embeddingModelHint')}
              >
                <Select
                  value={readString('embedding_model_id') || 'none'}
                  disabled={embeddingModelLocked}
                  onValueChange={(v) =>
                    setDraft({ ...draft, embedding_model_id: v === 'none' ? '' : v })
                  }
                >
                  <SelectTrigger id="embed-model">
                    <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">—</SelectItem>
                    {embeddingModels.map((m) => (
                      <SelectItem key={m.id} value={m.id}>
                        {m.label} (dim {m.dim})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>
          </div>

          {/* RAG retrieval & injection -------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">
              {t('admin:documents.ragSection', { defaultValue: 'Retrieval & injection' })}
            </h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              {t('admin:documents.ragLead', {
                defaultValue:
                  'Control when an uploaded document is injected whole vs. retrieved by relevance, and how much is injected.',
              })}
            </p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:documents.ragFullTextThreshold', { defaultValue: 'Full-inject threshold (tokens)' })}
                htmlFor="rag-threshold"
                hint={t('admin:documents.ragFullTextThresholdHint', {
                  defaultValue:
                    'A document at/below this estimated size is injected in full every turn; above it, the document is vectorized and only relevant chunks are retrieved.',
                })}
              >
                <Input
                  id="rag-threshold"
                  type="number"
                  min={0}
                  placeholder="8000"
                  value={String(readNumber('rag_full_text_threshold', 8000))}
                  onChange={(e) =>
                    setDraft({ ...draft, rag_full_text_threshold: Math.max(0, Number(e.target.value) || 0) })
                  }
                />
              </Field>
              <Field
                label={t('admin:documents.ragTopK', { defaultValue: 'Retrieved chunks (Top-K)' })}
                htmlFor="rag-topk"
                hint={t('admin:documents.ragTopKHint', {
                  defaultValue: 'How many chunks to retrieve for a vectorized document (when dynamic Top-K is off).',
                })}
              >
                <Input
                  id="rag-topk"
                  type="number"
                  min={1}
                  placeholder="8"
                  value={String(readNumber('rag_top_k', 8))}
                  onChange={(e) => setDraft({ ...draft, rag_top_k: Math.max(1, Number(e.target.value) || 1) })}
                />
              </Field>
              <div className="flex items-center justify-between gap-4">
                <div>
                  <div className="text-sm text-[var(--color-fg)]">
                    {t('admin:documents.ragDynamicTopk', { defaultValue: 'Dynamic Top-K (by similarity)' })}
                  </div>
                  <div className="mt-0.5 text-xs text-[var(--color-fg-subtle)]">
                    {t('admin:documents.ragDynamicTopkHint', {
                      defaultValue:
                        'Instead of a fixed K, inject every retrieved chunk whose similarity clears the threshold.',
                    })}
                  </div>
                </div>
                <Switch
                  checked={readBool('rag_dynamic_topk', false)}
                  onCheckedChange={(v) => setDraft({ ...draft, rag_dynamic_topk: v })}
                />
              </div>
              {readBool('rag_dynamic_topk', false) && (
                <Field
                  label={t('admin:documents.ragSimThreshold', { defaultValue: 'Similarity threshold (0–1)' })}
                  htmlFor="rag-sim"
                  hint={t('admin:documents.ragSimThresholdHint', {
                    defaultValue: 'Cosine-similarity cutoff. Chunks scoring at/above this are injected.',
                  })}
                >
                  <Input
                    id="rag-sim"
                    type="number"
                    min={0}
                    max={1}
                    step={0.05}
                    placeholder="0.5"
                    value={String(readNumber('rag_similarity_threshold', 0.5))}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        rag_similarity_threshold: Math.min(1, Math.max(0, Number(e.target.value) || 0)),
                      })
                    }
                  />
                </Field>
              )}
            </div>
          </div>

          {/* MinerU ---------------------------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:settings.fields.mineruSection')}</h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:settings.fields.mineruLead')}</p>
            <div className="mt-4 flex flex-col gap-5">
              <Field
                label={t('admin:settings.fields.mineruBaseUrl')}
                htmlFor="mineru-url"
                hint={t('admin:settings.fields.mineruBaseUrlHint')}
              >
                <Input
                  id="mineru-url"
                  type="url"
                  placeholder="https://mineru.net"
                  value={readString('mineru_api_url')}
                  onChange={(e) => setDraft({ ...draft, mineru_api_url: e.target.value })}
                />
              </Field>
              <Field
                label={t('admin:settings.fields.mineruToken')}
                htmlFor="mineru-token"
                hint={t('admin:settings.fields.mineruTokenHint')}
              >
                <Input
                  id="mineru-token"
                  type="password"
                  autoComplete="off"
                  value={readString('mineru_api_token')}
                  onChange={(e) => setDraft({ ...draft, mineru_api_token: e.target.value })}
                />
              </Field>
            </div>
          </div>

          {/* Object storage -------------------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
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
                  onValueChange={(v) =>
                    setDraft({ ...draft, storage_provider: v === 'none' ? '' : v })
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
                    placeholder="0"
                    value={readString('storage_archive_ttl_days')}
                    onChange={(e) => setDraft({ ...draft, storage_archive_ttl_days: e.target.value })}
                  />
                </Field>
              )}

              {storageProvider === 'local' && (
                <p className="rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4 text-xs leading-relaxed text-[var(--color-fg-muted)]">
                  {t('admin:settings.fields.storageLocalNote')}
                </p>
              )}

              {storageProvider === 's3' && (
                <div className="flex flex-col gap-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
                  <Field label={t('admin:settings.fields.s3Bucket')} htmlFor="s3-bucket">
                    <Input
                      id="s3-bucket"
                      value={readString('storage_s3_bucket')}
                      onChange={(e) => setDraft({ ...draft, storage_s3_bucket: e.target.value })}
                    />
                  </Field>
                  <div className="grid grid-cols-2 gap-4">
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
                  <div className="grid grid-cols-2 gap-4">
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
                <div className="flex flex-col gap-5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4">
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
                  <div className="grid grid-cols-2 gap-4">
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

          {/* Uploads --------------------------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
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
                  onChange={(e) =>
                    setDraft({ ...draft, upload_allowed_extensions: e.target.value })
                  }
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
            <Button loading={saving} onClick={() => void save()}>
              {t('common:actions.save')}
            </Button>
          </div>
        </section>
      )}
    </div>
  )
}
