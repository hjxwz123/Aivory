/**
 * AdminWorkspaces (§workspaces 管理端) — list every workspace (owner, member
 * count, created), drill into one (members / conversations / projects / KBs),
 * and delete a workspace with all its content.
 */
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Briefcase, ChevronLeft, ChevronRight, Trash2, Users } from 'lucide-react'
import { workspacesApi } from '@/api'
import type { ApiConversation, ApiKnowledgeBase, ApiProject, ApiWorkspace, ApiWorkspaceMember } from '@/api/types'
import { toast } from '@/hooks/use-toast'
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

function fmtDate(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString()
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
              owner: workspace.owner_name || workspace.owner_id,
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
            <Row key={k.id} main={k.name} sub={k.description} />
          ))}
        </Panel>
      </div>

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

function Row({ main, sub }: { main: string; sub?: string }) {
  return (
    <li className="rounded-[8px] px-2 py-1.5 hover:bg-[var(--color-bg)]">
      <div className="truncate text-[13px] text-[var(--color-fg)]">{main}</div>
      {sub ? <div className="truncate text-[11px] text-[var(--color-fg-subtle)]">{sub}</div> : null}
    </li>
  )
}
