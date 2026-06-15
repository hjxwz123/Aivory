import { useEffect } from 'react'
import { Outlet } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { PanelLeftOpen, Menu } from 'lucide-react'
import { Sidebar } from '@/components/sidebar/sidebar'
import { HtmlPreviewPanel } from '@/components/chat/html-preview-panel'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { useSettings } from '@/store/settings'
import { useUI } from '@/store/ui'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useTheme } from '@/store/theme'
import { Tooltip } from '@/components/ui/tooltip'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { Logo } from '@/components/brand/logo'
import { cn } from '@/lib/utils'

export default function ChatLayout() {
  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const collapsed = useSettings((s) => s.sidebarCollapsed)
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation('chat')
  const drawerOpen = useUI((s) => s.navOpen)
  const setDrawerOpen = useUI((s) => s.setNavOpen)
  const pageOwnsTopBar = useUI((s) => s.pageOwnsTopBar)

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
    <div className="flex h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]">
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
          <div
            className={cn(
              'flex-1 min-h-0 flex flex-col',
              isDesktop && collapsed && 'pl-11',
            )}
          >
            <Outlet />
          </div>
        </div>

        {/* Live HTML preview drawer — owns the right edge of the chat area */}
        <HtmlPreviewPanel />
      </main>
    </div>
  )
}
