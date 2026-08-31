/**
 * KnowledgeBasesList — gallery of the user's knowledge bases.
 */
import { activeWorkspaceId, useWorkspaces } from '@/store/workspaces'
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Plus, Database, MoreHorizontal, Trash2 } from 'lucide-react'
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
import { formatRelativeDate } from '@/lib/utils'
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
          <p className="max-w-[60ch] text-[13.5px] leading-relaxed text-[var(--color-fg-muted)]">
            {t('kb:lead')}
          </p>

          <section className="mt-6">
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
                icon={<Database size={20} aria-hidden />}
                title={t('kb:emptyTitle')}
                description={t('kb:emptyBody')}
                action={
                  canCreateKnowledgeBase ? (
                    <Button variant="secondary" onClick={() => setOpen(true)}>
                      {t('kb:createFirst')}
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <ul className="flex flex-col divide-y divide-[var(--color-divider)] border-b border-[var(--color-divider)]">
                {rows.map((kb) => (
                  <li key={kb.id} className="group/kb flex min-w-0 items-center gap-1 py-1">
                    <Link
                      to={`/kb/${kb.id}`}
                      className="flex min-h-14 min-w-0 flex-1 items-center gap-3 rounded-[10px] px-2 py-2 interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <span
                        className="grid size-8 shrink-0 place-items-center rounded-[8px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]"
                        aria-hidden
                      >
                        <Database size={15} />
                      </span>
                      <div className="min-w-0 flex-1">
                        <h3
                          title={kb.name}
                          className="truncate text-[14.5px] font-medium leading-snug tracking-normal text-[var(--color-fg)]"
                        >
                          {kb.name}
                        </h3>
                        {kb.description ? (
                          <p className="mt-0.5 truncate text-[12px] leading-snug text-[var(--color-fg-muted)]">
                            {kb.description}
                          </p>
                        ) : null}
                        {kb.access_role === 'read' || kb.access_role === 'write' ? (
                          <div className="mt-1 flex items-center gap-1.5">
                            <Badge size="xs" variant="neutral">
                              {kb.access_role === 'write'
                                ? t('kb:access.write', { defaultValue: 'Can upload' })
                                : t('kb:access.read', { defaultValue: 'Read only' })}
                            </Badge>
                            {kb.owner_name ? (
                              <span className="truncate text-[11px] text-[var(--color-fg-subtle)]">
                                {t('kb:access.sharedBy', { name: kb.owner_name, defaultValue: 'Shared by {{name}}' })}
                              </span>
                            ) : null}
                          </div>
                        ) : null}
                      </div>
                      <time
                        className="hidden shrink-0 text-[11px] tabular-nums text-[var(--color-fg-subtle)] sm:block"
                        dateTime={new Date(kb.created_at * 1000).toISOString()}
                      >
                        {t('kb:stats.created', {
                          when: formatRelativeDate(kb.created_at * 1000),
                        })}
                      </time>
                    </Link>
                    {kb.can_delete ? (
                    <div className="shrink-0">
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <button
                            type="button"
                            aria-label={`${t('common:actions.more', { defaultValue: 'More' })}: ${kb.name}`}
                            className="inline-flex size-[var(--tap-min)] items-center justify-center rounded-[8px] text-[var(--color-fg-subtle)] opacity-100 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:size-8 sm:opacity-0 sm:group-hover/kb:opacity-100 sm:data-[state=open]:opacity-100 sm:focus-visible:opacity-100"
                          >
                            <MoreHorizontal size={16} aria-hidden />
                          </button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem destructive onSelect={() => setToDelete(kb)}>
                            <Trash2 size={13} aria-hidden /> {t('common:actions.delete')}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                    ) : null}
                  </li>
                ))}
              </ul>
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

function KnowledgeBasesSkeleton({ label }: { label: string }) {
  return (
    <div className="divide-y divide-[var(--color-divider)]" role="status" aria-label={label}>
      {Array.from({ length: 4 }, (_, index) => (
        <div key={index} className="flex min-h-16 items-center gap-3 px-2 py-2">
          <Skeleton className="size-8 shrink-0 rounded-[8px]" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton shape="line" className="h-3.5 w-2/5" />
            <Skeleton shape="line" className="w-3/5" />
          </div>
          <Skeleton shape="line" className="hidden w-24 sm:block" />
          <Skeleton className="size-8 shrink-0 rounded-[8px]" />
        </div>
      ))}
      <span className="sr-only">{label}</span>
    </div>
  )
}
