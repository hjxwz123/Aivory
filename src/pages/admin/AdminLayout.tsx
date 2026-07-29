/**
 * AdminLayout — task-oriented admin navigation.
 *
 * The old shell exposed one rail item per broad area and then rendered a second
 * tab bar above the page. That hid most destinations until a parent area was
 * opened and wrapped badly as the console grew. The grouped rail below links
 * directly to every top-level admin surface.
 */
import { Suspense, useEffect, useState } from 'react'
import { NavLink, Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
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
import { RouteFade } from '@/components/ui/route-fade'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { UserMenu } from '@/components/sidebar/sidebar'
import { cn } from '@/lib/utils'

interface AdminNavItem {
  to: string
  labelKey: string
  defaultLabel: string
  /** Drill-down routes that should keep this item selected. */
  also?: string[]
}

interface AdminNavGroup {
  key: string
  icon: typeof Cpu
  labelKey: string
  defaultLabel: string
  items: AdminNavItem[]
}

const OVERVIEW: AdminNavItem = {
  to: '/admin/overview',
  labelKey: 'admin:menu.overview',
  defaultLabel: 'Overview',
}

const GROUPS: AdminNavGroup[] = [
  {
    key: 'ai',
    icon: Cpu,
    labelKey: 'admin:menu.aiModels',
    defaultLabel: 'AI & models',
    items: [
      { to: '/admin/channels', labelKey: 'admin:channels.title', defaultLabel: 'Channels' },
      {
        to: '/admin/models',
        labelKey: 'admin:models.title',
        defaultLabel: 'Models',
        also: ['/admin/model-tags'],
      },
      {
        to: '/admin/settings/model-policy',
        labelKey: 'admin:menu.modelPolicy',
        defaultLabel: 'Model policy',
      },
      {
        to: '/admin/settings/context-memory',
        labelKey: 'admin:menu.contextMemory',
        defaultLabel: 'Context & memory',
      },
      { to: '/admin/moderation', labelKey: 'admin:moderation.title', defaultLabel: 'Moderation' },
    ],
  },
  {
    key: 'capabilities',
    icon: Sparkles,
    labelKey: 'admin:menu.capabilities',
    defaultLabel: 'Capabilities & integrations',
    items: [
      { to: '/admin/skills', labelKey: 'admin:skills.title', defaultLabel: 'Skills' },
      { to: '/admin/prompts', labelKey: 'admin:prompts.title', defaultLabel: 'Prompt library' },
      { to: '/admin/tools', labelKey: 'admin:tools.title', defaultLabel: 'Tools' },
      { to: '/admin/documents', labelKey: 'admin:documents.title', defaultLabel: 'Documents & knowledge' },
      { to: '/admin/image-styles', labelKey: 'admin:imageStyles.title', defaultLabel: 'Image generation' },
      { to: '/admin/audio', labelKey: 'admin:audio.title', defaultLabel: 'Speech' },
    ],
  },
  {
    key: 'access',
    icon: Users,
    labelKey: 'admin:menu.usersAccess',
    defaultLabel: 'Users & access',
    items: [
      {
        to: '/admin/users',
        labelKey: 'admin:users.title',
        defaultLabel: 'Users',
      },
      {
        to: '/admin/settings/registration',
        labelKey: 'admin:menu.registrationPolicy',
        defaultLabel: 'Registration policy',
      },
      { to: '/admin/oauth', labelKey: 'admin:oauth.title', defaultLabel: 'Login providers' },
      { to: '/admin/workspaces', labelKey: 'admin:workspaces.title', defaultLabel: 'Workspaces' },
    ],
  },
  {
    key: 'billing',
    icon: CreditCard,
    labelKey: 'admin:menu.billing',
    defaultLabel: 'Billing & entitlements',
    items: [
      { to: '/admin/user-groups', labelKey: 'admin:groups.title', defaultLabel: 'Plans' },
      { to: '/admin/credits', labelKey: 'admin:menu.creditsQuotas', defaultLabel: 'Credits & quotas' },
      { to: '/admin/redeem-codes', labelKey: 'admin:redeemCodes.title', defaultLabel: 'Redeem codes' },
      {
        to: '/admin/payment-channels',
        labelKey: 'admin:paymentChannels.title',
        defaultLabel: 'Payment channels',
      },
      {
        to: '/admin/payment-methods',
        labelKey: 'admin:paymentMethods.title',
        defaultLabel: 'Payment methods',
      },
      { to: '/admin/payment-orders', labelKey: 'admin:paymentOrders.title', defaultLabel: 'Payment orders' },
    ],
  },
  {
    key: 'operations',
    icon: BarChart3,
    labelKey: 'admin:menu.operations',
    defaultLabel: 'Data & operations',
    items: [
      { to: '/admin/analytics', labelKey: 'admin:analytics.title', defaultLabel: 'Analytics' },
      { to: '/admin/usage', labelKey: 'admin:usage.title', defaultLabel: 'Usage records' },
      { to: '/admin/files', labelKey: 'admin:files.title', defaultLabel: 'Files' },
    ],
  },
  {
    key: 'platform',
    icon: Settings2,
    labelKey: 'admin:menu.platform',
    defaultLabel: 'System',
    items: [
      { to: '/admin/announcement', labelKey: 'admin:announcement.title', defaultLabel: 'Announcement' },
      {
        to: '/admin/settings/email',
        labelKey: 'admin:menu.emailService',
        defaultLabel: 'Email service',
      },
      { to: '/admin/storage', labelKey: 'admin:menu.storageUploads', defaultLabel: 'Storage & uploads' },
      {
        to: '/admin/settings/legal',
        labelKey: 'admin:menu.legalContact',
        defaultLabel: 'Legal & contact',
      },
      {
        to: '/admin/settings/logging',
        labelKey: 'admin:menu.loggingPrivacy',
        defaultLabel: 'Logging & privacy',
      },
      { to: '/admin/backup', labelKey: 'admin:backup.title', defaultLabel: 'Backup & migration' },
    ],
  },
]

function underPath(path: string, to: string): boolean {
  return path === to || path.startsWith(to.endsWith('/') ? to : `${to}/`)
}

function itemActive(path: string, item: AdminNavItem): boolean {
  return underPath(path, item.to) || (item.also ?? []).some((prefix) => underPath(path, prefix))
}

export default function AdminLayout() {
  const navigate = useNavigate()
  const location = useLocation()
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const { t } = useTranslation(['admin', 'nav', 'common'])
  const [mobileOpen, setMobileOpen] = useState(false)

  useEffect(() => {
    setMobileOpen(false)
  }, [location.pathname])

  if (user) {
    if (user.role !== 'admin') return <Navigate to="/" replace />
  } else if (status === 'unauthenticated') {
    return <Navigate to="/" replace />
  } else {
    return null
  }

  const path = location.pathname
  const filesWorkspace = underPath(path, '/admin/files')

  function LinkItem({ item }: { item: AdminNavItem }) {
    const active = itemActive(path, item)
    return (
      <NavLink
        to={item.to}
        className={cn(
          'flex h-8 items-center rounded-[7px] pl-8 pr-2 text-[12.5px] interactive',
          active
            ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)] shadow-[inset_2px_0_0_var(--color-accent)]'
            : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
        )}
      >
        <span className="truncate">{t(item.labelKey, { defaultValue: item.defaultLabel })}</span>
      </NavLink>
    )
  }

  function NavItems() {
    return (
      <>
        <NavLink
          to={OVERVIEW.to}
          className={cn(
            'mb-3 flex h-9 items-center gap-2.5 rounded-[8px] px-3 text-[13px] interactive',
            itemActive(path, OVERVIEW)
              ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
              : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
          )}
        >
          <LayoutDashboard size={14} aria-hidden />
          {t(OVERVIEW.labelKey, { defaultValue: OVERVIEW.defaultLabel })}
        </NavLink>

        {GROUPS.map((group) => (
          <section key={group.key} className="mb-3">
            <div className="flex h-7 items-center gap-2 px-3 text-[11px] font-medium uppercase text-[var(--color-fg-subtle)]">
              <group.icon size={12} aria-hidden />
              <span className="truncate">{t(group.labelKey, { defaultValue: group.defaultLabel })}</span>
            </div>
            <div className="mt-0.5 flex flex-col gap-px">
              {group.items.map((item) => <LinkItem key={item.to} item={item} />)}
            </div>
          </section>
        ))}
      </>
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
          <NavItems />
        </nav>
      </aside>

      <main
        className={cn(
          'relative min-h-0 min-w-0 flex-1 overscroll-y-contain',
          filesWorkspace ? 'flex flex-col overflow-hidden' : 'overflow-y-auto',
        )}
      >
        <div className="flex h-[var(--layout-topbar-h-mobile)] items-center gap-2 border-b border-[var(--color-divider)] px-2 md:hidden">
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
          <RouteFade dep={path} className={filesWorkspace ? 'flex min-h-0 flex-1 flex-col' : undefined}>
            <Suspense key={path} fallback={<PanelFallback />}>
              <Outlet />
            </Suspense>
          </RouteFade>
        </div>
      </main>
    </div>
  )
}
