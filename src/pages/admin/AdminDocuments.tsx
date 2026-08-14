/**
 * AdminDocuments — settings related to turning an upload into searchable
 * context: KB embedding model, retrieval, and MinerU parsing credentials.
 *
 * All keys are part of the shared `/admin/settings` endpoint — this page
 * PATCHes a focused subset of `settingsKeys` (admin_handlers.go) so saves
 * from other admin settings pages don't stomp on each other.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { AlertTriangle } from 'lucide-react'
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

function isOpenAIRerankBaseURL(value: string): boolean {
  try {
    const parsed = new URL(value)
    const path = parsed.pathname.replace(/\/+$/, '')
    return (
      (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
      parsed.hostname !== '' &&
      parsed.username === '' &&
      parsed.password === '' &&
      parsed.search === '' &&
      parsed.hash === '' &&
      path.endsWith('/v1')
    )
  } catch {
    return false
  }
}

// Keys this page owns. Used to PATCH only the relevant subset, so concurrent
// edits on other admin pages aren't clobbered.
const OWNED_KEYS = [
  'embedding_model_id',
  'rag_full_text_threshold',
  'rag_code_full_text_max_lines',
  'rag_top_k',
  'rag_dynamic_topk',
  'rag_similarity_threshold',
  'rag_rerank_enabled',
  'rag_rerank_api_url',
  'rag_rerank_api_key',
  'rag_rerank_model',
  'mineru_api_url',
  'mineru_api_token',
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
    const rerankEnabled = readBool('rag_rerank_enabled', false)
    const rerankBaseURL = readString('rag_rerank_api_url').trim()
    if (
      rerankEnabled &&
      (!rerankBaseURL || !readString('rag_rerank_model').trim())
    ) {
      toast.error(t('admin:documents.ragRerankConfigRequired'))
      return
    }
    if (rerankEnabled && !isOpenAIRerankBaseURL(rerankBaseURL)) {
      toast.error(t('admin:documents.ragRerankBaseUrlInvalid'))
      return
    }

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

  const embeddingModelLocked = lockedEmbeddingModelID !== ''
  const storageProvider = readString('storage_provider')
  const mineruStorageReady =
    (storageProvider === 's3' && Boolean(readString('storage_s3_bucket'))) ||
    (storageProvider === 'aliyun_oss' &&
      Boolean(
        readString('storage_aliyun_bucket') &&
        readString('storage_aliyun_endpoint') &&
        readString('storage_aliyun_access_key_id') &&
        readString('storage_aliyun_access_key_secret'),
      ))

  return (
    <div className="mx-auto max-w-[76rem]">
      <header>
        <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:documents.title')}</h1>
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
                    'A prose document (PDF / Office / Markdown / logs) at/below this estimated size is injected in full every turn; above it, the document is vectorized and only relevant chunks are retrieved.',
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
                label={t('admin:documents.ragCodeMaxLines', { defaultValue: 'Code / text full-inject cap (lines)' })}
                htmlFor="rag-code-lines"
                hint={t('admin:documents.ragCodeMaxLinesHint', {
                  defaultValue:
                    'Source code, config, .txt and unrecognized text formats at/below this many lines are injected in full (very long single-line files are size-scaled and may still be retrieved); above it they are vectorized and retrieved like other documents.',
                })}
              >
                <Input
                  id="rag-code-lines"
                  type="number"
                  min={1}
                  placeholder="2000"
                  value={String(readNumber('rag_code_full_text_max_lines', 2000))}
                  onChange={(e) =>
                    // ≥1: the backend treats 0/blank as "use the default (2000)",
                    // so persisting a literal 0 would show 0 while acting as 2000.
                    setDraft({ ...draft, rag_code_full_text_max_lines: Math.max(1, Number(e.target.value) || 1) })
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
              <div className="border-t border-[var(--color-divider)] pt-5">
                <div className="flex items-center justify-between gap-4">
                  <div className="min-w-0">
                    <div className="text-sm text-[var(--color-fg)]">
                      {t('admin:documents.ragRerankEnabled')}
                    </div>
                    <div className="mt-0.5 max-w-3xl text-xs text-[var(--color-fg-subtle)]">
                      {t('admin:documents.ragRerankEnabledHint')}
                    </div>
                  </div>
                  <Switch
                    aria-label={t('admin:documents.ragRerankEnabled')}
                    checked={readBool('rag_rerank_enabled', false)}
                    onCheckedChange={(v) => setDraft({ ...draft, rag_rerank_enabled: v })}
                  />
                </div>
                {readBool('rag_rerank_enabled', false) && (
                  <div className="mt-5 flex flex-col gap-5">
                    <Field
                      label={t('admin:documents.ragRerankBaseUrl')}
                      htmlFor="rag-rerank-url"
                      hint={t('admin:documents.ragRerankBaseUrlHint')}
                    >
                      <Input
                        id="rag-rerank-url"
                        type="url"
                        placeholder="https://api.example.com/v1"
                        value={readString('rag_rerank_api_url')}
                        onChange={(e) => setDraft({ ...draft, rag_rerank_api_url: e.target.value })}
                      />
                    </Field>
                    <Field
                      label={t('admin:documents.ragRerankApiKey')}
                      htmlFor="rag-rerank-api-key"
                      hint={t('admin:documents.ragRerankApiKeyHint')}
                    >
                      <Input
                        id="rag-rerank-api-key"
                        type="password"
                        autoComplete="off"
                        placeholder="sk-..."
                        value={readString('rag_rerank_api_key')}
                        onChange={(e) => setDraft({ ...draft, rag_rerank_api_key: e.target.value })}
                      />
                    </Field>
                    <Field
                      label={t('admin:documents.ragRerankModel')}
                      htmlFor="rag-rerank-model"
                      hint={t('admin:documents.ragRerankModelHint')}
                    >
                      <Input
                        id="rag-rerank-model"
                        placeholder="BAAI/bge-reranker-v2-m3"
                        value={readString('rag_rerank_model')}
                        onChange={(e) => setDraft({ ...draft, rag_rerank_model: e.target.value })}
                      />
                    </Field>
                  </div>
                )}
              </div>
            </div>
          </div>

          {/* MinerU ---------------------------------------------------------- */}
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-5">
            <h2 className="font-serif text-lg text-[var(--color-fg)]">{t('admin:settings.fields.mineruSection')}</h2>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">{t('admin:settings.fields.mineruLead')}</p>
            {!mineruStorageReady && (
              <div className="mt-4 flex items-center gap-3 border-y border-[var(--color-divider)] py-3 text-[12px] text-[var(--color-warning)]">
                <AlertTriangle size={15} className="shrink-0" aria-hidden />
                <p className="min-w-0 flex-1">
                  {t('admin:documents.mineruStorageRequired', {
                    defaultValue: 'MinerU requires a complete S3 or Aliyun OSS configuration.',
                  })}
                </p>
                <Link to="/admin/storage" className="shrink-0 font-medium underline underline-offset-4">
                  {t('admin:documents.configureStorage', { defaultValue: 'Configure storage' })}
                </Link>
              </div>
            )}
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
