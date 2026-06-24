import { useEffect } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { PanelLeftOpen, Menu } from 'lucide-react'
import { Sidebar } from '@/components/sidebar/sidebar'
import { HtmlPreviewPanel } from '@/components/chat/html-preview-panel'
import { InlineThreadPanel } from '@/components/chat/inline-thread-panel'
import { ConversationFilesPanel } from '@/components/chat/conversation-files-panel'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { useSettings } from '@/store/settings'
import { useUI } from '@/store/ui'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useTheme } from '@/store/theme'
import { Tooltip } from '@/components/ui/tooltip'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { Logo } from '@/components/brand/logo'
import { RouteFade } from '@/components/ui/route-fade'
import { cn } from '@/lib/utils'

export default function ChatLayout() {
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const collapsed = useSettings((s) => s.sidebarCollapsed)
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation('chat')
  const drawerOpen = useUI((s) => s.navOpen)
  const setDrawerOpen = useUI((s) => s.setNavOpen)
  const pageOwnsTopBar = useUI((s) => s.pageOwnsTopBar)
  // Coarse section key for page transitions: collapse param routes (e.g.
  // /chat/:id, /projects/:id, /settings/*) to their first segment so switching
  // conversations within a section doesn't re-fade — only section-to-section
  // navigation (the abrupt jumps) animates.
  const { pathname } = useLocation()
  // Home ('/') and the chat thread ('/chat', '/chat/:id') are one section so
  // creating a conversation (/ → /chat/:id) doesn't flash a transition.
  const section = pathname === '/' || pathname.startsWith('/chat') ? 'chat' : pathname.split('/')[1] || 'chat'

  useEffect(() => syncSystem(), [syncSystem])

  useHotkeys([
    {
      combo: 'mod+b',
      whenInputFocused: false,
      handler: () => {
        if (isDesktop) useSettings.getState().toggleSidebar()
        else useUI.getState().toggleNav()
      },
    },
  ])

  return (
    <div
      className={cn(
        'flex h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]',
        // Inset the whole app shell by the device safe areas so that, when
        // launched as an installed PWA (fullscreen/standalone), the header,
        // sidebar and bottom composer never slide under the notch or home
        // indicator. env() is 0 in a normal browser tab, so this is a no-op
        // there. box-border keeps total height at 100svh.
        'pt-[env(safe-area-inset-top)] pb-[env(safe-area-inset-bottom)]',
        'pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)]',
      )}
    >
      {isDesktop ? (
        <Sidebar variant="desktop" />
      ) : (
        <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
          <SheetContent side="left" size="md" label={t('sidebar.search')} className="bg-[var(--color-bg-muted)]">
            <Sidebar variant="sheet" onClose={() => setDrawerOpen(false)} />
          </SheetContent>
        </Sheet>
      )}

      <main className="relative flex-1 min-w-0 flex">
        <div className="flex-1 min-w-0 flex flex-col">
          {/* Mobile top bar — suppressed when the page renders its own combined
              header (e.g. a chat thread) so the two don't stack into two rows. */}
          {!isDesktop && !pageOwnsTopBar && (
            <div className="flex items-center justify-between h-12 px-3 border-b border-[var(--color-divider)] bg-[var(--color-bg)]/85 backdrop-blur-sm">
              <button
                type="button"
                aria-label={t('commandMenu.actions.toggleSidebar')}
                onClick={() => setDrawerOpen(true)}
                className="inline-flex items-center justify-center size-9 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]"
              >
                <Menu size={16} aria-hidden />
              </button>
              <Logo size="sm" />
              <div className="w-9" />
            </div>
          )}

          {/* Floating expand toggle when sidebar is collapsed */}
          {isDesktop && collapsed && (
            <div className="absolute left-3 top-3 z-10">
              <Tooltip content={t('commandMenu.actions.toggleSidebar')} side="right">
                <button
                  type="button"
                  aria-label={t('commandMenu.actions.toggleSidebar')}
                  onClick={() => useSettings.getState().toggleSidebar()}
                  className="inline-flex items-center justify-center size-8 rounded-[8px] bg-[var(--color-bg)]/85 backdrop-blur-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive"
                >
                  <PanelLeftOpen size={14} aria-hidden />
                </button>
              </Tooltip>
            </div>
          )}

          {/* Page content. When the sidebar is collapsed on desktop, reserve
              a 44px gutter on the left so the floating expand toggle never
              sits on top of titles, breadcrumbs, or topbar content. */}
          <RouteFade
            dep={section}
            className={cn(
              'flex-1 min-h-0 flex flex-col',
              isDesktop && collapsed && 'pl-11',
            )}
          >
            <Outlet />
          </RouteFade>
        </div>

        {/* Right-edge drawers — mutually exclusive (see store coordination). */}
        <HtmlPreviewPanel />
        <InlineThreadPanel />
        <ConversationFilesPanel />
      </main>
    </div>
  )
}
