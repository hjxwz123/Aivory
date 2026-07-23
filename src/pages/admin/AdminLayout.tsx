/**
 * AdminLayout — flat left rail of merged admin sections. Related pages are
 * consolidated under one rail entry; a secondary tab bar at the top of the
 * content area switches between the sibling pages. Every page/route/config is
 * unchanged — this only groups how they're reached. Gates access to admins only.
 */
import { Suspense, useEffect, useState } from 'react'
import { NavLink, Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, BarChart3, Briefcase, Cpu, FolderOpen, Menu, Settings2, Sparkles, Users } from 'lucide-react'
import { useAuth } from '@/store/auth'
import { Sheet, SheetContent, SheetTrigger } from '@/components/ui/sheet'
import { RouteFade } from '@/components/ui/route-fade'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { UserMenu } from '@/components/sidebar/sidebar'
import { cn } from '@/lib/utils'

interface AdminTab {
  to: string
  labelKey: string
  /** Extra path prefixes that still belong to this tab (drill-down routes). */
  also?: string[]
}
interface AdminSection {
  key: string
  icon: typeof Cpu
  labelKey: string
  /** Where the rail entry navigates (the section's first tab). */
  to: string
  tabs: AdminTab[]
}

// Merged sections (flat rail). Each groups similar pages; the rail shows one
// entry per section, and the section's pages appear as tabs in the content area.
const SECTIONS: AdminSection[] = [
  {
    key: 'system',
    icon: Settings2,
    labelKey: 'admin:menu.system',
    to: '/admin/settings',
    tabs: [
      { to: '/admin/settings', labelKey: 'admin:settings.title' },
      { to: '/admin/oauth', labelKey: 'admin:oauth.title' },
      { to: '/admin/moderation', labelKey: 'admin:moderation.title' },
      { to: '/admin/announcement', labelKey: 'admin:announcement.title' },
      { to: '/admin/backup', labelKey: 'admin:backup.title' },
    ],
  },
  {
    key: 'models',
    icon: Cpu,
    labelKey: 'admin:menu.models',
    to: '/admin/channels',
    tabs: [
      { to: '/admin/channels', labelKey: 'admin:channels.title' },
      { to: '/admin/models', labelKey: 'admin:models.title', also: ['/admin/model-tags'] },
    ],
  },
  {
    key: 'capabilities',
    icon: Sparkles,
    labelKey: 'admin:menu.capabilities',
    to: '/admin/skills',
    tabs: [
      { to: '/admin/skills', labelKey: 'admin:skills.title' },
      { to: '/admin/image-styles', labelKey: 'admin:imageStyles.title' },
      { to: '/admin/tools', labelKey: 'admin:tools.title' },
      { to: '/admin/documents', labelKey: 'admin:documents.title' },
      { to: '/admin/audio', labelKey: 'admin:audio.title' },
    ],
  },
  {
    key: 'users',
    icon: Users,
    labelKey: 'admin:menu.users',
    to: '/admin/users',
    tabs: [
      { to: '/admin/users', labelKey: 'admin:users.title' },
      { to: '/admin/user-groups', labelKey: 'admin:groups.title' },
      { to: '/admin/redeem-codes', labelKey: 'admin:redeemCodes.title' },
    ],
  },
  {
    key: 'workspaces',
    icon: Briefcase,
    labelKey: 'admin:menu.workspaces',
    to: '/admin/workspaces',
    tabs: [{ to: '/admin/workspaces', labelKey: 'admin:workspaces.title' }],
  },
  {
    key: 'data',
    icon: BarChart3,
    labelKey: 'admin:menu.data',
    to: '/admin/usage',
    tabs: [
      { to: '/admin/usage', labelKey: 'admin:usage.title' },
      { to: '/admin/analytics', labelKey: 'admin:analytics.title' },
    ],
  },
  {
    key: 'files',
    icon: FolderOpen,
    labelKey: 'admin:files.title',
    to: '/admin/files',
    tabs: [{ to: '/admin/files', labelKey: 'admin:files.title' }],
  },
]

