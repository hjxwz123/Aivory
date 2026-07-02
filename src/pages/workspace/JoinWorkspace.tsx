/**
 * JoinWorkspace (§workspaces) — the invite-link landing page. Resolves the
 * token to a preview (name / owner / member count) and joins on confirm.
 * Unauthenticated visitors are bounced to login and return here after.
 */
import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Briefcase, Users } from 'lucide-react'
import { workspacesApi, ApiError } from '@/api'
import { useAuth } from '@/store/auth'
import { useWorkspaces } from '@/store/workspaces'
import { Logo } from '@/components/brand/logo'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Ghost } from 'lucide-react'

export default function JoinWorkspace() {
  const { token = '' } = useParams<{ token: string }>()
  const { t } = useTranslation(['chat', 'common'])
  const navigate = useNavigate()
  const status = useAuth((s) => s.status)
  const [info, setInfo] = useState<{ id: string; name: string; owner_name: string; member_count: number } | null>(null)
  const [state, setState] = useState<'loading' | 'ready' | 'missing'>('loading')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (status === 'unauthenticated') {
      navigate(`/login?next=${encodeURIComponent(`/workspace/join/${token}`)}`, { replace: true })
      return
    }
    if (status !== 'authenticated') return
    let alive = true
    workspacesApi
      .inviteInfo(token)
      .then((d) => {
        if (!alive) return
        setInfo(d)
        setState('ready')
      })
      .catch(() => alive && setState('missing'))
    return () => {
      alive = false
    }
  }, [status, token, navigate])

  async function join() {
    if (busy) return
    setBusy(true)
    try {
      const ws = await workspacesApi.join(token)
      await useWorkspaces.getState().load()
      await useWorkspaces.getState().switchTo(ws.id)
      navigate('/', { replace: true })
    } catch (e) {
      setState(e instanceof ApiError && e.status === 404 ? 'missing' : 'missing')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-svh w-full bg-[var(--color-bg)] text-[var(--color-fg)]">
      <header className="border-b border-[var(--color-divider)]">
        <div className="mx-auto flex h-14 max-w-[40rem] items-center px-5">
          <Link to="/" aria-label="Aurelia" className="inline-flex items-center">
            <Logo />
          </Link>
        </div>
      </header>
      <main className="mx-auto w-full max-w-[40rem] px-5 pt-16">
        {state === 'loading' ? (
          <p className="text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</p>
        ) : state === 'missing' || !info ? (
          <EmptyState
            icon={<Ghost size={22} aria-hidden />}
            title={t('chat:workspace.inviteMissingTitle', { defaultValue: 'This invite link is no longer valid' })}
            description={t('chat:workspace.inviteMissingBody', {
              defaultValue: 'It may have been reset by the workspace owner. Ask them for a fresh link.',
            })}
            action={
              <Button asChild variant="secondary">
                <Link to="/">{t('chat:workspace.backHome', { defaultValue: 'Back to chat' })}</Link>
              </Button>
            }
          />
        ) : (
          <div className="rounded-[16px] border border-[var(--color-border)] bg-[var(--color-surface)] p-6">
            <div className="flex items-center gap-3">
              <span className="inline-flex size-10 items-center justify-center rounded-[12px] bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                <Briefcase size={18} aria-hidden />
              </span>
              <div className="min-w-0">
                <h1 className="truncate font-serif text-xl text-[var(--color-fg)]">{info.name}</h1>
                <p className="mt-0.5 flex items-center gap-1.5 text-[12px] text-[var(--color-fg-subtle)]">
                  <Users size={12} aria-hidden />
                  {t('chat:workspace.invitePreview', {
                    owner: info.owner_name,
                    count: info.member_count,
                    defaultValue: 'Created by {{owner}} · {{count}} members',
                  })}
                </p>
              </div>
            </div>
            <p className="mt-4 text-sm leading-relaxed text-[var(--color-fg-muted)]">
              {t('chat:workspace.inviteBody', {
                defaultValue:
                  'Joining gives you access to this workspace’s shared conversations, projects and knowledge bases. Your personal space stays separate.',
              })}
            </p>
            <div className="mt-5 flex items-center gap-2">
              <Button onClick={() => void join()} disabled={busy}>
                {t('chat:workspace.join', { defaultValue: 'Join workspace' })}
              </Button>
              <Button asChild variant="ghost">
                <Link to="/">{t('common:common.cancel', { defaultValue: 'Cancel' })}</Link>
              </Button>
            </div>
          </div>
        )}
      </main>
    </div>
  )
}