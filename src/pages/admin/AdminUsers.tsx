/**
 * AdminUsers — list users, ban / unban (realtime via the cache kill channel).
 * Each row links to the per-user conversation drill-down used for support /
 * abuse triage (§8.1).
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { MessageSquare } from 'lucide-react'
import { adminApi, ApiError } from '@/api'
import type { ApiUser } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { toast } from '@/hooks/use-toast'
import { useAuth } from '@/store/auth'

export default function AdminUsers() {
  const { t } = useTranslation('admin')
  const me = useAuth((s) => s.user)
  const [rows, setRows] = useState<ApiUser[]>([])
  const [loading, setLoading] = useState(true)

  async function load() {
    setLoading(true)
    try {
      setRows(await adminApi.users())
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function ban(u: ApiUser) {
    try {
      await adminApi.banUser(u.id)
      toast.success(t('users.banned'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    }
  }
  async function unban(u: ApiUser) {
    try {
      await adminApi.unbanUser(u.id)
      toast.success(t('users.reinstated'))
      await load()
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('common.failed'))
    }
  }

  return (
    <div>
      <header>
        <h1 className="font-serif text-3xl tracking-tight text-[var(--color-fg)]">{t('users.title')}</h1>
        <p className="mt-2 text-[var(--color-fg-muted)] text-sm max-w-2xl">{t('users.lead')}</p>
      </header>

      <section className="mt-8">
        {loading ? (
          <div className="text-sm text-[var(--color-fg-subtle)]">{t('common.loading')}</div>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-divider)] rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]">
            {rows.map((u) => {
              const isMe = me?.id === u.id
              return (
                <li key={u.id} className="grid grid-cols-[1fr_auto] gap-3 items-center px-5 py-4">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-[var(--color-fg)]">{u.name || u.email}</span>
                      <Badge size="xs">{u.role}</Badge>
                      {u.status !== 'active' ? <Badge size="xs" variant="neutral">{u.status}</Badge> : null}
                      {isMe ? <Badge size="xs" variant="neutral">{t('users.you')}</Badge> : null}
                    </div>
                    <div className="mt-0.5 text-[12px] text-[var(--color-fg-subtle)] font-mono truncate">{u.email}</div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button asChild variant="ghost" size="sm" leadingIcon={<MessageSquare size={12} aria-hidden />}>
                      <Link to={`/admin/users/${encodeURIComponent(u.id)}/conversations`}>
                        {t('users.viewConversations')}
                      </Link>
                    </Button>
                    {u.status === 'active' ? (
                      <Button variant="ghost" size="sm" disabled={isMe} onClick={() => void ban(u)}>
                        {t('users.ban')}
                      </Button>
                    ) : (
                      <Button variant="ghost" size="sm" onClick={() => void unban(u)}>
                        {t('users.unban')}
                      </Button>
                    )}
                  </div>
                </li>
              )
            })}
          </ul>
        )}
      </section>
    </div>
  )
}
