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
    } catch {
      toast.error(t('workspaces.deleteFailed', { defaultValue: 'Could not delete the workspace.' }))
    } finally {
      deletingRef.current = false
      setDeleting(false)
    }
  }

  if (selected) {
    return <WorkspaceDetail id={selected} onBack={() => setSelected(null)} onDelete={(id) => setConfirmDelete(id)} confirm={confirmDelete} onConfirmChange={setConfirmDelete} doDelete={remove} deleting={deleting} />
  }

  return (
    <section>
      <h1 className="font-serif text-2xl text-[var(--color-fg)] sm:text-3xl">
        {t('workspaces.title', { defaultValue: 'Workspaces' })}
      </h1>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        {t('workspaces.subtitle', { defaultValue: 'Every collaborative space, its owner and member count.' })}
      </p>
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
        <div className="mt-6 hidden overflow-x-auto rounded-[12px] border border-[var(--color-border)] md:block">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-[var(--color-divider)] bg-[var(--color-bg-muted)] text-left text-[11px] uppercase tracking-wide text-[var(--color-fg-subtle)]">
                <th className="px-3 py-2 font-medium">{t('workspaces.colName', { defaultValue: 'Name' })}</th>
                <th className="px-3 py-2 font-medium">{t('workspaces.colOwner', { defaultValue: 'Owner' })}</th>
                <th className="px-3 py-2 font-medium">{t('workspaces.colMembers', { defaultValue: 'Members' })}</th>
                <th className="px-3 py-2 font-medium">{t('workspaces.colCreated', { defaultValue: 'Created' })}</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {rows.map((w) => (
                <tr key={w.id} className="border-b border-[var(--color-divider)] last:border-0 hover:bg-[var(--color-bg)]">
                  <td className="px-3 py-2.5">
                    <button
                      type="button"
                      onClick={() => setSelected(w.id)}
                      className="font-medium text-[var(--color-fg)] hover:text-[var(--color-accent)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[4px]"
                    >
                      {w.name}
                    </button>
                  </td>
                  <td className="px-3 py-2.5 text-[var(--color-fg-muted)]">{w.owner_name || w.owner_id}</td>
                  <td className="px-3 py-2.5 tabular-nums text-[var(--color-fg-muted)]">{w.member_count ?? 0}</td>
                  <td className="px-3 py-2.5 tabular-nums text-[var(--color-fg-subtle)]">{fmtDate(w.created_at)}</td>
                  <td className="px-3 py-2.5 text-right">
                    <Button size="sm" variant="ghost" onClick={() => setSelected(w.id)}>
                      {t('workspaces.view', { defaultValue: 'View' })}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <ul className="mt-5 divide-y divide-[var(--color-divider)] overflow-hidden rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] md:hidden">
          {rows.map((w) => (
            <li key={w.id}>
              <button
                type="button"
                onClick={() => setSelected(w.id)}
                aria-label={`${t('workspaces.view', { defaultValue: 'View' })}: ${w.name}`}
                className="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_2.5rem] items-center gap-2 px-3 py-3.5 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]"
              >
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium text-[var(--color-fg)]">{w.name}</span>
                  <span className="mt-0.5 block truncate text-[12px] text-[var(--color-fg-muted)]">{w.owner_name || w.owner_id}</span>
                  <span className="mt-2 flex items-center gap-3 text-[11px] text-[var(--color-fg-subtle)]">
                    <span className="inline-flex items-center gap-1 tabular-nums">
                      <Users size={12} aria-hidden />
                      {w.member_count ?? 0}
                    </span>
                    <span className="tabular-nums">{fmtDate(w.created_at)}</span>
                  </span>
                </span>
                <span className="inline-flex size-10 items-center justify-center text-[var(--color-fg-subtle)]">
                  <ChevronRight size={17} aria-hidden />
                </span>
              </button>
            </li>
          ))}
        </ul>
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
      <div className="mt-3 flex flex-col items-stretch gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h1 className="flex min-w-0 items-center gap-2 font-serif text-2xl text-[var(--color-fg)]">
            <Briefcase size={20} aria-hidden className="text-[var(--color-fg-muted)]" />
            <span className="min-w-0 break-words">{workspace.name}</span>
          </h1>
          <p className="mt-1 flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)]">
            <Users size={13} aria-hidden />
            {t('workspaces.detailMeta', {
              owner: ownerName,
              count: members.length,
              defaultValue: 'Owner {{owner}} · {{count}} members',
            })}
          </p>
        </div>
        <Button variant="destructive" onClick={() => onDelete(id)} className="w-full sm:w-auto">
          <Trash2 size={13} aria-hidden />
          {t('workspaces.delete', { defaultValue: 'Delete workspace' })}
        </Button>
      </div>

      <div className="mt-5 grid gap-3 sm:mt-6 sm:gap-6 lg:grid-cols-2">
        <Panel title={t('workspaces.members', { defaultValue: 'Members' })}>
          {members.map((m) => (
            <Row
              key={m.user_id}
              main={m.name || m.email}
              sub={
                m.is_owner
                  ? t('workspaces.roleOwner', { defaultValue: 'Owner' })
                  : m.role === 'admin'
                    ? t('workspaces.roleAdmin', { defaultValue: 'Admin' })
                    : m.role === 'guest'
                      ? t('workspaces.roleGuest', { defaultValue: 'Guest' })
                      : t('workspaces.roleMember', { defaultValue: 'Member' })
              }
            />
          ))}
        </Panel>
        <Panel title={`${t('workspaces.conversations', { defaultValue: 'Conversations' })} · ${conversations.length}`}>
          {conversations.slice(0, 100).map((c) => (
            <li key={c.id}>
              <Link
                to={`/admin/users/${encodeURIComponent(c.user_id)}/conversations/${encodeURIComponent(c.id)}`}
                className="block rounded-[8px] px-2 py-1.5 hover:bg-[var(--color-bg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <div className="truncate text-[13px] text-[var(--color-fg)] hover:text-[var(--color-accent)]">{c.title || '—'}</div>
                {c.creator_name ? (
                  <div className="truncate text-[11px] text-[var(--color-fg-subtle)]">{c.creator_name}</div>
                ) : null}
              </Link>
            </li>
          ))}
        </Panel>
        <Panel title={`${t('workspaces.projects', { defaultValue: 'Projects' })} · ${projects.length}`}>
          {projects.map((p) => (
            <Row key={p.id} main={p.name} sub={p.description} />
          ))}
        </Panel>
        <Panel title={`${t('workspaces.kbs', { defaultValue: 'Knowledge bases' })} · ${kbs.length}`}>
          {kbs.map((k) => (
            <Row key={k.id} main={k.name} sub={k.description} onClick={() => void openKnowledgeBase(k)} />
          ))}
        </Panel>
      </div>

      <Sheet open={knowledgeBaseDetail !== null} onOpenChange={(open) => !open && closeKnowledgeBaseDetail()}>
        <SheetContent side="right" size="lg" label={t('resources.tabs.knowledgeBases')} className="w-[min(100vw,40rem)]">
          <SheetHeader className="relative border-b border-[var(--color-divider)] pr-14">
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
    <div className="min-w-0 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] p-3 sm:rounded-[12px] sm:p-4">
      <h2 className="text-[12px] font-medium text-[var(--color-fg-subtle)]">{title}</h2>
      <ul className="mt-2 max-h-72 space-y-1 overflow-y-auto scrollbar-thin">{children}</ul>
    </div>
  )
}

function Row({ main, sub, onClick }: { main: string; sub?: string; onClick?: () => void }) {
  const content = (
    <>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] text-[var(--color-fg)]">{main}</span>
        {sub ? <span className="block truncate text-[11px] text-[var(--color-fg-subtle)]">{sub}</span> : null}
      </span>
      {onClick ? <ChevronRight size={14} className="shrink-0 text-[var(--color-fg-faint)]" aria-hidden /> : null}
    </>
  )

  return (
    <li>
      {onClick ? (
        <button
          type="button"
          onClick={onClick}
          className="group flex w-full min-w-0 items-center gap-2 rounded-[8px] px-2 py-1.5 text-left interactive hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          {content}
        </button>
      ) : (
        <div className="flex min-w-0 items-center gap-2 rounded-[8px] px-2 py-1.5 hover:bg-[var(--color-bg)]">{content}</div>
      )}
    </li>
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
          <ul className="divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)]">
            {documents.map((document) => (
              <li key={document.id} className="flex min-w-0 items-center justify-between gap-3 py-3">
                <span className="min-w-0">
                  <span className="block truncate text-sm text-[var(--color-fg)]">{document.filename}</span>
                  <span className="mt-0.5 block truncate text-[11px] text-[var(--color-fg-subtle)]">
                    {[document.mime_type, formatBytes(document.size_bytes), fmtDate(document.created_at)].filter(Boolean).join(' · ')}
                  </span>
                </span>
                <KnowledgeBaseDocumentStatus status={document.status} t={t} />
              </li>
            ))}
          </ul>
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
  return <dl className="mt-3 divide-y divide-[var(--color-divider)] border-y border-[var(--color-divider)] text-[12.5px]">{children}</dl>
}

function KnowledgeBaseMetaRow({ label, value, mono = false }: { label: string; value: ReactNode; mono?: boolean }) {
  return (
    <div className="grid grid-cols-[8rem_minmax(0,1fr)] gap-3 py-2.5">
      <dt className="text-[var(--color-fg-subtle)]">{label}</dt>
      <dd className={mono ? 'min-w-0 break-words text-right font-mono text-[11px] text-[var(--color-fg)]' : 'min-w-0 break-words text-right text-[var(--color-fg)]'}>{value}</dd>
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
    <div className="min-w-0 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-sunken)] px-3 py-2.5">
      <p className="truncate text-[11px] text-[var(--color-fg-subtle)]">{label}</p>
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
