/**
 * AdminUserGroups — manage membership tiers (§ user groups). Each group has a
 * name, short description, settlement price and a feature list.
 * Per-model usage caps live on the model editor. Global credit and quota policy
 * lives on the dedicated credit settings page.
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, Pencil, Search, Trash2, UserRound } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type {
  ApiResourceAccessMode,
  ApiResourceAccessPolicy,
  ApiGroupUserSummary,
  ApiUserGroup,
  ApiUserGroupPermissions,
} from '@/api/types'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Switch } from '@/components/ui/switch'
import { Badge } from '@/components/ui/badge'
import { Pagination } from '@/components/ui/pagination'
import { SegmentedControl } from '@/components/ui/segmented-control'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/hooks/use-toast'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { PanelFallback } from '@/components/ui/panel-fallback'
import {
  currencyInputStep,
  formatCurrencyMinor,
  inputAmountToMinor,
  minorAmountToInput,
  normalizeSettlementCurrency,
} from '@/lib/currency'
import {
  CREDIT_PERIOD_SECONDS,
  splitCreditPeriod,
  type CreditPeriodUnit,
} from '@/lib/credit-period'

type Draft = Partial<ApiUserGroup> & {
  featuresText?: string
  researchEnabled?: boolean
  workspacesEnabled?: boolean
  creditPeriodValue?: number
  creditPeriodUnit?: CreditPeriodUnit
  monthlyPriceInput?: string
  yearlyPriceInput?: string
}

type EditorTab = 'plan' | 'quota' | 'permissions' | 'users'

interface PermissionResource {
  id: string
  name: string
  description?: string
}

interface PermissionCatalog {
  prompts: PermissionResource[]
  skills: PermissionResource[]
  tools: PermissionResource[]
}

const EMPTY_CATALOG: PermissionCatalog = { prompts: [], skills: [], tools: [] }
const GROUP_USERS_PAGE_SIZE = 20

const DEFAULT_PERMISSIONS: ApiUserGroupPermissions = {
  prompts: { mode: 'all', ids: [] },
  skills: { mode: 'all', ids: [] },
  tools: { mode: 'all', ids: [] },
  allow_sharing: true,
  allow_knowledge_bases: true,
  allow_knowledge_base_sharing: true,
  allow_file_upload: true,
  allow_conversation_export: true,
  allow_voice_transcription: true,
  allow_memory: true,
  allow_drawing: true,
}

function normalizePermissions(value?: Partial<ApiUserGroupPermissions>): ApiUserGroupPermissions {
  const policy = (candidate?: Partial<ApiResourceAccessPolicy>): ApiResourceAccessPolicy => ({
    mode: candidate?.mode ?? 'all',
    ids: candidate?.mode === 'selected' ? [...(candidate.ids ?? [])] : [],
  })
  const normalized = {
    ...DEFAULT_PERMISSIONS,
    ...value,
    prompts: policy(value?.prompts),
    skills: policy(value?.skills),
    tools: policy(value?.tools),
  }
  if (!normalized.allow_knowledge_bases) normalized.allow_knowledge_base_sharing = false
  return normalized
}

function ResourcePermissionEditor({
  title,
  description,
  policy,
  resources,
  loading,
  onChange,
}: {
  title: string
  description: string
  policy: ApiResourceAccessPolicy
  resources: PermissionResource[]
  loading: boolean
  onChange: (policy: ApiResourceAccessPolicy) => void
}) {
  const { t } = useTranslation('admin')
  const [query, setQuery] = useState('')
  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase()
    if (!needle) return resources
    return resources.filter((resource) =>
      `${resource.name}\n${resource.description ?? ''}`.toLocaleLowerCase().includes(needle),
    )
  }, [query, resources])
  const selected = new Set(policy.ids)

  function setMode(mode: ApiResourceAccessMode) {
    onChange({ mode, ids: mode === 'selected' ? policy.ids : [] })
  }

  function toggle(id: string) {
    const next = new Set(policy.ids)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    const visibleIDs = new Set(resources.map((item) => item.id))
    const hiddenSelections = policy.ids.filter((resourceID) => !visibleIDs.has(resourceID))
    const visibleSelections = resources.map((item) => item.id).filter((resourceID) => next.has(resourceID))
    onChange({ mode: 'selected', ids: [...hiddenSelections, ...visibleSelections] })
  }

  return (
    <section className="border-b border-[var(--color-divider)] py-5 first:pt-0 last:border-b-0 last:pb-0">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 max-w-[58ch]">
          <h3 className="text-sm font-medium text-[var(--color-fg)]">{title}</h3>
          <p className="mt-1 text-[12px] leading-relaxed text-[var(--color-fg-subtle)]">{description}</p>
        </div>
        <SegmentedControl
          label={title}
          value={policy.mode}
          options={[
            { value: 'all', label: t('groups.permissions.modes.all', { defaultValue: 'All' }) },
            { value: 'selected', label: t('groups.permissions.modes.selected', { defaultValue: 'Selected' }) },
            { value: 'none', label: t('groups.permissions.modes.none', { defaultValue: 'None' }) },
          ]}
          onChange={setMode}
          fullWidthOnMobile
        />
      </div>

      {policy.mode === 'selected' ? (
        <div className="mt-4 overflow-hidden rounded-[8px] border border-[var(--color-border)]">
          <div className="relative border-b border-[var(--color-divider)] p-2.5">
            <Search
              size={14}
              aria-hidden
              className="pointer-events-none absolute left-5 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]"
            />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t('groups.permissions.searchResources', { defaultValue: 'Search resources' })}
              aria-label={t('groups.permissions.searchResources', { defaultValue: 'Search resources' })}
              className="pl-9"
            />
          </div>
          <div className="max-h-48 overflow-y-auto overscroll-contain">
            {loading ? (
              <p className="px-4 py-6 text-center text-sm text-[var(--color-fg-muted)]">
                {t('common.loading', { defaultValue: 'Loading...' })}
              </p>
            ) : filtered.length === 0 ? (
              <p className="px-4 py-6 text-center text-sm text-[var(--color-fg-muted)]">
                {t('groups.permissions.noResources', { defaultValue: 'No matching resources.' })}
              </p>
            ) : (
              <div className="divide-y divide-[var(--color-divider)]">
                {filtered.map((resource) => (
                  <label
                    key={resource.id}
                    className="flex min-h-12 cursor-pointer items-start gap-3 px-3 py-2.5 hover:bg-[var(--color-bg-muted)]"
                  >
                    <Checkbox
                      checked={selected.has(resource.id)}
                      onChange={() => toggle(resource.id)}
                      aria-label={resource.name}
                      className="mt-0.5"
                    />
                    <span className="min-w-0">
                      <span className="block text-[13px] font-medium text-[var(--color-fg)]">{resource.name}</span>
                      {resource.description ? (
                        <span className="mt-0.5 block text-[11.5px] leading-snug text-[var(--color-fg-subtle)]">
                          {resource.description}
                        </span>
                      ) : null}
                    </span>
                  </label>
                ))}
              </div>
            )}
          </div>
          <p className="border-t border-[var(--color-divider)] px-3 py-2 text-[11.5px] text-[var(--color-fg-subtle)]">
            {t('groups.permissions.selectedCount', {
              count: policy.ids.length,
              defaultValue: '{{count}} selected',
            })}
          </p>
        </div>
      ) : null}
    </section>
  )
}

function CapabilityToggle({
  label,
  description,
  checked,
  disabled = false,
  onCheckedChange,
}: {
  label: string
  description: string
  checked: boolean
  disabled?: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className="flex min-h-16 items-center justify-between gap-4 border-b border-[var(--color-divider)] py-3 last:border-b-0">
      <div className="min-w-0 max-w-[58ch]">
        <p className="text-sm font-medium text-[var(--color-fg)]">{label}</p>
        <p className="mt-0.5 text-[12px] leading-relaxed text-[var(--color-fg-subtle)]">{description}</p>
      </div>
      <Switch checked={checked} disabled={disabled} onCheckedChange={onCheckedChange} aria-label={label} />
    </div>
  )
}

// Reserved functional feature flag (not a marketing bullet) — gates the Deep
// Research mode. Managed via a dedicated toggle and hidden from the free-text
// features editor + the subscription page's marketing list.
const RESEARCH_FEATURE = 'research'
// Reserved functional flag: whether the group may CREATE workspaces (§workspaces).
const WORKSPACES_FEATURE = 'workspaces'

export default function AdminUserGroups() {
  const { t, i18n } = useTranslation(['admin', 'common'])
  const [rows, setRows] = useState<ApiUserGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [editor, setEditor] = useState<{ open: boolean; row?: ApiUserGroup; draft: Draft }>({ open: false, draft: {} })
  const [confirmDelete, setConfirmDelete] = useState<ApiUserGroup | null>(null)
  const [settlementCurrency, setSettlementCurrency] = useState('USD')
  const [saving, setSaving] = useState(false)
  const savingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)
  const deletingRef = useRef(false)
  const [editorTab, setEditorTab] = useState<EditorTab>('plan')
  const [permissionCatalog, setPermissionCatalog] = useState<PermissionCatalog>(EMPTY_CATALOG)
  const [catalogLoading, setCatalogLoading] = useState(false)
  const [catalogLoaded, setCatalogLoaded] = useState(false)
  const [catalogLoadFailed, setCatalogLoadFailed] = useState(false)
  const [catalogLoadAttempt, setCatalogLoadAttempt] = useState(0)
  const catalogRequestRef = useRef(0)
  const [groupUsers, setGroupUsers] = useState<ApiGroupUserSummary[]>([])
  const [groupUsersTotal, setGroupUsersTotal] = useState(0)
  const [groupUsersLoading, setGroupUsersLoading] = useState(false)
  const [groupUsersLoaded, setGroupUsersLoaded] = useState(false)
  const [groupUsersLoadFailed, setGroupUsersLoadFailed] = useState(false)
  const [groupUsersLoadAttempt, setGroupUsersLoadAttempt] = useState(0)
  const [groupUsersQuery, setGroupUsersQuery] = useState('')
  const [groupUsersSearch, setGroupUsersSearch] = useState('')
  const [groupUsersPage, setGroupUsersPage] = useState(1)
  const groupUsersRequestRef = useRef(0)

  async function load() {
    setLoading(true)
    try {
      const [groups, settings] = await Promise.all([adminApi.userGroups(), adminApi.settings()])
      setRows(groups)
      setSettlementCurrency(normalizeSettlementCurrency(settings.settlement_currency ?? groups[0]?.settlement_currency))
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
    catalogRequestRef.current += 1
    groupUsersRequestRef.current += 1
    setCatalogLoaded(false)
    setCatalogLoadFailed(false)
    setEditorTab('plan')
    setGroupUsers([])
    setGroupUsersTotal(0)
    setGroupUsersLoaded(false)
    setGroupUsersLoadFailed(false)
    setGroupUsersQuery('')
    setGroupUsersSearch('')
    setGroupUsersPage(1)
    setEditor({
      open: true,
      draft: {
        featuresText: '',
        monthlyPriceInput: minorAmountToInput(0, settlementCurrency, i18n.resolvedLanguage),
        yearlyPriceInput: minorAmountToInput(0, settlementCurrency, i18n.resolvedLanguage),
        researchEnabled: false,
        workspacesEnabled: false,
        is_public: true,
        is_purchasable: true,
        max_workspaces: 0,
        max_storage_mb: 0,
        creditPeriodValue: 0,
        creditPeriodUnit: 'day',
        permissions: normalizePermissions(),
      },
    })
  }
  function openEdit(row: ApiUserGroup) {
    catalogRequestRef.current += 1
    groupUsersRequestRef.current += 1
    setCatalogLoaded(false)
    setCatalogLoadFailed(false)
    const feats = row.features ?? []
    const period = splitCreditPeriod(row.credit_period_seconds ?? 0)
    setEditorTab('plan')
    setGroupUsers([])
    setGroupUsersTotal(0)
    setGroupUsersLoaded(false)
    setGroupUsersLoadFailed(false)
    setGroupUsersQuery('')
    setGroupUsersSearch('')
    setGroupUsersPage(1)
    setEditor({
      open: true,
      row,
      draft: {
        ...row,
        monthlyPriceInput: minorAmountToInput(
          row.monthly_price_amount_minor ?? 0,
          settlementCurrency,
          i18n.resolvedLanguage,
        ),
        yearlyPriceInput: minorAmountToInput(
          row.yearly_price_amount_minor ?? 0,
          settlementCurrency,
          i18n.resolvedLanguage,
        ),
        // Hide the reserved functional flag from the marketing free-text editor.
        featuresText: feats.filter((f) => f !== RESEARCH_FEATURE && f !== WORKSPACES_FEATURE).join('\n'),
        researchEnabled: feats.includes(RESEARCH_FEATURE),
        workspacesEnabled: feats.includes(WORKSPACES_FEATURE),
        is_purchasable: row.is_purchasable !== false,
        creditPeriodValue: period.value,
        creditPeriodUnit: period.unit,
        permissions: normalizePermissions(row.permissions),
      },
    })
  }
  function setDraft(p: Partial<Draft>) {
    setEditor((ed) => ({ ...ed, draft: { ...ed.draft, ...p } }))
  }

  function setPermissions(patch: Partial<ApiUserGroupPermissions>) {
    setEditor((ed) => ({
      ...ed,
      draft: {
        ...ed.draft,
        permissions: { ...normalizePermissions(ed.draft.permissions), ...patch },
      },
    }))
  }

  function closeEditor() {
    catalogRequestRef.current += 1
    groupUsersRequestRef.current += 1
    setCatalogLoading(false)
    setGroupUsersLoading(false)
    setEditor((current) => ({ ...current, open: false }))
  }

  useEffect(() => {
    if (!editor.open || editorTab !== 'permissions' || catalogLoaded) return
    const requestID = ++catalogRequestRef.current
    let cancelled = false
    setCatalogLoading(true)
    setCatalogLoadFailed(false)
    void Promise.all([
      adminApi.prompts(),
      adminApi.skills(),
      adminApi.builtinTools(),
      adminApi.mcpServers(),
      adminApi.models('chat'),
    ])
      .then(([prompts, skills, builtins, mcpServers, models]) => {
        if (cancelled || requestID !== catalogRequestRef.current) return
        const hosted = new Map<string, PermissionResource>()
        for (const model of models) {
          for (const tool of model.official_tools ?? []) {
            const id = `hosted:${tool.name}`
            if (!hosted.has(id)) hosted.set(id, { id, name: tool.name })
          }
        }
        setPermissionCatalog({
          prompts: prompts.map((prompt) => ({ id: prompt.id, name: prompt.name, description: prompt.description })),
          skills: skills.map((skill) => ({
            id: skill.id,
            name: skill.name,
            description: skill.display_description || skill.description,
          })),
          tools: [
            ...builtins.filter((tool) => tool.globally_enabled !== false).map((tool) => ({
              id: `builtin:${tool.name}`,
              name: t(`chat:tools.${tool.name}`, { defaultValue: tool.name }),
              description: tool.description,
            })),
            ...hosted.values(),
            ...mcpServers
              .filter((server) => server.enabled)
              .map((server) => ({ id: `mcp:${server.id}`, name: server.name, description: server.description })),
          ],
        })
        setCatalogLoaded(true)
      })
      .catch((error) => {
        if (cancelled || requestID !== catalogRequestRef.current) return
        setCatalogLoadFailed(true)
        toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
      })
      .finally(() => {
        if (!cancelled && requestID === catalogRequestRef.current) setCatalogLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [catalogLoadAttempt, catalogLoaded, editor.open, editorTab, t])

  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (event.kind !== 'account') return
        catalogRequestRef.current += 1
        setPermissionCatalog(EMPTY_CATALOG)
        setCatalogLoaded(false)
        setCatalogLoadFailed(false)
        setCatalogLoading(false)
        setCatalogLoadAttempt((attempt) => attempt + 1)
      }),
    [],
  )

  useEffect(() => {
    const groupID = editor.row?.id
    if (!editor.open || editorTab !== 'users' || !groupID) return
    const requestID = ++groupUsersRequestRef.current
    let cancelled = false
    setGroupUsersLoading(true)
    setGroupUsersLoaded(false)
    setGroupUsersLoadFailed(false)
    const offset = (groupUsersPage - 1) * GROUP_USERS_PAGE_SIZE
    void adminApi
      .userGroupUsers(groupID, groupUsersSearch, GROUP_USERS_PAGE_SIZE, offset)
      .then((page) => {
        if (cancelled || requestID !== groupUsersRequestRef.current) return
        const lastPage = Math.max(1, Math.ceil(page.total / GROUP_USERS_PAGE_SIZE))
        if (groupUsersPage > lastPage) {
          setGroupUsersPage(lastPage)
          return
        }
        setGroupUsers(page.users)
        setGroupUsersTotal(page.total)
        setGroupUsersLoaded(true)
      })
      .catch((error) => {
        if (cancelled || requestID !== groupUsersRequestRef.current) return
        setGroupUsers([])
        setGroupUsersTotal(0)
        setGroupUsersLoadFailed(true)
        toast.error(error instanceof ApiError ? error.message : t('admin:common.failed'))
      })
      .finally(() => {
        if (!cancelled && requestID === groupUsersRequestRef.current) setGroupUsersLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [editor.open, editor.row?.id, editorTab, groupUsersLoadAttempt, groupUsersPage, groupUsersSearch, t])

  async function submit() {
    if (savingRef.current) return
    const d = editor.draft
    if (!d.name?.trim()) {
      toast.error(t('admin:groups.errors.nameRequired'))
      return
    }
    const marketing = (d.featuresText ?? '')
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean)
      .filter((f) => f !== RESEARCH_FEATURE && f !== WORKSPACES_FEATURE)
    // Append the reserved functional flags for the enabled toggles.
    const features = [
      ...marketing,
      ...(d.researchEnabled ? [RESEARCH_FEATURE] : []),
      ...(d.workspacesEnabled ? [WORKSPACES_FEATURE] : []),
    ]
    const periodSeconds = Math.max(0, Number(d.creditPeriodValue) || 0)
      * CREDIT_PERIOD_SECONDS[d.creditPeriodUnit ?? 'day']
    const creditAllowance = Math.max(0, Number(d.credit_allowance) || 0)
    if (creditAllowance > 0 && periodSeconds <= 0) {
      toast.error(t('admin:groups.errors.creditPeriodRequired'))
      return
    }
    const monthlyPriceAmountMinor = inputAmountToMinor(
      d.monthlyPriceInput ?? '',
      settlementCurrency,
      i18n.resolvedLanguage,
    )
    const yearlyPriceAmountMinor = inputAmountToMinor(
      d.yearlyPriceInput ?? '',
      settlementCurrency,
      i18n.resolvedLanguage,
    )
    if (monthlyPriceAmountMinor === null || yearlyPriceAmountMinor === null) {
      toast.error(t('admin:groups.errors.priceInvalid'))
      return
    }
    const body: Partial<ApiUserGroup> = {
      name: d.name,
      description: d.description ?? '',
      features,
      monthly_price_amount_minor: monthlyPriceAmountMinor,
      yearly_price_amount_minor: yearlyPriceAmountMinor,
      max_projects: Math.max(0, Number(d.max_projects) || 0),
      max_kbs: Math.max(0, Number(d.max_kbs) || 0),
      max_workspaces: Math.max(0, Number(d.max_workspaces) || 0),
      max_storage_mb: Math.max(0, Number(d.max_storage_mb) || 0),
      is_public: d.is_public !== false,
      is_purchasable: d.is_purchasable !== false,
      credit_allowance: creditAllowance,
      credit_period_seconds: periodSeconds,
      permissions: normalizePermissions(d.permissions),
    }
    savingRef.current = true
    setSaving(true)
    try {
      if (editor.row) {
        await adminApi.updateUserGroup(editor.row.id, body)
        toast.success(t('admin:groups.updated'))
      } else {
        await adminApi.createUserGroup(body)
        toast.success(t('admin:groups.created'))
      }
      closeEditor()
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

  async function remove(row: ApiUserGroup) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await adminApi.removeUserGroup(row.id)
      toast.success(t('admin:groups.removed'))
      setConfirmDelete(null)
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  function persistOrder(next: ApiUserGroup[], prev: ApiUserGroup[]) {
    void adminApi.reorderUserGroups(next.map((g) => g.id)).catch((e) => {
      setRows(prev)
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    })
  }

  return (
    <div>
      <header className="flex flex-col items-stretch gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <h1 className="font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl">{t('admin:groups.title')}</h1>
          <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('admin:groups.lead')}</p>
        </div>
        <Button className="w-full sm:w-auto" leadingIcon={<Plus size={15} aria-hidden />} onClick={openNew}>
          {t('admin:groups.new')}
        </Button>
      </header>

      <section className="mt-8">
        {loading ? (
          <PanelFallback />
        ) : (
          <AdminSortableList
            items={rows}
            onItemsChange={setRows}
            onOrderCommit={persistOrder}
            dragHandleLabel={t('admin:common.dragHandle')}
            moveUpLabel={t('admin:common.moveUp')}
            moveDownLabel={t('admin:common.moveDown')}
            mobileDragOnly
            rowClassName="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-2 gap-y-1.5 px-3 py-3 md:grid-cols-[auto_auto_minmax(0,1fr)_auto] md:gap-3 md:px-5 md:py-4"
            renderItem={(g) => (
              <>
                <div className="min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-[var(--color-fg)] truncate">{g.name}</span>
                    {g.is_default ? <Badge size="xs" variant="neutral">{t('admin:groups.default')}</Badge> : null}
                    <span className="text-[12px] text-[var(--color-fg-subtle)] tabular-nums">
                      {g.monthly_price_amount_minor > 0
                        ? `${formatCurrencyMinor(g.monthly_price_amount_minor, settlementCurrency, i18n.resolvedLanguage)} ${t('admin:groups.monthlyShort')}`
                        : null}
                      {g.monthly_price_amount_minor > 0 && g.yearly_price_amount_minor > 0 ? ' · ' : null}
                      {g.yearly_price_amount_minor > 0
                        ? `${formatCurrencyMinor(g.yearly_price_amount_minor, settlementCurrency, i18n.resolvedLanguage)} ${t('admin:groups.yearlyShort')}`
                        : null}
                      {g.monthly_price_amount_minor <= 0 && g.yearly_price_amount_minor <= 0
                        ? t('admin:groups.freePrice')
                        : null}
                    </span>
                  </div>
                  {g.description ? (
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] line-clamp-1">{g.description}</div>
                  ) : null}
                </div>
                <div className="flex items-center justify-end gap-1 max-md:col-start-2">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="max-md:size-11"
                    leadingIcon={<Pencil size={14} aria-hidden />}
                    onClick={() => openEdit(g)}
                    aria-label={`${t('admin:common.edit')}: ${g.name}`}
                  />
                  {!g.is_default ? (
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      className="text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] max-md:size-11"
                      leadingIcon={<Trash2 size={14} aria-hidden />}
                      onClick={() => setConfirmDelete(g)}
                      aria-label={`${t('admin:common.remove')}: ${g.name}`}
                    />
                  ) : null}
                </div>
              </>
            )}
          />
        )}
      </section>

      <Dialog
        open={editor.open}
        onOpenChange={(open) => {
          if (savingRef.current || open) return
          closeEditor()
        }}
      >
        <DialogContent
          size="xl"
          className="h-[min(50rem,calc(100dvh-2rem))] max-sm:h-[calc(100dvh-1rem)] max-sm:max-h-[calc(100dvh-1rem)] max-sm:w-[calc(100vw-1rem)]"
          closeDisabled={saving}
        >
          <DialogHeader>
            <DialogTitle>{editor.row ? t('admin:groups.editorTitle') : t('admin:groups.newTitle')}</DialogTitle>
            <DialogDescription>{t('admin:groups.editorLead')}</DialogDescription>
          </DialogHeader>
          <DialogBody className="flex min-h-0 flex-col overflow-hidden px-0 pb-0">
            <Tabs value={editorTab} onValueChange={(value) => setEditorTab(value as EditorTab)} className="flex min-h-0 flex-1 flex-col">
              <div className="shrink-0 border-b border-[var(--color-divider)] px-4 pb-3 sm:px-6">
                <TabsList variant="segmented" className="grid w-full grid-cols-2 sm:grid-cols-4">
                  <TabsTrigger variant="segmented" value="plan" className="min-w-0 justify-center px-2">
                    {t('admin:groups.tabs.plan', { defaultValue: 'Plan' })}
                  </TabsTrigger>
                  <TabsTrigger variant="segmented" value="quota" className="min-w-0 justify-center px-2">
                    {t('admin:groups.tabs.quota', { defaultValue: 'Quotas' })}
                  </TabsTrigger>
                  <TabsTrigger variant="segmented" value="permissions" className="min-w-0 justify-center px-2">
                    {t('admin:groups.tabs.permissions', { defaultValue: 'Permissions' })}
                  </TabsTrigger>
                  <TabsTrigger variant="segmented" value="users" className="min-w-0 justify-center px-2">
                    {t('admin:groups.tabs.users', { defaultValue: 'Users' })}
                  </TabsTrigger>
                </TabsList>
              </div>

              <TabsContent value="plan" className="mt-0 min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
                <div className="grid gap-4">
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={t('admin:groups.fields.name')} htmlFor="g-name">
                      <Input id="g-name" value={editor.draft.name ?? ''} onChange={(e) => setDraft({ name: e.target.value })} placeholder="VIP" />
                    </Field>
                    <Field label={t('admin:groups.fields.description')} htmlFor="g-desc">
                      <Input
                        id="g-desc"
                        value={editor.draft.description ?? ''}
                        onChange={(e) => setDraft({ description: e.target.value })}
                        placeholder={t('admin:groups.fields.descriptionPlaceholder')}
                      />
                    </Field>
                  </div>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field
                      label={t('admin:groups.fields.monthlyPrice', { currency: settlementCurrency })}
                      htmlFor="g-monthly-price"
                      hint={t('admin:groups.fields.priceHint')}
                    >
                      <Input
                        id="g-monthly-price"
                        type="number"
                        min={0}
                        step={currencyInputStep(settlementCurrency, i18n.resolvedLanguage)}
                        value={editor.draft.monthlyPriceInput ?? ''}
                        onChange={(e) => setDraft({ monthlyPriceInput: e.target.value })}
                      />
                    </Field>
                    <Field
                      label={t('admin:groups.fields.yearlyPrice', { currency: settlementCurrency })}
                      htmlFor="g-yearly-price"
                      hint={t('admin:groups.fields.priceHint')}
                    >
                      <Input
                        id="g-yearly-price"
                        type="number"
                        min={0}
                        step={currencyInputStep(settlementCurrency, i18n.resolvedLanguage)}
                        value={editor.draft.yearlyPriceInput ?? ''}
                        onChange={(e) => setDraft({ yearlyPriceInput: e.target.value })}
                      />
                    </Field>
                  </div>
                  <Field label={t('admin:groups.fields.features')} htmlFor="g-feat" hint={t('admin:groups.fields.featuresHint')}>
                    <Textarea
                      id="g-feat"
                      rows={5}
                      value={editor.draft.featuresText ?? ''}
                      onChange={(e) => setDraft({ featuresText: e.target.value })}
                      placeholder={t('admin:groups.fields.featuresPlaceholder')}
                    />
                  </Field>
                  <div className="border-t border-[var(--color-divider)]">
                    <CapabilityToggle
                      label={t('admin:groups.fields.research', { defaultValue: 'Deep Research' })}
                      description={t('admin:groups.fields.researchHint', { defaultValue: 'Allow this group to use Deep Research.' })}
                      checked={Boolean(editor.draft.researchEnabled)}
                      onCheckedChange={(value) => setDraft({ researchEnabled: value })}
                    />
                    <CapabilityToggle
                      label={t('admin:groups.fields.workspaces', { defaultValue: 'Workspaces' })}
                      description={t('admin:groups.fields.workspacesHint', { defaultValue: 'Allow this group to create workspaces.' })}
                      checked={Boolean(editor.draft.workspacesEnabled)}
                      onCheckedChange={(value) => setDraft({ workspacesEnabled: value })}
                    />
                    <CapabilityToggle
                      label={t('admin:groups.fields.isPublic', { defaultValue: 'Show on subscription page' })}
                      description={t('admin:groups.fields.isPublicHint', { defaultValue: 'Show this plan on the subscription page.' })}
                      checked={editor.draft.is_public !== false}
                      onCheckedChange={(value) => setDraft({ is_public: value })}
                    />
                    <CapabilityToggle
                      label={t('admin:groups.fields.purchaseEnabled', { defaultValue: 'Allow purchases' })}
                      description={t('admin:groups.fields.purchaseEnabledHint', { defaultValue: 'Allow users to purchase this plan.' })}
                      checked={editor.draft.is_purchasable !== false}
                      onCheckedChange={(value) => setDraft({ is_purchasable: value })}
                    />
                  </div>
                </div>
              </TabsContent>

              <TabsContent value="quota" className="mt-0 min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
                <div className="grid gap-5">
                  <div>
                    <h3 className="text-sm font-medium text-[var(--color-fg)]">
                      {t('admin:groups.quota.resourceTitle', { defaultValue: 'Resource quotas' })}
                    </h3>
                    <p className="mt-1 text-[12px] text-[var(--color-fg-subtle)]">
                      {t('admin:groups.quota.resourceLead', { defaultValue: 'Use 0 for no limit.' })}
                    </p>
                  </div>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label={t('admin:groups.fields.maxProjects')} htmlFor="g-maxproj" hint={t('admin:groups.fields.limitHint')}>
                      <Input id="g-maxproj" type="number" min={0} value={String(editor.draft.max_projects ?? 0)} onChange={(e) => setDraft({ max_projects: Number(e.target.value) })} />
                    </Field>
                    <Field label={t('admin:groups.fields.maxKbs')} htmlFor="g-maxkbs" hint={t('admin:groups.fields.limitHint')}>
                      <Input id="g-maxkbs" type="number" min={0} value={String(editor.draft.max_kbs ?? 0)} onChange={(e) => setDraft({ max_kbs: Number(e.target.value) })} />
                    </Field>
                    <Field label={t('admin:groups.fields.maxWorkspaces', { defaultValue: 'Max workspaces' })} htmlFor="g-maxws" hint={t('admin:groups.fields.limitHint')}>
                      <Input id="g-maxws" type="number" min={0} value={String(editor.draft.max_workspaces ?? 0)} onChange={(e) => setDraft({ max_workspaces: Number(e.target.value) })} />
                    </Field>
                    <Field label={t('admin:groups.maxStorage', { defaultValue: 'Storage (MB)' })} htmlFor="g-maxstorage" hint={t('admin:groups.maxStorageHint', { defaultValue: '0 = unlimited. Non-image uploads only.' })}>
                      <Input id="g-maxstorage" type="number" min={0} value={String(editor.draft.max_storage_mb ?? 0)} onChange={(e) => setDraft({ max_storage_mb: Number(e.target.value) })} />
                    </Field>
                  </div>
                  <div className="border-t border-[var(--color-divider)] pt-5">
                    <h3 className="text-sm font-medium text-[var(--color-fg)]">{t('admin:groups.fields.creditsSection')}</h3>
                    <p className="mt-1 text-[12px] text-[var(--color-fg-subtle)]">{t('admin:groups.fields.creditsLead')}</p>
                    <div className="mt-4 grid gap-4 sm:grid-cols-2">
                      <Field label={t('admin:groups.fields.creditAllowance')} htmlFor="g-allow" hint={t('admin:groups.fields.creditAllowanceHint')}>
                        <Input id="g-allow" type="number" min={0} value={String(editor.draft.credit_allowance ?? 0)} onChange={(e) => setDraft({ credit_allowance: Number(e.target.value) })} />
                      </Field>
                      <Field label={t('admin:groups.fields.creditPeriod')} hint={t('admin:groups.fields.creditPeriodHint')}>
                        <div className="flex items-stretch gap-2">
                          <Input type="number" min={0} aria-label={t('admin:groups.fields.creditPeriod')} value={String(editor.draft.creditPeriodValue ?? 0)} onChange={(e) => setDraft({ creditPeriodValue: Number(e.target.value) })} wrapperClassName="min-w-0 flex-1" />
                          <Select value={editor.draft.creditPeriodUnit ?? 'day'} onValueChange={(value) => setDraft({ creditPeriodUnit: value as CreditPeriodUnit })}>
                            <SelectTrigger className="w-[120px] shrink-0"><SelectValue /></SelectTrigger>
                            <SelectContent>
                              <SelectItem value="hour">{t('admin:groups.fields.unitHour')}</SelectItem>
                              <SelectItem value="day">{t('admin:groups.fields.unitDay')}</SelectItem>
                              <SelectItem value="week">{t('admin:groups.fields.unitWeek')}</SelectItem>
                              <SelectItem value="month">{t('admin:groups.fields.unitMonth')}</SelectItem>
                            </SelectContent>
                          </Select>
                        </div>
                      </Field>
                    </div>
                  </div>
                </div>
              </TabsContent>

              <TabsContent value="permissions" className="mt-0 min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
                {catalogLoadFailed ? (
                  <div
                    role="alert"
                    className="flex min-h-28 flex-col items-center justify-center gap-3 border-b border-[var(--color-divider)] pb-5 text-center"
                  >
                    <p className="text-sm text-[var(--color-fg-muted)]">
                      {t('admin:groups.permissions.loadFailed', { defaultValue: 'Could not load the permission catalog.' })}
                    </p>
                    <Button
                      size="sm"
                      variant="secondary"
                      loading={catalogLoading}
                      onClick={() => setCatalogLoadAttempt((attempt) => attempt + 1)}
                    >
                      {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
                    </Button>
                  </div>
                ) : (
                  <>
                    <ResourcePermissionEditor
                      title={t('admin:groups.permissions.prompts', { defaultValue: 'Prompt access' })}
                      description={t('admin:groups.permissions.promptsHint', { defaultValue: 'Choose which administrator prompts members may access.' })}
                      policy={normalizePermissions(editor.draft.permissions).prompts}
                      resources={permissionCatalog.prompts}
                      loading={catalogLoading}
                      onChange={(prompts) => setPermissions({ prompts })}
                    />
                    <ResourcePermissionEditor
                      title={t('admin:groups.permissions.skills', { defaultValue: 'Skill access' })}
                      description={t('admin:groups.permissions.skillsHint', { defaultValue: 'Choose which administrator skills members may access.' })}
                      policy={normalizePermissions(editor.draft.permissions).skills}
                      resources={permissionCatalog.skills}
                      loading={catalogLoading}
                      onChange={(skills) => setPermissions({ skills })}
                    />
                    <ResourcePermissionEditor
                      title={t('admin:groups.permissions.tools', { defaultValue: 'Tool and MCP access' })}
                      description={t('admin:groups.permissions.toolsHint', { defaultValue: 'Restricted tools remain visible but cannot be selected or invoked.' })}
                      policy={normalizePermissions(editor.draft.permissions).tools}
                      resources={permissionCatalog.tools}
                      loading={catalogLoading}
                      onChange={(tools) => setPermissions({ tools })}
                    />
                  </>
                )}
                <section className="pt-5">
                  <h3 className="text-sm font-medium text-[var(--color-fg)]">
                    {t('admin:groups.permissions.capabilities', { defaultValue: 'Capabilities' })}
                  </h3>
                  <div className="mt-2">
                    <CapabilityToggle label={t('admin:groups.permissions.sharing', { defaultValue: 'Share conversations' })} description={t('admin:groups.permissions.sharingHint', { defaultValue: 'Create and manage public conversation links.' })} checked={normalizePermissions(editor.draft.permissions).allow_sharing} onCheckedChange={(allow_sharing) => setPermissions({ allow_sharing })} />
                    <CapabilityToggle label={t('admin:groups.permissions.knowledgeBases', { defaultValue: 'Use knowledge bases' })} description={t('admin:groups.permissions.knowledgeBasesHint', { defaultValue: 'Access personal, workspace, project, and shared knowledge bases.' })} checked={normalizePermissions(editor.draft.permissions).allow_knowledge_bases} onCheckedChange={(allow_knowledge_bases) => setPermissions({ allow_knowledge_bases, ...(!allow_knowledge_bases ? { allow_knowledge_base_sharing: false } : {}) })} />
                    <CapabilityToggle label={t('admin:groups.permissions.knowledgeBaseSharing', { defaultValue: 'Share knowledge bases' })} description={t('admin:groups.permissions.knowledgeBaseSharingHint', { defaultValue: 'Share owned personal knowledge bases as read-only or upload-enabled.' })} checked={normalizePermissions(editor.draft.permissions).allow_knowledge_base_sharing} disabled={!normalizePermissions(editor.draft.permissions).allow_knowledge_bases} onCheckedChange={(allow_knowledge_base_sharing) => setPermissions({ allow_knowledge_base_sharing })} />
                    <CapabilityToggle label={t('admin:groups.permissions.fileUpload', { defaultValue: 'Upload files' })} description={t('admin:groups.permissions.fileUploadHint', { defaultValue: 'Upload files to chats, projects, and writable knowledge bases.' })} checked={normalizePermissions(editor.draft.permissions).allow_file_upload} onCheckedChange={(allow_file_upload) => setPermissions({ allow_file_upload })} />
                    <CapabilityToggle label={t('admin:groups.permissions.export', { defaultValue: 'Export conversations' })} description={t('admin:groups.permissions.exportHint', { defaultValue: 'Export individual replies or all account conversations.' })} checked={normalizePermissions(editor.draft.permissions).allow_conversation_export} onCheckedChange={(allow_conversation_export) => setPermissions({ allow_conversation_export })} />
                    <CapabilityToggle label={t('admin:groups.permissions.voice', { defaultValue: 'Voice recognition' })} description={t('admin:groups.permissions.voiceHint', { defaultValue: 'Use streaming or recorded speech transcription.' })} checked={normalizePermissions(editor.draft.permissions).allow_voice_transcription} onCheckedChange={(allow_voice_transcription) => setPermissions({ allow_voice_transcription })} />
                    <CapabilityToggle label={t('admin:groups.permissions.memory', { defaultValue: 'Use memory' })} description={t('admin:groups.permissions.memoryHint', { defaultValue: 'Use and manage long-term personal memory.' })} checked={normalizePermissions(editor.draft.permissions).allow_memory} onCheckedChange={(allow_memory) => setPermissions({ allow_memory })} />
                    <CapabilityToggle label={t('admin:groups.permissions.drawing', { defaultValue: 'Drawing' })} description={t('admin:groups.permissions.drawingHint', { defaultValue: 'Use image models, image styles, and image generation tools.' })} checked={normalizePermissions(editor.draft.permissions).allow_drawing} onCheckedChange={(allow_drawing) => setPermissions({ allow_drawing })} />
                  </div>
                </section>
              </TabsContent>

              <TabsContent value="users" className="mt-0 min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
                {!editor.row ? (
                  <div className="grid min-h-64 place-items-center text-center">
                    <div className="max-w-sm">
                      <UserRound size={24} className="mx-auto text-[var(--color-fg-subtle)]" aria-hidden />
                      <p className="mt-3 text-sm font-medium text-[var(--color-fg)]">
                        {t('admin:groups.users.saveFirst', { defaultValue: 'Save this group to view its users' })}
                      </p>
                      <p className="mt-1 text-[12px] leading-relaxed text-[var(--color-fg-subtle)]">
                        {t('admin:groups.users.saveFirstHint', { defaultValue: 'Membership can be searched after the group has been created.' })}
                      </p>
                    </div>
                  </div>
                ) : (
                  <div>
                    <form
                      className="flex flex-col gap-2 sm:flex-row"
                      onSubmit={(event) => {
                        event.preventDefault()
                        setGroupUsersPage(1)
                        setGroupUsersSearch(groupUsersQuery.trim())
                        setGroupUsersLoadAttempt((attempt) => attempt + 1)
                      }}
                    >
                      <div className="relative min-w-0 flex-1">
                        <Search size={14} aria-hidden className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-faint)]" />
                        <Input value={groupUsersQuery} onChange={(event) => setGroupUsersQuery(event.target.value)} placeholder={t('admin:groups.users.search', { defaultValue: 'Search name or email' })} aria-label={t('admin:groups.users.search', { defaultValue: 'Search name or email' })} className="pl-9" />
                      </div>
                      <Button type="submit" variant="secondary" loading={groupUsersLoading} className="w-full sm:w-auto">
                        {t('common:actions.search', { defaultValue: 'Search' })}
                      </Button>
                    </form>
                    {groupUsersLoaded ? (
                      <p className="mt-3 text-[12px] text-[var(--color-fg-subtle)]">
                        {t('admin:groups.users.total', { count: groupUsersTotal, defaultValue: '{{count}} users' })}
                      </p>
                    ) : null}
                    <div className="mt-2 overflow-hidden rounded-[8px] border border-[var(--color-border)]">
                      {groupUsersLoading || (!groupUsersLoaded && !groupUsersLoadFailed) ? (
                        <div className="space-y-1 p-2">
                          {[0, 1, 2, 3].map((item) => <div key={item} className="h-12 animate-pulse rounded-[6px] bg-[var(--color-bg-muted)]" />)}
                        </div>
                      ) : groupUsersLoadFailed ? (
                        <div role="alert" className="flex min-h-40 flex-col items-center justify-center gap-3 px-4 py-8 text-center">
                          <p className="text-sm text-[var(--color-fg-muted)]">
                            {t('admin:groups.users.loadFailed', { defaultValue: 'Could not load users in this group.' })}
                          </p>
                          <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => setGroupUsersLoadAttempt((attempt) => attempt + 1)}
                          >
                            {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
                          </Button>
                        </div>
                      ) : groupUsers.length === 0 ? (
                        <p className="px-4 py-12 text-center text-sm text-[var(--color-fg-muted)]">
                          {t('admin:groups.users.empty', { defaultValue: 'No users found.' })}
                        </p>
                      ) : (
                        <ul className="divide-y divide-[var(--color-divider)]">
                          {groupUsers.map((groupUser) => (
                            <li key={groupUser.id} className="flex min-h-14 items-center gap-3 px-3 py-2.5">
                              <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-[12px] font-medium text-[var(--color-fg-muted)]">
                                {(groupUser.name || groupUser.email).slice(0, 1).toLocaleUpperCase()}
                              </span>
                              <span className="min-w-0 flex-1">
                                <span className="block truncate text-[13px] font-medium text-[var(--color-fg)]">{groupUser.name || groupUser.email}</span>
                                <span className="block truncate text-[11.5px] text-[var(--color-fg-subtle)]">{groupUser.email}</span>
                              </span>
                              {groupUser.role === 'admin' ? <Badge size="xs" variant="neutral">{t('admin:users.admin', { defaultValue: 'Admin' })}</Badge> : null}
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>
                    {groupUsersLoaded && groupUsersTotal > GROUP_USERS_PAGE_SIZE ? (
                      <Pagination page={groupUsersPage} pageCount={Math.max(1, Math.ceil(groupUsersTotal / GROUP_USERS_PAGE_SIZE))} onPage={setGroupUsersPage} />
                    ) : null}
                  </div>
                )}
              </TabsContent>
            </Tabs>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" disabled={saving} onClick={closeEditor}>
              {t('common:actions.cancel')}
            </Button>
            <Button loading={saving} onClick={() => void submit()}>{t('common:actions.save')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && !deletingRef.current && setConfirmDelete(null)}>
        <DialogContent size="sm" closeDisabled={deleting}>
          <DialogHeader>
            <DialogTitle>{t('admin:groups.removeTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete ? t('admin:groups.removeBody', { name: confirmDelete.name }) : ''}
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
