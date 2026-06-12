/**
 * AdminUserConversations — list every conversation owned by a single user, so
 * an admin can drill into any one of them for triage. Companion to
 * `AdminUserConversation`, which renders the message timeline of one row.
 *
 * Read-only by design: this surface bypasses the per-user ownership filter
 * (router gate is the admin role), so it stays a viewer — no edit/delete from
 * here. Style follows the rest of /admin: card list, ghost actions, tokens-only.
 */
import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, ChevronRight, MessageSquare } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiConversation, ApiUser } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { toast } from '@/hooks/use-toast'

function formatStamp(unixSec: number): string {
  if (!unixSec) return ''
  try {
    return new Date(unixSec * 1000).toLocaleString()
  } catch {
    return String(unixSec)
  }
}

export default function AdminUserConversations() {
  const { t } = useTranslation('admin')
  const navigate = useNavigate()
  const { id = '' } = useParams<{ id: string }>()
  const [user, setUser] = useState<ApiUser | null>(null)
  const [rows, setRows] = useState<ApiConversation[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      try {
        // The users list is small enough that re-fetching it for one row is
        // cheaper than carving out a single-user GET endpoint.
        const [users, convs] = await Promise.all([
          adminApi.users(),
          adminApi.userConversations(id),
        ])
        if (cancelled) return
        setUser(users.find((u) => u.id === id) ?? null)
        setRows(convs)
      } catch (e) {
        if (!cancelled) toast.error(e instanceof ApiError ? e.message : t('common.failed'))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [id, t])

  const headerName = useMemo(() => {
    if (!user) return id
    return user.name || user.email
  }, [user, id])

  return (
    <div>
      <button
        type="button"
        onClick={() => navigate('/admin/users')}
        className="inline-flex items-center gap-1.5 text-[12.5px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] interactive rounded-[6px] -ml-2 px-2 py-1.5 mb-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <ArrowLeft size={12} aria-hidden />
        {t('users.backToUsers')}
      </button>

      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">
          {t('users.conversationsTitle', { name: headerName })}
        </h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">
          {t('users.conversationsLead')}
        </p>
      </header>

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="text-sm text-[var(--color-fg-subtle)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] px-5 py-10 text-center">
            {t('users.noConversations')}
          </div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((c) => (
              <li key={c.id}>
                <Link
                  to={`/admin/users/${encodeURIComponent(id)}/conversations/${encodeURIComponent(c.id)}`}
                  className="group grid grid-cols-[auto_1fr_auto] items-center gap-3 px-5 py-4 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <MessageSquare size={14} aria-hidden className="text-[var(--color-fg-subtle)]" />
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-[var(--color-fg)] truncate">
                        {c.title || t('users.untitledConversation')}
                      </span>
                      {c.archived ? (
                        <Badge size="xs" variant="neutral">{t('users.archived')}</Badge>
                      ) : null}
                      {c.starred ? <Badge size="xs">{t('users.starred')}</Badge> : null}
                    </div>
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">
                      {c.model_id || c.provider || '—'} · {formatStamp(c.updated_at)}
                    </div>
                  </div>
                  <ChevronRight
                    size={14}
                    aria-hidden
                    className="text-[var(--color-fg-subtle)] group-hover:text-[var(--color-fg)]"
                  />
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* Footnote so the surface doesn't read as an editor. */}
      <p className="mt-6 text-[12px] text-[var(--color-fg-subtle)] flex items-center gap-1.5">
        <Button asChild variant="ghost" size="sm">
          <Link to="/admin/users">{t('users.backToUsers')}</Link>
        </Button>
      </p>
    </div>
  )
}
