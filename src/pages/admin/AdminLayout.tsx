/**
 * AdminLayout keeps broad task areas in the rail and exposes the current area's
 * destinations as a route-aware tab row above the page.
 */
import { Suspense, useEffect, useRef, useState } from 'react'
import { Link, Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  BarChart3,
  Cpu,
  CreditCard,
  LayoutDashboard,
  Menu,
  Settings2,
  Sparkles,
  Users,
} from 'lucide-react'
import { useAuth } from '@/store/auth'
import { Sheet, SheetContent, SheetTrigger } from '@/components/ui/sheet'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { UserMenu } from '@/components/sidebar/sidebar'
import {
  ADMIN_NAV_GROUPS,
  ADMIN_OVERVIEW,
  adminNavGroupActive,
  adminNavGroupForPath,
  adminNavItemActive,
  underAdminPath,
  type AdminNavGroupKey,
} from '@/lib/admin-navigation'
import { cn } from '@/lib/utils'

const GROUP_ICONS = {
  ai: Cpu,
  capabilities: Sparkles,
  access: Users,
  billing: CreditCard,
  operations: BarChart3,
  platform: Settings2,
} satisfies Record<AdminNavGroupKey, typeof Cpu>

export default function AdminLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const { t } = useTranslation(['admin', 'nav', 'common'])
  const [mobileOpen, setMobileOpen] = useState(false)
  const contentScrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setMobileOpen(false)
    contentScrollRef.current?.scrollTo(0, 0)
  }, [location.pathname])

  if (user) {
    if (user.role !== 'admin') return <Navigate to="/" replace />
  } else if (status === 'unauthenticated') {
    return <Navigate to="/" replace />
  } else {
    return null
  }

  const path = location.pathname
  const currentGroup = adminNavGroupForPath(path)
  const filesWorkspace = underAdminPath(path, '/admin/files')

  function renderNavItems() {
    const overviewActive = adminNavItemActive(path, ADMIN_OVERVIEW)
    return (
      <div className="flex flex-col gap-0.5">
        <Link
          to={ADMIN_OVERVIEW.to}
          aria-current={overviewActive ? 'page' : undefined}
          onClick={() => setMobileOpen(false)}
          className={cn(
            'flex h-9 items-center gap-2.5 rounded-[8px] px-3 text-[13px] interactive',
            overviewActive
              ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
              : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
          )}
        >
          <LayoutDashboard size={14} aria-hidden />
          <span className="truncate">
            {t(ADMIN_OVERVIEW.labelKey, { defaultValue: ADMIN_OVERVIEW.defaultLabel })}
          </span>
        </Link>

        {ADMIN_NAV_GROUPS.map((group) => {
          const active = adminNavGroupActive(path, group)
          const Icon = GROUP_ICONS[group.key]
          return (
            <Link
              key={group.key}
              to={group.to}
              aria-current={active ? 'location' : undefined}
              onClick={() => setMobileOpen(false)}
              className={cn(
                'flex h-9 items-center gap-2.5 rounded-[8px] px-3 text-[13px] interactive',
                active
                  ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <Icon size={14} aria-hidden />
              <span className="truncate">
                {t(group.labelKey, { defaultValue: group.defaultLabel })}
              </span>
            </Link>
          )
        })}
      </div>
    )
  }

  function renderGroupTabs() {
    if (!currentGroup) return null

    const groupLabel = t(currentGroup.labelKey, { defaultValue: currentGroup.defaultLabel })
    return (
      <nav
        aria-label={groupLabel}
        className={cn(
          'shrink-0 overflow-x-auto border-b border-[var(--color-divider)] scrollbar-none',
          filesWorkspace && 'px-5 pt-4 sm:px-8 lg:px-12',
        )}
      >
        <div className="flex w-max min-w-full items-end gap-1">
          {currentGroup.items.map((item) => {
            const active = adminNavItemActive(path, item)
            return (
              <Link
                key={item.to}
                to={item.to}
                aria-current={active ? 'page' : undefined}
                className={cn(
                  '-mb-px inline-flex h-9 shrink-0 items-center whitespace-nowrap border-b-2 px-3.5 text-[13px] interactive',
                  active
                    ? 'border-[var(--color-accent)] font-medium text-[var(--color-fg)]'
                    : 'border-transparent text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                )}
              >
                {t(item.labelKey, { defaultValue: item.defaultLabel })}
              </Link>
            )
          })}
        </div>
      </nav>
    )
  }

  return (
    <div className="flex h-full w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
      <aside className="hidden w-[16rem] flex-col border-r border-[var(--color-divider)] bg-[var(--color-bg-muted)]/40 md:flex">
        <button
          type="button"
          onClick={() => navigate('/')}
          className="m-3 inline-flex items-center gap-2 self-start rounded-[6px] px-2 py-1.5 text-[12.5px] text-[var(--color-fg-subtle)] interactive hover:text-[var(--color-fg)]"
        >
          <ArrowLeft size={12} aria-hidden />
          {t('admin:backToChat')}
        </button>
        <h2 className="px-5 pt-1 font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
        <nav className="mt-4 min-h-0 flex-1 overflow-y-auto px-3 pb-4">
          {renderNavItems()}
        </nav>
      </aside>

      <main
        className={cn(
          'relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden',
          filesWorkspace && 'overscroll-y-contain',
        )}
      >
        <div className="flex h-[var(--layout-topbar-h-mobile)] shrink-0 items-center gap-2 border-b border-[var(--color-divider)] px-2 md:hidden">
          <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
            <SheetTrigger asChild>
              <button
                type="button"
                aria-label={t('admin:title')}
                className="inline-flex size-[var(--tap-min)] items-center justify-center rounded-[10px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <Menu size={18} aria-hidden />
              </button>
            </SheetTrigger>
            <SheetContent side="left" size="sm" label={t('admin:title')}>
              <div className="flex h-full flex-col">
                <button
                  type="button"
                  onClick={() => { setMobileOpen(false); navigate('/') }}
                  className="m-3 inline-flex items-center gap-2 self-start rounded-[6px] px-2 py-1.5 text-[12.5px] text-[var(--color-fg-subtle)] interactive hover:text-[var(--color-fg)]"
                >
                  <ArrowLeft size={12} aria-hidden />
                  {t('admin:backToChat')}
                </button>
                <h2 className="px-5 pt-1 font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
                <nav className="mt-4 min-h-0 flex-1 overflow-y-auto px-3 pb-4">
                  {renderNavItems()}
                </nav>
              </div>
            </SheetContent>
          </Sheet>
          <h2 className="min-w-0 flex-1 truncate font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
          <UserMenu placement="header" />
        </div>

        {filesWorkspace ? (
          <div className="flex min-h-0 w-full flex-1 flex-col">
            {renderGroupTabs()}
            <div className="flex min-h-0 flex-1 flex-col">
              <Suspense fallback={<PanelFallback />}>
                <Outlet />
              </Suspense>
            </div>
          </div>
        ) : (
          <div className="flex min-h-0 w-full flex-1 flex-col">
            {currentGroup ? (
              <div className="mx-auto w-full max-w-[84rem] shrink-0 px-5 pt-8 sm:px-8 sm:pt-12 lg:px-12">
                {renderGroupTabs()}
              </div>
            ) : null}
            <div
              ref={contentScrollRef}
              className="min-h-0 flex-1 overflow-y-auto overscroll-y-contain scrollbar-thin"
            >
              <div
                className={cn(
                  'mx-auto w-full max-w-[84rem] px-5 pb-8 sm:px-8 sm:pb-12 lg:px-12',
                  currentGroup ? 'pt-6' : 'pt-8 sm:pt-12',
                )}
              >
                <Suspense fallback={<PanelFallback />}>
                  <Outlet />
                </Suspense>
              </div>
            </div>
          </div>
        )}
      </main>
    </div>
  )
}
