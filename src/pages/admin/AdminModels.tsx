/**
 * AdminModels — list, quick-create, and entry to per-model settings.
 *
 * The list is shallow on purpose: the New-model dialog asks for only the
 * fields needed to register a row (channel, kind, label, request_id, icon,
 * description). Behaviour, system prompt, param_controls and pricing live on
 * the per-model settings page (/admin/models/:id) — reachable via the gear
 * icon on each row. This avoids a 15-field overflow modal on small screens
 * and matches the editorial-feel "one job per surface" rule.
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, RefreshCw, Search, Settings as SettingsIcon, Trash2, Tags as TagsIcon } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import { embeddingGuardErrorText } from '@/lib/admin-embedding-errors'
import type { ApiChannel, ApiChannelModelCandidate, ApiModel } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { Switch } from '@/components/ui/switch'
import { Tooltip } from '@/components/ui/tooltip'
import { IconUploader } from '@/components/admin/icon-uploader'
import { AdminSortableList } from '@/components/admin/AdminSortableList'
import { ModelIcon } from '@/components/chat/model-icon'
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
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminListFilter, AdminListToolbar } from '@/components/admin/admin-list-toolbar'
import { matchesAdminSearch, mergeVisibleAdminOrder } from '@/lib/admin-list-filter'

const KINDS = ['chat', 'image', 'embedding', 'decision'] as const

type CreateDraft = {
  channel_id: string
  kind: ApiModel['kind']
  label: string
  request_id: string
  icon: string
  description: string
}

type PullModelsState = {
  open: boolean
  channelId: string
  loading: boolean
  fetched: boolean
  error: boolean
  candidates: ApiChannelModelCandidate[]
  selected: Set<string>
  skippedUnsupported: number
  search: string
}

const emptyCreate: CreateDraft = {
  channel_id: '',
  kind: 'chat',
  label: '',
  request_id: '',
  icon: '',
  description: '',
}

const emptyPullModels: PullModelsState = {
  open: false,
  channelId: '',
  loading: false,
  fetched: false,
  error: false,
  candidates: [],
  selected: new Set(),
  skippedUnsupported: 0,
  search: '',
}

export default function AdminModels() {
  const { t } = useTranslation(['admin', 'common'])
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [channels, setChannels] = useState<ApiChannel[]>([])
  const [creatorChannels, setCreatorChannels] = useState<ApiChannel[]>([])
  const [models, setModels] = useState<ApiModel[]>([])
  const [search, setSearch] = useState('')
  const [channelFilter, setChannelFilter] = useState('all')
  const [kindFilter, setKindFilter] = useState('all')
  const [statusFilter, setStatusFilter] = useState('all')
  const filteredModels = useMemo(() => {
    const channelById = new Map(channels.map((channel) => [channel.id, channel]))
    return models.filter((model) => {
      const channel = channelById.get(model.channel_id)
      const bindingNames = (model.channel_bindings ?? []).map((binding) => binding.channel_name).filter(Boolean).join(' ')
      const boundChannelIDs = (model.channel_bindings ?? []).filter((binding) => binding.role === 'regular').map((binding) => binding.channel_id)
      return matchesAdminSearch(search, [model.label, model.request_id, model.id, model.description, channel?.name, channel?.type, bindingNames])
        && (channelFilter === 'all' || model.channel_id === channelFilter || boundChannelIDs.includes(channelFilter))
        && (kindFilter === 'all' || model.kind === kindFilter)
        && (statusFilter === 'all' || model.enabled === (statusFilter === 'enabled'))
    })
  }, [models, channels, search, channelFilter, kindFilter, statusFilter])
  const [loading, setLoading] = useState(true)
  const [creator, setCreator] = useState<{ open: boolean; draft: CreateDraft }>({
    open: false,
    draft: emptyCreate,
  })
  const [submitting, setSubmitting] = useState(false)
  const submittingRef = useRef(false)
  const [pullModels, setPullModels] = useState<PullModelsState>(emptyPullModels)
  const [addingPulledModels, setAddingPulledModels] = useState(false)
  const addingPulledModelsRef = useRef(false)
  const pullRequestRef = useRef(0)
  const [confirmDelete, setConfirmDelete] = useState<ApiModel | null>(null)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const [togglingModelIds, setTogglingModelIds] = useState<Set<string>>(() => new Set())
  const togglingModelIdsRef = useRef(new Set<string>())

  const requestedKind = searchParams.get('kind')
  const createKind: ApiModel['kind'] = (KINDS as readonly string[]).includes(requestedKind ?? '')
    ? requestedKind as ApiModel['kind']
    : 'chat'

  async function load() {
    setLoading(true)
    try {
      const [c, m] = await Promise.all([adminApi.channels(), adminApi.models()])
      setChannels(c)
      setModels(m)
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

  useEffect(() => {
    const requestID = creator.draft.request_id.trim()
    if (!creator.open || !requestID) {
      setCreatorChannels([])
      return
    }
    let cancelled = false
    void adminApi.channelCapabilities(requestID).then((rows) => { if (!cancelled) setCreatorChannels(rows) }).catch(() => { if (!cancelled) setCreatorChannels([]) })
    return () => { cancelled = true }
  }, [creator.open, creator.draft.request_id])

  useEffect(() => {
    if (!creator.open || !creator.draft.request_id.trim() || creatorChannels.length === 0) return
    if (creatorChannels.some((channel) => channel.id === creator.draft.channel_id)) return
    const next = creatorChannels[0]
    setCreator((current) => ({
      ...current,
      draft: {
        ...current.draft,
        channel_id: next.id,
        kind: next.type === 'typesafe' ? 'decision' : current.draft.kind === 'decision' ? 'chat' : current.draft.kind,
      },
    }))
  }, [creator.open, creator.draft.request_id, creator.draft.channel_id, creatorChannels])

  function openNew() {
    setCreator({
      open: true,
      draft: { ...emptyCreate, kind: channels[0]?.type === 'typesafe' ? 'decision' : createKind === 'decision' ? 'chat' : createKind, channel_id: channels[0]?.id ?? '' },
    })
  }

  function openPullModels() {
    setPullModels({
      ...emptyPullModels,
      open: true,
      channelId: channels[0]?.id ?? '',
      selected: new Set(),
    })
  }

  function selectPullChannel(channelId: string) {
    pullRequestRef.current++
    setPullModels({
      ...emptyPullModels,
      open: true,
      channelId,
      selected: new Set(),
    })
  }

  async function discoverSavedModels() {
    if (!pullModels.channelId || pullModels.loading) return
    const requestID = ++pullRequestRef.current
    setPullModels((current) => ({
      ...current,
      loading: true,
      fetched: false,
      error: false,
      candidates: [],
      selected: new Set(),
    }))
    try {
      const result = await adminApi.discoverSavedChannelModels(pullModels.channelId)
      if (requestID !== pullRequestRef.current) return
      setPullModels((current) => ({
        ...current,
        loading: false,
        fetched: true,
        candidates: result.models,
        skippedUnsupported: result.skipped_unsupported,
      }))
    } catch {
      if (requestID !== pullRequestRef.current) return
      setPullModels((current) => ({
        ...current,
        loading: false,
        fetched: false,
        error: true,
      }))
    }
  }

  function togglePulledModel(requestID: string) {
    const key = requestID.trim().toLowerCase()
    setPullModels((current) => {
      const selected = new Set(current.selected)
      if (selected.has(key)) selected.delete(key)
      else selected.add(key)
      return { ...current, selected }
    })
  }

  async function addPulledModels(candidates: ApiChannelModelCandidate[]) {
    if (addingPulledModelsRef.current || !pullModels.channelId || candidates.length === 0) return
    addingPulledModelsRef.current = true
    setAddingPulledModels(true)
    try {
      const result = await adminApi.createChannelModelsBatch(pullModels.channelId, candidates)
      await load()
      setPullModels((current) => ({ ...current, selected: new Set() }))
      const skipped = result.skipped_existing + result.skipped_duplicate
      if (result.created > 0) {
        toast.success(
          skipped > 0
            ? t('admin:models.pull.partial', { created: result.created, skipped })
            : t('admin:models.pull.success', { count: result.created }),
        )
      } else {
        toast.warning(t('admin:models.pull.noneAdded'))
      }
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      addingPulledModelsRef.current = false
      setAddingPulledModels(false)
    }
  }

  async function submitCreate() {
    if (submittingRef.current) return
    const d = creator.draft
    if (!d.channel_id || !d.label.trim() || !d.request_id.trim()) {
      toast.error(t('admin:models.errors.missingFields'))
      return
    }
    if (creatorChannels.length === 0 || !creatorChannels.some((channel) => channel.id === d.channel_id)) {
      toast.error(t('admin:models.channels.noCapability', { defaultValue: 'Configure this request_id in the channel model list before creating the model.' }))
      return
    }
    submittingRef.current = true
    setSubmitting(true)
    try {
      // Sensible defaults so the row is immediately usable; user fine-tunes on
      // the settings page. param_controls stays empty list — the editor
      // accepts JSON text and parses on save.
      const created = await adminApi.createModel({
        channel_id: d.channel_id,
        kind: d.kind,
        label: d.label.trim(),
        request_id: d.request_id.trim(),
        icon: d.icon.trim(),
        description: d.description.trim(),
        enabled: true,
        tool_mode: 'native',
        vision: true,
        stream: true,
        research_enabled: true,
        param_controls: [],
        currency: 'USD',
        price_input: d.kind === 'decision' || channels.find((c) => c.id === d.channel_id)?.type === 'typesafe' ? 0.042 : 0,
      })
      toast.success(t('admin:models.created'))
      setCreator({ open: false, draft: emptyCreate })
      await load()
      // Take the user straight to the full settings page so the next action
      // (pricing, system prompt, tool mode) is one click away.
      navigate(`/admin/models/${encodeURIComponent(created.id)}`)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(t('admin:common.nameExists', { defaultValue: 'A record with this name already exists.' }))
      } else {
        toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
      }
    } finally {
      submittingRef.current = false
      setSubmitting(false)
    }
  }

  async function remove(row: ApiModel) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeModel(row.id)
      toast.success(t('admin:models.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(embeddingGuardErrorText(t, e) || (e instanceof ApiError ? e.message : t('admin:common.failed')))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  // Reordering is optimistic: the list updates instantly (no refetch / loading
  // flash) and the new order is persisted in one PATCH. On failure we revert.
  function persistOrder(next: ApiModel[], prev: ApiModel[]) {
    void adminApi.reorderModels(mergeVisibleAdminOrder(models, next).map((m) => m.id)).catch((e) => {
      setModels((current) => mergeVisibleAdminOrder(current, prev))
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  // Quick show/hide: flip `enabled` inline (optimistic + revert on error).
  async function toggleEnabled(m: ApiModel) {
    if (togglingModelIdsRef.current.has(m.id)) return
    togglingModelIdsRef.current.add(m.id)
    setTogglingModelIds(new Set(togglingModelIdsRef.current))
    const next = !m.enabled
    setModels((list) => list.map((x) => (x.id === m.id ? { ...x, enabled: next } : x)))
    try {
      await adminApi.updateModel(m.id, { enabled: next })
    } catch (e) {
      setModels((list) => list.map((x) => (x.id === m.id ? { ...x, enabled: m.enabled } : x)))
      toast.error(embeddingGuardErrorText(t, e) || (e instanceof ApiError ? e.message : t('admin:common.failed')))
    } finally {
      togglingModelIdsRef.current.delete(m.id)
      setTogglingModelIds(new Set(togglingModelIdsRef.current))
    }
  }

  const pulledExistingKeys = new Set(
    models
      .filter((model) => model.channel_id === pullModels.channelId)
      .map((model) => model.request_id.trim().toLowerCase()),
  )
  const pulledAvailable = pullModels.candidates.filter(
    (candidate) => !pulledExistingKeys.has(candidate.request_id.trim().toLowerCase()),
  )
  const pulledExistingCount = pullModels.candidates.length - pulledAvailable.length
  const pulledQuery = pullModels.search.trim().toLowerCase()
  const pulledFiltered = pullModels.candidates.filter((candidate) =>
    !pulledQuery
    || candidate.request_id.toLowerCase().includes(pulledQuery)
    || candidate.label.toLowerCase().includes(pulledQuery),
  )
  const pulledSelectedCandidates = pulledAvailable.filter((candidate) =>
    pullModels.selected.has(candidate.request_id.trim().toLowerCase()),
  )

  return (
    <div>
      <AdminPageHeader
        title={t('admin:models.title')}
        description={t('admin:models.lead')}
      />
      <AdminListToolbar
        search={search}
        onSearchChange={setSearch}
        placeholder={t('admin:listToolbar.search.models')}
        activeFilterCount={Number(channelFilter !== 'all') + Number(kindFilter !== 'all') + Number(statusFilter !== 'all')}
        onResetFilters={() => { setChannelFilter('all'); setKindFilter('all'); setStatusFilter('all') }}
        filters={<>
          <AdminListFilter label={t('admin:models.fields.channel')} value={channelFilter} onValueChange={setChannelFilter} options={[{ value: 'all', label: t('admin:listToolbar.allChannels') }, ...channels.map((channel) => ({ value: channel.id, label: channel.name }))]} />
          <AdminListFilter label={t('admin:models.fields.kind')} value={kindFilter} onValueChange={setKindFilter} options={[{ value: 'all', label: t('admin:listToolbar.allTypes') }, ...KINDS.map((kind) => ({ value: kind, label: kind }))]} />
          <AdminListFilter label={t('admin:common.status')} value={statusFilter} onValueChange={setStatusFilter} options={['all', 'enabled', 'disabled'].map((value) => ({ value, label: t(`admin:listToolbar.${value === 'all' ? 'allStatuses' : value}`) }))} />
        </>}
        actions={(
          <>
            <Tooltip content={t('admin:modelTags.manage', { defaultValue: 'Manage tags' })}>
              <Button
                size="icon-sm"
                variant="secondary"
                className="size-8 max-sm:size-[var(--tap-min)]"
                aria-label={t('admin:modelTags.manage', { defaultValue: 'Manage tags' })}
                onClick={() => navigate('/admin/model-tags')}
              >
                <TagsIcon size={15} aria-hidden />
              </Button>
            </Tooltip>
            <Tooltip content={t('admin:models.pull.action')}>
              <Button
                size="icon-sm"
                variant="secondary"
                className="size-8 max-sm:size-[var(--tap-min)]"
                aria-label={t('admin:models.pull.action')}
                onClick={openPullModels}
              >
                <RefreshCw size={15} aria-hidden />
              </Button>
            </Tooltip>
            <Button
              data-admin-tour="models-create"
              size="sm"
              leadingIcon={<Plus size={15} aria-hidden />}
              onClick={openNew}
            >
              {t('admin:models.new')}
            </Button>
          </>
        )}
      />

      <section className="mt-4">
        {loading ? (
          <PanelFallback />
        ) : models.length === 0 ? (
          <div className="rounded-[12px] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:models.empty')}
          </div>
        ) : (
          <AdminSortableList
            items={filteredModels}
            onItemsChange={(next) => setModels((current) => mergeVisibleAdminOrder(current, next))}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            tableLabel={t('admin:models.title')}
            columns={[
              { id: 'model', header: t('admin:models.fields.label'), width: 260, render: (m) => (
                <div className="flex min-w-0 items-center gap-2.5">
                  <span className="grid size-8 shrink-0 place-items-center rounded-[8px] bg-[var(--color-bg-muted)]"><ModelIcon icon={m.icon} size={20} /></span>
                  <div className="min-w-0">
                    <span className="block truncate font-medium" title={m.label}>{m.label}</span>
                    <span className="block truncate font-mono text-[12px] text-[var(--color-fg-muted)]" title={m.request_id}>{m.request_id}</span>
                  </div>
                </div>
              ) },
              { id: 'channel', header: t('admin:models.fields.channel'), width: 180, render: (m) => {
                const regular = (m.channel_bindings ?? []).filter((binding) => binding.role === 'regular')
                const primaryName = regular[0]?.channel_name ?? channels.find((c) => c.id === m.channel_id)?.name ?? '—'
                return <div className="flex min-w-0 items-center gap-1.5"><span className="block min-w-0 truncate" title={regular.map((binding) => binding.channel_name).filter(Boolean).join(', ') || primaryName}>{primaryName}</span>{regular.length > 1 ? <Badge size="xs">+{regular.length - 1}</Badge> : null}</div>
              } },
              { id: 'kind', header: t('admin:models.fields.kind'), width: 100, render: (m) => <Badge size="xs">{m.kind}</Badge> },
              { id: 'toolMode', header: t('admin:models.fields.toolMode'), width: 100, render: (m) => <Badge size="xs">{m.tool_mode}</Badge> },
              { id: 'pricing', header: t('admin:common.pricing'), width: 170, render: (m) => (
                <div className="text-[12px] tabular-nums text-[var(--color-fg-muted)]">
                  {m.kind === 'chat' ? <><div>{t('admin:models.fields.priceIn')}: ${m.price_input}</div><div>{t('admin:models.fields.priceOut')}: ${m.price_output}</div></> : null}
                  {m.kind === 'image' ? `$${m.price_per_image}/img` : null}
                  {m.kind === 'embedding' ? `${t('admin:models.fields.dim')}: ${m.dim}` : null}
                  {m.kind === 'decision' ? '—' : null}
                </div>
              ) },
              { id: 'enabled', header: t('admin:common.status'), width: 80, align: 'center', render: (m) => (
                  <Tooltip content={t('admin:models.visibleToggle', { defaultValue: m.enabled ? 'Visible to users' : 'Hidden from users' })}>
                    <span className="inline-flex">
                      <Switch
                        checked={m.enabled}
                        disabled={togglingModelIds.has(m.id)}
                        aria-busy={togglingModelIds.has(m.id) || undefined}
                        onCheckedChange={() => void toggleEnabled(m)}
                        aria-label={t('admin:models.visibleToggle', { defaultValue: 'Show in app' })}
                      />
                    </span>
                  </Tooltip>
              ) },
              { id: 'actions', header: t('admin:common.actions'), width: 100, align: 'right', render: (m) => (
                  <div className="flex items-center justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      title={t('admin:models.settings')}
                      aria-label={`${t('admin:models.settings')}: ${m.label}`}
                      leadingIcon={<SettingsIcon size={13} aria-hidden />}
                      onClick={() => navigate(`/admin/models/${encodeURIComponent(m.id)}`)}
                    >
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      title={t('admin:common.remove')}
                      aria-label={`${t('admin:common.remove')}: ${m.label}`}
                      leadingIcon={<Trash2 size={13} aria-hidden />}
                      onClick={() => setConfirmDelete(m)}
                    >
                    </Button>
                  </div>
              ) },
            ]}
          />
        )}
      </section>

      <Dialog
        open={pullModels.open}
        onOpenChange={(open) => {
          if (addingPulledModelsRef.current) return
          if (!open) pullRequestRef.current++
          setPullModels((current) => ({ ...current, open }))
        }}
      >
        <DialogContent size="lg" closeDisabled={addingPulledModels}>
          <DialogHeader>
            <DialogTitle>{t('admin:models.pull.title')}</DialogTitle>
            <DialogDescription>{t('admin:models.pull.description')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            {channels.length === 0 ? (
              <div className="rounded-[12px] bg-[var(--color-bg-muted)] px-5 py-8 text-center text-sm text-[var(--color-fg-muted)]">
                {t('admin:models.pull.noChannels')}
              </div>
            ) : (
              <div className="grid gap-4">
                <div className="grid items-end gap-3 sm:grid-cols-[minmax(0,1fr)_auto]">
                  <Field label={t('admin:models.pull.channel')} htmlFor="pull-model-channel">
                    <Select value={pullModels.channelId} onValueChange={selectPullChannel} disabled={pullModels.loading || addingPulledModels}>
                      <SelectTrigger id="pull-model-channel">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {channels.map((channel) => (
                          <SelectItem key={channel.id} value={channel.id}>
                            {channel.name} ({channel.type})
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                  <Button
                    variant="secondary"
                    className="min-h-[var(--tap-min)] sm:min-h-0"
                    leadingIcon={<RefreshCw size={15} aria-hidden />}
                    onClick={() => void discoverSavedModels()}
                    loading={pullModels.loading}
                    disabled={!pullModels.channelId || addingPulledModels}
                  >
                    {pullModels.loading ? t('admin:models.pull.fetching') : t('admin:models.pull.fetch')}
                  </Button>
                </div>

                {pullModels.error ? (
                  <div className="flex flex-col items-start gap-3 rounded-[12px] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-4 py-3 text-sm text-[var(--color-fg)] sm:flex-row sm:items-center sm:justify-between">
                    <span>{t('admin:models.pull.failed')}</span>
                    <Button variant="ghost" size="sm" onClick={() => void discoverSavedModels()}>
                      {t('admin:models.pull.retry')}
                    </Button>
                  </div>
                ) : null}

                {pullModels.fetched ? (
                  <>
                      <div className="flex flex-col gap-3 py-3 sm:flex-row sm:items-center sm:justify-between">
                        <div>
                          <p className="text-sm text-[var(--color-fg-muted)]">
                            {t('admin:models.pull.summary', {
                              available: pulledAvailable.length,
                              existing: pulledExistingCount,
                              selected: pulledSelectedCandidates.length,
                            })}
                          </p>
                          {pullModels.skippedUnsupported > 0 ? (
                            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
                              {t('admin:models.pull.unsupportedHidden', { count: pullModels.skippedUnsupported })}
                            </p>
                          ) : null}
                        </div>
                        {pulledAvailable.length > 0 ? (
                          <div className="flex shrink-0 gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              onClick={() => setPullModels((current) => ({
                                ...current,
                                selected: new Set(pulledAvailable.map((candidate) => candidate.request_id.trim().toLowerCase())),
                              }))}
                            >
                              {t('admin:models.pull.selectAll')}
                            </Button>
                            {pullModels.selected.size > 0 ? (
                              <Button variant="ghost" size="sm" onClick={() => setPullModels((current) => ({ ...current, selected: new Set() }))}>
                                {t('admin:models.pull.clearSelection')}
                              </Button>
                            ) : null}
                          </div>
                        ) : null}
                      </div>

                      {pullModels.candidates.length > 0 ? (
                        <div className="relative">
                          <Search className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" size={16} aria-hidden />
                          <Input
                            value={pullModels.search}
                            onChange={(event) => setPullModels((current) => ({ ...current, search: event.target.value }))}
                            className="pl-9"
                            placeholder={t('admin:models.pull.search')}
                            aria-label={t('admin:models.pull.search')}
                          />
                        </div>
                      ) : null}

                      {pullModels.candidates.length === 0 ? (
                        <div className="py-8 text-center text-sm text-[var(--color-fg-muted)]">{t('admin:models.pull.empty')}</div>
                      ) : pulledFiltered.length === 0 ? (
                        <div className="py-8 text-center text-sm text-[var(--color-fg-muted)]">{t('admin:models.pull.noSearchResults')}</div>
                      ) : (
                        <div>
                          {pulledAvailable.length === 0 ? (
                            <p className="mb-3 text-sm text-[var(--color-fg-muted)]">{t('admin:models.pull.allAdded')}</p>
                          ) : null}
                          <div className="max-h-[min(42vh,24rem)] overflow-y-auto rounded-[12px]">
                          {pulledFiltered.map((candidate) => {
                            const key = candidate.request_id.trim().toLowerCase()
                            const existing = pulledExistingKeys.has(key)
                            return (
                              <label
                                key={key}
                                className={`flex min-h-14 items-center gap-3 rounded-[8px] px-3 py-2.5 ${existing ? 'cursor-default bg-[var(--color-bg-muted)]/60' : 'cursor-pointer hover:bg-[var(--color-bg-muted)]'}`}
                              >
                                <Checkbox
                                  checked={existing || pullModels.selected.has(key)}
                                  disabled={existing || addingPulledModels}
                                  onChange={() => togglePulledModel(candidate.request_id)}
                                  aria-label={`${candidate.label}: ${existing ? t('admin:models.pull.alreadyAdded') : t('admin:models.pull.available')}`}
                                />
                                <span className="min-w-0 flex-1">
                                  <span className="flex flex-wrap items-center gap-2">
                                    <span className="truncate text-sm font-medium text-[var(--color-fg)]">{candidate.label}</span>
                                    <Badge size="xs" variant="neutral">{candidate.kind}</Badge>
                                  </span>
                                  <span className="mt-0.5 block truncate font-mono text-xs text-[var(--color-fg-subtle)]">{candidate.request_id}</span>
                                </span>
                                <Badge size="xs" variant={existing ? 'neutral' : 'accent'}>
                                  {existing ? t('admin:models.pull.alreadyAdded') : t('admin:models.pull.available')}
                                </Badge>
                              </label>
                            )
                          })}
                          </div>
                        </div>
                      )}
                  </>
                ) : null}
              </div>
            )}
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setPullModels((current) => ({ ...current, open: false }))} disabled={addingPulledModels}>
              {t('common:actions.cancel')}
            </Button>
            {pullModels.fetched ? (
              <Button
                onClick={() => void addPulledModels(pulledSelectedCandidates)}
                loading={addingPulledModels}
                disabled={pulledSelectedCandidates.length === 0}
              >
                {t('admin:models.pull.addSelected', { count: pulledSelectedCandidates.length })}
              </Button>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Quick-create dialog — only the six fields needed to register a row.
          Everything else lives on /admin/models/:id. */}
      <Dialog open={creator.open} onOpenChange={(o) => !submittingRef.current && setCreator({ ...creator, open: o })}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('admin:models.newTitle')}</DialogTitle>
            <DialogDescription>{t('admin:models.newDialogLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label={t('admin:models.fields.requestId')} htmlFor="m-new-req">
                <Input
                  id="m-new-req"
                  value={creator.draft.request_id}
                  onChange={(e) => setCreator({ ...creator, draft: { ...creator.draft, request_id: e.target.value } })}
                  placeholder="gpt-4o"
                />
              </Field>
              <Field label={t('admin:models.fields.channel')} htmlFor="m-new-ch">
                <Select
                  value={creator.draft.channel_id}
                  onValueChange={(v) => setCreator({ ...creator, draft: { ...creator.draft, channel_id: v, kind: channels.find((c) => c.id === v)?.type === 'typesafe' ? 'decision' : creator.draft.kind === 'decision' ? 'chat' : creator.draft.kind } })}
                >
                  <SelectTrigger id="m-new-ch">
                    <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
                  </SelectTrigger>
                  <SelectContent>
                    {(creator.draft.request_id.trim() ? creatorChannels : channels).map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name} ({c.type})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('admin:models.fields.kind')} htmlFor="m-new-kind">
                <Select
                  value={creator.draft.kind}
                  onValueChange={(v) =>
                    setCreator({ ...creator, draft: { ...creator.draft, kind: v as ApiModel['kind'] } })
                  }
                >
                  <SelectTrigger id="m-new-kind">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {KINDS.filter((k) => channels.find((c) => c.id === creator.draft.channel_id)?.type === 'typesafe' ? k === 'decision' : k !== 'decision').map((k) => (
                      <SelectItem key={k} value={k}>
                        {k === 'decision' ? t('admin:models.fields.decisionKind') : k}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t('admin:models.fields.label')} htmlFor="m-new-label">
                <Input
                  id="m-new-label"
                  value={creator.draft.label}
                  onChange={(e) => setCreator({ ...creator, draft: { ...creator.draft, label: e.target.value } })}
                  placeholder="Claude Opus 4.8"
                />
              </Field>
              <Field label={t('admin:models.fields.icon')} htmlFor="m-new-icon" className="sm:col-span-2">
                <IconUploader
                  id="m-new-icon"
                  value={creator.draft.icon}
                  onChange={(v) => setCreator({ ...creator, draft: { ...creator.draft, icon: v } })}
                  placeholder="🌟 or https://example.com/icon.png"
                />
              </Field>
              <Field label={t('admin:models.fields.description')} htmlFor="m-new-desc" className="sm:col-span-2">
                <Input
                  id="m-new-desc"
                  value={creator.draft.description}
                  onChange={(e) =>
                    setCreator({ ...creator, draft: { ...creator.draft, description: e.target.value } })
                  }
                />
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreator({ ...creator, open: false })} disabled={submitting}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void submitCreate()} loading={submitting}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:models.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:models.removeBody', { label: confirmDelete.label }) : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)} disabled={deleting}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" onClick={() => confirmDelete && void remove(confirmDelete)} loading={deleting}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
