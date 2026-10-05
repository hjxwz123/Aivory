/**
 * KnowledgeBasesList — gallery of the user's knowledge bases.
 */
import { activeWorkspaceId, useWorkspaces } from '@/store/workspaces'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Database, Loader2, MoreHorizontal, Plus, Search, Trash2 } from 'lucide-react'
import { ApiError, kbsApi } from '@/api'
import type { ApiKnowledgeBase } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Field } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { ContentHeader } from '@/components/layout/content-header'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
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
import { cn, formatRelativeDate } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { useAuth } from '@/store/auth'
import { userCan } from '@/lib/user-permissions'
import { workspaceCapabilitiesForScope } from '@/lib/workspace-permissions'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { knowledgeBaseErrorText, knowledgeBaseOperationErrorText } from '@/lib/knowledge-base-errors'

export default function KnowledgeBasesList() {
  const { t } = useTranslation(['kb', 'common'])
  const user = useAuth((s) => s.user)
  // §workspaces: KBs aren't part of reloadSpaceData(), so this page re-fetches
  // itself when the active space changes (after the switch settles).
  const activeWsId = useWorkspaces((s) => s.activeId)
  const wsSwitching = useWorkspaces((s) => s.switching)
  const workspacesLoaded = useWorkspaces((s) => s.loaded)
  const activeWorkspace = useWorkspaces((s) =>
    s.activeId ? s.workspaces.find((workspace) => workspace.id === s.activeId) : undefined,
  )
  const activeWorkspacePolicy = useWorkspaces((s) =>
    s.activeId ? s.policies[s.activeId] : undefined,
  )
  const workspacePolicyLoading = useWorkspaces((s) =>
    s.activeId ? s.policyLoading[s.activeId] === true : false,
  )
  const workspacePolicyError = useWorkspaces((s) =>
    s.activeId ? s.policyErrors[s.activeId] : null,
  )
  const workspaceCaps = workspaceCapabilitiesForScope(activeWsId, activeWorkspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching: wsSwitching,
    policyError: workspacePolicyError,
  })
  const workspacePolicyPending = Boolean(
    activeWsId && !activeWorkspacePolicy && (!workspacesLoaded || workspacePolicyLoading || wsSwitching),
  )
  const canUseWorkspaceKnowledgeBases = workspaceCaps.knowledgeBases
  const canUseKnowledgeBases = userCan(user, 'allow_knowledge_bases') && canUseWorkspaceKnowledgeBases
  const canCreateKnowledgeBase = canUseKnowledgeBases &&
    (!activeWsId || activeWorkspace?.can_create_kb === true)
  const [rows, setRows] = useState<ApiKnowledgeBase[]>([])
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState({ name: '', description: '' })
  const [creating, setCreating] = useState(false)
  const creatingRef = useRef(false)
  // Delete-KB confirmation (removes the KB + its documents/vectors, and
  // auto-unbinds it from any conversation that referenced it — server-side).
  const [toDelete, setToDelete] = useState<ApiKnowledgeBase | null>(null)
  const [deleting, setDeleting] = useState(false)
  // Stale-response guard for the space-switch reloads: a slow earlier space's
  // response must never overwrite the current space's rows (same epoch pattern
  // as the conversations/projects stores).
  const loadEpochRef = useRef(0)

  const load = useCallback(async () => {
    const epoch = ++loadEpochRef.current
    setLoading(true)
    setLoadError('')
    try {
      const kb = await kbsApi.list(activeWorkspaceId())
      if (epoch !== loadEpochRef.current) return // superseded by a space switch
      setRows(kb)
    } catch (e) {
      if (epoch !== loadEpochRef.current) return
      const message = knowledgeBaseErrorText(t, e, t('common:common.error'))
      setLoadError(message)
      toast.error(message)
    } finally {
      if (epoch === loadEpochRef.current) setLoading(false)
    }
  }, [t])

  useEffect(() => {
    if (workspacePolicyPending) {
      loadEpochRef.current += 1
      setRows([])
      setOpen(false)
      setToDelete(null)
      setLoadError('')
      setLoading(true)
      return
    }
    if (!canUseKnowledgeBases) {
      loadEpochRef.current += 1
      setRows([])
      setOpen(false)
      setToDelete(null)
      setLoadError(canUseWorkspaceKnowledgeBases ? 'knowledge_base_group_permission_required' : 'workspace_knowledge_base_disabled')
      setLoading(false)
      return
    }
    if (wsSwitching) return
    void load()
  }, [activeWsId, canUseKnowledgeBases, canUseWorkspaceKnowledgeBases, load, workspacePolicyPending, wsSwitching])

  useEffect(
    () =>
      subscribeAccessInvalidation((event) => {
        if (event.kind !== 'account' && event.kind !== 'workspace' && event.kind !== 'knowledge-base') return
        if (workspacePolicyPending) return
        if (!canUseKnowledgeBases) {
          loadEpochRef.current += 1
          setRows([])
          setOpen(false)
          setToDelete(null)
          setLoadError(
            canUseWorkspaceKnowledgeBases
              ? 'knowledge_base_group_permission_required'
              : 'workspace_knowledge_base_disabled',
          )
          setLoading(false)
          return
        }
        void load()
      }),
    [canUseKnowledgeBases, canUseWorkspaceKnowledgeBases, load, workspacePolicyPending],
  )

  useEffect(() => {
    if (open && (!canUseKnowledgeBases || !canCreateKnowledgeBase)) setOpen(false)
  }, [canCreateKnowledgeBase, canUseKnowledgeBases, open, workspacePolicyPending])

  async function doDelete() {
    if (!toDelete || !canUseKnowledgeBases) return
    setDeleting(true)
    try {
      await kbsApi.remove(toDelete.id)
      toast.success(t('kb:deleted', { defaultValue: 'Knowledge base deleted' }))
      setToDelete(null)
      await load()
    } catch (e) {
      toast.error(knowledgeBaseOperationErrorText(t, e, t('common:common.error')))
      if (e instanceof ApiError && (e.status === 403 || e.status === 404)) {
        setToDelete(null)
        await load()
      }
    } finally {
      setDeleting(false)
    }
  }

  async function create() {
    if (creatingRef.current) return
    if (!canUseKnowledgeBases || !canCreateKnowledgeBase) {
      setOpen(false)
      toast.error(
        !canUseKnowledgeBases
          ? canUseWorkspaceKnowledgeBases
            ? t('kb:groupPermissionRequired')
            : t('kb:workspaceDisabledBody', { defaultValue: 'The workspace administrator has disabled knowledge bases.' })
          : t('kb:workspaceCreatePermissionRequired'),
      )
      return
    }
    if (!draft.name.trim()) {
      toast.error(t('kb:dialog.nameRequired'))
      return
    }
    creatingRef.current = true
    setCreating(true)
    try {
      await kbsApi.create({ ...draft, workspace_id: activeWorkspaceId() })
      toast.success(t('kb:dialog.created'))
      setOpen(false)
      setDraft({ name: '', description: '' })
      await load()
    } catch (e) {
      toast.error(knowledgeBaseErrorText(t, e, t('common:common.error')))
      if (e instanceof ApiError && e.status === 403) setOpen(false)
    } finally {
      creatingRef.current = false
      setCreating(false)
    }
  }

  const visibleRows = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return rows
    return rows.filter((kb) =>
      kb.name.toLowerCase().includes(q) || (kb.description ?? '').toLowerCase().includes(q),
    )
  }, [query, rows])

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader
        title={t('kb:title')}
        actions={
          canUseKnowledgeBases && canCreateKnowledgeBase ? (
            <Button
              variant="secondary"
              size="sm"
              leadingIcon={<Plus size={15} aria-hidden />}
              onClick={() => setOpen(true)}
            >
              {t('kb:new')}
            </Button>
          ) : null
        }
      />
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 pb-24 pt-5 sm:px-8 sm:pt-6">
          <section>
            {workspacePolicyPending ? (
              <KnowledgeBasesSkeleton label={t('common:common.loading')} />
            ) : !canUseKnowledgeBases ? (
              <EmptyState
                icon={<Database size={20} aria-hidden />}
                title={t('kb:workspaceDisabledTitle', { defaultValue: 'Knowledge bases are unavailable in this workspace.' })}
                description={
                  canUseWorkspaceKnowledgeBases
                    ? t('kb:groupPermissionRequired', { defaultValue: 'Your user group does not have knowledge-base access.' })
                    : t('kb:workspaceDisabledBody', { defaultValue: 'The workspace administrator has disabled knowledge bases.' })
                }
              />
            ) : loading ? (
              <KnowledgeBasesSkeleton label={t('common:common.loading')} />
            ) : loadError ? (
              <EmptyState
                icon={<Database size={20} aria-hidden />}
                title={t('common:common.error')}
                description={
                  loadError === 'knowledge_base_group_permission_required'
                    ? t('kb:groupPermissionRequired', { defaultValue: 'Your user group does not have knowledge-base access.' })
                    : loadError === 'workspace_knowledge_base_disabled'
                      ? t('kb:workspaceDisabledBody', { defaultValue: 'The workspace administrator has disabled knowledge bases.' })
                    : knowledgeBaseErrorText(t, loadError, loadError)
                }
                action={
                  canUseKnowledgeBases ? (
                    <Button variant="secondary" onClick={() => void load()}>
                      {t('common:actions.tryAgain', { defaultValue: 'Try again' })}
                    </Button>
                  ) : undefined
                }
              />
            ) : rows.length === 0 ? (
              <EmptyState
                className="mt-6"
                icon={<Database size={20} aria-hidden />}
                title={t('kb:emptyTitle')}
                description={t('kb:emptyBody')}
                action={
                  canCreateKnowledgeBase ? (
                    <Button variant="secondary" leadingIcon={<Plus size={15} aria-hidden />} onClick={() => setOpen(true)}>
                      {t('kb:createFirst')}
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <>
                {/* Same control strip as Projects: search left, count right,
                    one divider instead of a container. */}
                <div className="flex flex-col gap-2.5 border-b border-[var(--color-divider)] pb-3 sm:flex-row sm:items-center sm:justify-between">
                  <Input
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    leadingIcon={<Search size={14} aria-hidden />}
                    placeholder={t('kb:list.searchPlaceholder')}
                    aria-label={t('kb:list.searchPlaceholder')}
                    wrapperClassName="w-full sm:max-w-xs"
                  />
                  <span className="text-[12.5px] tabular-nums text-[var(--color-fg-subtle)]">
                    {t('kb:list.count', { count: rows.length })}
                  </span>
                </div>
                {visibleRows.length === 0 ? (
                  <EmptyState
                    className="mt-6"
                    icon={<Search size={20} aria-hidden />}
                    title={t('kb:list.noMatchesTitle')}
                    description={t('kb:list.noMatchesBody')}
                  />
                ) : (
                  <ul className="mt-1 flex flex-col divide-y divide-[var(--color-divider)]">
                    {visibleRows.map((kb) => (
                      <KnowledgeBaseRow key={kb.id} kb={kb} onDelete={() => setToDelete(kb)} />
                    ))}
                  </ul>
                )}
              </>
            )}
          </section>
        </div>
      </div>

      <Dialog open={open && canUseKnowledgeBases && canCreateKnowledgeBase} onOpenChange={(next) => !creatingRef.current && setOpen(next)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t('kb:dialog.title')}</DialogTitle>
            <DialogDescription>{t('kb:dialog.body')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <div className="grid gap-4">
              <Field label={t('kb:dialog.name')} htmlFor="kb-name">
                <Input
                  id="kb-name"
                  value={draft.name}
                  onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  placeholder={t('kb:dialog.namePlaceholder')}
                />
              </Field>
              <Field label={t('kb:dialog.description')} htmlFor="kb-desc">
                <Textarea
                  id="kb-desc"
                  rows={3}
                  value={draft.description}
                  onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                />
              </Field>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={creating}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => void create()} loading={creating}>
              {t('kb:dialog.create')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={toDelete !== null}
        onOpenChange={(next) => {
          if (!next && !deleting) setToDelete(null)
        }}
      >
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('kb:deleteTitle', { defaultValue: 'Delete knowledge base?' })}</DialogTitle>
            <DialogDescription>
              {t('kb:deleteBody', {
                name: toDelete?.name ?? '',
                defaultValue:
                  'This permanently deletes “{{name}}” and all its documents and index data. Conversations that reference it will be unlinked. This cannot be undone.',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setToDelete(null)} disabled={deleting}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void doDelete()}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/**
 * One library row: the same 64px two-column rhythm as ProjectRow, with
 * document totals and indexing health on the right so the overview answers
 * "what is in here and is it searchable" without opening each library.
 */
function KnowledgeBaseRow({ kb, onDelete }: { kb: ApiKnowledgeBase; onDelete: () => void }) {
  const { t } = useTranslation(['kb', 'common'])
  const stats = kb.stats
  const shared = kb.access_role === 'read' || kb.access_role === 'write'
  const updatedAt = (stats?.updated_at || kb.created_at) * 1000
  const subtitle = kb.description || (shared && kb.owner_name
    ? t('kb:access.sharedBy', { name: kb.owner_name, defaultValue: 'Shared by {{name}}' })
    : '')
  return (
    <li className="group/kb relative">
      <Link
        to={`/kb/${kb.id}`}
        className={cn(
          'grid min-h-16 grid-cols-[2rem_minmax(0,1fr)] items-center gap-x-3 gap-y-1 rounded-[8px] py-2.5 interactive',
          '-mx-2.5 px-2.5 sm:-mx-3 sm:grid-cols-[2rem_minmax(0,1fr)_auto] sm:px-3',
          kb.can_delete && 'pr-12 sm:pr-12',
          'hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        )}
      >
        <span
          className="row-span-2 inline-flex size-8 shrink-0 items-center justify-center self-start rounded-[8px] bg-[var(--color-accent-soft)] text-[var(--color-accent)] sm:row-span-1 sm:self-center"
          aria-hidden
        >
          <Database size={15} />
        </span>
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-1.5">
            <h3 title={kb.name} className="truncate text-[14.5px] font-medium leading-[18px] tracking-normal text-[var(--color-fg)]">
              {kb.name}
            </h3>
            {shared ? (
              <Badge size="xs" variant="neutral" className="shrink-0">
                {kb.access_role === 'write'
                  ? t('kb:access.write', { defaultValue: 'Can upload' })
                  : t('kb:access.read', { defaultValue: 'Read only' })}
              </Badge>
            ) : null}
          </div>
          {subtitle ? (
            <p className="mt-0.5 truncate text-[12.5px] leading-4 text-[var(--color-fg-muted)]">{subtitle}</p>
          ) : null}
        </div>
        <div className="col-start-2 flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-0.5 text-[12px] leading-4 tabular-nums text-[var(--color-fg-subtle)] sm:col-start-3 sm:row-start-1 sm:flex-nowrap sm:justify-end sm:pl-4">
          {stats ? (
            <span className="whitespace-nowrap">
              {stats.document_count > 0
                ? t('kb:stats.documents', { count: stats.document_count })
                : t('kb:stats.empty')}
            </span>
          ) : null}
          {stats && stats.processing_document_count > 0 ? (
            <span className="inline-flex items-center gap-1 whitespace-nowrap text-[var(--color-accent)]">
              <Loader2 size={11} className="animate-spin motion-reduce:animate-none" aria-hidden />
              {t('kb:stats.processing', { count: stats.processing_document_count })}
            </span>
          ) : null}
          {stats && stats.failed_document_count > 0 ? (
            <span className="inline-flex items-center gap-1 whitespace-nowrap text-[var(--color-danger)]">
              <AlertTriangle size={11} aria-hidden />
              {t('kb:stats.failed', { count: stats.failed_document_count })}
            </span>
          ) : null}
          <time className="whitespace-nowrap" dateTime={new Date(updatedAt).toISOString()}>
            {t('kb:stats.updated', { when: formatRelativeDate(updatedAt) })}
          </time>
        </div>
      </Link>
      {kb.can_delete ? (
        <div className="absolute right-0 top-1/2 -translate-y-1/2 sm:-right-1">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label={t('kb:detail.moreActions', { name: kb.name })}
                className="inline-flex size-[var(--tap-min)] items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:size-8 sm:opacity-0 sm:group-hover/kb:opacity-100 sm:group-focus-within/kb:opacity-100 sm:data-[state=open]:opacity-100"
              >
                <MoreHorizontal size={16} aria-hidden />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem destructive onSelect={onDelete}>
                <Trash2 size={13} aria-hidden /> {t('kb:deleteAction', { defaultValue: 'Delete knowledge base' })}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      ) : null}
    </li>
  )
}

/** Mirrors the control strip and row geometry so content lands without a jump. */
function KnowledgeBasesSkeleton({ label }: { label: string }) {
  return (
    <div role="status" aria-label={label}>
      <div className="flex items-center justify-between border-b border-[var(--color-divider)] pb-3">
        <Skeleton className="h-10 w-full max-w-xs" />
        <Skeleton shape="line" className="hidden w-24 sm:block" />
      </div>
      <div className="mt-1 divide-y divide-[var(--color-divider)]">
        {Array.from({ length: 4 }, (_, index) => (
          <div
            key={index}
            className="grid min-h-16 grid-cols-[2rem_minmax(0,1fr)] items-center gap-x-3 gap-y-1 py-2.5 sm:grid-cols-[2rem_minmax(0,1fr)_auto]"
          >
            <Skeleton className="row-span-2 size-8 rounded-[8px] sm:row-span-1" />
            <div className="min-w-0 space-y-1.5">
              <Skeleton shape="line" className="h-3.5 w-2/5" />
              <Skeleton shape="line" className="h-3 w-3/5" />
            </div>
            <div className="col-start-2 flex items-center gap-2 sm:col-start-3 sm:row-start-1 sm:pl-4">
              <Skeleton shape="line" className="h-3 w-16" />
              <Skeleton shape="line" className="h-3 w-24" />
            </div>
          </div>
        ))}
      </div>
      <span className="sr-only">{label}</span>
    </div>
  )
}
