/**
 * AdminLayout — sidebar-style nav rail along the left for the six admin
 * surfaces. Gates access to admins only.
 */
import { useEffect } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, Boxes, Cpu, GaugeCircle, Settings2, Sparkles, Users } from 'lucide-react'
import { useAuth } from '@/store/auth'
import { cn } from '@/lib/utils'

export default function AdminLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const user = useAuth((s) => s.user)
  const { t } = useTranslation(['admin', 'nav', 'common'])

  useEffect(() => {
    if (user && user.role !== 'admin') navigate('/', { replace: true })
  }, [user, navigate])

  const items = [
    { to: '/admin/channels', icon: Boxes, label: t('admin:channels.title') },
    { to: '/admin/models', icon: Cpu, label: t('admin:models.title') },
    { to: '/admin/skills', icon: Sparkles, label: t('admin:skills.title') },
    { to: '/admin/users', icon: Users, label: t('admin:users.title') },
    { to: '/admin/usage', icon: GaugeCircle, label: t('admin:usage.title') },
    { to: '/admin/settings', icon: Settings2, label: t('admin:settings.title') },
  ]

  // §react-router: when end is true, /admin/users only highlights on the
  // exact path — drill-down routes like /admin/users/:id/conversations would
  // visually orphan. Match by prefix so nested admin surfaces keep their
  // parent tab lit.
  function isItemActive(to: string): boolean {
    return location.pathname === to || location.pathname.startsWith(to + '/')
  }

  return (
    <div className="flex h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
      <aside className="hidden md:flex w-[15rem] flex-col border-r border-[var(--color-divider)] bg-[var(--color-bg-muted)]/40">
        <button
          type="button"
          onClick={() => navigate('/')}
          className="m-3 inline-flex items-center gap-2 text-[12.5px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] interactive rounded-[6px] px-2 py-1.5 self-start"
        >
          <ArrowLeft size={12} aria-hidden />
          {t('admin:backToChat')}
        </button>
        <h2 className="px-5 pt-2 font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
        <nav className="mt-4 flex-1 px-3">
          {items.map((it) => (
            <NavLink
              key={it.to}
              to={it.to}
              end={it.to !== '/admin/users'}
              className={cn(
                'flex items-center gap-2.5 h-9 px-3 rounded-[8px] text-[13px] interactive',
                isItemActive(it.to)
                  ? 'bg-[var(--color-surface)] text-[var(--color-fg)] font-medium'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <it.icon size={14} aria-hidden />
              {it.label}
            </NavLink>
          ))}
        </nav>
      </aside>
      <main className="flex-1 min-w-0 overflow-y-auto">
        <div className="mx-auto w-full max-w-[68rem] px-5 sm:px-8 lg:px-12 py-8 sm:py-12">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
