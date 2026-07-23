import { Suspense, useEffect, useRef, type ComponentType } from 'react'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import { useTranslation } from 'react-i18next'
import { User, Wand2, Palette, Sparkles, ShieldCheck, Keyboard, Info, X } from 'lucide-react'
import { DialogOverlay, DialogTitle } from '@/components/ui/dialog'
import { useSettingsModal, type SettingsTab } from '@/store/settings-modal'
import { useAuth } from '@/store/auth'
import { RouteFade } from '@/components/ui/route-fade'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { lazyWithPreload, type PreloadableLazy } from '@/lib/lazy-preload'
import { cn } from '@/lib/utils'

const tabDefs = [
  { key: 'account', icon: User },
  { key: 'personalization', icon: Wand2 },
  { key: 'appearance', icon: Palette },
  { key: 'models', icon: Sparkles },
  { key: 'privacy', icon: ShieldCheck },
  { key: 'shortcuts', icon: Keyboard },
  { key: 'about', icon: Info },
] as const satisfies readonly { key: SettingsTab; icon: ComponentType<{ size?: number | string }> }[]

// Tab bodies stay lazy so the settings chunks load only on first open — the
// dialog shell itself ships in the main bundle (it's mounted app-wide).
// The dynamic-import cycle with the tab pages (they import SettingsSection/
// SettingsRow back from this module) is fine: this module is fully evaluated
// long before a tab chunk resolves.
const tabPages: Record<SettingsTab, PreloadableLazy<ComponentType>> = {
  account: lazyWithPreload(() => import('./Account')),
  personalization: lazyWithPreload(() => import('./Personalization')),
  appearance: lazyWithPreload(() => import('./Appearance')),
  models: lazyWithPreload(() => import('./Models')),
  privacy: lazyWithPreload(() => import('./Privacy')),
  shortcuts: lazyWithPreload(() => import('./Shortcuts')),
  about: lazyWithPreload(() => import('./About')),
}

function preloadSettingsTabs(): Promise<PromiseSettledResult<{ default: ComponentType }>[]> {
  return Promise.allSettled(tabDefs.map(({ key }) => tabPages[key].preload()))
}

// Settings is a high-frequency global surface. Start its small page chunks as
// soon as the shell module is evaluated, rather than making the first click on
// each tab pay a network round-trip. The imports remain split from the initial
// bundle and lazy() consumes these same memoized promises.
void preloadSettingsTabs()

