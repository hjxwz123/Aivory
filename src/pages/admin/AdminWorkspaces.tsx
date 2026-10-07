/**
 * AdminWorkspaces (§workspaces 管理端) — list every workspace (owner, member
 * count, created), drill into one (members / conversations / projects / KBs),
 * and delete a workspace with all its content.
 */
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Briefcase, ChevronLeft, ChevronRight, CircleAlert, FileText, Trash2, Users, X } from 'lucide-react'
import { adminApi, ApiError, workspacesApi } from '@/api'
import type {
  ApiAdminKnowledgeBaseResourceDetail,
  ApiConversation,
  ApiDocument,
  ApiKnowledgeBase,
  ApiProject,
  ApiWorkspace,
  ApiWorkspaceMember,
} from '@/api/types'
import { toast } from '@/hooks/use-toast'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { PanelFallback } from '@/components/ui/panel-fallback'
import {
  Sheet,
  SheetBody,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { WorkspaceAdminControls } from './workspace-admin-controls'
import { AdminPageHeader } from '@/components/admin/admin-page-header'
import { AdminTable } from '@/components/admin/AdminTable'
import { AdminListToolbar } from '@/components/admin/admin-list-toolbar'
import { matchesAdminSearch } from '@/lib/admin-list-filter'

function fmtDate(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString()
}

function formatBytes(value: number): string {
  if (value >= 1024 * 1024 * 1024) return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GB`
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${value || 0} B`
}

function workspaceOwnerName(workspace: ApiWorkspace, members: ApiWorkspaceMember[] = []): string {
  return workspace.owner_name || members.find((member) => member.is_owner || member.user_id === workspace.owner_id)?.name || workspace.owner_id
}

export default function AdminWorkspaces() {
  const { t } = useTranslation('admin')
  const [rows, setRows] = useState<ApiWorkspace[]>([])
  const [search, setSearch] = useState('')
  const filteredRows = rows.filter((row) => matchesAdminSearch(search, [row.name, row.id, row.owner_name, row.owner_id, row.description]))
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const deletingRef = useRef(false)
  const [deleting, setDeleting] = useState(false)

  async function load() {
    setLoading(true)
    try {
      const { workspaces } = await workspacesApi.adminList()
      setRows(workspaces)
    } catch {
      toast.error(t('workspaces.loadFailed', { defaultValue: 'Could not load workspaces.' }))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function remove(id: string) {
    if (deletingRef.current) return
    deletingRef.current = true
    setDeleting(true)
    try {
      await workspacesApi.adminRemove(id)
      setRows((r) => r.filter((w) => w.id !== id))
      setSelected(null)
      toast.success(t('workspaces.deleted', { defaultValue: 'Workspace deleted.' }))
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t('workspaces.deleteFailed', { defaultValue: 'Could not delete the workspace.' }))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  if (selected) {
    return <WorkspaceDetail id={selected} onBack={() => { setSelected(null); void load() }} onDelete={(id) => setConfirmDelete(id)} confirm={confirmDelete} onConfirmChange={setConfirmDelete} doDelete={remove} deleting={deleting} />
  }

  return (
    <section>
      <AdminPageHeader
        title={t('workspaces.title', { defaultValue: 'Workspaces' })}
        description={t('workspaces.subtitle', { defaultValue: 'Every collaborative space, its owner and member count.' })}
      />
      <AdminListToolbar search={search} onSearchChange={setSearch} placeholder={t('listToolbar.search.workspaces')} actions={<WorkspaceAdminControls onSaved={() => void load()} />} />
      {loading ? (
        <PanelFallback />
      ) : rows.length === 0 ? (
        <div className="mt-10">
          <EmptyState
            icon={<Briefcase size={22} aria-hidden />}
            title={t('workspaces.emptyTitle', { defaultValue: 'No workspaces yet' })}
            description={t('workspaces.emptyBody', { defaultValue: 'Users create workspaces from the sidebar avatar menu.' })}
          />
        </div>
      ) : (
        <>
        <AdminTable
          className="mt-4"
          items={filteredRows}
          rowKey={(workspace) => workspace.id}
          label={t('workspaces.title')}
          columns={[
            { id: 'name', header: t('workspaces.colName'), width: 240, render: (workspace) => <button type="button" className="admin-table-link font-medium" onClick={() => setSelected(workspace.id)}>{workspace.name}</button> },
            { id: 'owner', header: t('workspaces.colOwner'), width: 200, render: (workspace) => <span className="block truncate">{workspace.owner_name || workspace.owner_id}</span> },
            { id: 'members', header: t('workspaces.colMembers'), width: 100, align: 'right', render: (workspace) => <span className="tabular-nums">{workspace.member_count ?? 0}</span> },
            { id: 'created', header: t('workspaces.colCreated'), width: 160, render: (workspace) => fmtDate(workspace.created_at) },
            { id: 'actions', header: t('common.actions'), width: 60, align: 'right', render: (workspace) => <Button size="icon-sm" variant="ghost" title={t('workspaces.view')} aria-label={t('workspaces.view')} onClick={() => setSelected(workspace.id)}><ChevronRight size={14} aria-hidden /></Button> },
          ]}
        />
        </>
      )}
    </section>
  )
}

function WorkspaceDetail({
  id,
  onBack,
  onDelete,
  confirm,
  onConfirmChange,
  doDelete,
  deleting,
}: {
  id: string
  onBack: () => void
  onDelete: (id: string) => void
  confirm: string | null
  onConfirmChange: (v: string | null) => void
  doDelete: (id: string) => Promise<void>
  deleting: boolean
}) {
  const { t } = useTranslation('admin')
  const [data, setData] = useState<{
    workspace: ApiWorkspace
    members: ApiWorkspaceMember[]
    conversations: ApiConversation[]
    projects: ApiProject[]
    kbs: ApiKnowledgeBase[]
  } | null>(null)
  const [knowledgeBaseDetail, setKnowledgeBaseDetail] = useState<{
    summary: ApiKnowledgeBase
    item: ApiAdminKnowledgeBaseResourceDetail | null
    documents: ApiDocument[]
    loading: boolean
    error: string
  } | null>(null)
  const knowledgeBaseRequestRef = useRef(0)

  useEffect(() => {
    workspacesApi
      .adminDetail(id)
      .then(setData)
      .catch(() => toast.error(t('workspaces.loadFailed', { defaultValue: 'Could not load workspaces.' })))
  }, [id, t])

  if (!data) {
    return <PanelFallback />
  }
  const { workspace, members, conversations, projects, kbs } = data
  const ownerName = workspaceOwnerName(workspace, members)

  function closeKnowledgeBaseDetail() {
    knowledgeBaseRequestRef.current += 1
    setKnowledgeBaseDetail(null)
  }

  async function openKnowledgeBase(summary: ApiKnowledgeBase) {
    const request = ++knowledgeBaseRequestRef.current
    setKnowledgeBaseDetail({ summary, item: null, documents: [], loading: true, error: '' })
    try {
      const [{ item }, documents] = await Promise.all([
        adminApi.adminKnowledgeBase(summary.id),
        adminApi.kbDocuments(summary.id),
      ])
      if (request !== knowledgeBaseRequestRef.current) return
      setKnowledgeBaseDetail({ summary, item, documents, loading: false, error: '' })
    } catch (error) {
      if (request !== knowledgeBaseRequestRef.current) return
      setKnowledgeBaseDetail((current) => current && {
        ...current,
        loading: false,
        error: error instanceof ApiError ? error.message : t('resources.detailLoadFailed', { defaultValue: 'Could not load resource details.' }),
      })
    }
  }

  return (
    <section>
      <button
        type="button"
        onClick={onBack}
        className="inline-flex items-center gap-1 text-[13px] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[6px]"
      >
        <ChevronLeft size={14} aria-hidden />
        {t('workspaces.back', { defaultValue: 'All workspaces' })}
      </button>
      <AdminPageHeader
        className="mt-3"
        title={(
          <span className="flex min-w-0 items-center gap-2">
            <Briefcase size={18} aria-hidden className="shrink-0 text-[var(--color-fg-muted)]" />
            <span className="min-w-0 break-words">{workspace.name}</span>
          </span>
        )}
        showDescription
        description={(
          <span className="flex items-center gap-1.5">
            <Users size={13} aria-hidden />
            {t('workspaces.detailMeta', {
              owner: ownerName,
              count: members.length,
              defaultValue: 'Owner {{owner}} · {{count}} members',
            })}
          </span>
        )}
        actions={(
          <Button size="sm" variant="destructive" onClick={() => onDelete(id)} className="max-sm:min-h-[var(--tap-min)]">
            <Trash2 size={13} aria-hidden />
            {t('workspaces.delete', { defaultValue: 'Delete workspace' })}
          </Button>
        )}
      />

      <div className="mt-4"><WorkspaceAdminControls workspaceId={id} ownerId={workspace.owner_id} members={members} onSaved={() => {
        workspacesApi.adminDetail(id).then(setData).catch((e) => toast.error(e instanceof Error ? e.message : t('domains.loadFailed')))
      }} /></div>
      <div className="mt-5 grid gap-3 sm:mt-6 sm:gap-6 lg:grid-cols-2">
        <Panel title={t('workspaces.members', { defaultValue: 'Members' })}>
          <AdminTable
            className="max-h-72 overflow-y-auto"
            items={members}
            rowKey={(member) => member.user_id}
            label={t('workspaces.members')}
            columns={[
              { id: 'name', header: t('users.fields.name'), width: 180, render: (member) => <span className="block truncate">{member.name || member.email}</span> },
              { id: 'email', header: t('users.fields.email'), width: 220, render: (member) => <span className="block truncate">{member.email}</span> },
              { id: 'role', header: t('users.fields.role'), width: 100, render: (member) => <Badge size="xs">{t(member.is_owner ? 'workspaces.roleOwner' : member.role === 'admin' ? 'workspaces.roleAdmin' : member.role === 'guest' ? 'workspaces.roleGuest' : 'workspaces.roleMember')}</Badge> },
            ]}
          />
        </Panel>
        <Panel title={`${t('workspaces.conversations', { defaultValue: 'Conversations' })} · ${conversations.length}`}>
          <AdminTable
            className="max-h-72 overflow-y-auto"
            items={conversations.slice(0, 100)}
            rowKey={(conversation) => conversation.id}
            label={t('workspaces.conversations')}
            columns={[
              { id: 'title', header: t('workspaces.conversations'), width: 260, render: (conversation) => <Link to={`/admin/users/${encodeURIComponent(conversation.user_id)}/conversations/${encodeURIComponent(conversation.id)}`} className="admin-table-link">{conversation.title || t('resources.details.untitled')}</Link> },
              { id: 'user', header: t('users.fields.name'), width: 180, render: (conversation) => <span className="block truncate">{conversation.creator_name || '-'}</span> },
            ]}
          />
        </Panel>
        <Panel title={`${t('workspaces.projects', { defaultValue: 'Projects' })} · ${projects.length}`}>
          <AdminTable
            className="max-h-72 overflow-y-auto"
            items={projects}
            rowKey={(project) => project.id}
            label={t('workspaces.projects')}
            columns={[
              { id: 'name', header: t('resources.table.name'), width: 200, render: (project) => <span className="block truncate" title={project.name}>{project.name}</span> },
              { id: 'description', header: t('prompts.fields.description'), width: 260, render: (project) => <span className="line-clamp-2" title={project.description}>{project.description || '-'}</span> },
            ]}
          />
        </Panel>
        <Panel title={`${t('workspaces.kbs', { defaultValue: 'Knowledge bases' })} · ${kbs.length}`}>
          <AdminTable
            className="max-h-72 overflow-y-auto"
            items={kbs}
            rowKey={(knowledgeBase) => knowledgeBase.id}
            label={t('workspaces.kbs')}
            columns={[
              { id: 'name', header: t('resources.table.name'), width: 200, render: (knowledgeBase) => <button type="button" className="admin-table-link" onClick={() => void openKnowledgeBase(knowledgeBase)}>{knowledgeBase.name}</button> },
              { id: 'description', header: t('prompts.fields.description'), width: 240, render: (knowledgeBase) => <span className="line-clamp-2" title={knowledgeBase.description}>{knowledgeBase.description || '-'}</span> },
              { id: 'actions', header: t('common.actions'), width: 60, align: 'right', render: (knowledgeBase) => <Button variant="ghost" size="icon-sm" title={t('common.details')} aria-label={t('common.details')} onClick={() => void openKnowledgeBase(knowledgeBase)}><ChevronRight size={14} aria-hidden /></Button> },
            ]}
          />
        </Panel>
      </div>

      <Sheet open={knowledgeBaseDetail !== null} onOpenChange={(open) => !open && closeKnowledgeBaseDetail()}>
        <SheetContent side="right" size="lg" label={t('resources.tabs.knowledgeBases')} className="w-[min(100vw,40rem)]">
          <SheetHeader className="relative pr-14">
            <SheetTitle className="break-words">{knowledgeBaseDetail?.item?.name || knowledgeBaseDetail?.summary.name}</SheetTitle>
            <SheetDescription>{t('resources.tabs.knowledgeBases')}</SheetDescription>
            <SheetClose asChild>
              <Button variant="ghost" size="icon" aria-label={t('common.close', { ns: 'common', defaultValue: 'Close' })} className="absolute right-4 top-4">
                <X size={17} aria-hidden />
              </Button>
            </SheetClose>
          </SheetHeader>
          <SheetBody className="py-5">
            {knowledgeBaseDetail?.loading ? (
              <KnowledgeBaseDetailSkeleton />
            ) : knowledgeBaseDetail?.error ? (
              <KnowledgeBaseDetailError
                message={knowledgeBaseDetail.error}
                retryLabel={t('resources.retry', { defaultValue: 'Retry' })}
                onRetry={() => void openKnowledgeBase(knowledgeBaseDetail.summary)}
              />
            ) : knowledgeBaseDetail?.item ? (
              <WorkspaceKnowledgeBaseDetails item={knowledgeBaseDetail.item} documents={knowledgeBaseDetail.documents} t={t} />
            ) : null}
          </SheetBody>
        </SheetContent>
      </Sheet>

      <Dialog open={confirm === id} onOpenChange={(v) => onConfirmChange(v ? id : null)}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('workspaces.deleteTitle', { defaultValue: 'Delete this workspace?' })}</DialogTitle>
            <DialogDescription>
              {t('workspaces.deleteBody', {
                defaultValue: 'Every conversation, project and knowledge base inside is removed and all members lose access. This cannot be undone.',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" disabled={deleting} onClick={() => onConfirmChange(null)}>
              {t('common.cancel', { ns: 'common', defaultValue: 'Cancel' })}
            </Button>
            <Button variant="destructive" loading={deleting} onClick={() => void doDelete(id)}>
              {t('workspaces.delete', { defaultValue: 'Delete workspace' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="min-w-0">
      <h2 className="mb-2 text-[13px] font-medium text-[var(--color-fg-muted)]">{title}</h2>
      {children}
    </section>
  )
}

function WorkspaceKnowledgeBaseDetails({
  item,
  documents,
  t,
}: {
  item: ApiAdminKnowledgeBaseResourceDetail
  documents: ApiDocument[]
  t: (key: string, options?: Record<string, unknown>) => string
}) {
  return (
    <div className="space-y-7">
      <KnowledgeBaseSection title={t('resources.details.basic')}>
        <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-[var(--color-fg)]">
          {item.description || t('resources.noDescription')}
        </p>
        <KnowledgeBaseMetaList>
          <KnowledgeBaseMetaRow label={t('resources.details.resourceId')} value={item.id} mono />
          <KnowledgeBaseMetaRow label={t('resources.details.created')} value={fmtDate(item.created_at)} />
          <KnowledgeBaseMetaRow label={t('resources.details.lastActivity')} value={fmtDate(item.last_activity_at)} />
        </KnowledgeBaseMetaList>
      </KnowledgeBaseSection>

      <KnowledgeBaseSection title={t('resources.details.owner')}>
        <KnowledgeBaseMetaList>
          <KnowledgeBaseMetaRow label={t('resources.details.user')} value={[item.creator_name, item.creator_email, item.creator_id].filter(Boolean).join(' / ')} />
          <KnowledgeBaseMetaRow label={t('resources.details.workspace')} value={item.workspace_name || t('resources.details.personal')} />
        </KnowledgeBaseMetaList>
      </KnowledgeBaseSection>

      <KnowledgeBaseSection title={t('resources.details.index')}>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <KnowledgeBaseStat label={t('resources.details.documents')} value={item.document_count} />
          <KnowledgeBaseStat label={t('resources.details.ready')} value={item.ready_document_count} tone="success" />
          <KnowledgeBaseStat label={t('resources.details.processing')} value={item.processing_document_count} tone="warning" />
          <KnowledgeBaseStat label={t('resources.details.failed')} value={item.failed_document_count} tone="danger" />
          <KnowledgeBaseStat label={t('resources.details.chunks')} value={item.chunk_count} />
          <KnowledgeBaseStat label={t('resources.details.size')} value={formatBytes(item.total_size_bytes)} />
          <KnowledgeBaseStat label={t('resources.details.dimension')} value={item.embedding_dim || '-'} />
        </div>
        <KnowledgeBaseMetaList>
          <KnowledgeBaseMetaRow label={t('resources.details.embeddingModel')} value={item.embedding_model_label || item.embedding_model_id || '-'} />
          <KnowledgeBaseMetaRow label={t('resources.details.modelStatus')} value={t(item.embedding_model_enabled ? 'resources.details.enabled' : 'resources.details.disabled')} />
        </KnowledgeBaseMetaList>
      </KnowledgeBaseSection>

      <KnowledgeBaseSection title={t('resources.details.documentList')} icon={<FileText size={14} aria-hidden />}>
        {documents.length ? (
          <AdminTable
            items={documents}
            rowKey={(document) => document.id}
            label={t('resources.details.documentList')}
            columns={[
              { id: 'filename', header: t('resources.details.filename'), width: 230, render: (document) => <><span className="block truncate" title={document.filename}>{document.filename}</span><span className="text-[12px] text-[var(--color-fg-muted)]">{document.mime_type}</span></> },
              { id: 'status', header: t('common.status'), width: 100, render: (document) => <KnowledgeBaseDocumentStatus status={document.status} t={t} /> },
              { id: 'size', header: t('resources.details.size'), width: 90, render: (document) => formatBytes(document.size_bytes) },
              { id: 'created', header: t('resources.details.created'), width: 160, render: (document) => fmtDate(document.created_at) },
            ]}
          />
        ) : (
          <p className="text-sm text-[var(--color-fg-muted)]">{t('resources.details.noDocuments')}</p>
        )}
      </KnowledgeBaseSection>
    </div>
  )
}

function KnowledgeBaseDocumentStatus({ status, t }: { status: ApiDocument['status']; t: (key: string) => string }) {
  const variant = status === 'ready' ? 'success' : status === 'failed' ? 'danger' : 'warning'
  return <Badge size="xs" variant={variant}>{t(`resources.documentStatus.${status}`)}</Badge>
}

function KnowledgeBaseSection({ title, icon, children }: { title: string; icon?: ReactNode; children: ReactNode }) {
  return (
    <section>
      <h3 className="flex items-center gap-1.5 text-xs font-medium uppercase text-[var(--color-fg-subtle)]">
        {icon}
        {title}
      </h3>
      <div className="mt-2.5">{children}</div>
    </section>
  )
}

function KnowledgeBaseMetaList({ children }: { children: ReactNode }) {
  return <dl className="mt-3 text-[12.5px]">{children}</dl>
}

function KnowledgeBaseMetaRow({ label, value, mono = false }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[8rem_minmax(0,1fr)] gap-3 py-2.5">
      <dt className="text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className={mono ? 'min-w-0 break-words text-right font-mono text-[12px] text-[var(--color-fg)]' : 'min-w-0 break-words text-right text-[var(--color-fg)]'}>{value}</dd>
    </div>
  )
}

function KnowledgeBaseStat({
  label,
  value,
  tone = 'neutral',
}: {
  label: string
  value: ReactNode
  tone?: 'neutral' | 'success' | 'warning' | 'danger'
}) {
  const color = tone === 'success'
    ? 'text-[var(--color-success)]'
    : tone === 'warning'
      ? 'text-[var(--color-warning)]'
      : tone === 'danger'
        ? 'text-[var(--color-danger)]'
        : 'text-[var(--color-fg)]'
  return (
    <div className="min-w-0 rounded-[8px] bg-[var(--color-surface-sunken)] px-3 py-2.5">
      <p className="truncate text-[12px] text-[var(--color-fg-subtle)]">{label}</p>
      <p className={`mt-1 truncate text-base font-medium tabular-nums ${color}`}>{value}</p>
    </div>
  )
}

function KnowledgeBaseDetailSkeleton() {
  return (
    <div className="space-y-7" role="status">
      <div className="space-y-3">
        <Skeleton shape="line" className="w-28" />
        <Skeleton className="h-24" />
      </div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-16" />)}
      </div>
      <div className="space-y-2">
        {Array.from({ length: 5 }, (_, index) => <Skeleton key={index} className="h-10" />)}
      </div>
    </div>
  )
}

function KnowledgeBaseDetailError({ message, retryLabel, onRetry }: { message: string; retryLabel: string; onRetry: () => void }) {
  return (
    <div className="flex min-h-64 flex-col items-center justify-center px-6 text-center" role="alert">
      <CircleAlert size={24} className="text-[var(--color-danger)]" aria-hidden />
      <p className="mt-3 max-w-md text-sm text-[var(--color-fg-muted)]">{message}</p>
      <Button variant="secondary" size="sm" className="mt-4" onClick={onRetry}>{retryLabel}</Button>
    </div>
  )
}
