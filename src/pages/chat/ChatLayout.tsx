import { Suspense, useEffect, useRef } from 'react'
import { Navigate, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Menu } from 'lucide-react'
import { Sidebar } from '@/components/sidebar/sidebar'
import { ArtifactPanel } from '@/components/chat/artifact-panel'
import { InlineThreadPanel } from '@/components/chat/inline-thread-panel'
import { ConversationFilesPanel } from '@/components/chat/conversation-files-panel'
import { SandboxFilesPanel } from '@/components/chat/sandbox-files-panel'
import { QueuedTurnDispatcher } from '@/components/chat/queued-turn-dispatcher'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { useSettings } from '@/store/settings'
import { useUI } from '@/store/ui'
import { useWorkspaces } from '@/store/workspaces'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { useTheme } from '@/store/theme'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { AnnouncementBar } from '@/components/announcement/announcement-bar'
import { AnnouncementPopup } from '@/components/announcement/announcement-popup'
import { WorkspaceAnnouncementBar, WorkspaceAnnouncementPopup } from '@/components/announcement/workspace-announcement'
import { CreditAdjustmentNotice } from '@/components/credits/credit-adjustment-notice'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { TracedLogo } from '@/components/brand/logo'
import { RouteFade } from '@/components/ui/route-fade'
import { chatRouteAccessRedirect, chatRouteKeys } from '@/lib/chat-route'
import { workspaceSwitchDestination } from '@/lib/workspace-navigation'
import { cn } from '@/lib/utils'
import { usePrivateChatPermission } from '@/hooks/use-private-chat-permission'