// Settings render as a MODAL over the app (§ settings modal, § 设置-去路由化):
// the current page stays dimmed behind the overlay. The shell is a Radix
// dialog with a left nav rail (vertical on desktop, a top scroll-row on
// mobile) and a scrolling right pane hosting the active tab. It is driven
// entirely by the useSettingsModal store — opening/switching tabs/closing
// never touches the URL, so the page behind keeps its route, state and
// scroll. Old /settings/:tab links and the OAuth ?linked callback still work
// via the redirect route in App.tsx. Radix plays the exit animation on close
// (state-driven now, no route-unmount hack needed). The file keeps its
// pages/settings home because the tab pages and their shared SettingsSection/
// SettingsRow helpers live here.
export default function SettingsDialog() {
  const open = useSettingsModal((s) => s.open)
  const tab = useSettingsModal((s) => s.tab)
  const setTab = useSettingsModal((s) => s.setTab)
  const close = useSettingsModal((s) => s.close)
  const { t } = useTranslation(['settings', 'common'])

  // Auth latch. Signing out INSIDE the dialog (change password / delete
  // account → navigate('/login')) makes AuthGate remount this whole subtree —
  // an effect watching the route here would lose its ref across that remount
  // and leave the dialog floating over the login page (App-level
  // CloseSettingsOnNavigate handles route changes; it can't win this race
  // because the remount happens in the same commit). Gating the Radix `open`
  // on auth hides the dialog synchronously in that very render — no ghost
  // frame — and the effect then settles the store for the next session.
  const authed = useAuth((s) => Boolean(s.user))
  useEffect(() => {
    if (!authed) close()
  }, [authed, close])

  // Each tab starts reading from the top — without this the pane keeps the
  // previous tab's scroll offset (the sticky headers made that look broken).
  const paneRef = useRef<HTMLDivElement>(null)
  // Keep tab bodies mounted after their first visit while this dialog instance
  // remains open. Switching back then preserves local form state and avoids
  // re-running page-level data effects.
  const visitedTabsRef = useRef<Set<SettingsTab>>(new Set([tab]))
  visitedTabsRef.current.add(tab)
  useEffect(() => {
    paneRef.current?.scrollTo({ top: 0 })
  }, [tab])

  return (
    <DialogPrimitive.Root
      open={open && authed}
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
              {tabDefs.map((def) => {
                const active = def.key === tab
                return (
                  <button
                    key={def.key}
                    type="button"
                    onClick={() => setTab(def.key)}
                    aria-current={active ? 'page' : undefined}
                    className={cn(
                      'inline-flex items-center gap-2.5 rounded-[10px] whitespace-nowrap interactive',
                      'px-3 py-2 text-sm font-medium',
                      'sm:w-full',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      active
                        ? 'bg-[var(--color-surface)] text-[var(--color-fg)] shadow-[var(--shadow-xs)] sm:shadow-none sm:bg-[var(--color-bg-muted)]'
                        : 'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]/60',
                    )}
                  >
                    <def.icon size={15} aria-hidden className="shrink-0" />
                    {t(`settings:tabs.${def.key}`)}
                  </button>
                )
              })}
            </nav>
          </div>

          {/* ===== Right pane ===== */}
          {/* The close button sits OUTSIDE the scrolling pane (anchored to the
              dialog frame) so it stays pinned top-right while the pane scrolls. */}
          <DialogPrimitive.Close
            aria-label={t('common:aria.close', { defaultValue: 'Close' })}
            className={cn(
              'max-sm:hidden absolute right-3 top-3 z-20 inline-flex items-center justify-center size-8 rounded-[8px]',
              'text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
              'transition-colors duration-150',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            )}
          >
            <X size={16} aria-hidden />
          </DialogPrimitive.Close>
          {/* scroll-padding clears the pinned page header, so focus/anchor
              auto-scrolls land visible instead of underneath it. */}
          <div ref={paneRef} className="relative min-h-0 min-w-0 flex-1 overflow-y-auto scrollbar-hover [scroll-padding-top:7.5rem]">
            <div
              className={cn(
                // No top padding here — the pinned header carries it instead.
                // With wrapper padding above it, the header would first travel
                // that distance before sticking; owning the padding makes it
                // pinned from the very first scrolled pixel.
                'px-5 sm:px-8 pb-6 sm:pb-8',
                // Pin each settings page's <header> (title + lead) while the
                // body scrolls under it. About has no header and pads itself.
                '[&_header]:sticky [&_header]:top-0 [&_header]:z-10',
                '[&_header]:pt-6 sm:[&_header]:pt-8',
                '[&_header]:bg-[var(--color-surface)] [&_header]:pb-4',
                '[&_header]:border-b [&_header]:border-[var(--color-divider)]',
              )}
            >
              <RouteFade dep={tab}>
                {Array.from(visitedTabsRef.current, (visitedTab) => {
                  const TabPage = tabPages[visitedTab]
                  const active = visitedTab === tab
                  return (
                    <div key={visitedTab} hidden={!active} aria-hidden={!active || undefined}>
                      {/* Each first visit owns a fresh local boundary. Its chunk
                          is normally warm from preload; on a slow connection the
                          fallback replaces only this right pane. Visited pages
                          stay mounted so returning never repeats initialization. */}
                      <Suspense fallback={<PanelFallback />}>
                        <TabPage />
                      </Suspense>
                    </div>
                  )
                })}
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
