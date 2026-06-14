/**
 * AdminLayout — sidebar-style nav rail along the left for the six admin
 * surfaces. Gates access to admins only. Mobile uses a sheet-based nav.
 */
import { useEffect, useState } from 'react'
import { NavLink, Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, BarChart3, Boxes, Cpu, FileText, GaugeCircle, KeyRound, Layers, Menu, Mic, Settings2, ShieldAlert, Sparkles, Users, Wrench } from 'lucide-react'
import { useAuth } from '@/store/auth'
import { Sheet, SheetContent, SheetTrigger } from '@/components/ui/sheet'
import { cn } from '@/lib/utils'

export default function AdminLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const { t } = useTranslation(['admin', 'nav', 'common'])
  const [mobileOpen, setMobileOpen] = useState(false)

  // Close mobile nav on route change.
  useEffect(() => {
    setMobileOpen(false)
  }, [location.pathname])

  // Render-gate (not a post-mount effect): a non-admin must never mount the
  // admin pages or fire their API calls, even for a frame. While auth is still
  // resolving (hydrate in flight: user null, status idle/authenticating) we
  // render nothing rather than flashing a redirect.
  if (user) {
    if (user.role !== 'admin') return <Navigate to="/" replace />
  } else if (status === 'unauthenticated') {
    return <Navigate to="/" replace />
  } else {
    return null
  }

  const items = [
    { to: '/admin/channels', icon: Boxes, label: t('admin:channels.title') },
    { to: '/admin/models', icon: Cpu, label: t('admin:models.title') },
    { to: '/admin/skills', icon: Sparkles, label: t('admin:skills.title') },
    { to: '/admin/documents', icon: FileText, label: t('admin:documents.title') },
    { to: '/admin/tools', icon: Wrench, label: t('admin:tools.title') },
    { to: '/admin/audio', icon: Mic, label: t('admin:audio.title') },
    { to: '/admin/oauth', icon: KeyRound, label: t('admin:oauth.title') },
    { to: '/admin/users', icon: Users, label: t('admin:users.title') },
    { to: '/admin/user-groups', icon: Layers, label: t('admin:groups.title') },
    { to: '/admin/usage', icon: GaugeCircle, label: t('admin:usage.title') },
    { to: '/admin/analytics', icon: BarChart3, label: t('admin:analytics.title') },
    { to: '/admin/moderation', icon: ShieldAlert, label: t('admin:moderation.title') },
    { to: '/admin/settings', icon: Settings2, label: t('admin:settings.title') },
  ]

  // §react-router: when end is true, /admin/users only highlights on the
  // exact path — drill-down routes like /admin/users/:id/conversations would
  // visually orphan. Match by prefix so nested admin surfaces keep their
  // parent tab lit.
  function isItemActive(to: string): boolean {
    return location.pathname === to || location.pathname.startsWith(to + '/')
  }

  function NavItems() {
    return (
      <>
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
      </>
    )
  }

  return (
    <div className="flex h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
      {/* Desktop sidebar */}
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
          <NavItems />
        </nav>
      </aside>

      <main className="flex-1 min-w-0 overflow-y-auto">
        {/* Mobile topbar */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-[var(--color-divider)] md:hidden">
          <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
            <SheetTrigger asChild>
              <button
                type="button"
                aria-label={t('admin:title')}
                className="inline-flex items-center justify-center size-9 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <Menu size={18} aria-hidden />
              </button>
            </SheetTrigger>
            <SheetContent side="left" size="sm" label={t('admin:title')}>
              <div className="flex flex-col h-full">
                <button
                  type="button"
                  onClick={() => { setMobileOpen(false); navigate('/') }}
                  className="m-3 inline-flex items-center gap-2 text-[12.5px] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] interactive rounded-[6px] px-2 py-1.5 self-start"
                >
                  <ArrowLeft size={12} aria-hidden />
                  {t('admin:backToChat')}
                </button>
                <h2 className="px-5 pt-2 font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
                <nav className="mt-4 flex-1 px-3">
                  <NavItems />
                </nav>
              </div>
            </SheetContent>
          </Sheet>
          <h2 className="font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
        </div>

        <div className="mx-auto w-full max-w-[68rem] px-5 sm:px-8 lg:px-12 py-8 sm:py-12">
          <Outlet />
        </div>
      </main>
    </div>
  )
}
