import { Link, NavLink, Outlet, useLocation } from 'react-router-dom'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, User, Palette, Sparkles, ShieldCheck, Keyboard, CreditCard } from 'lucide-react'
import { Logo } from '@/components/brand/logo'
import { ThemeToggle } from '@/components/ui/theme-toggle'
import { LanguageToggle } from '@/components/ui/language-toggle'
import { cn } from '@/lib/utils'
import { useTheme } from '@/store/theme'

const tabDefs = [
  { to: '/settings/account', key: 'account', icon: User },
  { to: '/settings/appearance', key: 'appearance', icon: Palette },
  { to: '/settings/models', key: 'models', icon: Sparkles },
  { to: '/settings/privacy', key: 'privacy', icon: ShieldCheck },
  { to: '/settings/shortcuts', key: 'shortcuts', icon: Keyboard },
  { to: '/settings/billing', key: 'billing', icon: CreditCard },
] as const

export default function SettingsLayout() {
  const syncSystem = useTheme((s) => s.syncSystem)
  const { pathname } = useLocation()
  const { t } = useTranslation(['settings', 'chat'])
  useEffect(() => syncSystem(), [syncSystem])

  return (
    <div className="min-h-svh bg-[var(--color-bg)] text-[var(--color-fg)]">
      <header className="border-b border-[var(--color-divider)] sticky top-0 z-30 bg-[var(--color-bg)]/85 backdrop-blur-sm">
        <div className="mx-auto max-w-[var(--layout-content-max-w)] flex items-center gap-3 px-5 sm:px-8 h-14">
          <Link
            to="/chat"
            className="inline-flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] interactive"
            aria-label={t('chat:sidebar.search')}
          >
            <ArrowLeft size={14} aria-hidden /> <span className="max-sm:hidden">{t('chat:sidebar.recents')}</span>
          </Link>
          <span className="mx-3 h-5 w-px bg-[var(--color-divider)]" aria-hidden />
          <Logo size="sm" />
          <span className="mx-2 text-[var(--color-fg-faint)]" aria-hidden>·</span>
          <h1 className="font-serif tracking-tight text-[var(--color-fg)] text-[17px]">{t('settings:title')}</h1>
          <div className="ml-auto flex items-center gap-2">
            <LanguageToggle />
            <ThemeToggle />
          </div>
        </div>
        <nav
          className="mx-auto max-w-[var(--layout-content-max-w)] px-5 sm:px-8 flex items-center gap-1 overflow-x-auto scrollbar-none -mb-px"
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
      </header>

      <main className="mx-auto max-w-[var(--layout-content-max-w)] px-5 sm:px-8 py-10">
        <Outlet />
      </main>
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
