/**
 * AdminChannels — list, create and edit upstream channels.
 * Channels carry endpoint credentials and supported request IDs.
 * The api_key column is never re-displayed; admins can leave
 * the field blank when editing to keep the existing secret.
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, RefreshCw, Search, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiChannel, ApiChannelHealth, ApiChannelModel, ApiChannelModelCandidate, ApiChannelModelHealth } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Field } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
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
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminListFilter, AdminListToolbar } from '@/components/admin/admin-list-toolbar'
import { AdminAdvancedDisclosure } from '@/components/admin/admin-advanced-disclosure'
import { matchesAdminSearch, mergeVisibleAdminOrder } from '@/lib/admin-list-filter'
import { parseChannelHeaders } from '@/lib/channel-headers'
import { ModelProtocolSelect } from '@/components/admin/model-protocol-select'
import type { ApiModelProtocol } from '@/api/types'

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

type PendingChannelModel = ApiChannelModelCandidate & { source?: 'upstream' | 'manual' }

function inferManualModelKind(requestID: string): ApiChannelModelCandidate['kind'] {
  const id = requestID.toLowerCase()
  if (id.startsWith('jev-')) return 'decision'
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
  const [channelHealthByID, setChannelHealthByID] = useState<Record<string, ApiChannelModelHealth[]>>({})
  const [search, setSearch] = useState('')
  const [discoveryProtocol, setDiscoveryProtocol] = useState<ApiModelProtocol>('openai.chat')
  const [statusFilter, setStatusFilter] = useState('all')
  const filteredRows = useMemo(() => rows.filter((row) =>
    matchesAdminSearch(search, [row.name, row.id, row.base_url])
    && (statusFilter === 'all' || row.enabled === (statusFilter === 'enabled')),
  ), [rows, search, statusFilter])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<ChannelEditor>({
    open: false,
    draft: { enabled: true },
  })
  const [confirmDelete, setConfirmDelete] = useState<ApiChannel | null>(null)
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const [showBaseUrlError, setShowBaseUrlError] = useState(false)
  const [headersText, setHeadersText] = useState('{}')
  const [showHeadersError, setShowHeadersError] = useState(false)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [advancedTab, setAdvancedTab] = useState('headers')
  const parsedHeaders = useMemo(() => parseChannelHeaders(headersText), [headersText])
  const [modelInput, setModelInput] = useState('')
  const [pendingModels, setPendingModels] = useState<PendingChannelModel[]>([])
  const [channelModels, setChannelModels] = useState<ApiChannelModel[]>([])
  const [channelModelsLoaded, setChannelModelsLoaded] = useState(false)
  const [channelHealth, setChannelHealth] = useState<ApiChannelHealth | null>(null)
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
  const editorModels = editor.row ? channelModels : pendingModels

  async function load() {
    setLoading(true)
    try {
      const [nextRows, modelHealth] = await Promise.all([
        adminApi.channels(),
        adminApi.channelsModelHealth().catch(() => ({})),
      ])
      setRows(nextRows)
      setChannelHealthByID(modelHealth)
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
    setAdvancedOpen(false)
    setAdvancedTab('headers')
    setShowBaseUrlError(false)
    setHeadersText('{}')
    setShowHeadersError(false)
    resetModelDiscovery()
    setModelInput('')
    setPendingModels([])
    setChannelModels([])
    setChannelModelsLoaded(true)
    setChannelHealth(null)
    setEditor({
      open: true,
      draft: { enabled: true, name: '', base_url: '' },
    })
  }

  function openEdit(row: ApiChannel) {
    setAdvancedOpen(false)
    setAdvancedTab('headers')
    setShowBaseUrlError(false)
    setHeadersText(JSON.stringify(row.headers ?? {}, null, 2))
    setShowHeadersError(false)
    resetModelDiscovery()
    setModelInput('')
    setPendingModels([])
    setChannelModels([])
    setChannelModelsLoaded(false)
    setChannelHealth(null)
    setEditor({ open: true, row, draft: { ...row, api_key: '' } })
    void adminApi.channelHealth(row.id).then(setChannelHealth).catch(() => setChannelHealth(null))
    void adminApi.channelModels(row.id).then((models) => { setChannelModels(models); setChannelModelsLoaded(true) }).catch(() => { setChannelModels([]); setChannelModelsLoaded(false) })
  }

  function addExistingModel() {
    const requestID = modelInput.trim()
    if (!requestID) return
    setChannelModels((current) => current.some((model) => model.request_id.toLowerCase() === requestID.toLowerCase())
      ? current
      : [...current, { id: `new-${Date.now()}`, channel_id: editor.row?.id ?? '', request_id: requestID, label: requestID, description: '', kind: inferManualModelKind(requestID), enabled: true, source: 'manual', updated_at: 0 }])
    setModelInput('')
  }

  function removeExistingModel(requestID: string) {
    setChannelModels((current) => current.filter((model) => model.request_id !== requestID))
  }

  function addPendingModels(models: ApiChannelModelCandidate[], source: PendingChannelModel['source'] = 'manual') {
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
          source,
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
    }], 'manual')
    setModelInput('')
  }

  function removePendingModel(requestID: string) {
    setPendingModels((current) => current.filter((model) => model.request_id !== requestID))
  }

  async function discoverModels() {
    if (parsedHeaders.error) {
      setAdvancedOpen(true)
      setAdvancedTab('headers')
      setShowHeadersError(true)
      return
    }
    const d = editor.draft
    const normalizedBaseUrl = normalizeOpenAIBaseUrl(d.base_url ?? '')
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
      const result = await adminApi.discoverChannelModels({ channel_id: editor.row?.id, base_url: normalizedBaseUrl, api_key: d.api_key, headers: parsedHeaders.headers, protocol: discoveryProtocol })
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
    const normalizedBaseUrl = normalizeOpenAIBaseUrl(editor.draft.base_url ?? '')
    if (normalizedBaseUrl === null) {
      setShowBaseUrlError(true)
      return
    }
    setModelSearch('')
    setUpstreamModelsOpen(true)
  }

  function confirmUpstreamModels() {
    if (editor.row) {
      setChannelModels((current) => {
        const seen = new Set(current.map((model) => model.request_id.toLowerCase()))
        return [...current, ...selectedDiscoveredModels.filter((model) => !seen.has(model.request_id.toLowerCase())).map((model) => ({
          id: `new-${model.request_id}`,
          channel_id: editor.row?.id ?? '',
          request_id: model.request_id,
          label: model.label,
          description: model.description,
          kind: model.kind,
          enabled: true,
          source: 'upstream',
          updated_at: 0,
        }))]
      })
    } else {
      addPendingModels(selectedDiscoveredModels, 'upstream')
    }
    setUpstreamModelsOpen(false)
  }

  async function submit() {
    if (savingRef.current) return
    if (parsedHeaders.error) {
      setAdvancedOpen(true)
      setAdvancedTab('headers')
      setShowHeadersError(true)
      return
    }
    const d = editor.draft
    if (!d.name) {
      toast.error(t('admin:channels.errors.nameRequired'))
      return
    }
    const modelsToCreate = editor.row ? [] : pendingModels
    const normalizedBaseUrl = normalizeOpenAIBaseUrl(d.base_url ?? '')
    if (normalizedBaseUrl === null) {
      setShowBaseUrlError(true)
      return
    }
    const { type: legacyType, api_format: legacyFormat, ...credentials } = d
    void legacyType
    void legacyFormat
    const payload = { ...credentials, name: d.name.trim(), base_url: normalizedBaseUrl, headers: parsedHeaders.headers }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updateChannel(editor.row.id, payload)
        if (channelModelsLoaded) await adminApi.replaceChannelModels(editor.row.id, channelModels)
        toast.success(t('admin:channels.updated'))
      } else {
        const created = await adminApi.createChannel(payload)
        let modelBatchCreated = 0
        const modelBatchSkipped = 0
        let modelBatchFailed = false
        if (modelsToCreate.length > 0) {
          try {
            // A channel's model list is a capability registry. Do not create
            // logical model rows here; those are created separately after an
            // administrator enters the request_id on the Models page.
            const result = await adminApi.replaceChannelModels(created.id, modelsToCreate.map((model) => ({
              id: '',
              channel_id: created.id,
              request_id: model.request_id,
              label: model.label,
              description: model.description,
              kind: model.kind,
              enabled: true,
              source: model.source ?? 'manual',
              updated_at: 0,
            })))
            modelBatchCreated = result.length
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
    void adminApi.reorderChannels(mergeVisibleAdminOrder(rows, next).map((r) => r.id)).catch((e) => {
      setRows((current) => mergeVisibleAdminOrder(current, prev))
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  return (
    <div>
      <AdminPageHeader
        title={t('admin:channels.title')}
        description={t('admin:channels.lead')}
      />
      <AdminListToolbar
        search={search}
        onSearchChange={setSearch}
        placeholder={t('admin:listToolbar.search.channels')}
        activeFilterCount={Number(statusFilter !== 'all')}
        onResetFilters={() => { setStatusFilter('all') }}
        filters={<>
          <AdminListFilter label={t('admin:common.status')} value={statusFilter} onValueChange={setStatusFilter} options={['all', 'enabled', 'disabled'].map((value) => ({ value, label: t(`admin:listToolbar.${value === 'all' ? 'allStatuses' : value}`) }))} />
        </>}
        actions={(
          <Button
            data-admin-tour="channels-create"
            size="sm"
            leadingIcon={<Plus size={15} aria-hidden />}
            onClick={openNew}
          >
            {t('admin:channels.new')}
          </Button>
        )}
      />

      <section className="mt-4">
      {loading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="rounded-[12px] bg-[var(--color-surface)] px-6 py-10 text-center">
            <p className="text-[var(--color-fg-muted)] text-sm">{t('admin:channels.empty')}</p>
            <div className="mt-4">
              <Button onClick={openNew}>{t('admin:common.createFirst', { kind: t('admin:channels.title').toLowerCase() })}</Button>
            </div>
          </div>
        ) : (
          <AdminSortableList
            items={filteredRows}
            onItemsChange={(next) => setRows((current) => mergeVisibleAdminOrder(current, next))}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            tableLabel={t('admin:channels.title')}
            columns={[
              { id: 'name', header: t('admin:channels.fields.name'), width: 200, render: (r) => <span className="block truncate font-medium" title={r.name}>{r.name}</span> },
              { id: 'endpoint', header: t('admin:channels.fields.baseUrl'), width: 260, render: (r) => <span className="block truncate font-mono text-[12px] text-[var(--color-fg-muted)]" title={r.base_url}>{r.base_url || t('admin:channels.labels.defaultEndpoint')}</span> },
              { id: 'key', header: t('admin:channels.fields.apiKey'), width: 100, render: (r) => <span className="text-[12px] text-[var(--color-fg-muted)]">{r.has_api_key ? t('admin:channels.labels.keySet') : t('admin:channels.labels.noKey')}</span> },
              { id: 'status', header: t('admin:common.status'), width: 190, render: (r) => <div className="flex flex-wrap gap-1"><Badge size="xs" variant={r.enabled ? 'success' : 'neutral'}>{t(r.enabled ? 'admin:channels.fields.enabled' : 'admin:channels.labels.disabled')}</Badge>{(r.auto_disabled_until ?? 0) > Math.floor(Date.now() / 1000) ? <Badge size="xs" variant="warning">{t('admin:channels.labels.autoDisabled', { defaultValue: '自动禁用' })}</Badge> : null}{(channelHealthByID[r.id]?.some((item) => item.disabled_until > Math.floor(Date.now() / 1000))) ? <Badge size="xs" variant="warning">{t('admin:channels.labels.partialUnavailable', { defaultValue: '部分模型不可用' })}</Badge> : null}</div> },
              { id: 'actions', header: t('admin:common.actions'), width: 100, align: 'right', render: (r) => (
                <div className="flex items-center justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    title={t('admin:common.edit')}
                    aria-label={`${t('admin:common.edit')}: ${r.name}`}
                    leadingIcon={<Pencil size={13} aria-hidden />}
                    onClick={() => openEdit(r)}
                  >
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    title={t('admin:common.remove')}
                    aria-label={`${t('admin:common.remove')}: ${r.name}`}
                    leadingIcon={<Trash2 size={13} aria-hidden />}
                    onClick={() => setConfirmDelete(r)}
                  >
                  </Button>
                </div>
              ) },
            ]}
          />
        )}
      </section>

      <Dialog open={editor.open} onOpenChange={(o) => !savingRef.current && setEditor({ ...editor, open: o })}>
        <DialogContent presentation="drawer" size="lg" className="w-[min(100vw,42rem)]">
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
                <Field
                  label={t('admin:channels.fields.baseUrl')}
                  htmlFor="ch-url"
                  hint={t('admin:channels.fields.openAIBaseUrlHint')}
                  error={showBaseUrlError
                    && normalizeOpenAIBaseUrl(editor.draft.base_url ?? '') === null
                    ? t('admin:channels.errors.openAIBaseUrlInvalid')
                    : undefined}
                >
                  <Input
                    id="ch-url"
                    disabled={saving}
                    value={editor.draft.base_url ?? ''}
                    onChange={(e) => updateDraft({ base_url: e.target.value }, true)}
                    onBlur={() => setShowBaseUrlError(true)}
                    invalid={showBaseUrlError
                      && normalizeOpenAIBaseUrl(editor.draft.base_url ?? '') === null}
                    placeholder="https://api.example.com/v1"
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
                <div className="rounded-[8px] bg-[var(--color-bg-muted)] p-1">
                  <label className="flex min-h-11 items-center justify-between gap-4 rounded-[8px] px-2.5 py-2">
                    <span className="text-sm text-[var(--color-fg)]">{t('admin:channels.fields.enabled')}</span>
                    <Switch
                      disabled={saving}
                      checked={editor.draft.enabled ?? true}
                      onCheckedChange={(v) => updateDraft({ enabled: v })}
                    />
                  </label>
                </div>
                <AdminAdvancedDisclosure
                  title={t('admin:channels.advanced')}
                  open={advancedOpen}
                  onOpenChange={setAdvancedOpen}
                >
                  <Tabs value={advancedTab} onValueChange={setAdvancedTab} className="min-w-0">
                    <div className="max-w-full overflow-x-auto pb-1">
                      <TabsList variant="segmented" aria-label={t('admin:channels.advanced')} className="w-max max-w-none rounded-[8px]">
                        {['headers', 'reliability'].map((tab) => (
                          <TabsTrigger key={tab} value={tab} variant="segmented" className="shrink-0 whitespace-nowrap">
                            {t(`admin:channels.advancedTabs.${tab}`)}
                          </TabsTrigger>
                        ))}
                      </TabsList>
                    </div>
                    <TabsContent value="headers" forceMount className="mt-4 min-w-0 data-[state=inactive]:hidden">
                      <Field
                        label={t('admin:channels.headers.label')}
                        htmlFor="ch-headers"
                        hint={t('admin:channels.headers.hint')}
                        error={showHeadersError && parsedHeaders.error ? t(`admin:channels.headers.errors.${parsedHeaders.error}`) : undefined}
                      >
                        <Textarea
                          id="ch-headers"
                          rows={4}
                          spellCheck={false}
                          disabled={saving}
                          value={headersText}
                          onChange={(event) => { setHeadersText(event.target.value); resetModelDiscovery() }}
                          onBlur={() => setShowHeadersError(true)}
                          invalid={showHeadersError && !!parsedHeaders.error}
                          placeholder={'{\n  "A": "a"\n}'}
                          className="rounded-[8px] font-mono text-[13px] leading-5"
                        />
                      </Field>
                    </TabsContent>
                    <TabsContent value="reliability" forceMount className="mt-4 min-w-0 data-[state=inactive]:hidden">
                      <div className="quiet-field-group rounded-[8px] bg-[var(--color-bg-muted)] p-3">
                        <div className="mb-2 flex items-center justify-between gap-3">
                          <div>
                            <p className="text-sm font-medium text-[var(--color-fg)]">{t('admin:channels.autoDisable.title', { defaultValue: '渠道自动禁用' })}</p>
                            <p className="mt-1 text-xs text-[var(--color-fg-muted)]">{t('admin:channels.autoDisable.hint', { defaultValue: '该渠道下任意模型连续失败或首字超时后暂时停止调度。首字超时使用模型策略中的阈值。' })}</p>
                          </div>
                        </div>
                        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
                          <Field label={t('admin:channels.autoDisable.errors', { defaultValue: '连续错误次数' })} htmlFor="ch-disable-errors"><Input id="ch-disable-errors" type="number" min="0" step="1" disabled={saving} value={String(editor.draft.auto_disable_errors ?? 0)} onChange={(event) => updateDraft({ auto_disable_errors: Math.max(0, Number(event.target.value) || 0) })} /></Field>
                          <Field label={t('admin:channels.autoDisable.timeouts', { defaultValue: '连续超时次数' })} htmlFor="ch-disable-timeouts"><Input id="ch-disable-timeouts" type="number" min="0" step="1" disabled={saving} value={String(editor.draft.auto_disable_timeouts ?? 0)} onChange={(event) => updateDraft({ auto_disable_timeouts: Math.max(0, Number(event.target.value) || 0) })} /></Field>
                          <Field label={t('admin:channels.autoDisable.minutes', { defaultValue: '禁用时长（分钟）' })} htmlFor="ch-disable-minutes"><Input id="ch-disable-minutes" type="number" min="0" step="1" disabled={saving} value={String(editor.draft.auto_disable_minutes ?? 0)} onChange={(event) => updateDraft({ auto_disable_minutes: Math.max(0, Number(event.target.value) || 0) })} /></Field>
                        </div>
                      </div>
                    </TabsContent>
                  </Tabs>
                </AdminAdvancedDisclosure>
              </div>

              {editor.row && ((editor.draft.auto_disabled_until ?? 0) > Math.floor(Date.now() / 1000)
                || channelHealth?.models.some((item) => item.disabled_until > Math.floor(Date.now() / 1000))) ? (
                <div className="flex flex-wrap items-center justify-between gap-3 rounded-[8px] bg-[var(--color-bg-muted)] px-3 py-2.5">
                  <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-[var(--color-fg-muted)]">
                    {(editor.draft.auto_disabled_until ?? 0) > Math.floor(Date.now() / 1000) ? (
                      <Badge size="xs" variant="warning">{t('admin:channels.labels.autoDisabled')}</Badge>
                    ) : null}
                    {channelHealth?.models.some((item) => item.disabled_until > Math.floor(Date.now() / 1000)) ? (
                      <span>
                        <span className="font-medium text-[var(--color-fg)]">{t('admin:channels.autoDisable.partial', { defaultValue: '部分模型暂时不可用：' })}</span>{' '}
                        {channelHealth.models.filter((item) => item.disabled_until > Math.floor(Date.now() / 1000)).map((item) => `${item.model_label || item.request_id} (${t(`admin:models.channels.${item.role}`)})`).join('、')}
                      </span>
                    ) : null}
                  </div>
                  {(editor.draft.auto_disabled_until ?? 0) > Math.floor(Date.now() / 1000) ? (
                    <Button type="button" size="sm" variant="secondary" disabled={saving} onClick={async () => {
                      try {
                        const recovered = await adminApi.recoverChannel(editor.row!.id)
                        setEditor((current) => ({ ...current, draft: { ...current.draft, ...recovered }, row: recovered }))
                        setRows((current) => current.map((row) => row.id === recovered.id ? recovered : row))
                        setChannelHealth((current) => current ? { ...current, channel: recovered } : current)
                        toast.success(t('admin:channels.autoDisable.recovered', { defaultValue: '渠道已恢复' }))
                      } catch (error) { toast.error(error instanceof ApiError ? error.message : t('admin:common.failed')) }
                    }}>{t('admin:channels.autoDisable.recover', { defaultValue: '恢复渠道' })}</Button>
                  ) : null}
                </div>
              ) : null}

              <section className="mt-2 grid gap-3" aria-labelledby="ch-models-heading">
                <div>
                  <h3 id="ch-models-heading" className="text-sm font-medium text-[var(--color-fg)]">
                    {t('admin:channels.modelAdd.title')}
                  </h3>
                  <p className="mt-1 text-xs leading-5 text-[var(--color-fg-muted)]">
                    {t(editor.row ? 'admin:channels.modelAdd.editHint' : 'admin:channels.modelAdd.hint')}
                  </p>
                </div>
                <div className="grid gap-3">
                  <form
                    className="flex w-full flex-col gap-2 sm:flex-row"
                    onSubmit={(event) => {
                      event.preventDefault()
                      if (editor.row) addExistingModel()
                      else addManualModel()
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
                      type="button"
                      variant="outline"
                      disabled={saving}
                      leadingIcon={<RefreshCw size={14} aria-hidden />}
                      onClick={openUpstreamModels}
                      className="sm:shrink-0"
                    >
                      {t('admin:channels.modelAdd.fromUpstream')}
                    </Button>
                  </form>

                  <div className="min-h-28 max-h-56 overflow-y-auto rounded-[8px] bg-[var(--color-surface-sunken)] p-1">
                    {editorModels.length > 0 ? editorModels.map((model) => (
                      <div key={model.request_id} className="flex min-h-11 items-center gap-3 rounded-[6px] px-2.5 py-2">
                        <span className="min-w-0 flex-1">
                          <span className="flex min-w-0 items-center gap-2">
                            <span className="truncate text-sm font-medium text-[var(--color-fg)]">{model.label || model.request_id}</span>
                            <Badge size="xs">{t(`admin:models.kinds.${model.kind}`)}</Badge>
                          </span>
                          {model.label && model.label !== model.request_id ? (
                            <span className="mt-0.5 block truncate font-mono text-[12px] text-[var(--color-fg-subtle)]">
                              {model.request_id}
                            </span>
                          ) : null}
                        </span>
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          disabled={saving}
                          aria-label={t('admin:channels.modelAdd.removeModel', { name: model.label || model.request_id })}
                          title={t('admin:channels.modelAdd.remove')}
                          onClick={() => editor.row ? removeExistingModel(model.request_id) : removePendingModel(model.request_id)}
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
              </section>
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
        <DialogContent presentation="drawer" size="lg" className="w-[min(100vw,42rem)]">
          <DialogHeader>
            <DialogTitle>{t('admin:channels.modelAdd.upstreamTitle')}</DialogTitle>
            <DialogDescription>{t('admin:channels.modelAdd.upstreamDescription')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="mb-5 grid items-end gap-3 sm:grid-cols-[minmax(0,1fr)_auto]">
              <ModelProtocolSelect id="ch-discovery-protocol" discovery value={discoveryProtocol} disabled={modelDiscovery.loading} onChange={(protocol) => {
                discoveryRequestRef.current++
                setDiscoveryProtocol(protocol)
                setModelDiscovery({ loading: false, fetched: false, error: null, models: [], selected: new Set(), skippedUnsupported: 0 })
              }} />
              <Button size="sm" variant="secondary" leadingIcon={<RefreshCw size={14} aria-hidden />} disabled={modelDiscovery.loading} onClick={() => void discoverModels()}>
                {t('admin:models.pull.fetch')}
              </Button>
            </div>
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
                <div className="h-80 overflow-y-auto rounded-[8px] bg-[var(--color-surface-sunken)] p-1">
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
                          <span className="mt-0.5 block truncate font-mono text-[12px] text-[var(--color-fg-subtle)]">
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