// True when `path` is `to` exactly or a drill-down under it (`to/...`).
function underPath(path: string, to: string): boolean {
  return path === to || path.startsWith(to + '/')
}
function tabActive(path: string, tab: AdminTab): boolean {
  return underPath(path, tab.to) || (tab.also ?? []).some((p) => underPath(path, p))
}
function sectionActive(path: string, section: AdminSection): boolean {
  return section.tabs.some((tab) => tabActive(path, tab))
}

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

  const path = location.pathname
  const currentSection = SECTIONS.find((s) => sectionActive(path, s))
  const filesWorkspace = currentSection?.key === 'files'

  function NavItems() {
    return (
      <>
        {SECTIONS.map((s) => {
          const active = sectionActive(path, s)
          return (
            <NavLink
              key={s.key}
              to={s.to}
              className={cn(
                'flex items-center gap-2.5 h-9 px-3 rounded-[8px] text-[13px] interactive',
                active
                  ? 'bg-[var(--color-surface)] text-[var(--color-fg)] font-medium'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <s.icon size={14} aria-hidden />
              {t(s.labelKey)}
            </NavLink>
          )
        })}
      </>
    )
  }

  // Secondary tab bar for the active section (only when it has >1 page).
  function SectionTabs() {
    if (!currentSection || currentSection.tabs.length < 2) return null
    return (
      <div className="mb-6 flex flex-wrap gap-1 border-b border-[var(--color-divider)]">
        {currentSection.tabs.map((tab) => {
          const active = tabActive(path, tab)
          return (
            <NavLink
              key={tab.to}
              to={tab.to}
              className={cn(
                '-mb-px inline-flex items-center h-9 px-3.5 text-[13px] border-b-2 interactive',
                active
                  ? 'border-[var(--color-accent)] text-[var(--color-fg)] font-medium'
                  : 'border-transparent text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              {t(tab.labelKey)}
            </NavLink>
          )
        })}
      </div>
    )
  }

  // The shell matches #root's concrete viewport height. <main> is also a
  // permanent positioning context: RouteFade temporarily creates one while its
  // transform animation runs, then removes it. Without a positioned scroll
  // root, deep absolute `sr-only` controls can jump to document coordinates
  // when that animation ends and make <html> scroll alongside <main>.
  return (
    <div className="flex h-full w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
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
        <nav className="mt-4 flex-1 px-3 flex flex-col gap-0.5">
          <NavItems />
        </nav>
      </aside>

      <main
        className={cn(
          'relative min-h-0 min-w-0 flex-1 overscroll-y-contain',
          filesWorkspace ? 'flex flex-col overflow-hidden' : 'overflow-y-auto',
        )}
      >
        {/* Mobile topbar */}
        <div className="flex h-[var(--layout-topbar-h-mobile)] items-center gap-2 border-b border-[var(--color-divider)] px-2 md:hidden">
          <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
            <SheetTrigger asChild>
              <button
                type="button"
                aria-label={t('admin:title')}
                className="inline-flex size-[var(--tap-min)] items-center justify-center rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
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
                <nav className="mt-4 flex-1 px-3 flex flex-col gap-0.5">
                  <NavItems />
                </nav>
              </div>
            </SheetContent>
          </Sheet>
          <h2 className="min-w-0 flex-1 truncate font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
          <UserMenu placement="header" />
        </div>

        <div
          className={cn(
            filesWorkspace
              ? 'flex min-h-0 w-full flex-1 flex-col'
              : 'mx-auto w-full max-w-[84rem] px-5 py-8 sm:px-8 sm:py-12 lg:px-12',
          )}
        >
          <SectionTabs />
          {/* Content-scoped Suspense: switching tabs/sections keeps the sidebar +
              tabs on screen and only this panel shows a loader while the lazy page
              chunk loads — instead of the whole admin shell blanking to the
              app-level full-screen spinner.
              key=path: router navigations run inside startTransition, and an
              ALREADY-MOUNTED boundary doesn't show its fallback during a
              transition — React freezes the old page (rail/tab highlight included)
              until the next chunk resolves, which reads as click lag. A key change
              mounts a NEW boundary, which commits the fallback immediately: the
              section/tab switches on click, spinner while it loads (§ instant nav). */}
          <RouteFade
            dep={path}
            className={filesWorkspace ? 'flex min-h-0 flex-1 flex-col' : undefined}
          >
            <Suspense key={path} fallback={<PanelFallback />}>
              <Outlet />
            </Suspense>
          </RouteFade>
        </div>
      </main>
    </div>
  )
}
