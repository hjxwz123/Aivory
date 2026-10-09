/**
 * AdminLayout keeps broad task areas in the rail and exposes the current area's
 * destinations as a route-aware tab row above the page.
 */
import { Suspense, useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { Link, Navigate, Outlet, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  BarChart3,
  Compass,
  Cpu,
  CreditCard,
  LayoutDashboard,
  Menu,
  ScrollText,
  Settings2,
  Sparkles,
  Users,
  X,
} from 'lucide-react'
import { useAuth } from '@/store/auth'
import { useSettings } from '@/store/settings'
import { Sheet, SheetContent, SheetTrigger } from '@/components/ui/sheet'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { UserMenu } from '@/components/sidebar/sidebar'
import { Tooltip } from '@/components/ui/tooltip'
import { TracedLogo } from '@/components/brand/logo'
import { AdminOnboardingTour } from '@/components/admin/admin-onboarding-tour'
import { DesktopUpdateNotice } from '@/components/admin/desktop-update-notice'
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
import { QuietSurfaceContext } from '@/contexts/quiet-surface'
import '@/i18n/admin-resources'

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
  logs: ScrollText,
  platform: Settings2,
} satisfies Record<AdminNavGroupKey, typeof Cpu>

export default function AdminLayout() {
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
  const groupTabsRef = useRef<HTMLElement>(null)
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
  const sidebarWidth = useSettings((s) => s.sidebarWidth)

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
      settled = true
      if (cancelled || requestID !== onboardingStartupRequestRef.current) {
        release()
        return
      }
      onboardingStartupReleaseRef.current = release
      presentOnboarding(requestID)
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
    const nav = groupTabsRef.current
    if (!nav) return
    const revealActiveTab = () => {
      const active = nav.querySelector('[aria-current="page"]')
      if (!active) return
      const bounds = nav.getBoundingClientRect()
      const tab = active.getBoundingClientRect()
      if (tab.left < bounds.left) nav.scrollLeft -= bounds.left - tab.left
      else if (tab.right > bounds.right) nav.scrollLeft += tab.right - bounds.right
    }
    revealActiveTab()
    const observer = new ResizeObserver(revealActiveTab)
    observer.observe(nav)
    return () => observer.disconnect()
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

  // Shares the chat sidebar's row recipe (SidebarNavItem): same height,
  // radius, type size and hover/selected fills, so moving between the app and
  // the console never changes how navigation looks.
  function navRowClass(active: boolean) {
    return cn(
      'group/nav inline-flex h-8 w-full items-center gap-3 overflow-hidden rounded-[8px] px-2.5 text-[13px] interactive max-lg:h-[var(--tap-min)] max-sm:!h-9',
      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
      active
        ? 'bg-[var(--color-sidebar-active)] font-medium text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
        : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-sidebar-hover)] hover:text-[var(--color-fg)]',
    )
  }

  function renderNavItems() {
    const overviewActive = adminNavItemActive(path, ADMIN_OVERVIEW)
    return (
      <div className="flex flex-col gap-1.5">
        <Link
          to="/"
          onClick={() => setMobileOpen(false)}
          className={navRowClass(false)}
        >
          <ArrowLeft size={15} aria-hidden className="shrink-0" />
          <span className="min-w-0 flex-1 truncate text-left">{t('admin:backToChat')}</span>
        </Link>

        <div aria-hidden className="h-3 shrink-0" />

        <Link
          to={ADMIN_OVERVIEW.to}
          aria-current={overviewActive ? 'page' : undefined}
          aria-busy={navigationTarget === ADMIN_OVERVIEW.to || undefined}
          onClick={() => setMobileOpen(false)}
          className={navRowClass(overviewActive)}
        >
          <LayoutDashboard size={15} aria-hidden className="shrink-0" />
          <span className="min-w-0 flex-1 truncate text-left">
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
              className={navRowClass(active)}
            >
              <Icon size={15} aria-hidden className="shrink-0" />
              <span className="min-w-0 flex-1 truncate text-left">
                {t(group.labelKey, { defaultValue: group.defaultLabel })}
              </span>
              {navigationSpinner(group.to)}
            </Link>
          )
        })}
      </div>
    )
  }

  function renderSidebar(variant: 'desktop' | 'sheet') {
    return (
      <div className="flex h-full min-h-0 w-full flex-col bg-[var(--color-sidebar-bg)]">
        {/* Brand row mirrors the chat sidebar header: 56px tall, mark on the
            nav icons' x-line, sans name at the workspace-name size. */}
        <div className="flex h-[56px] shrink-0 items-center justify-between gap-2 px-3 max-sm:h-12 max-sm:px-2">
          <div className="ml-1.5 flex min-w-0 items-center max-sm:ml-2.5">
            <span className="relative inline-flex shrink-0 items-start pb-3">
              <TracedLogo size="sm" />
              <span className="absolute bottom-0 right-0 whitespace-nowrap text-xs font-medium leading-3 text-[var(--color-fg-muted)]">
                {t('admin:title')}
              </span>
            </span>
          </div>
          {variant === 'sheet' ? (
            <button
              type="button"
              onClick={() => setMobileOpen(false)}
              aria-label={t('common:actions.close', { defaultValue: 'Close' })}
              className="inline-flex size-[var(--tap-min)] shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-sidebar-hover)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-9"
            >
              <X size={18} aria-hidden />
            </button>
          ) : null}
        </div>
        <nav
          aria-label={t('admin:title')}
          className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-3 scrollbar-thin"
        >
          {renderNavItems()}
        </nav>
        <div className="mt-auto shrink-0 p-1">
          <UserMenu />
        </div>
      </div>
    )
  }

  function renderGroupTabs() {
    if (!currentGroup) return null

    const groupLabel = t(currentGroup.labelKey, { defaultValue: currentGroup.defaultLabel })
    return (
      <nav
        ref={groupTabsRef}
        aria-label={groupLabel}
        className="flex min-h-12 min-w-0 items-center overflow-x-auto overscroll-x-contain scrollbar-none"
      >
        {/* Matches the resource library's kind switcher. */}
        <div className="inline-flex w-max shrink-0 items-center gap-1.5 rounded-[9px] bg-[var(--color-bg-muted)] p-1">
          {currentGroup.items.map((item) => {
            const active = adminNavItemActive(path, item)
            return (
              <Link
                key={item.to}
                to={item.to}
                aria-current={active ? 'page' : undefined}
                aria-busy={navigationTarget === item.to || navigationTarget?.startsWith(`${item.to}?`) || undefined}
                className={cn(
                  'inline-flex h-[var(--tap-min)] min-w-0 shrink-0 items-center justify-center whitespace-nowrap rounded-[7px] px-3 text-[13px] font-medium interactive sm:h-8 sm:px-4',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-ring)]',
                  active
                    ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
                    : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
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

  const areaLabel = currentGroup
    ? t(currentGroup.labelKey, { defaultValue: currentGroup.defaultLabel })
    : t('admin:title')
  const groupTabs = renderGroupTabs()

  return (
    <QuietSurfaceContext.Provider value>
      <div
        className="app-viewport flex w-full bg-[var(--color-bg)] text-[var(--color-fg)]"
        onClickCapture={handleAdminNavigationClick}
      >
        {/* Desktop rail: same width (the user's resized chat sidebar width),
            surface and breakpoint as the chat sidebar. */}
        <aside
          data-window-sidebar
          style={{ width: `${sidebarWidth}px` }}
          className="hidden shrink-0 pt-[var(--safe-top)] lg:flex"
        >
          {renderSidebar('desktop')}
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
                className="fixed bottom-[max(0.75rem,var(--safe-bottom))] left-1/2 flex max-w-[calc(100%-2rem)] -translate-x-1/2 items-center gap-2 rounded-[8px] bg-[var(--color-surface-raised)] px-3 py-2 text-[12px] font-medium text-[var(--color-fg-muted)] shadow-[var(--shadow-md)] md:absolute md:bottom-auto md:top-3"
              >
                <span
                  aria-hidden
                  className="inline-block size-3.5 shrink-0 rounded-full border-2 border-[var(--color-accent)] border-r-transparent animate-[spin_700ms_linear_infinite]"
                />
                <span className="min-w-0 whitespace-normal text-center">{activityMessage}</span>
              </div>
            </div>
          ) : null}

          {/* The desktop rail names the area; its destinations share one toolbar. */}
          <header className="shrink-0 bg-[var(--color-bg)] pt-[var(--safe-top)]">
            <div
              className={cn(
                'grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2 px-4 sm:gap-x-3 sm:px-8 lg:flex',
                !filesWorkspace && 'mx-auto max-w-[var(--layout-content-max-w)]',
              )}
            >
              <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
                <SheetTrigger asChild>
                  <button
                    type="button"
                    aria-label={t('admin:title')}
                    className="-ml-1 inline-flex size-[var(--tap-min)] shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] lg:hidden"
                  >
                    <Menu size={18} aria-hidden />
                  </button>
                </SheetTrigger>
                <SheetContent side="left" size="nav" label={t('admin:title')} className="bg-[var(--color-sidebar-bg)]">
                  {renderSidebar('sheet')}
                </SheetContent>
              </Sheet>
              <p className={cn('col-start-2 row-start-1 flex h-12 min-w-0 items-center text-[13px] font-medium text-[var(--color-fg-muted)]', currentGroup ? 'lg:hidden' : 'lg:flex-1')}>
                <span className="truncate">{areaLabel}</span>
              </p>
              {groupTabs ? (
                <div className="col-span-3 row-start-2 min-w-0 lg:flex-1">
                  {groupTabs}
                </div>
              ) : null}
              <div className="col-start-3 row-start-1 flex h-12 shrink-0 items-center gap-1 lg:ml-auto">
                <Tooltip content={t('admin:onboarding.review')}>
                  <button
                    type="button"
                    onClick={openOnboarding}
                    aria-label={t('admin:onboarding.review')}
                    className="inline-flex size-[var(--tap-min)] shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] lg:size-9"
                  >
                    <Compass size={16} aria-hidden />
                  </button>
                </Tooltip>
                <div className="lg:hidden">
                  <UserMenu placement="header" />
                </div>
              </div>
            </div>
          </header>

          <DesktopUpdateNotice />

          {filesWorkspace ? (
            <div className="flex min-h-0 w-full flex-1 flex-col">
              <Suspense fallback={<PanelFallback />}>
                <Outlet />
              </Suspense>
            </div>
          ) : (
            <div
              ref={contentScrollRef}
              className="relative min-h-0 min-w-0 flex-1 overflow-x-auto overflow-y-auto overscroll-contain scrollbar-thin"
            >
              <div className="mx-auto w-full min-w-0 max-w-[var(--layout-content-max-w)] px-4 pb-[max(1.5rem,var(--safe-bottom))] pt-3 sm:px-8 sm:pb-12 sm:pt-4">
                <Suspense fallback={<PanelFallback />}>
                  <Outlet />
                </Suspense>
              </div>
            </div>
          )}
        </main>
        {(onboardingOpen || onboardingRefreshKey > 0 || (
          onboardingAutoEligible &&
          !['dismissed', 'completed'].includes(
            String((user?.settings as Record<string, unknown> | undefined)?.admin_onboarding_v1 ?? ''),
          )
        )) ? (
          <AdminOnboardingTour
            open={onboardingOpen}
            onOpenChange={handleOnboardingOpenChange}
            refreshKey={onboardingRefreshKey}
            onSnapshot={handleOnboardingSnapshot}
            onPresented={handleOnboardingPresented}
          />
        ) : null}
      </div>
    </QuietSurfaceContext.Provider>
  )
}