export default function ChatLayout() {
  const isDesktop = useMediaQuery(mediaQuery.desktop)
  const syncSystem = useTheme((s) => s.syncSystem)
  const { t } = useTranslation('chat')
  const drawerOpen = useUI((s) => s.navOpen)
  const setDrawerOpen = useUI((s) => s.setNavOpen)
  const pageOwnsTopBar = useUI((s) => s.pageOwnsTopBar)
  const { allowed: canUsePrivateChat, resolved: privateChatPermissionResolved, canRender: canRenderPrivateChat } = usePrivateChatPermission()
  const domainLocked = useWorkspaces((s) => !!s.lockedWorkspaceId)
  const activeWsId = useWorkspaces((s) => s.activeId)
  const workspaceSwitching = useWorkspaces((s) => s.switching)
  const navigate = useNavigate()
  const previousWorkspaceRef = useRef(activeWsId)
  // Coarse section key for page transitions: collapse param routes (e.g.
  // /chat/:id, /projects/:id, /kb/:id) to their first segment so switching
  // conversations within a section doesn't re-fade — only section-to-section
  // navigation (the abrupt jumps) animates.
  const location = useLocation()
  const { pathname } = location
  const privateChat = pathname === '/private-chat'
  // Home ('/') and the chat thread ('/chat', '/chat/:id') are one section so
  // creating a conversation (/ → /chat/:id) doesn't flash a transition.
  const routeKeys = chatRouteKeys(pathname)
  // The first send swaps an optimistic temp id for the server id in the URL.
  // That is the same thread, so keep its Suspense boundary (no remount) —
  // remounting would also cut short the send view transition, whose captured
  // composer must stay in the document.
  const contentKeyRef = useRef(routeKeys.content)
  const rekeyedFrom = (location.state as { rekeyedFrom?: unknown } | null)?.rekeyedFrom
  if (!(typeof rekeyedFrom === 'string' && contentKeyRef.current === `/chat/${rekeyedFrom}`)) {
    contentKeyRef.current = routeKeys.content
  }
  const accessRedirect = chatRouteAccessRedirect(pathname, { domainLocked, canUsePrivateChat })

  useEffect(() => syncSystem(), [syncSystem])

  useEffect(() => {
    const destination = workspaceSwitchDestination(previousWorkspaceRef.current, activeWsId)
    previousWorkspaceRef.current = activeWsId
    if (!destination) return
    // Detail endpoints deliberately support direct links independent of the
    // sidebar's active scope. Keeping a /chat/:id, /projects/:id, or /kb/:id
    // route across a workspace switch would therefore reload the old resource
    // under the new workspace header. A scope change always starts at the new
    // chat home, where every draft and knowledge-base selection is fresh.
    navigate(destination, { replace: true })
  }, [activeWsId, navigate])

  useHotkeys([
    {
      combo: 'mod+b',
      // Keep the sidebar toggle available while the composer is focused.
      whenInputFocused: true,
      handler: () => {
        if (isDesktop) useSettings.getState().toggleSidebar()
        else useUI.getState().toggleNav()
      },
    },
  ])

  if (privateChat && !privateChatPermissionResolved && !canRenderPrivateChat) return <PanelFallback />
  if (accessRedirect && (!privateChat || privateChatPermissionResolved)) return <Navigate to={accessRedirect} replace />

  return (
    <div
      className={cn(
        'flex flex-col h-svh w-full overflow-hidden bg-[var(--color-bg)] text-[var(--color-fg)]',
        // Keep the top/side notch inset, but intentionally let phone layouts
        // run to the visual bottom edge. The composer owns a small regular
        // padding instead of reserving iPhone's home-indicator safe area.
        'pt-[var(--safe-top)]',
        'pl-[var(--safe-left)] pr-[var(--safe-right)]',
      )}
    >
      {!privateChat && <QueuedTurnDispatcher />}
      <AnnouncementPopup />
      <WorkspaceAnnouncementPopup />
      <CreditAdjustmentNotice />
      <div className="flex flex-1 min-h-0 w-full">
      {isDesktop ? (
        <Sidebar variant="desktop" />
      ) : (
        <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
          <SheetContent side="left" size="nav" label={t('sidebar.search')} className="bg-[var(--color-bg-muted)]">
            <Sidebar variant="sheet" onClose={() => setDrawerOpen(false)} />
          </SheetContent>
        </Sheet>
      )}

      <main className="relative flex-1 min-w-0 flex">
        <div className="flex-1 min-w-0 flex flex-col">
          {/* Pinned announcement bar — spans only the chat/content column (NOT the
              sidebar), pinned to the top of the content area; null when inactive. */}
          <AnnouncementBar />
          <WorkspaceAnnouncementBar />
          {/* Mobile top bar — suppressed when the page renders its own combined
              header (e.g. a chat thread) so the two don't stack into two rows. */}
          {!isDesktop && !pageOwnsTopBar && !privateChat && (
            <div className="flex items-center justify-between h-[var(--layout-topbar-h-mobile)] px-2 bg-[var(--color-bg)]/85 backdrop-blur-sm">
              <button
                type="button"
                aria-label={t('commandMenu.actions.toggleSidebar')}
                onClick={() => setDrawerOpen(true)}
                className="inline-flex items-center justify-center size-[var(--tap-min)] rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <Menu size={18} aria-hidden />
              </button>
              <TracedLogo size="sm" />
              <div className="size-[var(--tap-min)]" />
            </div>
          )}

          {/* Page content. The collapsed rail carries its own expand control,
              so the page keeps its full width in both sidebar states. */}
          <RouteFade
            dep={`${routeKeys.section}:${activeWsId ?? 'personal'}`}
            className="flex-1 min-h-0 flex flex-col"
          >
            {/* Switching sections keeps the desktop shell visible with a panel
                loader. On mobile the same canonical loader covers the screen,
                preventing a transient brand top bar from stacking above it. */}
            {/* Reset for every destination, including detail routes. React
                Router schedules navigations as transitions; a previously
                revealed, unkeyed boundary otherwise keeps the OLD page visible
                until the next lazy chunk resolves. The fresh boundary commits
                the target location + sidebar state immediately and confines
                loading feedback to this content pane. */}
            <Suspense
              key={contentKeyRef.current}
              fallback={<PanelFallback scope={isDesktop ? 'panel' : 'screen'} />}
            >
              {/* activeId changes before the new space-scoped stores finish
                  loading. Hide the old route during that interval so a stale
                  conversation, project, or knowledge base cannot be acted on
                  under the newly selected workspace. */}
              {workspaceSwitching ? <PanelFallback scope={isDesktop ? 'panel' : 'screen'} /> : <Outlet />}
            </Suspense>
          </RouteFade>
        </div>

        {/* Right-edge drawers — mutually exclusive (see store coordination). */}
        {!privateChat && <>
          <ArtifactPanel />
          <InlineThreadPanel />
          <SandboxFilesPanel />
          <ConversationFilesPanel />
        </>}
      </main>
      </div>
    </div>
  )
}
