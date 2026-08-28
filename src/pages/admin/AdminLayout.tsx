/**
 * AdminLayout keeps broad task areas in the rail and exposes the current area's
 * destinations as a route-aware tab row above the page.
 */
import { Suspense, useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { Link, Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  BarChart3,
  Compass,
  Cpu,
  CreditCard,
  LayoutDashboard,
  Menu,
  Settings2,
  Sparkles,
  Users,
} from 'lucide-react'
import { useAuth } from '@/store/auth'
import { adminApi } from '@/api'
import { Sheet, SheetContent, SheetTrigger } from '@/components/ui/sheet'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { UserMenu } from '@/components/sidebar/sidebar'
import { Tooltip } from '@/components/ui/tooltip'
import { AdminOnboardingTour } from '@/components/admin/admin-onboarding-tour'
import type { ApiAdminOnboarding } from '@/api/types'
import { acquireStartupDialog } from '@/lib/startup-dialog-queue'
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
import { useRequestActivity } from '@/lib/request-activity'

const NAVIGATION_MIN_VISIBLE_MS = 180
const NAVIGATION_WATCHDOG_MS = 10_000
const STARTUP_DIALOG_PRESENTED_RELEASE_MS = 180
const MOBILE_SHEET_EXIT_MS = 180

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
  const authPolicy = useAuth((s) => s.authPolicy)
  const authPolicyLoaded = useAuth((s) => s.authPolicyLoaded)
  const { t } = useTranslation(['admin', 'nav', 'common'])
  const [mobileOpen, setMobileOpen] = useState(false)
  const [navigationTarget, setNavigationTarget] = useState<string | null>(null)
  const [onboardingOpen, setOnboardingOpen] = useState(false)
  const [onboardingRefreshKey, setOnboardingRefreshKey] = useState(0)
  const [onboardingSnapshot, setOnboardingSnapshot] = useState<ApiAdminOnboarding | null>(null)
  const contentScrollRef = useRef<HTMLDivElement>(null)
  const onboardingStartupClaimedRef = useRef(false)
  const onboardingStartupRequestRef = useRef(0)
  const onboardingStartupReleaseRef = useRef<(() => void) | null>(null)
  const onboardingStartupReleaseTimerRef = useRef<number | null>(null)
  const onboardingOpenTimerRef = useRef<number | null>(null)
  const onboardingOpenRef = useRef(false)
  const mobileOpenRef = useRef(false)
  const navigationStartedAtRef = useRef(0)
  const navigationPendingRef = useRef(false)
  const navigationFinishTimerRef = useRef<number | null>(null)
  const navigationWatchdogRef = useRef<number | null>(null)
  const requestActivity = useRequestActivity()

  const handleOnboardingSnapshot = useCallback((snapshot: ApiAdminOnboarding) => {
    // Manual replays refresh the live checklist as well. Keeping the newest
    // status prevents a just-completed or skipped guide from auto-opening later.
    setOnboardingSnapshot(snapshot)
  }, [])

  useEffect(() => {
    mobileOpenRef.current = mobileOpen
  }, [mobileOpen])

  const presentOnboarding = useCallback((startupRequestID?: number) => {
    if (onboardingOpenTimerRef.current !== null) {
      window.clearTimeout(onboardingOpenTimerRef.current)
      onboardingOpenTimerRef.current = null
    }
    if (onboardingStartupReleaseTimerRef.current !== null) {
      window.clearTimeout(onboardingStartupReleaseTimerRef.current)
      onboardingStartupReleaseTimerRef.current = null
    }
    const waitForMobileSheet = mobileOpenRef.current
    setMobileOpen(false)
    const present = () => {
      onboardingOpenTimerRef.current = null
      if (startupRequestID !== undefined && startupRequestID !== onboardingStartupRequestRef.current) return
      onboardingOpenRef.current = true
      setOnboardingOpen(true)
    }
    if (waitForMobileSheet) {
      onboardingOpenTimerRef.current = window.setTimeout(present, MOBILE_SHEET_EXIT_MS)
      return
    }
    present()
  }, [])

  const queueOnboardingPresentation = useCallback((requestID: number) => {
    void acquireStartupDialog().then((release) => {
      // A newer automatic or manual request superseded this one while it was
      // waiting. Always release the acquired slot so the next dialog can run.
      if (requestID !== onboardingStartupRequestRef.current) {
        release()
        return
      }
      onboardingStartupReleaseRef.current = release
      presentOnboarding(requestID)
    })
  }, [presentOnboarding])

  const openOnboarding = useCallback(() => {
    // Manual replays wait behind active startup notices, then release that
    // slot as soon as the non-modal coachmark is visible.
    onboardingStartupClaimedRef.current = true
    if (onboardingStartupReleaseRef.current) {
      onboardingStartupRequestRef.current += 1
      presentOnboarding()
    } else {
      const requestID = ++onboardingStartupRequestRef.current
      queueOnboardingPresentation(requestID)
    }
    setOnboardingRefreshKey((current) => current + 1)
  }, [presentOnboarding, queueOnboardingPresentation])

  const releaseOnboardingStartup = useCallback((delay = STARTUP_DIALOG_PRESENTED_RELEASE_MS) => {
    const release = onboardingStartupReleaseRef.current
    if (!release) return
    if (onboardingStartupReleaseTimerRef.current !== null) {
      window.clearTimeout(onboardingStartupReleaseTimerRef.current)
    }
    onboardingStartupReleaseTimerRef.current = window.setTimeout(() => {
      onboardingStartupReleaseTimerRef.current = null
      if (onboardingStartupReleaseRef.current !== release) return
      onboardingStartupReleaseRef.current = null
      release()
    }, delay)
  }, [])

  const onboardingPasswordPolicy = user?.oauth_initial_password_policy ?? authPolicy.oauth_initial_password_policy
  const onboardingNeedsPassword = user?.has_password === false && onboardingPasswordPolicy === 'required'
  const onboardingAutoEligible =
    authPolicyLoaded &&
    status === 'authenticated' &&
    user?.role === 'admin' &&
    Boolean((user?.settings as Record<string, unknown> | undefined)?.onboarded) &&
    !onboardingNeedsPassword

  // The first-run tour waits for the account welcome/password gates and shares
  // the startup lock only until its first coachmark is visible. It is then
  // non-modal, so announcements and other normal dialogs can continue above it.
  useEffect(() => {
    if (onboardingSnapshot?.status !== 'unseen' || !onboardingAutoEligible || onboardingStartupClaimedRef.current) return

    onboardingStartupClaimedRef.current = true
    const requestID = ++onboardingStartupRequestRef.current
    let cancelled = false
    let settled = false
    void acquireStartupDialog().then((release) => {
      if (cancelled || requestID !== onboardingStartupRequestRef.current) {
        settled = true
        release()
        return
      }
      void adminApi.onboarding().then((latest) => {
        settled = true
        if (cancelled || requestID !== onboardingStartupRequestRef.current || latest.status !== 'unseen') {
          release()
          return
        }
        setOnboardingSnapshot(latest)
        onboardingStartupReleaseRef.current = release
        presentOnboarding(requestID)
      }).catch(() => {
        settled = true
        release()
      })
    })
    return () => {
      cancelled = true
      // Strict mode replays effects before the queue or freshness check settles.
      // Allow the second effect instance to claim the slot instead of losing the
      // guide while the cancelled request releases its own slot.
      if (!settled) onboardingStartupClaimedRef.current = false
    }
  }, [onboardingAutoEligible, onboardingSnapshot, presentOnboarding])

  useEffect(() => () => {
    onboardingStartupRequestRef.current += 1
    onboardingOpenRef.current = false
    const release = onboardingStartupReleaseRef.current
    onboardingStartupReleaseRef.current = null
    if (onboardingStartupReleaseTimerRef.current !== null) {
      window.clearTimeout(onboardingStartupReleaseTimerRef.current)
      onboardingStartupReleaseTimerRef.current = null
    }
    if (onboardingOpenTimerRef.current !== null) {
      window.clearTimeout(onboardingOpenTimerRef.current)
      onboardingOpenTimerRef.current = null
    }
    release?.()
  }, [])

  const handleOnboardingOpenChange = useCallback((nextOpen: boolean) => {
    onboardingOpenRef.current = nextOpen
    setOnboardingOpen(nextOpen)
    if (!nextOpen) releaseOnboardingStartup()
  }, [releaseOnboardingStartup])

  const handleOnboardingPresented = useCallback(() => {
    releaseOnboardingStartup()
  }, [releaseOnboardingStartup])

  const clearNavigationActivity = useCallback(() => {
    navigationPendingRef.current = false
    setNavigationTarget(null)
    if (navigationFinishTimerRef.current !== null) {
      window.clearTimeout(navigationFinishTimerRef.current)
      navigationFinishTimerRef.current = null
    }
    if (navigationWatchdogRef.current !== null) {
      window.clearTimeout(navigationWatchdogRef.current)
      navigationWatchdogRef.current = null
    }
  }, [])

  const beginNavigationActivity = useCallback((target: string) => {
    if (navigationFinishTimerRef.current !== null) {
      window.clearTimeout(navigationFinishTimerRef.current)
      navigationFinishTimerRef.current = null
    }
    if (navigationWatchdogRef.current !== null) {
      window.clearTimeout(navigationWatchdogRef.current)
    }
    navigationStartedAtRef.current = Date.now()
    navigationPendingRef.current = true
    setNavigationTarget(target)
    navigationWatchdogRef.current = window.setTimeout(clearNavigationActivity, NAVIGATION_WATCHDOG_MS)
  }, [clearNavigationActivity])

  function handleAdminNavigationClick(event: ReactMouseEvent<HTMLDivElement>): void {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    if (!(event.target instanceof Element)) return

    const anchor = event.target.closest<HTMLAnchorElement>('a[href]')
    if (!anchor || anchor.hasAttribute('download') || (anchor.target && anchor.target !== '_self')) return

    const target = new URL(anchor.href, window.location.href)
    if (target.origin !== window.location.origin || !underAdminPath(target.pathname, '/admin')) return

    const current = `${location.pathname}${location.search}${location.hash}`
    const next = `${target.pathname}${target.search}${target.hash}`
    if (current === next) return
    beginNavigationActivity(`${target.pathname}${target.search}`)
  }

  useEffect(() => {
    setMobileOpen(false)
    contentScrollRef.current?.scrollTo(0, 0)
  }, [location.pathname])

  useEffect(() => {
    if (!navigationPendingRef.current) return
    const elapsed = Date.now() - navigationStartedAtRef.current
    const remaining = Math.max(0, NAVIGATION_MIN_VISIBLE_MS - elapsed)
    navigationFinishTimerRef.current = window.setTimeout(clearNavigationActivity, remaining)
  }, [clearNavigationActivity, location.key, location.pathname, location.search])

  useEffect(() => () => {
    if (navigationFinishTimerRef.current !== null) {
      window.clearTimeout(navigationFinishTimerRef.current)
    }
    if (navigationWatchdogRef.current !== null) {
      window.clearTimeout(navigationWatchdogRef.current)
    }
  }, [])

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
  const activityVisible = navigationTarget !== null || requestActivity.active
  const activityMessage = navigationTarget !== null
    ? t('admin:activity.navigating')
    : requestActivity.slow
      ? t('admin:activity.stillWaiting')
      : t('admin:activity.loading')

  function navigationSpinner(target: string) {
    const pending = navigationTarget === target || navigationTarget?.startsWith(`${target}?`) === true
    return (
      <span
        aria-hidden
        className={cn(
          'ml-auto inline-block size-3 shrink-0 rounded-full border-2 border-current border-r-transparent',
          pending ? 'animate-[spin_700ms_linear_infinite] opacity-70' : 'opacity-0',
        )}
      />
    )
  }

  function renderNavItems() {
    const overviewActive = adminNavItemActive(path, ADMIN_OVERVIEW)
    return (
      <div className="flex flex-col gap-0.5">
        <Link
          to={ADMIN_OVERVIEW.to}
          aria-current={overviewActive ? 'page' : undefined}
          aria-busy={navigationTarget === ADMIN_OVERVIEW.to || undefined}
          onClick={() => setMobileOpen(false)}
          className={cn(
            'flex h-11 items-center gap-2.5 rounded-[8px] px-3 text-[13px] interactive md:h-9',
            overviewActive
              ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
              : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
          )}
        >
          <LayoutDashboard size={14} aria-hidden />
          <span className="truncate">
            {t(ADMIN_OVERVIEW.labelKey, { defaultValue: ADMIN_OVERVIEW.defaultLabel })}
          </span>
          {navigationSpinner(ADMIN_OVERVIEW.to)}
        </Link>

        {ADMIN_NAV_GROUPS.map((group) => {
          const active = adminNavGroupActive(path, group)
          const Icon = GROUP_ICONS[group.key]
          return (
            <Link
              key={group.key}
              to={group.to}
              aria-current={active ? 'location' : undefined}
              aria-busy={navigationTarget === group.to || undefined}
              onClick={() => setMobileOpen(false)}
              className={cn(
                'flex h-11 items-center gap-2.5 rounded-[8px] px-3 text-[13px] interactive md:h-9',
                active
                  ? 'bg-[var(--color-surface)] font-medium text-[var(--color-fg)]'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <Icon size={14} aria-hidden />
              <span className="truncate">
                {t(group.labelKey, { defaultValue: group.defaultLabel })}
              </span>
              {navigationSpinner(group.to)}
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
          'min-w-0 shrink-0 overflow-x-auto overscroll-x-contain border-b border-[var(--color-divider)] scrollbar-none',
          filesWorkspace && 'px-4 pt-3 sm:px-8 sm:pt-4 lg:px-12',
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
                aria-busy={navigationTarget === item.to || navigationTarget?.startsWith(`${item.to}?`) || undefined}
                className={cn(
                  '-mb-px inline-flex h-11 shrink-0 items-center whitespace-nowrap border-b-2 px-3 text-[13px] interactive sm:h-9 sm:px-3.5',
                  active
                    ? 'border-[var(--color-accent)] font-medium text-[var(--color-fg)]'
                    : 'border-transparent text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                )}
              >
                {t(item.labelKey, { defaultValue: item.defaultLabel })}
                {navigationSpinner(item.to)}
              </Link>
            )
          })}
        </div>
      </nav>
    )
  }

  return (
    <div
      className="flex h-full w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]"
      onClickCapture={handleAdminNavigationClick}
    >
      <aside className="hidden w-[16rem] flex-col border-r border-[var(--color-divider)] bg-[var(--color-bg-muted)]/40 md:flex">
        <button
          type="button"
          onClick={() => navigate('/')}
          className="m-3 inline-flex items-center gap-2 self-start rounded-[6px] px-2 py-1.5 text-[12.5px] text-[var(--color-fg-subtle)] interactive hover:text-[var(--color-fg)]"
        >
          <ArrowLeft size={12} aria-hidden />
          {t('admin:backToChat')}
        </button>
        <div className="flex items-center justify-between gap-2 px-4 pt-1">
          <h2 className="min-w-0 flex-1 truncate font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
          <Tooltip content={t('admin:onboarding.review')} side="right">
            <button
              type="button"
              onClick={openOnboarding}
              aria-label={t('admin:onboarding.review')}
              className="inline-flex size-8 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <Compass size={15} aria-hidden />
            </button>
          </Tooltip>
        </div>
        <nav className="mt-4 min-h-0 flex-1 overflow-y-auto px-3 pb-4">
          {renderNavItems()}
        </nav>
      </aside>

      <main
        aria-busy={activityVisible || undefined}
        className={cn(
          'relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden',
          filesWorkspace && 'overscroll-y-contain',
        )}
      >
        {activityVisible ? (
          <div className="pointer-events-none absolute inset-x-0 top-0 z-50">
            <div className="h-0.5 overflow-hidden bg-[var(--color-accent-soft)]">
              <span className="block h-full w-1/3 bg-[var(--color-accent)] animate-[indeterminate_1200ms_ease-in-out_infinite]" />
            </div>
            <div
              role="status"
              aria-live="polite"
              aria-atomic="true"
              aria-busy="true"
              className="fixed bottom-[max(0.75rem,var(--safe-bottom))] left-1/2 flex max-w-[calc(100%-2rem)] -translate-x-1/2 items-center gap-2 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-raised)] px-3 py-2 text-[12px] font-medium text-[var(--color-fg-muted)] shadow-[var(--shadow-md)] md:absolute md:bottom-auto md:top-3"
            >
              <span
                aria-hidden
                className="inline-block size-3.5 shrink-0 rounded-full border-2 border-[var(--color-accent)] border-r-transparent animate-[spin_700ms_linear_infinite]"
              />
              <span className="min-w-0 whitespace-normal text-center">{activityMessage}</span>
            </div>
          </div>
        ) : null}
        <div className="flex h-[calc(var(--layout-topbar-h-mobile)+var(--safe-top))] shrink-0 items-center gap-2 border-b border-[var(--color-divider)] pl-[max(.5rem,var(--safe-left))] pr-[max(.5rem,var(--safe-right))] pt-[var(--safe-top)] md:hidden">
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
                <div className="flex items-center justify-between gap-2 px-4 pt-1">
                  <h2 className="min-w-0 flex-1 truncate font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
                  <Tooltip content={t('admin:onboarding.review')} side="right">
                    <button
                      type="button"
                      onClick={() => { setMobileOpen(false); openOnboarding() }}
                      aria-label={t('admin:onboarding.review')}
                      className="inline-flex size-8 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <Compass size={15} aria-hidden />
                    </button>
                  </Tooltip>
                </div>
                <nav className="mt-4 min-h-0 flex-1 overflow-y-auto px-3 pb-4">
                  {renderNavItems()}
                </nav>
              </div>
            </SheetContent>
          </Sheet>
          <h2 className="min-w-0 flex-1 truncate font-serif text-[15px] text-[var(--color-fg)]">{t('admin:title')}</h2>
          <Tooltip content={t('admin:onboarding.review')}>
            <button
              type="button"
              onClick={openOnboarding}
              aria-label={t('admin:onboarding.review')}
              className="inline-flex size-[var(--tap-min)] shrink-0 items-center justify-center rounded-[10px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <Compass size={18} aria-hidden />
            </button>
          </Tooltip>
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
              <div className="mx-auto w-full max-w-[84rem] shrink-0 px-4 pt-3 sm:px-8 sm:pt-8 lg:px-12 lg:pt-10">
                {renderGroupTabs()}
              </div>
            ) : null}
            <div
              ref={contentScrollRef}
              className="min-h-0 min-w-0 flex-1 overflow-x-auto overflow-y-auto overscroll-contain scrollbar-thin"
            >
              <div
                className={cn(
                  'mx-auto min-w-0 w-full max-w-[84rem] px-4 pb-[max(1.5rem,var(--safe-bottom))] sm:px-8 sm:pb-12 lg:px-12',
                  currentGroup ? 'pt-5 sm:pt-6' : 'pt-5 sm:pt-12',
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
      <AdminOnboardingTour
        open={onboardingOpen}
        onOpenChange={handleOnboardingOpenChange}
        refreshKey={onboardingRefreshKey}
        onSnapshot={handleOnboardingSnapshot}
        onPresented={handleOnboardingPresented}
      />
    </div>
  )
}
