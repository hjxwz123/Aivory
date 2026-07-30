/**
 * AdminUserConversations — list every conversation owned by a single user, so
 * an admin can drill into any one of them for triage. Companion to
 * `AdminUserConversation`, which renders the message timeline of one row.
 *
 * Read-only by design: this surface bypasses the per-user ownership filter
 * (router gate is the admin role), so it stays a viewer — no edit/delete from
 * here. Style follows the rest of /admin: card list, ghost actions, tokens-only.
 */
import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ChevronRight, MessageSquare, Trash2 } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiConversation, ApiUser } from '@/api/types'
import { AdminDetailHeader } from '@/components/admin/admin-detail-header'
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
import { toast } from '@/hooks/use-toast'
import { PanelFallback } from '@/components/ui/panel-fallback'

function formatStamp(unixSec: number): string {
  if (!unixSec) return ''
  try {
    return new Date(unixSec * 1000).toLocaleString()
  } catch {
    return String(unixSec)
  }
}

export default function AdminUserConversations() {
  const { t } = useTranslation(['admin', 'common'])
  const { id = '' } = useParams<{ id: string }>()
  const [user, setUser] = useState<ApiUser | null>(null)
  const [rows, setRows] = useState<ApiConversation[]>([])
  const [loading, setLoading] = useState(true)
  const [loadedId, setLoadedId] = useState('')
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
      setUser(null)
      setRows([])
      try {
        const [targetUser, convs] = await Promise.all([
          adminApi.user(id),
          adminApi.userConversations(id),
        ])
        if (cancelled) return
        setUser(targetUser)
        setRows(convs)
      } catch (e) {
        if (!cancelled) toast.error(e instanceof ApiError ? e.message : t('common.failed'))
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
  }, [id, t])

  const currentUser = user?.id === id ? user : null
  const pageLoading = loading || loadedId !== id
  const headerName = currentUser?.name.trim() || currentUser?.email.trim() || ''

  return (
    <div>
      <AdminDetailHeader backTo="/admin/users" backLabel={t('users.backToUsers')} />

      <header>
        <h1
          className="break-words font-serif text-2xl tracking-tight text-[var(--color-fg)] sm:text-3xl"
          aria-busy={pageLoading}
        >
          {pageLoading ? (
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
        </h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">
          {t('users.conversationsLead')}
        </p>
      </header>

      <section className="mt-6 sm:mt-8">
        {pageLoading ? (
          <PanelFallback />
        ) : rows.length === 0 ? (
          <div className="text-sm text-[var(--color-fg-subtle)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-10 text-center">
            {t('users.noConversations')}
          </div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((c) => (
              <li key={c.id} className="grid grid-cols-[minmax(0,1fr)_3rem] items-stretch sm:flex sm:items-center">
                <Link
                  to={`/admin/users/${encodeURIComponent(id)}/conversations/${encodeURIComponent(c.id)}`}
                  className="group grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-start gap-2.5 px-3 py-3 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] sm:flex-1 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:items-center sm:gap-3 sm:px-5 sm:py-4"
                >
                  <MessageSquare size={14} aria-hidden className="mt-1 text-[var(--color-fg-subtle)] sm:mt-0" />
                  <div className="min-w-0">
                    <div className="flex min-w-0 flex-col items-start gap-1.5 sm:flex-row sm:items-center sm:gap-2">
                      <span className="line-clamp-2 min-w-0 break-words font-medium text-[var(--color-fg)] sm:line-clamp-1">
                        {c.title || t('users.untitledConversation')}
                      </span>
                      {c.archived || c.starred ? (
                        <span className="flex shrink-0 flex-wrap items-center gap-1">
                          {c.archived ? (
                            <Badge size="xs" variant="neutral">{t('users.archived')}</Badge>
                          ) : null}
                          {c.starred ? <Badge size="xs">{t('users.starred')}</Badge> : null}
                        </span>
                      ) : null}
                    </div>
                    <div className="mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[12px] font-mono text-[var(--color-fg-subtle)] sm:mt-0.5 sm:flex-nowrap">
                      <span className="min-w-0 break-all sm:truncate">{c.model_id || c.provider || '—'}</span>
                      <span aria-hidden className="hidden shrink-0 sm:inline">·</span>
                      <span className="shrink-0">{formatStamp(c.updated_at)}</span>
                    </div>
                  </div>
                  <ChevronRight
                    size={14}
                    aria-hidden
                    className="hidden text-[var(--color-fg-subtle)] group-hover:text-[var(--color-fg)] sm:block"
                  />
                </Link>
                <Button
                  variant="ghost"
                  size="icon-lg"
                  className="my-1.5 mr-1 shrink-0 self-start text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)] sm:my-0 sm:mr-3 sm:size-7 sm:self-auto"
                  aria-label={t('admin:users.deleteConversation')}
                  onClick={() => setConfirmDelete(c)}
                >
                  <Trash2 size={14} aria-hidden />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <p className="mt-6 text-[12px] text-[var(--color-fg-subtle)] flex items-center gap-1.5">
        <Button asChild variant="ghost" size="sm">
          <Link to="/admin/users">{t('users.backToUsers')}</Link>
        </Button>
      </p>

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
