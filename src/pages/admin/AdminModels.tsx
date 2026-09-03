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
import { useEffect, useRef, useState } from 'react'
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

const KINDS = ['chat', 'image', 'embedding'] as const

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
  const [models, setModels] = useState<ApiModel[]>([])
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

  function openNew() {
    setCreator({
      open: true,
      draft: { ...emptyCreate, kind: createKind, channel_id: channels[0]?.id ?? '' },
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
    void adminApi.reorderModels(next.map((m) => m.id)).catch((e) => {
      setModels(prev)
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
      <header className="flex flex-col items-start gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:models.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:models.lead')}</p>
        </div>
        <div className="flex w-full flex-wrap gap-2 sm:w-auto sm:items-center sm:justify-end">
          <Button
            variant="secondary"
            className="min-h-[var(--tap-min)] flex-1 px-2 sm:min-h-0 sm:flex-none sm:px-4"
            leadingIcon={<TagsIcon size={15} aria-hidden />}
            onClick={() => navigate('/admin/model-tags')}
          >
            {t('admin:modelTags.manage', { defaultValue: 'Manage tags' })}
          </Button>
          <Button
            variant="secondary"
            className="min-h-[var(--tap-min)] flex-1 px-2 sm:min-h-0 sm:flex-none sm:px-4"
            leadingIcon={<RefreshCw size={15} aria-hidden />}
            onClick={openPullModels}
          >
            {t('admin:models.pull.action')}
          </Button>
          <Button data-admin-tour="models-create" className="min-h-[var(--tap-min)] flex-1 px-2 sm:min-h-0 sm:flex-none sm:px-4" leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
            {t('admin:models.new')}
          </Button>
        </div>
      </header>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : models.length === 0 ? (
          <div className="rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-6 py-10 text-center text-sm text-[var(--color-fg-muted)]">
            {t('admin:models.empty')}
          </div>
        ) : (
          <AdminSortableList
            items={models}
            onItemsChange={setModels}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            mobileDragOnly
            rowClassName="grid grid-cols-[2.75rem_auto_minmax(0,1fr)_auto] items-center gap-x-2 gap-y-2 px-2 py-3.5 md:grid-cols-[auto_auto_auto_minmax(0,1fr)_auto_auto_auto] md:gap-2 md:px-5 md:py-4"
            renderItem={(m) => {
              const ch = channels.find((c) => c.id === m.channel_id)
              const toggling = togglingModelIds.has(m.id)
              return (
                <>
                  <div className="col-start-2 row-start-1 grid size-9 shrink-0 place-items-center rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-muted)] md:col-start-auto md:row-start-auto">
                    <ModelIcon icon={m.icon} size={22} />
                  </div>
                  <div className="col-start-3 row-start-1 min-w-0 md:col-start-auto md:row-start-auto">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="font-medium text-[var(--color-fg)] truncate">{m.label}</span>
                      <Badge size="xs">{m.kind}</Badge>
                      <Badge size="xs" variant="neutral">{m.tool_mode}</Badge>
                      {!m.enabled ? <Badge size="xs" variant="neutral">disabled</Badge> : null}
                    </div>
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">
                      {ch?.name ?? '(unknown channel)'} · {m.request_id}
                      {m.kind === 'chat' ? ` · in $${m.price_input}/M · out $${m.price_output}/M` : ''}
                      {m.kind === 'image' ? ` · $${m.price_per_image}/img` : ''}
                      {m.kind === 'embedding' ? ` · dim ${m.dim}` : ''}
                    </div>
                  </div>
                  <Tooltip content={t('admin:models.visibleToggle', { defaultValue: m.enabled ? 'Visible to users' : 'Hidden from users' })}>
                    <span className="col-start-4 row-start-1 shrink-0 md:col-start-auto md:row-start-auto">
                      <Switch
                        checked={m.enabled}
                        disabled={toggling}
                        aria-busy={toggling || undefined}
                        onCheckedChange={() => void toggleEnabled(m)}
                        aria-label={t('admin:models.visibleToggle', { defaultValue: 'Show in app' })}
                      />
                    </span>
                  </Tooltip>
                  <div className="col-span-4 row-start-2 flex items-center justify-end gap-1 md:contents">
                    <Button
                      variant="ghost"
                      size="sm"
                      className="max-md:size-[var(--tap-min)] max-md:gap-0 max-md:px-0"
                      aria-label={`${t('admin:models.settings')}: ${m.label}`}
                      leadingIcon={<SettingsIcon size={13} aria-hidden />}
                      onClick={() => navigate(`/admin/models/${encodeURIComponent(m.id)}`)}
                    >
                      <span className="max-md:sr-only">{t('admin:models.settings')}</span>
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="max-md:size-[var(--tap-min)] max-md:gap-0 max-md:px-0"
                      aria-label={`${t('admin:common.remove')}: ${m.label}`}
                      leadingIcon={<Trash2 size={13} aria-hidden />}
                      onClick={() => setConfirmDelete(m)}
                    >
                      <span className="max-md:sr-only">{t('admin:common.remove')}</span>
                    </Button>
                  </div>
                </>
              )
            }}
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
              <div className="rounded-lg border border-dashed border-[var(--color-border)] px-5 py-8 text-center text-sm text-[var(--color-fg-muted)]">
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
                  <div className="flex flex-col items-start gap-3 rounded-lg border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-4 py-3 text-sm text-[var(--color-fg)] sm:flex-row sm:items-center sm:justify-between">
                    <span>{t('admin:models.pull.failed')}</span>
                    <Button variant="ghost" size="sm" onClick={() => void discoverSavedModels()}>
                      {t('admin:models.pull.retry')}
                    </Button>
                  </div>
                ) : null}

                {pullModels.fetched ? (
                  <>
                      <div className="flex flex-col gap-3 border-y border-[var(--color-border)] py-3 sm:flex-row sm:items-center sm:justify-between">
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
                          <div className="max-h-[min(42vh,24rem)] overflow-y-auto rounded-lg border border-[var(--color-border)]">
                          {pulledFiltered.map((candidate) => {
                            const key = candidate.request_id.trim().toLowerCase()
                            const existing = pulledExistingKeys.has(key)
                            return (
                              <label
                                key={key}
                                className={`flex min-h-14 items-center gap-3 border-b border-[var(--color-border)] px-3 py-2.5 last:border-b-0 ${existing ? 'cursor-default bg-[var(--color-bg-muted)]/60' : 'cursor-pointer hover:bg-[var(--color-bg-muted)]'}`}
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
              <Field label={t('admin:models.fields.channel')} htmlFor="m-new-ch">
                <Select
                  value={creator.draft.channel_id}
                  onValueChange={(v) => setCreator({ ...creator, draft: { ...creator.draft, channel_id: v } })}
                >
                  <SelectTrigger id="m-new-ch">
                    <SelectValue placeholder={t('admin:settings.fields.pickModel')} />
                  </SelectTrigger>
                  <SelectContent>
                    {channels.map((c) => (
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
                    {KINDS.map((k) => (
                      <SelectItem key={k} value={k}>
                        {k}
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
              <Field label={t('admin:models.fields.requestId')} htmlFor="m-new-req">
                <Input
                  id="m-new-req"
                  value={creator.draft.request_id}
                  onChange={(e) =>
                    setCreator({ ...creator, draft: { ...creator.draft, request_id: e.target.value } })
                  }
                  placeholder="claude-opus-4-8"
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
