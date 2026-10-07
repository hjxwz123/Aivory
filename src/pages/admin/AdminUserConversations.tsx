/**
 * AdminUserConversations — list every conversation owned by a single user, so
 * an admin can drill into any one of them for triage. Companion to
 * `AdminUserConversation`, which renders the message timeline of one row.
 *
 * Shared by the user-management drawer and the standalone list route.
 */
import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { AlertCircle, ChevronRight, MessageSquare, RefreshCw, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiConversation, ApiUser } from '@/api/types'
import { AdminDetailHeader } from '@/components/admin/admin-detail-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { AdminTable } from '@/components/admin/AdminTable'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AdminPageHeader } from '@/components/admin/admin-page-header'

function formatStamp(unixSec: number): string {
  if (!unixSec) return ''
  try {
    return new Date(unixSec * 1000).toLocaleString()
  } catch {
    return String(unixSec)
  }
}

export default function AdminUserConversations({ userId, embedded = false }: { userId?: string; embedded?: boolean } = {}) {
  const { t } = useTranslation(['admin', 'common'])
  const { id: routeId = '' } = useParams<{ id: string }>()
  const id = userId ?? routeId
  const [user, setUser] = useState<ApiUser | null>(null)
  const [rows, setRows] = useState<ApiConversation[]>([])
  const [loading, setLoading] = useState(true)
  const [loadedId, setLoadedId] = useState('')
  const [error, setError] = useState('')
  const [reloadKey, setReloadKey] = useState(0)
  const [confirmDelete, setConfirmDelete] = useState<ApiConversation | null>(null)
  const [deleting, setDeleting] = useState(false)

  async function remove(c: ApiConversation) {
    setDeleting(true)
    try {
      await adminApi.deleteConversation(c.id)
      setRows((rs) => rs.filter((x) => x.id !== c.id))
      setConfirmDelete(null)
      toast.success(t('admin:users.conversationDeleted'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('admin:common.failed'))
    } finally {
      setDeleting(false)
    }
  }

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      setError('')
      setUser(null)
      setRows([])
      try {
        const [targetUser, convs] = await Promise.all([
          embedded ? Promise.resolve(null) : adminApi.user(id),
          adminApi.userConversations(id),
        ])
        if (cancelled) return
        setUser(targetUser)
        setRows(convs)
      } catch (e) {
        if (!cancelled) setError(e instanceof ApiError ? e.message : t('admin:common.failed'))
      } finally {
        if (!cancelled) {
          setLoadedId(id)
          setLoading(false)
        }
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [id, embedded, reloadKey, t])

  const currentUser = user?.id === id ? user : null
  const pageLoading = loading || loadedId !== id
  const headerName = currentUser?.name.trim() || currentUser?.email.trim() || ''

  return (
    <div>
      {!embedded ? (
        <>
          <AdminDetailHeader backTo="/admin/users" backLabel={t('users.backToUsers')} />
          <AdminPageHeader
            title={pageLoading ? (
              <span className="block" role="status" aria-live="polite">
                <span className="sr-only">{t('admin:common.loading')}</span>
                <span
                  aria-hidden
                  className="block h-8 w-[min(16rem,70vw)] animate-pulse rounded-[8px] bg-[var(--color-bg-muted)] sm:h-9"
                />
              </span>
            ) : headerName ? (
              t('users.conversationsTitle', { name: headerName })
            ) : (
              t('users.conversationsFallbackTitle')
            )}
            titleBusy={pageLoading}
            description={t('users.conversationsLead')}
          />
        </>
      ) : null}

      <section className={embedded ? undefined : 'mt-6 sm:mt-8'} aria-label={t('admin:users.viewConversations')}>
        {pageLoading ? (
          <PanelFallback />
        ) : error ? (
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-[8px] bg-[var(--color-danger-soft)] p-4" role="alert">
            <div className="flex min-w-0 items-start gap-2.5 text-sm text-[var(--color-danger)]">
              <AlertCircle size={16} aria-hidden className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{error}</span>
            </div>
            <Button variant="secondary" size="sm" leadingIcon={<RefreshCw size={13} aria-hidden />} onClick={() => setReloadKey((key) => key + 1)}>
              {t('common:actions.tryAgain')}
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <div className="text-sm text-[var(--color-fg-subtle)] rounded-[12px] bg-[var(--color-surface)] px-5 py-10 text-center">
            {t('users.noConversations')}
          </div>
        ) : (
          <AdminTable
            items={rows}
            rowKey={(c) => c.id}
            label={t('users.viewConversations')}
            columns={[
              { id: 'title', header: t('admin:userFeedback.conversation'), width: 360, render: (c) => <Link to={`/admin/users/${encodeURIComponent(id)}/conversations/${encodeURIComponent(c.id)}`} className="admin-table-link"><span className="flex min-w-0 items-center gap-2"><MessageSquare size={14} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden /><span className="truncate" title={c.title}>{c.title || t('users.untitledConversation')}</span></span></Link> },
              { id: 'model', header: t('admin:resources.table.model'), width: 220, render: (c) => <span className="block truncate font-mono text-[12px] text-[var(--color-fg-muted)]" title={c.model_id || c.provider}>{c.model_id || c.provider || '—'}</span> },
              { id: 'status', header: t('admin:common.status'), width: 150, render: (c) => <div className="flex flex-wrap gap-1">{c.archived ? <Badge size="xs">{t('users.archived')}</Badge> : null}{c.starred ? <Badge size="xs">{t('users.starred')}</Badge> : null}{!c.archived && !c.starred ? '—' : null}</div> },
              { id: 'updated', header: t('admin:common.lastActive'), width: 180, render: (c) => <span className="text-[12px] tabular-nums text-[var(--color-fg-muted)]">{formatStamp(c.updated_at)}</span> },
              { id: 'actions', header: t('admin:common.actions'), width: 100, align: 'right', render: (c) => <div className="flex gap-1"><Button asChild variant="ghost" size="icon-sm" title={t('admin:common.details')} aria-label={t('admin:common.details')}><Link to={`/admin/users/${encodeURIComponent(id)}/conversations/${encodeURIComponent(c.id)}`}><ChevronRight size={14} aria-hidden /></Link></Button><Button variant="ghost" size="icon-sm" title={t('admin:users.deleteConversation')} aria-label={t('admin:users.deleteConversation')} onClick={() => setConfirmDelete(c)}><Trash2 size={14} aria-hidden /></Button></div> },
            ]}
          />
        )}
      </section>

      {!embedded ? (
        <p className="mt-6 text-[12px] text-[var(--color-fg-subtle)] flex items-center gap-1.5">
          <Button asChild variant="ghost" size="sm">
            <Link to="/admin/users">{t('users.backToUsers')}</Link>
          </Button>
        </p>
      ) : null}

      <Dialog open={Boolean(confirmDelete)} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('admin:users.deleteTitle')}</DialogTitle>
            <DialogDescription>
              {confirmDelete
                ? t('admin:users.deleteBody', {
                    title: confirmDelete.title || t('admin:users.untitledConversation'),
                  })
                : ''}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)}>
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
