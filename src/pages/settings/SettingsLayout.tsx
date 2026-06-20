import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { User, Wand2, Palette, Sparkles, ShieldCheck, Keyboard, Info } from 'lucide-react'
import { ContentHeader } from '@/components/layout/content-header'
import { cn } from '@/lib/utils'

const tabDefs = [
  { to: '/settings/account', key: 'account', icon: User },
  { to: '/settings/personalization', key: 'personalization', icon: Wand2 },
  { to: '/settings/appearance', key: 'appearance', icon: Palette },
  { to: '/settings/models', key: 'models', icon: Sparkles },
  { to: '/settings/privacy', key: 'privacy', icon: ShieldCheck },
  { to: '/settings/shortcuts', key: 'shortcuts', icon: Keyboard },
  { to: '/settings/about', key: 'about', icon: Info },
] as const

// Renders inside ChatLayout's content panel: the conversation sidebar stays on
// the left while settings occupy the right, sharing the same ContentHeader as
// Subscription. The header/tab-row is a fixed flex child; only the body scrolls.
export default function SettingsLayout() {
  const { pathname } = useLocation()
  const { t } = useTranslation(['settings', 'chat'])

  return (
    <div className="flex-1 min-h-0 flex flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <ContentHeader title={t('settings:title')} backTo="/chat" backLabel={t('chat:sidebar.recents')}>
        <nav
          className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 flex items-center gap-1 overflow-x-auto scrollbar-none -mb-px"
          aria-label={t('settings:title')}
        >
          {tabDefs.map((tab) => (
            <NavLink
              key={tab.to}
              to={tab.to}
              end={tab.to === '/settings/account' ? false : undefined}
              className={({ isActive }) =>
                cn(
                  'inline-flex items-center gap-1.5 px-3 h-10 text-sm font-medium whitespace-nowrap',
                  'text-[var(--color-fg-muted)] border-b-2 border-transparent interactive',
                  (isActive || (tab.to === '/settings/account' && (pathname === '/settings' || pathname === '/settings/'))) &&
                    'text-[var(--color-fg)] border-[var(--color-fg)]',
                  'hover:text-[var(--color-fg)]',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[6px]',
                )
              }
            >
              <tab.icon size={13} aria-hidden />
              {t(`settings:tabs.${tab.key}`)}
            </NavLink>
          ))}
        </nav>
      </ContentHeader>

      <div className="flex-1 min-h-0 overflow-y-auto">
        <main className="mx-auto w-full max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-10">
          <Outlet />
        </main>
      </div>
    </div>
  )
}

export function SettingsSection({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children: React.ReactNode
}) {
  return (
    <section className="mb-12">
      <div className="mb-5">
        <h2 className="font-serif tracking-tight text-xl text-[var(--color-fg)]">{title}</h2>
        {description ? (
          <p className="mt-1.5 text-sm text-[var(--color-fg-muted)]">{description}</p>
        ) : null}
      </div>
      <div className="rounded-2xl border border-[var(--color-border)] bg-[var(--color-surface)] divide-y divide-[var(--color-divider)]">
        {children}
      </div>
    </section>
  )
}

export function SettingsRow({
  label,
  description,
  children,
}: {
  label: string
  description?: string
  children?: React.ReactNode
}) {
  return (
    <div className="px-5 sm:px-6 py-4 sm:py-5 flex flex-col sm:flex-row sm:items-center gap-3 sm:gap-6">
      <div className="flex-1 min-w-0">
        <div className="text-sm font-medium text-[var(--color-fg)]">{label}</div>
        {description ? (
          <p className="mt-1 text-xs text-[var(--color-fg-muted)] leading-relaxed max-w-md">
            {description}
          </p>
        ) : null}
      </div>
      <div className="sm:shrink-0">{children}</div>
    </div>
  )
}
