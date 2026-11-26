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
import { AlertTriangle, Check } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/hooks/use-toast'
import { embeddingGuardErrorText } from '@/lib/admin-embedding-errors'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { SettingsBlock, SettingsSection } from '@/components/settings/settings-section'

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
  'document_retrieval_mode',
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
  'grep_full_text_threshold',
  'grep_code_full_text_max_lines',
  'grep_max_matches',
  'grep_context_lines',
  'grep_read_max_lines',
  'grep_read_max_chars',
  'mineru_api_url',
  'mineru_api_token',
] as const

export default function AdminDocuments() {
  const { t } = useTranslation(['admin', 'common'])
  const [embeddingModels, setEmbeddingModels] = useState<ApiModel[]>([])
  const [draft, setDraft] = useState<Settings>({})
  const [lockedEmbeddingModelID, setLockedEmbeddingModelID] = useState('')
  const [embeddingModelsLoaded, setEmbeddingModelsLoaded] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  async function load() {
    setLoading(true)
    setEmbeddingModelsLoaded(false)
    try {
      const [s, em] = await Promise.all([adminApi.settings(), adminApi.models('embedding')])
      setDraft(s)
      setLockedEmbeddingModelID(typeof s.embedding_model_id === 'string' ? s.embedding_model_id : '')
      setEmbeddingModels(em)
      setEmbeddingModelsLoaded(true)
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

  const retrievalMode: 'rag' | 'grep' =
    readString('document_retrieval_mode', 'rag') === 'grep' ? 'grep' : 'rag'

  async function save() {
    const rerankEnabled = retrievalMode === 'rag' && readBool('rag_rerank_enabled', false)
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
      const patch: Settings = { document_retrieval_mode: retrievalMode }
      for (const k of OWNED_KEYS) {
        if (k in draft) patch[k] = draft[k]
      }
      const saved = await adminApi.updateSettings(patch)
      // Re-derive the lock from the server's authoritative post-write snapshot:
      // the PATCH returns the full settings map, so a repair-save that CLEARED a
      // dangling lock (select "—") no longer leaves a stale lock id (and its
      // dangling hint) behind.
      setLockedEmbeddingModelID(
        typeof saved.embedding_model_id === 'string' ? saved.embedding_model_id : '',
      )
      toast.success(t('admin:settings.saved'))
    } catch (e) {
      toast.error(embeddingGuardErrorText(t, e)
        || (e instanceof ApiError ? e.message : t('admin:common.failed')))
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

  // A lock pointing at a deleted model protects nothing, yet keeps refusing
  // every KB creation with knowledge_base_unavailable (the historical
  // delete-channel cascade damage). The server allows repairing exactly this
  // state, so the client must not keep the picker disabled for it.
  const embeddingModelDangling =
    lockedEmbeddingModelID !== '' && !embeddingModels.some((m) => m.id === lockedEmbeddingModelID)
  const embeddingModelLocked = lockedEmbeddingModelID !== '' && !embeddingModelDangling
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
    <div>
      <AdminPageHeader
        title={t('admin:documents.title')}
      />

      {loading ? (
        <PanelFallback />
      ) : (
        <section className="admin-form-layout mt-6 flex w-full min-w-0 flex-col gap-8">
          {/* Retrieval mode (RAG vs Grep) ----------------------------------- */}
          <SettingsSection
            title={t('admin:documents.modeSection', { defaultValue: 'Retrieval mode' })}
            description={t('admin:documents.modeDescription', {
              defaultValue: 'Choose how documents and knowledge bases are indexed and queried.',
            })}
            className="mb-0"
            bodyClassName="divide-y-0"
          >
            <SettingsBlock>
              <div className="grid gap-3 sm:grid-cols-2" role="radiogroup" aria-label={t('admin:documents.modeSection', { defaultValue: 'Retrieval mode' })}>
                {([
                  {
                    value: 'rag' as const,
                    title: t('admin:documents.modeRagTitle', { defaultValue: 'RAG (Vector retrieval)' }),
                    desc: t('admin:documents.modeRagDesc', {
                      defaultValue: 'Chunk documents, generate embeddings, and retrieve top-K fragments by semantic similarity and reranking.',
                    }),
                  },
                  {
                    value: 'grep' as const,
                    title: t('admin:documents.modeGrepTitle', { defaultValue: 'Grep (Full-text inspection)' }),
                    desc: t('admin:documents.modeGrepDesc', {
                      defaultValue: 'Skip embedding models completely. Search exact keywords/patterns and read exact line ranges on demand.',
                    }),
                  },
                ]).map((opt) => {
                  const selected = retrievalMode === opt.value
                  return (
                    <button
                      key={opt.value}
                      type="button"
                      role="radio"
                      aria-checked={selected}
                      onClick={() => setDraft({ ...draft, document_retrieval_mode: opt.value })}
                      className={[
                        'flex flex-col items-start gap-1.5 rounded-[12px] border p-4 text-left transition-colors',
                        selected
                          ? 'border-[var(--color-accent)] bg-[var(--color-accent-soft,var(--color-bg-muted))]'
                          : 'border-[var(--color-border)] bg-[var(--color-bg-subtle)] hover:border-[var(--color-fg-subtle)]',
                      ].join(' ')}
                    >
                      <div className="flex w-full items-center justify-between gap-2">
                        <span className="text-[14px] font-medium text-[var(--color-fg)]">{opt.title}</span>
                        {selected ? (
                          <span className="inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-accent)] text-white">
                            <Check size={12} aria-hidden />
                          </span>
                        ) : null}
                      </div>
                      <p className="text-[12.5px] leading-relaxed text-[var(--color-fg-muted)]">{opt.desc}</p>
                    </button>
                  )
                })}
              </div>
            </SettingsBlock>
          </SettingsSection>

          {retrievalMode === 'rag' ? (
            <>
              {/* Embedding model ------------------------------------------------- */}
              <SettingsSection
                title={t('admin:documents.embeddingSection')}
                className="mb-0"
                bodyClassName="divide-y-0"
              >
                <SettingsBlock>
                  <div>
                    <Field
                      label={t('admin:documents.embeddingModel')}
                      htmlFor="embed-model"
                      hint={embeddingModelLocked
                        ? t('admin:documents.embeddingModelLockedHint')
                        : embeddingModelDangling
                          ? t('admin:documents.embeddingModelDanglingHint', {
                              defaultValue:
                                'The previously selected embedding model was deleted. Pick a replacement and save to restore knowledge-base features; existing knowledge bases must be re-indexed.',
                            })
                          : undefined}
                      className="sm:max-w-md"
                    >
                      <Select
                        value={readString('embedding_model_id') || 'none'}
                        disabled={embeddingModelLocked}
                        onValueChange={(v) =>
                          setDraft({ ...draft, embedding_model_id: v === 'none' ? '' : v })
                        }
                      >
                        <SelectTrigger id="embed-model" data-admin-tour="documents-embedding-model">
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
                    {embeddingModelsLoaded && embeddingModels.length === 0 ? (
                      <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
                        <p className="text-xs text-[var(--color-fg-subtle)]">
                          {t('admin:documents.embeddingModelEmpty')}
                        </p>
                        <Button asChild variant="secondary" size="sm" className="shrink-0">
                          <Link to="/admin/models?kind=embedding">{t('admin:documents.addEmbeddingModel')}</Link>
                        </Button>
                      </div>
                    ) : null}
                  </div>
                </SettingsBlock>
              </SettingsSection>

              {/* RAG retrieval & injection -------------------------------------- */}
              <SettingsSection
                title={t('admin:documents.ragSection', { defaultValue: 'Retrieval & injection' })}
                className="mb-0"
                bodyClassName="divide-y-0"
              >
                <SettingsBlock>
                  <div className="admin-form-grid">
                    <Field
                      label={t('admin:documents.ragFullTextThreshold', { defaultValue: 'Full-inject threshold (tokens)' })}
                      htmlFor="rag-threshold"
                    >
                      <Input
                        id="rag-threshold"
                        type="number"
                        min={0}
                        wrapperClassName="w-40"
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
                    >
                      <Input
                        id="rag-code-lines"
                        type="number"
                        min={1}
                        wrapperClassName="w-40"
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
                    >
                      <Input
                        id="rag-topk"
                        type="number"
                        min={1}
                        wrapperClassName="w-40"
                        placeholder="8"
                        value={String(readNumber('rag_top_k', 8))}
                        onChange={(e) => setDraft({ ...draft, rag_top_k: Math.max(1, Number(e.target.value) || 1) })}
                      />
                    </Field>
                    <div className="admin-form-wide flex items-center justify-between gap-4">
                      <div>
                        <div className="text-sm text-[var(--color-fg)]">
                          {t('admin:documents.ragDynamicTopk', { defaultValue: 'Dynamic Top-K (by similarity)' })}
                        </div>
                      </div>
                      <Switch
                        aria-label={t('admin:documents.ragDynamicTopk')}
                        checked={readBool('rag_dynamic_topk', false)}
                        onCheckedChange={(v) => setDraft({ ...draft, rag_dynamic_topk: v })}
                      />
                    </div>
                    {readBool('rag_dynamic_topk', false) && (
                      <Field
                        label={t('admin:documents.ragSimThreshold', { defaultValue: 'Similarity threshold (0–1)' })}
                        htmlFor="rag-sim"
                      >
                        <Input
                          id="rag-sim"
                          type="number"
                          min={0}
                          max={1}
                          step={0.05}
                          wrapperClassName="w-40"
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
                    <div className="admin-form-wide">
                      <div className="flex items-center justify-between gap-4">
                        <div className="min-w-0">
                          <div className="text-sm text-[var(--color-fg)]">
                            {t('admin:documents.ragRerankEnabled')}
                          </div>
                        </div>
                        <Switch
                          aria-label={t('admin:documents.ragRerankEnabled')}
                          checked={readBool('rag_rerank_enabled', false)}
                          onCheckedChange={(v) => setDraft({ ...draft, rag_rerank_enabled: v })}
                        />
                      </div>
                      {readBool('rag_rerank_enabled', false) && (
                        <div className="admin-form-grid mt-6">
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
                </SettingsBlock>
              </SettingsSection>
            </>
          ) : (
            /* Grep retrieval settings --------------------------------------- */
            <SettingsSection
              title={t('admin:documents.grepSection', { defaultValue: 'Grep retrieval' })}
              description={t('admin:documents.grepSectionDesc', {
                defaultValue: 'No embedding model is required. Documents are parsed to normalized text for keyword/regex matching and line-range reading.',
              })}
              className="mb-0"
              bodyClassName="divide-y-0"
            >
              <SettingsBlock>
                <div className="admin-form-grid">
                  <Field
                    label={t('admin:documents.grepFullTextThreshold', { defaultValue: 'Small-doc full-inject threshold (tokens)' })}
                    htmlFor="grep-threshold"
                    hint={t('admin:documents.grepFullTextThresholdHint', {
                      defaultValue: 'Small conversation attachments at or below this limit inject directly; set 0 to always use Grep.',
                    })}
                  >
                    <Input
                      id="grep-threshold"
                      type="number"
                      min={0}
                      max={200000}
                      wrapperClassName="w-40"
                      placeholder="500"
                      value={String(readNumber('grep_full_text_threshold', 500))}
                      onChange={(e) =>
                        setDraft({ ...draft, grep_full_text_threshold: Math.max(0, Math.min(200000, Number(e.target.value) || 0)) })
                      }
                    />
                  </Field>
                  <Field
                    label={t('admin:documents.grepCodeMaxLines', { defaultValue: 'Code / text full-inject cap (lines)' })}
                    htmlFor="grep-code-lines"
                  >
                    <Input
                      id="grep-code-lines"
                      type="number"
                      min={0}
                      max={20000}
                      wrapperClassName="w-40"
                      placeholder="200"
                      value={String(readNumber('grep_code_full_text_max_lines', 200))}
                      onChange={(e) =>
                        setDraft({ ...draft, grep_code_full_text_max_lines: Math.max(0, Math.min(20000, Number(e.target.value) || 0)) })
                      }
                    />
                  </Field>
                  <Field
                    label={t('admin:documents.grepMaxMatches', { defaultValue: 'Max matches per search' })}
                    htmlFor="grep-max-matches"
                  >
                    <Input
                      id="grep-max-matches"
                      type="number"
                      min={1}
                      max={100}
                      wrapperClassName="w-40"
                      placeholder="20"
                      value={String(readNumber('grep_max_matches', 20))}
                      onChange={(e) =>
                        setDraft({ ...draft, grep_max_matches: Math.max(1, Math.min(100, Number(e.target.value) || 1)) })
                      }
                    />
                  </Field>
                  <Field
                    label={t('admin:documents.grepContextLines', { defaultValue: 'Context lines around match' })}
                    htmlFor="grep-context-lines"
                  >
                    <Input
                      id="grep-context-lines"
                      type="number"
                      min={0}
                      max={20}
                      wrapperClassName="w-40"
                      placeholder="3"
                      value={String(readNumber('grep_context_lines', 3))}
                      onChange={(e) =>
                        setDraft({ ...draft, grep_context_lines: Math.max(0, Math.min(20, Number(e.target.value) || 0)) })
                      }
                    />
                  </Field>
                  <Field
                    label={t('admin:documents.grepReadMaxLines', { defaultValue: 'Max lines per read' })}
                    htmlFor="grep-read-max-lines"
                  >
                    <Input
                      id="grep-read-max-lines"
                      type="number"
                      min={10}
                      max={1000}
                      wrapperClassName="w-40"
                      placeholder="200"
                      value={String(readNumber('grep_read_max_lines', 200))}
                      onChange={(e) =>
                        setDraft({ ...draft, grep_read_max_lines: Math.max(10, Math.min(1000, Number(e.target.value) || 10)) })
                      }
                    />
                  </Field>
                  <Field
                    label={t('admin:documents.grepReadMaxChars', { defaultValue: 'Max chars per tool output' })}
                    htmlFor="grep-read-max-chars"
                  >
                    <Input
                      id="grep-read-max-chars"
                      type="number"
                      min={1000}
                      max={64000}
                      step={1000}
                      wrapperClassName="w-40"
                      placeholder="16000"
                      value={String(readNumber('grep_read_max_chars', 16000))}
                      onChange={(e) =>
                        setDraft({ ...draft, grep_read_max_chars: Math.max(1000, Math.min(64000, Number(e.target.value) || 1000)) })
                      }
                    />
                  </Field>
                </div>
              </SettingsBlock>
            </SettingsSection>
          )}

          {/* MinerU ---------------------------------------------------------- */}
          <SettingsSection
            title={t('admin:settings.fields.mineruSection')}
            className="mb-0"
            bodyClassName="divide-y-0"
          >
            <SettingsBlock>
              {!mineruStorageReady && (
                <div className="flex items-center gap-3 py-3 text-[12px] text-[var(--color-warning)]">
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
              <div className="admin-form-grid admin-form-grid--pair">
                <Field
                  label={t('admin:settings.fields.mineruBaseUrl')}
                  htmlFor="mineru-url"
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
            </SettingsBlock>
          </SettingsSection>

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
