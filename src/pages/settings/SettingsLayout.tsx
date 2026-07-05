import { Suspense } from 'react'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { User, Wand2, Palette, Sparkles, ShieldCheck, Keyboard, Info, X } from 'lucide-react'
import { DialogOverlay, DialogTitle } from '@/components/ui/dialog'
import { type SettingsBackgroundState } from '@/hooks/use-open-settings'
import { RouteFade } from '@/components/ui/route-fade'
import { PanelFallback } from '@/components/ui/panel-fallback'
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

// Settings render as a MODAL over the app (§ settings modal): the conversation
// sidebar/current page stay dimmed behind the overlay. The shell is a Radix
// dialog with a left nav rail (vertical on desktop, a top scroll-row on mobile)
// and a scrolling right pane that hosts the nested route <Outlet/> — so every
// entry point (mod+, / sidebar / command menu) and the OAuth ?linked deep-link
// keep working unchanged: they just navigate to /settings/:tab, which mounts
// this dialog. Closing (X / Esc / backdrop) returns to /chat.
export default function SettingsLayout() {
  const location = useLocation()
  const { pathname } = location
  const navigate = useNavigate()
  const { t } = useTranslation(['settings', 'common'])

  const isAccountDefault = pathname === '/settings' || pathname === '/settings/'
  // The page the modal was opened OVER (set by useOpenSettings). Threaded through
  // tab NavLinks so switching tabs keeps the live backdrop, and used on close to
  // return to that exact page (navigate(-1) restores its live instance + scroll).
  const backgroundLocation = (location.state as SettingsBackgroundState | null)?.backgroundLocation
  const navState = backgroundLocation ? { backgroundLocation } : undefined
  const close = () => {
    if (backgroundLocation) navigate(-1)
    else navigate('/chat')
  }

  return (
    <DialogPrimitive.Root
      open
      onOpenChange={(o) => {
        if (!o) close()
      }}
    >
      <DialogPrimitive.Portal>
        <DialogOverlay />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className={cn(
            'fixed z-[60] bg-[var(--color-surface)] text-[var(--color-fg)] overflow-hidden',
            'focus-visible:outline-none',
            // Mobile: full-screen sheet. Desktop: centered panel.
            'inset-0 flex flex-col',
            'sm:inset-auto sm:left-1/2 sm:top-1/2 sm:-translate-x-1/2 sm:-translate-y-1/2',
            'sm:w-[min(94vw,60rem)] sm:h-[min(88vh,44rem)] sm:flex-row',
            'sm:rounded-[18px] sm:border sm:border-[var(--color-border)] sm:shadow-[var(--shadow-xl)]',
            'data-[state=open]:animate-[pop-in_220ms_var(--ease-out)]',
            'data-[state=closed]:animate-[fade-out_140ms_var(--ease-in)]',
          )}
        >
          {/* ===== Left rail ===== */}
          <div
            className={cn(
              'shrink-0 flex flex-col bg-[var(--color-bg-muted)]/50',
              'sm:w-56',
            )}
          >
            <div className="flex items-center justify-between gap-2 px-4 sm:px-5 pt-4 sm:pt-5 pb-2 sm:pb-4">
              <DialogTitle className="!text-lg sm:!text-xl">{t('settings:title')}</DialogTitle>
              <DialogPrimitive.Close
                aria-label={t('common:aria.close', { defaultValue: 'Close' })}
                className={cn(
                  'sm:hidden inline-flex items-center justify-center size-8 rounded-[8px]',
                  'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
                  'transition-colors duration-150',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <X size={18} aria-hidden />
              </DialogPrimitive.Close>
            </div>
            <nav
              className={cn(
                'flex gap-1 px-2 sm:px-3 pb-2 sm:pb-4',
                'flex-row overflow-x-auto scrollbar-none',
                'sm:flex-col sm:overflow-y-auto sm:flex-1',
              )}
              aria-label={t('settings:title')}
            >
              {tabDefs.map((tab) => (
                <NavLink
                  key={tab.to}
                  to={tab.to}
                  end={tab.to === '/settings/account' ? false : undefined}
                  // replace + carry the background so switching tabs neither
                  // stacks history (close still returns to the backdrop page)
                  // nor drops the live blurred backdrop.
                  replace
                  state={navState}
                  className={({ isActive }) =>
                    cn(
                      'inline-flex items-center gap-2.5 rounded-[10px] whitespace-nowrap interactive',
                      'px-3 py-2 text-sm font-medium',
                      'sm:w-full',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      isActive || (tab.to === '/settings/account' && isAccountDefault)
                        ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)] sm:shadow-none sm:bg-[var(--color-bg-muted)]'
                        : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]/60',
                    )
                  }
                >
                  <tab.icon size={15} aria-hidden className="shrink-0" />
                  {t(`settings:tabs.${tab.key}`)}
                </NavLink>
              ))}
            </nav>
          </div>

          {/* ===== Right pane ===== */}
          <div className="relative min-h-0 min-w-0 flex-1 overflow-y-auto">
            <DialogPrimitive.Close
              aria-label={t('common:aria.close', { defaultValue: 'Close' })}
              className={cn(
                'max-sm:hidden absolute right-3 top-3 z-10 inline-flex items-center justify-center size-8 rounded-[8px]',
                'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
                'transition-colors duration-150',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              )}
            >
              <X size={16} aria-hidden />
            </DialogPrimitive.Close>
            <div className="px-5 sm:px-8 py-6 sm:py-8">
              <RouteFade dep={pathname}>
                {/* key=pathname: a router navigation runs inside startTransition,
                    and an already-mounted Suspense boundary won't show its
                    fallback during a transition. Re-keying mounts a fresh
                    boundary so the tab switches instantly with a spinner while
                    the lazy chunk loads (§ instant nav). */}
                <Suspense key={pathname} fallback={<PanelFallback />}>
                  <Outlet />
                </Suspense>
              </RouteFade>
            </div>
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
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
        <h2 className="tracking-tight text-xl text-[var(--color-fg)]">{title}</h2>
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
