/**
 * AdminWorkspaces (§workspaces 管理端) — list every workspace (owner, member
 * count, created), drill into one (members / conversations / projects / KBs),
 * and delete a workspace with all its content.
 */
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Briefcase, ChevronLeft, Trash2, Users } from 'lucide-react'
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

function fmtDate(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString()
}

export default function AdminWorkspaces() {
  const { t } = useTranslation('admin')
  const [rows, setRows] = useState<ApiWorkspace[]>([])
  const [loading, setLoading] = useState(true)
  const [selected, setSelected] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)

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
    try {
      await workspacesApi.adminRemove(id)
      setRows((r) => r.filter((w) => w.id !== id))
      setSelected(null)
      toast.success(t('workspaces.deleted', { defaultValue: 'Workspace deleted.' }))
    } catch {
      toast.error(t('workspaces.deleteFailed', { defaultValue: 'Could not delete the workspace.' }))
    }
  }

  if (selected) {
    return <WorkspaceDetail id={selected} onBack={() => setSelected(null)} onDelete={(id) => setConfirmDelete(id)} confirm={confirmDelete} onConfirmChange={setConfirmDelete} doDelete={remove} />
  }

  return (
    <section>
      <h1 className="font-serif text-2xl text-[var(--color-fg)]">
        {t('workspaces.title', { defaultValue: 'Workspaces' })}
      </h1>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
        {t('workspaces.subtitle', { defaultValue: 'Every collaborative space, its owner and member count.' })}
      </p>
      {loading ? (
        <p className="mt-8 text-sm text-[var(--color-fg-subtle)]">{t('common.loading', { ns: 'common', defaultValue: 'Loading…' })}</p>
      ) : rows.length === 0 ? (
        <div className="mt-10">
          <EmptyState
            icon={<Briefcase size={22} aria-hidden />}
            title={t('workspaces.emptyTitle', { defaultValue: 'No workspaces yet' })}
            description={t('workspaces.emptyBody', { defaultValue: 'Users create workspaces from the sidebar avatar menu.' })}
          />
        </div>
      ) : (
        <div className="mt-6 overflow-x-auto rounded-[12px] border border-[var(--color-border)]">
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
}: {
  id: string
  onBack: () => void
  onDelete: (id: string) => void
  confirm: string | null
  onConfirmChange: (v: string | null) => void
  doDelete: (id: string) => Promise<void>
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
    return <p className="text-sm text-[var(--color-fg-subtle)]">{t('common.loading', { ns: 'common', defaultValue: 'Loading…' })}</p>
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
      <div className="mt-3 flex items-start justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-serif text-2xl text-[var(--color-fg)]">
            <Briefcase size={20} aria-hidden className="text-[var(--color-fg-muted)]" />
            {workspace.name}
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
        <Button variant="destructive" onClick={() => onDelete(id)}>
          <Trash2 size={13} aria-hidden />
          {t('workspaces.delete', { defaultValue: 'Delete workspace' })}
        </Button>
      </div>

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <Panel title={t('workspaces.members', { defaultValue: 'Members' })}>
          {members.map((m) => (
            <Row key={m.user_id} main={m.name || m.email} sub={m.role === 'owner' ? t('workspaces.roleOwner', { defaultValue: 'Owner' }) : t('workspaces.roleMember', { defaultValue: 'Member' })} />
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
            <Button variant="ghost" onClick={() => onConfirmChange(null)}>
              {t('common.cancel', { ns: 'common', defaultValue: 'Cancel' })}
            </Button>
            <Button variant="destructive" onClick={() => void doDelete(id)}>
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
    <div className="rounded-[12px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4">
      <h2 className="text-[12px] font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">{title}</h2>
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