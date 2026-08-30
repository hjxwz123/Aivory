/**
 * AdminChannels — list, create and edit upstream channels.
 * Channels carry the (type + base_url + api_key + api_format) tuple from
 * design.md §2.3-B. The api_key column is never re-displayed; admins can leave
 * the field blank when editing to keep the existing secret.
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, RefreshCw, Search, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiChannel, ApiChannelModelCandidate } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
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
import { Badge } from '@/components/ui/badge'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { normalizeOpenAIBaseUrl } from '@/lib/channel-base-url'
import { embeddingGuardErrorText } from '@/lib/admin-embedding-errors'

type Editable = Partial<ApiChannel> & { api_key?: string }
type ChannelEditor = {
  open: boolean
  row?: ApiChannel
  draft: Editable
}
type ModelDiscoveryState = {
  loading: boolean
  fetched: boolean
  error: string | null
  models: ApiChannelModelCandidate[]
  selected: Set<string>
  skippedUnsupported: number
}

const TYPES = ['openai', 'claude', 'gemini'] as const

function inferManualModelKind(requestID: string): ApiChannelModelCandidate['kind'] {
  const id = requestID.toLowerCase()
  if (id.includes('embedding') || id.startsWith('embed-')) return 'embedding'
  if (
    id.startsWith('dall-e')
    || id.startsWith('gpt-image-')
    || id.startsWith('imagen-')
    || id.includes('image-generation')
    || id.includes('-image-')
    || id.endsWith('-image')
  ) return 'image'
  return 'chat'
}

export default function AdminChannels() {
  const { t } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiChannel[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<ChannelEditor>({
    open: false,
    draft: { type: 'openai', api_format: 'chat', enabled: true },
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiChannel | null>(null)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const [showBaseUrlError, setShowBaseUrlError] = useState(false)
  const [modelInput, setModelInput] = useState('')
  const [pendingModels, setPendingModels] = useState<ApiChannelModelCandidate[]>([])
  const [upstreamModelsOpen, setUpstreamModelsOpen] = useState(false)
  const [modelSearch, setModelSearch] = useState('')
  const discoveryRequestRef = useRef(0)
  const [modelDiscovery, setModelDiscovery] = useState<ModelDiscoveryState>({
    loading: false,
    fetched: false,
    error: null,
    models: [],
    selected: new Set(),
    skippedUnsupported: 0,
  })

  const filteredDiscoveredModels = useMemo(() => {
    const query = modelSearch.trim().toLowerCase()
    if (!query) return modelDiscovery.models
    return modelDiscovery.models.filter((model) =>
      model.request_id.toLowerCase().includes(query)
      || model.label.toLowerCase().includes(query)
      || model.kind.includes(query),
    )
  }, [modelDiscovery.models, modelSearch])
  const selectedDiscoveredModels = useMemo(
    () => modelDiscovery.models.filter((model) => modelDiscovery.selected.has(model.request_id.toLowerCase())),
    [modelDiscovery.models, modelDiscovery.selected],
  )
  const pendingModelKeys = useMemo(
    () => new Set(pendingModels.map((model) => model.request_id.toLowerCase())),
    [pendingModels],
  )

  async function load() {
    setLoading(true)
    try {
      setRows(await adminApi.channels())
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

  function resetModelDiscovery() {
    discoveryRequestRef.current++
    setUpstreamModelsOpen(false)
    setModelSearch('')
    setModelDiscovery({
      loading: false,
      fetched: false,
      error: null,
      models: [],
      selected: new Set(),
      skippedUnsupported: 0,
    })
  }

  function updateDraft(patch: Partial<Editable>, invalidateDiscovery = false) {
    setEditor((current) => ({ ...current, draft: { ...current.draft, ...patch } }))
    if (invalidateDiscovery) resetModelDiscovery()
  }

  function openNew() {
    setShowBaseUrlError(false)
    resetModelDiscovery()
    setModelInput('')
    setPendingModels([])
    setEditor({
      open: true,
      draft: { type: 'openai', api_format: 'chat', enabled: true, name: '', base_url: '' },
    })
  }

  function openEdit(row: ApiChannel) {
    setShowBaseUrlError(false)
    resetModelDiscovery()
    setModelInput('')
    setPendingModels([])
    setEditor({ open: true, row, draft: { ...row, api_key: '' } })
  }

  function addPendingModels(models: ApiChannelModelCandidate[]) {
    setPendingModels((current) => {
      const seen = new Set(current.map((model) => model.request_id.toLowerCase()))
      const next = [...current]
      models.forEach((model) => {
        const requestID = model.request_id.trim()
        const key = requestID.toLowerCase()
        if (!requestID || seen.has(key)) return
        seen.add(key)
        next.push({
          ...model,
          request_id: requestID,
          label: model.label.trim() || requestID,
          description: model.description.trim(),
        })
      })
      return next
    })
  }

  function addManualModel() {
    const requestID = modelInput.trim()
    if (!requestID) return
    addPendingModels([{
      request_id: requestID,
      label: requestID,
      description: '',
      kind: inferManualModelKind(requestID),
    }])
    setModelInput('')
  }

  function removePendingModel(requestID: string) {
    setPendingModels((current) => current.filter((model) => model.request_id !== requestID))
  }

  async function discoverModels() {
    const d = editor.draft
    const normalizedBaseUrl = d.type === 'openai'
      ? normalizeOpenAIBaseUrl(d.base_url ?? '')
      : (d.base_url ?? '').trim()
    if (normalizedBaseUrl === null) {
      setShowBaseUrlError(true)
      return
    }
    const requestID = ++discoveryRequestRef.current
    setModelDiscovery({
      loading: true,
      fetched: false,
      error: null,
      models: [],
      selected: new Set(),
      skippedUnsupported: 0,
    })
    try {
      const result = await adminApi.discoverChannelModels({ ...d, base_url: normalizedBaseUrl })
      if (requestID !== discoveryRequestRef.current) return
      setModelDiscovery({
        loading: false,
        fetched: true,
        error: null,
        models: result.models,
        selected: new Set(),
        skippedUnsupported: result.skipped_unsupported,
      })
    } catch {
      if (requestID !== discoveryRequestRef.current) return
      setModelDiscovery({
        loading: false,
        fetched: false,
        error: t('admin:channels.modelAdd.discoverFailed'),
        models: [],
        selected: new Set(),
        skippedUnsupported: 0,
      })
    }
  }

  function toggleDiscoveredModel(model: ApiChannelModelCandidate) {
    const key = model.request_id.toLowerCase()
    setModelDiscovery((current) => {
      const selected = new Set(current.selected)
      if (selected.has(key)) selected.delete(key)
      else selected.add(key)
      return { ...current, selected }
    })
  }

  function selectAllDiscoveredModels() {
    setModelDiscovery((current) => {
      const selected = new Set(current.selected)
      current.models.forEach((model) => {
        const key = model.request_id.toLowerCase()
        if (!pendingModelKeys.has(key)) selected.add(key)
      })
      return { ...current, selected }
    })
  }

  function clearSelectedModels() {
    setModelDiscovery((current) => ({ ...current, selected: new Set() }))
  }

  function openUpstreamModels() {
    const normalizedBaseUrl = editor.draft.type === 'openai'
      ? normalizeOpenAIBaseUrl(editor.draft.base_url ?? '')
      : (editor.draft.base_url ?? '').trim()
    if (normalizedBaseUrl === null) {
      setShowBaseUrlError(true)
      return
    }
    setModelSearch('')
    setUpstreamModelsOpen(true)
    void discoverModels()
  }

  function confirmUpstreamModels() {
    addPendingModels(selectedDiscoveredModels)
    setUpstreamModelsOpen(false)
  }

  async function submit() {
    if (savingRef.current) return
    const d = editor.draft
    if (!d.name) {
      toast.error(t('admin:channels.errors.nameRequired'))
      return
    }
    const modelsToCreate = editor.row ? [] : pendingModels
    const normalizedBaseUrl = d.type === 'openai'
      ? normalizeOpenAIBaseUrl(d.base_url ?? '')
      : (d.base_url ?? '').trim()
    if (normalizedBaseUrl === null) {
      setShowBaseUrlError(true)
      return
    }
    const payload = { ...d, name: d.name.trim(), base_url: normalizedBaseUrl }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updateChannel(editor.row.id, payload)
        toast.success(t('admin:channels.updated'))
      } else {
        const created = await adminApi.createChannel(payload)
        let modelBatchCreated = 0
        let modelBatchSkipped = 0
        let modelBatchFailed = false
        if (modelsToCreate.length > 0) {
          try {
            const result = await adminApi.createChannelModelsBatch(created.id, modelsToCreate)
            modelBatchCreated = result.created
            modelBatchSkipped = result.skipped_existing + result.skipped_duplicate
          } catch {
            modelBatchFailed = true
          }
        }
        setEditor({ ...editor, open: false })
        await load()
        if (modelBatchFailed) {
          toast.warning(t('admin:channels.created'), t('admin:channels.modelAdd.batchFailed'))
        } else if (modelsToCreate.length > 0) {
          if (modelBatchCreated > 0) {
            toast.success(
              t('admin:channels.created'),
              modelBatchSkipped > 0
                ? t('admin:channels.modelAdd.batchPartial', { created: modelBatchCreated, skipped: modelBatchSkipped })
                : t('admin:channels.modelAdd.batchSuccess', { count: modelBatchCreated }),
            )
          } else {
            toast.warning(t('admin:channels.created'), t('admin:channels.modelAdd.batchEmpty'))
          }
        } else {
          toast.success(t('admin:channels.created'))
        }
        return
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

  async function remove(row: ApiChannel) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeChannel(row.id)
      toast.success(t('admin:channels.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(embeddingGuardErrorText(t, e) || (e instanceof ApiError ? e.message : t('admin:common.failed')))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  function persistOrder(next: ApiChannel[], prev: ApiChannel[]) {
    void adminApi.reorderChannels(next.map((r) => r.id)).catch((e) => {
      setRows(prev)
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  return (
    <div>
      <header className="flex flex-col items-start gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:channels.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:channels.lead')}</p>
        </div>
        <Button data-admin-tour="channels-create" className="min-h-[var(--tap-min)] w-full sm:min-h-0 sm:w-auto" leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
          {t('admin:channels.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center">
            <p className="text-[var(--color-fg-muted)] text-sm">{t('admin:channels.empty')}</p>
            <div className="mt-4">
              <Button onClick={openNew}>{t('admin:common.createFirst', { kind: t('admin:channels.title').toLowerCase() })}</Button>
            </div>
          </div>
        ) : (
          <AdminSortableList
            items={rows}
            onItemsChange={setRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            mobileDragOnly
            rowClassName="grid grid-cols-[2.75rem_minmax(0,1fr)] items-start gap-x-2 gap-y-2 px-2 py-3.5 md:grid-cols-[auto_auto_minmax(0,1fr)_auto_auto] md:items-center md:gap-4 md:px-5 md:py-4"
            renderItem={(r) => (
              <>
                <div className="col-start-2 row-start-1 min-w-0 md:col-start-auto md:row-start-auto">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-[var(--color-fg)] truncate">{r.name}</span>
                    <Badge size="xs">{r.type}</Badge>
                    {r.type === 'openai' && r.api_format ? <Badge size="xs">{r.api_format}</Badge> : null}
                    {r.enabled ? null : <Badge size="xs" variant="neutral">{t('admin:channels.labels.disabled')}</Badge>}
                  </div>
                  <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">
                    {r.base_url || t('admin:channels.labels.defaultEndpoint')} · {r.has_api_key ? t('admin:channels.labels.keySet') : t('admin:channels.labels.noKey')}
                  </div>
                </div>
                <div className="col-span-2 row-start-2 flex items-center justify-end gap-1 md:contents">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="max-md:size-[var(--tap-min)] max-md:gap-0 max-md:px-0"
                    aria-label={`${t('admin:common.edit')}: ${r.name}`}
                    leadingIcon={<Pencil size={13} aria-hidden />}
                    onClick={() => openEdit(r)}
                  >
                    <span className="max-md:sr-only">{t('admin:common.edit')}</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="max-md:size-[var(--tap-min)] max-md:gap-0 max-md:px-0"
                    aria-label={`${t('admin:common.remove')}: ${r.name}`}
                    leadingIcon={<Trash2 size={13} aria-hidden />}
                    onClick={() => setConfirmDelete(r)}
                  >
                    <span className="max-md:sr-only">{t('admin:common.remove')}</span>
                  </Button>
                </div>
              </>
            )}
          />
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => !savingRef.current && setEditor({ ...editor, open: o })}>
        <DialogContent size={editor.row ? 'md' : 'lg'}>
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:channels.editorTitle') : t('admin:channels.newTitle')}</DialogTitle>
            <DialogDescription>
              {editor.row ? t('admin:channels.editorDescriptionEdit') : t('admin:channels.editorDescriptionNew')}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <div className="grid content-start gap-4">
                <Field label={t('admin:channels.fields.name')} htmlFor="ch-name">
                  <Input
                    id="ch-name"
                    disabled={saving}
                    value={editor.draft.name ?? ''}
                    onChange={(e) => updateDraft({ name: e.target.value })}
                    placeholder="Anthropic production"
                  />
                </Field>
                <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                  <Field label={t('admin:channels.fields.type')} htmlFor="ch-type">
                    <Select
                      disabled={saving}
                      value={editor.draft.type ?? 'openai'}
                      onValueChange={(v) => {
                        const type = v as ApiChannel['type']
                        setShowBaseUrlError(false)
                        updateDraft({
                          type,
                          api_format: type === 'openai' ? (editor.draft.api_format || 'chat') : '',
                        }, true)
                      }}
                    >
                      <SelectTrigger id="ch-type">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {TYPES.map((tp) => (
                          <SelectItem key={tp} value={tp}>
                            {tp}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                  {editor.draft.type === 'openai' ? (
                    <Field label={t('admin:channels.fields.apiFormat')} htmlFor="ch-fmt" hint={t('admin:channels.fields.apiFormatHint')}>
                      <Select
                        disabled={saving}
                        value={editor.draft.api_format ?? 'chat'}
                        onValueChange={(v) => updateDraft({ api_format: v as ApiChannel['api_format'] }, true)}
                      >
                        <SelectTrigger id="ch-fmt">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="chat">chat</SelectItem>
                          <SelectItem value="responses">responses</SelectItem>
                        </SelectContent>
                      </Select>
                    </Field>
                  ) : null}
                </div>
                <Field
                  label={t('admin:channels.fields.baseUrl')}
                  htmlFor="ch-url"
                  hint={t(editor.draft.type === 'openai'
                    ? 'admin:channels.fields.openAIBaseUrlHint'
                    : 'admin:channels.fields.baseUrlHint')}
                  error={editor.draft.type === 'openai'
                    && showBaseUrlError
                    && normalizeOpenAIBaseUrl(editor.draft.base_url ?? '') === null
                    ? t('admin:channels.errors.openAIBaseUrlV1Required')
                    : undefined}
                >
                  <Input
                    id="ch-url"
                    disabled={saving}
                    value={editor.draft.base_url ?? ''}
                    onChange={(e) => updateDraft({ base_url: e.target.value }, true)}
                    onBlur={() => editor.draft.type === 'openai' && setShowBaseUrlError(true)}
                    invalid={editor.draft.type === 'openai'
                      && showBaseUrlError
                      && normalizeOpenAIBaseUrl(editor.draft.base_url ?? '') === null}
                    placeholder={editor.draft.type === 'openai' ? 'https://api.openai.com/v1' : 'https://api.example.com'}
                  />
                </Field>
                <Field
                  label={t('admin:channels.fields.apiKey')}
                  htmlFor="ch-key"
                  hint={editor.row ? t('admin:channels.fields.apiKeyHintEdit') : t('admin:channels.fields.apiKeyHintNew')}
                >
                  <Input
                    id="ch-key"
                    type="password"
                    disabled={saving}
                    value={editor.draft.api_key ?? ''}
                    onChange={(e) => updateDraft({ api_key: e.target.value }, true)}
                    placeholder="sk-…"
                  />
                </Field>
                <div className="rounded-[10px] bg-[var(--color-bg-muted)] p-1">
                  <label className="flex min-h-11 items-center justify-between gap-4 rounded-[8px] px-2.5 py-2">
                    <span className="text-sm text-[var(--color-fg)]">{t('admin:channels.fields.enabled')}</span>
                    <Switch
                      disabled={saving}
                      checked={editor.draft.enabled ?? true}
                      onCheckedChange={(v) => updateDraft({ enabled: v })}
                    />
                  </label>
                </div>
              </div>

              {!editor.row ? (
                <div className="grid gap-3 border-t border-[var(--color-border)] pt-5">
                  <div>
                    <h3 className="text-sm font-medium text-[var(--color-fg)]">{t('admin:channels.modelAdd.title')}</h3>
                    <p className="mt-1 text-xs leading-5 text-[var(--color-fg-muted)]">{t('admin:channels.modelAdd.hint')}</p>
                  </div>
                  <form
                    className="flex w-full flex-col gap-2 sm:flex-row"
                    onSubmit={(event) => {
                      event.preventDefault()
                      addManualModel()
                    }}
                  >
                    <Input
                      aria-label={t('admin:channels.modelAdd.inputLabel')}
                      disabled={saving}
                      value={modelInput}
                      onChange={(event) => setModelInput(event.target.value)}
                      placeholder={t('admin:channels.modelAdd.inputPlaceholder')}
                      wrapperClassName="w-full min-w-0 sm:flex-1"
                      className="font-mono"
                    />
                    <Button
                      type="submit"
                      variant="secondary"
                      disabled={saving || !modelInput.trim()}
                      leadingIcon={<Plus size={14} aria-hidden />}
                      className="sm:shrink-0"
                    >
                      {t('admin:channels.modelAdd.add')}
                    </Button>
                    <Button
                      variant="outline"
                      disabled={saving}
                      leadingIcon={<RefreshCw size={14} aria-hidden />}
                      onClick={openUpstreamModels}
                      className="sm:shrink-0"
                    >
                      {t('admin:channels.modelAdd.fromUpstream')}
                    </Button>
                  </form>

                  <div className="min-h-28 max-h-56 overflow-y-auto rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-1">
                    {pendingModels.length > 0 ? pendingModels.map((model) => (
                      <div key={model.request_id} className="flex min-h-11 items-center gap-3 rounded-[6px] px-2.5 py-2">
                        <span className="min-w-0 flex-1">
                          <span className="flex min-w-0 items-center gap-2">
                            <span className="truncate text-sm font-medium text-[var(--color-fg)]">{model.label}</span>
                            <Badge size="xs">{model.kind}</Badge>
                          </span>
                          {model.label !== model.request_id ? (
                            <span className="mt-0.5 block truncate font-mono text-[11px] text-[var(--color-fg-subtle)]">
                              {model.request_id}
                            </span>
                          ) : null}
                        </span>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={t('admin:channels.modelAdd.removeModel', { name: model.label })}
                          title={t('admin:channels.modelAdd.remove')}
                          onClick={() => removePendingModel(model.request_id)}
                        >
                          <Trash2 size={14} aria-hidden />
                        </Button>
                      </div>
                    )) : (
                      <div className="flex min-h-24 items-center justify-center px-4 text-center text-xs text-[var(--color-fg-muted)]">
                        {t('admin:channels.modelAdd.empty')}
                      </div>
                    )}
                  </div>
                </div>
              ) : null}
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" disabled={saving} onClick={() => setEditor({ ...editor, open: false })}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submit()}>
              {editor.row
                ? t('common:actions.save')
                : (pendingModels.length > 0
                    ? t('admin:channels.modelAdd.createWithModels', { count: pendingModels.length })
                    : t('admin:channels.modelAdd.createChannel'))}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={upstreamModelsOpen && editor.open} onOpenChange={setUpstreamModelsOpen}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{t('admin:channels.modelAdd.upstreamTitle')}</DialogTitle>
            <DialogDescription>{t('admin:channels.modelAdd.upstreamDescription')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            {modelDiscovery.loading ? (
              <div className="flex h-72 items-center justify-center text-sm text-[var(--color-fg-muted)]">
                <span className="mr-2 inline-block size-4 animate-spin rounded-full border-2 border-current border-r-transparent" aria-hidden />
                {t('admin:channels.modelAdd.loading')}
              </div>
            ) : modelDiscovery.error ? (
              <div className="flex min-h-56 flex-col items-center justify-center gap-4 px-6 text-center">
                <p role="alert" className="max-w-md text-sm leading-6 text-[var(--color-danger)]">{modelDiscovery.error}</p>
                <Button variant="outline" leadingIcon={<RefreshCw size={14} aria-hidden />} onClick={() => void discoverModels()}>
                  {t('admin:channels.modelAdd.retry')}
                </Button>
              </div>
            ) : modelDiscovery.fetched && modelDiscovery.models.length > 0 ? (
              <div className="grid gap-3">
                <div className="relative">
                  <Search
                    size={15}
                    aria-hidden
                    className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]"
                  />
                  <Input
                    aria-label={t('admin:channels.modelAdd.search')}
                    value={modelSearch}
                    onChange={(event) => setModelSearch(event.target.value)}
                    placeholder={t('admin:channels.modelAdd.search')}
                    className="pl-9"
                  />
                </div>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="text-xs text-[var(--color-fg-muted)]">
                    {t('admin:channels.modelAdd.discoverSummary', {
                      available: modelDiscovery.models.length,
                      selected: modelDiscovery.selected.size,
                      skipped: modelDiscovery.skippedUnsupported,
                    })}
                  </p>
                  <div className="flex items-center gap-1">
                    <Button variant="ghost" size="xs" onClick={selectAllDiscoveredModels}>
                      {t('admin:channels.modelAdd.selectAll')}
                    </Button>
                    <Button variant="ghost" size="xs" disabled={modelDiscovery.selected.size === 0} onClick={clearSelectedModels}>
                      {t('common:actions.clear')}
                    </Button>
                  </div>
                </div>
                <div className="h-80 overflow-y-auto rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)] p-1">
                  {filteredDiscoveredModels.length > 0 ? filteredDiscoveredModels.map((model) => {
                    const key = model.request_id.toLowerCase()
                    const alreadyAdded = pendingModelKeys.has(key)
                    const checked = alreadyAdded || modelDiscovery.selected.has(key)
                    return (
                      <label
                        key={model.request_id}
                        className="flex min-h-11 items-start gap-3 rounded-[6px] px-2.5 py-2 hover:bg-[var(--color-bg-muted)]"
                      >
                        <Checkbox
                          className="mt-0.5"
                          checked={checked}
                          disabled={alreadyAdded}
                          onChange={() => toggleDiscoveredModel(model)}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="flex min-w-0 items-center gap-2">
                            <span className="truncate text-sm font-medium text-[var(--color-fg)]">{model.label}</span>
                            <Badge size="xs">{model.kind}</Badge>
                            {alreadyAdded ? <Badge size="xs" variant="success">{t('admin:channels.modelAdd.added')}</Badge> : null}
                          </span>
                          <span className="mt-0.5 block truncate font-mono text-[11px] text-[var(--color-fg-subtle)]">
                            {model.request_id}
                          </span>
                        </span>
                      </label>
                    )
                  }) : (
                    <div className="flex h-full items-center justify-center px-4 text-center text-xs text-[var(--color-fg-muted)]">
                      {t('admin:channels.modelAdd.noSearchResults')}
                    </div>
                  )}
                </div>
              </div>
            ) : (
              <div className="flex h-56 items-center justify-center px-6 text-center text-sm text-[var(--color-fg-muted)]">
                {t('admin:channels.modelAdd.noModelsFound')}
              </div>
            )}
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setUpstreamModelsOpen(false)}>{t('common:actions.cancel')}</Button>
            <Button disabled={selectedDiscoveredModels.length === 0} onClick={confirmUpstreamModels}>
              {t('admin:channels.modelAdd.confirmSelected', { count: selectedDiscoveredModels.length })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:channels.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:channels.removeBody', { name: confirmDelete.name }) : ''}
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
    </div>
  )
}
