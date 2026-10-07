import { WorkspaceIcon } from '@/components/workspace/workspace-icon'
import { type ReactNode, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import {
  Search,
  Plus,
  PanelLeftClose,
  PanelLeftOpen,
  Settings,
  Star,
  Pencil,
  Trash2,
  Archive,
  MoreHorizontal,
  Share2,
  ChevronRight,
  Database,
  ImagePlus,
  ShieldCheck,
  Layers,
  Languages,
  Loader2,
  X,
  ArrowLeftRight,
  ArrowUp,
  ArrowDown,
  FolderOpen,
  LibraryBig,
  CircleHelp,
  FileText,
  UserRound,
  Download,
  PackageCheck,
  Presentation,
  type LucideIcon,
} from 'lucide-react'
import { LogoMark, TracedLogo } from '@/components/brand/logo'
import { useWorkspaces } from '@/store/workspaces'
import {
  CreateWorkspaceDialog,
  SpaceSwitcherButton,
  WorkspaceMenuItems,
  WorkspaceMembersDialog,
} from '@/components/sidebar/workspace-menu'
import { SidebarResizeHandle } from '@/components/sidebar/sidebar-resize-handle'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { Tooltip } from '@/components/ui/tooltip'
import { Kbd } from '@/components/ui/kbd'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { PanelFallback } from '@/components/ui/panel-fallback'
import { SystemUpdateDialog, type SystemUpdateSummary } from '@/components/admin/system-update-dialog'
import { NewProjectDialog } from '@/components/projects/new-project-dialog'
import { MoveToProjectSub } from '@/components/projects/move-to-project-menu'
import { ProjectActionsMenu } from '@/components/projects/project-actions-menu'
import { RenameConversationDialog } from '@/components/chat/rename-conversation-dialog'
import { ShareConversationDialog } from '@/components/chat/share-conversation-dialog'
import { useConversations, sameConvListShape } from '@/store/conversations'
import { resetComposerForNewConversation } from '@/store/composer-prefs'
import { useProjects } from '@/store/projects'
import { useModels } from '@/store/models'
import { useSettings } from '@/store/settings'
import { useAuth } from '@/store/auth'
import { useAiPPT } from '@/store/aippt'
import { useLanguage } from '@/store/language'
import { SUPPORTED_LANGUAGES } from '@/i18n'
import { useCommandMenu } from '@/hooks/use-command-menu'
import { useOpenSettings } from '@/hooks/use-open-settings'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useConversationReorder, type ConversationReorderController } from '@/hooks/use-conversation-reorder'
import { duration } from '@/lib/design-tokens'
import { accentClasses } from '@/lib/project-helpers'
import { partitionConversationNavigation } from '@/lib/conversation-navigation'
import { userCan } from '@/lib/user-permissions'
import { subscribeAccessInvalidation } from '@/lib/access-events'
import { workspaceCapabilitiesForScope } from '@/lib/workspace-permissions'
import { type DateBucket, bucketFor, formatShortcut, cn, truncate } from '@/lib/utils'
import { toast } from '@/hooks/use-toast'
import { exportConversation } from '@/lib/conversation-export'
import { useTranslation } from 'react-i18next'
import { useDomainData } from '@/store/domain-data'
import type { TFunction } from 'i18next'
import type { Conversation } from '@/types/chat'

interface SidebarProps {
  variant?: 'desktop' | 'sheet'
  onClose?: () => void
}

const groupOrder: DateBucket[] = ['today', 'yesterday', 'last_7', 'last_30', 'older']

function isConversationStreaming(conversation: Conversation): boolean {
  return conversation.messages.some((message) => message.streaming)
}

export function Sidebar({ variant = 'desktop', onClose }: SidebarProps) {
  const user = useAuth((s) => s.user)
  const activeWsId = useWorkspaces((s) => s.activeId)
  const activeWorkspacePolicy = useWorkspaces((s) =>
    s.activeId ? s.policies[s.activeId] : undefined,
  )
  const workspacesLoaded = useWorkspaces((s) => s.loaded)
  const workspacePolicyLoading = useWorkspaces((s) =>
    s.activeId ? s.policyLoading[s.activeId] === true : false,
  )
  const switching = useWorkspaces((s) => s.switching)
  const workspacePolicyError = useWorkspaces((s) =>
    activeWsId ? s.policyErrors[activeWsId] : null,
  )
  const workspaceCaps = workspaceCapabilitiesForScope(activeWsId, activeWorkspacePolicy, {
    workspacesLoaded,
    policyLoading: workspacePolicyLoading,
    switching,
    policyError: workspacePolicyError,
  })
  const canDraw = userCan(user, 'allow_drawing') && workspaceCaps.drawing
  const canUseKnowledgeBases = userCan(user, 'allow_knowledge_bases') && workspaceCaps.knowledgeBases
  const activeWorkspace = useWorkspaces((s) => (s.activeId ? s.workspaces.find((w) => w.id === s.activeId) : undefined))
  const canCreateProject = canUseKnowledgeBases &&
    (!activeWsId || activeWorkspace?.can_create_projects === true)
  // The library route remains directly addressable for backwards
  // compatibility, but once all three workspace resource families are turned
  // off there is no useful sidebar destination to show.
  const resourceLibraryVisible = !activeWsId ||
    workspaceCaps.prompts || workspaceCaps.skills || workspaceCaps.mcp
  const navigate = useNavigate()
  const { id: currentId } = useParams<{ id?: string }>()
  const location = useLocation()
  const { t } = useTranslation('chat')
  const { t: tCommon } = useTranslation('common')
  const { t: tProjects } = useTranslation('projects')
  const { t: tNav } = useTranslation('nav')
  // Gate re-renders on the conversation SUMMARY (title/flags/order), not message
  // content — so a streaming turn's per-token message updates don't re-run the
  // filter/sort/bucket pipeline below or reconcile every row (§ perf).
  const allConversationsRaw = useConversations((s) => s.conversations, sameConvListShape)
  const conversationsLoaded = useConversations((s) => s.loaded)
  const conversationsLoading = useConversations((s) => s.loading)
  const conversationsError = useConversations((s) => s.error)
  const loadConversations = useConversations((s) => s.load)
  // §workspaces isolation: the cache can transiently hold rows from another
  // space (loadOne of a cross-space deep link, a stale in-flight list) — the
  // sidebar only ever RENDERS the current space's rows.
  const allConversations = useMemo(
    () => allConversationsRaw.filter((c) => (c.workspaceId ?? '') === (activeWsId ?? '')),
    [allConversationsRaw, activeWsId],
  )
  const hasMore = useConversations((s) => s.hasMore)
  const loadingMore = useConversations((s) => s.loadingMore)
  const loadMore = useConversations((s) => s.loadMore)
  const loadProjectConversations = useConversations((s) => s.loadProjectConversations)
  // Infinite scroll: reveal older conversations when the sentinel nears view.
  const listScrollRef = useRef<HTMLDivElement>(null)
  const loadMoreRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!hasMore || loadingMore) return
    const node = loadMoreRef.current
    const root = listScrollRef.current
    if (!node || !root) return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) void loadMore()
      },
      { root, rootMargin: '300px 0px' },
    )
    io.observe(node)
    return () => io.disconnect()
  }, [hasMore, loadingMore, loadMore])
  const activeConversations = useMemo(
    // Sort by last-updated so a conversation jumps to the top the moment the
    // user sends/continues a message in it (sendMessage bumps updatedAt). The
    // date buckets below preserve this order within each group.
    () =>
      allConversations
        .filter((c) => !c.archived && !c.inline)
        .slice()
        .sort((a, b) => {
          const aStreaming = isConversationStreaming(a)
          const bStreaming = isConversationStreaming(b)
          if (aStreaming !== bStreaming) return aStreaming ? -1 : 1
          return b.updatedAt - a.updatedAt || b.id.localeCompare(a.id)
        }),
    [allConversations],
  )
  // The shared cache intentionally includes project conversations because the
  // project list/detail pages consume it too. Split only at the sidebar render
  // boundary so project chats can never leak into global Starred/date buckets.
  const navigationConversations = useMemo(
    () => partitionConversationNavigation(activeConversations),
    [activeConversations],
  )
  const conversations = navigationConversations.ordinary
  const reorderConversation = useConversations((s) => s.reorderConversation)
  const conversationReorder = useConversationReorder({
    conversations: activeConversations,
    enabled: !switching && (!activeWsId || Boolean(activeWorkspace && activeWorkspace.role !== 'guest')),
    scope: activeWsId ?? '',
    scrollRef: listScrollRef,
    onReorder: reorderConversation,
  })
  // Workspace discovery runs before the first history request. Keep one
  // loading state across both steps, then fade in the completed list. A usable
  // same-space cache stays visible during background refreshes.
  const historyPending = switching ||
    ((!workspacesLoaded || !conversationsLoaded) && !conversationsError) ||
    (conversationsLoading && activeConversations.length === 0)
  const projectConversationsById = navigationConversations.byProject
  const projects = useProjects((s) => s.projects)
  // §4.20: show the Draw entry only when an image model is configured.
  const hasImageModels = useModels((s) => s.imageModels.length > 0) && canDraw
  // Draw links to '/?mode=draw' — same pathname as New chat, so its active
  // state must read the query string (NavLink's isActive ignores search).
  // Gated on hasImageModels: when the Draw row isn't rendered, New chat keeps
  // its usual look instead of leaving no entry highlighted.
  const drawActive =
    hasImageModels &&
    location.pathname === '/' &&
    new URLSearchParams(location.search).get('mode') === 'draw'
  // "New chat" is the current page ONLY on the plain new-chat home ('/', not
  // draw mode). Elsewhere (/chat/:id, /projects, /kb, …) it's just an action,
  // not the selected entry — so it must not keep a permanent "selected" fill.
  // Private mode is the same home screen with a different composer, so it
  // keeps the entry selected too.
  const newChatActive = (location.pathname === '/' && !drawActive) || location.pathname === '/private-chat'
  // Projects, Files, and Skills are their own routes — highlight their entry
  // when the current path is under them.
  const filesActive = location.pathname === '/files'
  const aiPptActive = location.pathname === '/ppt' || location.pathname.startsWith('/ppt/')
  // § AI PPT: the entry appears only when the deployment actually configured the
  // Docmee integration, so an unused third-party surface never shows up.
  const aiPptEnabled = useAiPPT((s) => s.config?.enabled === true)
  const loadAiPPTConfig = useAiPPT((s) => s.load)
  const knowledgeBasesActive = location.pathname === '/kb' || location.pathname.startsWith('/kb/')
  const skillsActive = location.pathname === '/skills' || location.pathname.startsWith('/skills/')
  const sortedProjects = useMemo(
    () =>
      projects
        .slice()
        .sort((a, b) => {
          if ((a.pinned ? 1 : 0) !== (b.pinned ? 1 : 0)) return a.pinned ? -1 : 1
          const aUpdatedAt = Math.max(
            a.updatedAt,
            projectConversationsById.get(a.id)?.[0]?.updatedAt ?? 0,
          )
          const bUpdatedAt = Math.max(
            b.updatedAt,
            projectConversationsById.get(b.id)?.[0]?.updatedAt ?? 0,
          )
          return bUpdatedAt - aUpdatedAt
        }),
    [projectConversationsById, projects],
  )
  const setOpen = useCommandMenu((s) => s.setOpen)
  const collapsed = useSettings((s) => s.sidebarCollapsed) && variant === 'desktop'
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  // Collapse/expand motion: while the rail's width animates, the content keeps
  // its expanded layout at the full width and is simply clipped (labels fade),
  // so icons never shift. The compact rail layout takes over only once the
  // collapse has finished; expanding switches back immediately.
  const [railSettled, setRailSettled] = useState(collapsed)
  const [expanding, setExpanding] = useState(false)
  const previousCollapsedRef = useRef(collapsed)
  useEffect(() => {
    if (previousCollapsedRef.current === collapsed) return
    previousCollapsedRef.current = collapsed
    // The aside's width transitionend settles the layout (see onTransitionEnd
    // below); this timer is only the fallback for a missed or skipped event.
    const wait = reducedMotion ? 0 : duration.base + 120
    if (collapsed) {
      const timer = window.setTimeout(() => setRailSettled(true), wait)
      return () => window.clearTimeout(timer)
    }
    setRailSettled(false)
    setExpanding(true)
    const timer = window.setTimeout(() => setExpanding(false), wait)
    return () => window.clearTimeout(timer)
  }, [collapsed, reducedMotion])
  const compact = collapsed && railSettled
  const holdExpandedLayout = !compact && (collapsed || expanding)
  const sidebarWidth = useSettings((s) => s.sidebarWidth)
  const setSidebarWidth = useSettings((s) => s.setSidebarWidth)
  const toggleSidebar = useSettings((s) => s.toggleSidebar)
  const sidebarRef = useRef<HTMLElement>(null)
  const sidebarId = useId()
  const [newProjectOpen, setNewProjectOpen] = useState(false)
  // Which list edges have rows hidden beyond them — drives the top/bottom fades.
  const listContentRef = useRef<HTMLDivElement>(null)
  const [listEdges, setListEdges] = useState({ top: false, bottom: false })
  const measureListEdges = useCallback(() => {
    const list = listScrollRef.current
    if (!list) return
    const top = list.scrollTop > 2
    const bottom = list.scrollTop + list.clientHeight < list.scrollHeight - 2
    setListEdges((current) => (current.top === top && current.bottom === bottom ? current : { top, bottom }))
  }, [])
  useEffect(() => {
    if (compact) return
    measureListEdges()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measureListEdges)
    if (listScrollRef.current) observer.observe(listScrollRef.current)
    if (listContentRef.current) observer.observe(listContentRef.current)
    return () => observer.disconnect()
  }, [compact, measureListEdges])
  const [expandedProjectIds, setExpandedProjectIds] = useState<Set<string>>(() => new Set())
  const [loadingProjectIds, setLoadingProjectIds] = useState<Set<string>>(() => new Set())
  const loadedProjectIdsRef = useRef<Set<string>>(new Set())
  const loadingProjectIdsRef = useRef<Set<string>>(new Set())
  const expandedWorkspaceIdRef = useRef(activeWsId)
  const activeProjectId = useMemo(() => {
    if (!currentId) return undefined
    if (location.pathname.startsWith('/projects/')) {
      return projects.some((project) => project.id === currentId) ? currentId : undefined
    }
    if (location.pathname.startsWith('/chat/')) {
      return activeConversations.find((conversation) => conversation.id === currentId)?.projectId
    }
    return undefined
  }, [activeConversations, currentId, location.pathname, projects])

  // Expansion belongs to the current workspace. Prune deleted projects and
  // always reveal whichever project owns the active route/conversation.
  useEffect(() => {
    setExpandedProjectIds((previous) => {
      const workspaceChanged = expandedWorkspaceIdRef.current !== activeWsId
      expandedWorkspaceIdRef.current = activeWsId
      const availableIds = new Set(projects.map((project) => project.id))
      const next = workspaceChanged
        ? new Set<string>()
        : new Set([...previous].filter((projectId) => availableIds.has(projectId)))
      if (activeProjectId && availableIds.has(activeProjectId)) next.add(activeProjectId)
      if (
        !workspaceChanged &&
        next.size === previous.size &&
        [...next].every((projectId) => previous.has(projectId))
      ) {
        return previous
      }
      return next
    })
  }, [activeProjectId, activeWsId, projects])

  useEffect(() => {
    loadedProjectIdsRef.current = new Set()
    loadingProjectIdsRef.current = new Set()
    setLoadingProjectIds(new Set())
  }, [activeWsId])

  // § AI PPT: one cached config read decides whether the entry exists. The store
  // de-duplicates concurrent loads, so mounting several sidebars is harmless.
  useEffect(() => {
    void loadAiPPTConfig()
  }, [activeWsId, loadAiPPTConfig])

  useEffect(() => subscribeAccessInvalidation(() => { void loadAiPPTConfig(true) }), [loadAiPPTConfig])

  function ensureProjectConversations(projectId: string) {
    if (loadedProjectIdsRef.current.has(projectId) || loadingProjectIdsRef.current.has(projectId)) return
    loadingProjectIdsRef.current.add(projectId)
    setLoadingProjectIds((previous) => new Set(previous).add(projectId))
    void loadProjectConversations(projectId).then((loaded) => {
      if (loaded) loadedProjectIdsRef.current.add(projectId)
      loadingProjectIdsRef.current.delete(projectId)
      setLoadingProjectIds((previous) => {
        const next = new Set(previous)
        next.delete(projectId)
        return next
      })
    })
  }

  function toggleProject(projectId: string, expanded: boolean) {
    setExpandedProjectIds((previous) => {
      const next = new Set(previous)
      if (expanded) next.delete(projectId)
      else next.add(projectId)
      return next
    })
    if (!expanded) ensureProjectConversations(projectId)
  }

  useEffect(() => {
    if (!activeProjectId) return
    if (loadedProjectIdsRef.current.has(activeProjectId) || loadingProjectIdsRef.current.has(activeProjectId)) {
      return
    }
    const projectId = activeProjectId
    loadingProjectIdsRef.current.add(projectId)
    setLoadingProjectIds((previous) => new Set(previous).add(projectId))
    void loadProjectConversations(projectId).then((loaded) => {
      if (loaded) loadedProjectIdsRef.current.add(projectId)
      loadingProjectIdsRef.current.delete(projectId)
      setLoadingProjectIds((previous) => {
        const next = new Set(previous)
        next.delete(projectId)
        return next
      })
    })
  }, [activeProjectId, loadProjectConversations])

  // Reveal the ACTIVE conversation in the history list whenever the user lands
  // on one that isn't already visible — arriving via a gallery tile, the command
  // menu, a project, or a deep link to a very old chat. Without this, jumping far
  // down a long list leaves the row off-screen and the user can't find it.
  // Scrolls ONLY the list container (never the page) and only when the row is
  // off-screen, so clicking an already-visible row never jumps. Runs once per
  // active id — a deep-linked row is inserted by loadOne asynchronously, so the
  // effect re-runs as `conversations` updates until the row exists (and bails
  // O(1) once handled). Resets on collapse so re-expanding re-centers it.
  const scrolledForIdRef = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (compact) {
      scrolledForIdRef.current = undefined
      return
    }
    if (!currentId || scrolledForIdRef.current === currentId) return
    const container = listScrollRef.current
    if (!container) return
    const row = container.querySelector<HTMLElement>(`[data-conversation-id="${CSS.escape(currentId)}"]`)
    if (!row) return // not in the list yet (loadOne pending / cross-workspace) — retry on next update
    const cr = container.getBoundingClientRect()
    const rr = row.getBoundingClientRect()
    if (rr.top < cr.top || rr.bottom > cr.bottom) {
      // Off-screen → bring it roughly to the middle so it's easy to spot.
      const target = Math.max(0, container.scrollTop + (rr.top - cr.top) - (cr.height - rr.height) / 2)
      // Near jumps animate; a far jump (deep-linked OLD chat hundreds of rows
      // down) snaps so the user isn't stuck watching a long scroll.
      const near = Math.abs(target - container.scrollTop) < container.clientHeight * 3
      container.scrollTo({ top: target, behavior: !reducedMotion && near ? 'smooth' : 'auto' })
    }
    scrolledForIdRef.current = currentId
  }, [activeConversations, compact, currentId, expandedProjectIds, reducedMotion])

  function startNewChat() {
    // A new chat starts from model defaults, never a prior conversation's
    // per-model hand-picked tool subset.
    resetComposerForNewConversation()
    // Go to the empty home screen — the conversation is created only when the
    // user sends the first message, so clicking "New chat" never piles up blank
    // conversations.
    navigate('/')
    onClose?.()
  }

  // Group conversations
  const starred = conversations.filter((c) => c.starred)
  const others = conversations.filter((c) => !c.starred)
  const grouped: Record<DateBucket, typeof conversations> = {
    today: [],
    yesterday: [],
    last_7: [],
    last_30: [],
    older: [],
  }
  const now = Date.now()
  for (const c of others) grouped[bucketFor(isConversationStreaming(c) ? now : c.updatedAt)].push(c)

  return (
    <aside
      id={sidebarId}
      ref={sidebarRef}
      data-variant={variant}
      data-collapsed={collapsed ? 'true' : 'false'}
      aria-label={t('sidebar.navAria', { defaultValue: 'Conversation navigation' })}
      style={variant === 'desktop' && !collapsed ? { width: `${sidebarWidth}px` } : undefined}
      onTransitionEnd={(event) => {
        if (event.target !== event.currentTarget || event.propertyName !== 'width') return
        if (collapsed) setRailSettled(true)
        else setExpanding(false)
      }}
      className={cn(
        'relative flex h-full shrink-0 flex-col bg-[var(--color-sidebar-bg)]',
        variant === 'desktop' && collapsed && 'w-[var(--layout-sidebar-w-collapsed)]',
        variant === 'sheet' && 'w-full',
        'transition-[width] duration-[var(--duration-base)] ease-[var(--ease-out)] data-[resizing=true]:transition-none',
      )}
    >
      {/* Clip layer: the resize handle sits outside the rail, so clipping
          lives here instead of on the aside. */}
      <div className="flex h-full min-h-0 w-full flex-col overflow-hidden">
      <div
        className="flex h-full min-h-0 w-full shrink-0 flex-col"
        style={holdExpandedLayout ? { width: `${sidebarWidth}px` } : undefined}
      >
      {/* Header — inside a workspace the brand slot shows the WORKSPACE NAME
          (§workspaces spec: sidebar 上方原先显示 aivory 的地方显示工作空间名称).
          Keyed on the active space so switching replays a fade-in (§ workspace
          switch animation) instead of the name jump-cutting. */}
      <div className="flex h-[56px] shrink-0 items-center justify-between px-3 max-sm:h-12 max-sm:px-2">
        {/* The brand mark sits on the nav icons' x-line (18px) in both rail
            states, so collapsing never moves it. */}
        {!compact ? (
          activeWorkspace ? (
            <div key={activeWorkspace.id} className="page-enter ml-1.5 flex min-w-0 items-center gap-1.5 max-sm:ml-2.5">
              <Link
                to="/"
                onClick={() => {
                  resetComposerForNewConversation()
                  onClose?.()
                }}
                className="inline-flex min-w-0 items-center gap-2"
                aria-label={activeWorkspace.name}
                title={activeWorkspace.name}
              >
                <WorkspaceIcon icon={activeWorkspace.icon_url} size={20} />
                <span className="truncate font-sans text-[15px] font-semibold text-[var(--color-fg)]">{activeWorkspace.name}</span>
              </Link>
              {/* Prominent escape hatch back to the personal space, right next to
                  the workspace name (§workspaces: 标题旁显著切换按钮). Sage =
                  the AI/workspace status accent; always visible, not hover-only. */}
              <Tooltip content={t('workspace.backToPersonal', { defaultValue: 'Switch to personal space' })}>
                <button
                  type="button"
                  onClick={() => void useWorkspaces.getState().switchTo(null)}
                  aria-label={t('workspace.backToPersonal', { defaultValue: 'Switch to personal space' })}
                  className="inline-flex size-6 shrink-0 items-center justify-center rounded-[7px] bg-[var(--color-secondary-soft)] text-[var(--color-secondary)] hover:bg-[var(--color-secondary)] hover:text-[var(--color-fg-inverted)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <ArrowLeftRight size={13} aria-hidden />
                </button>
              </Tooltip>
            </div>
          ) : (
          <Link
            key="personal"
            to="/"
            onClick={() => {
              resetComposerForNewConversation()
              onClose?.()
            }}
            className="page-enter ml-1.5 inline-flex items-center max-sm:ml-2.5"
            aria-label={tCommon('aria.homeLink')}
          >
            <TracedLogo size="sm" />
          </Link>
          )
        ) : (
          // The collapsed rail owns its own expand control: the mark turns into
          // the panel icon on hover/focus, so no second floating button has to
          // sit on top of the page content.
          <Tooltip content={t('commandMenu.actions.toggleSidebar')} shortcut={formatShortcut('B')} side="right">
            <button
              type="button"
              onClick={toggleSidebar}
              aria-label={t('commandMenu.actions.toggleSidebar')}
              className="group/rail-toggle relative -ml-[3px] inline-flex size-9 shrink-0 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-sidebar-hover)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <LogoMark
                size={18}
                className="transition-opacity duration-[var(--duration-fast)] group-hover/rail-toggle:opacity-0 group-focus-visible/rail-toggle:opacity-0"
              />
              <PanelLeftOpen
                size={15}
                aria-hidden
                className="absolute opacity-0 transition-opacity duration-[var(--duration-fast)] group-hover/rail-toggle:opacity-100 group-focus-visible/rail-toggle:opacity-100"
              />
            </button>
          </Tooltip>
        )}
        {!compact && variant === 'desktop' && (
          <Tooltip content={t('commandMenu.actions.toggleSidebar')} shortcut={formatShortcut('B')}>
            <button
              type="button"
              onClick={toggleSidebar}
              aria-label={t('commandMenu.actions.toggleSidebar')}
              className="inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-sidebar-hover)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              <PanelLeftClose size={14} aria-hidden />
            </button>
          </Tooltip>
        )}
        {/* Mobile drawer gets an explicit 44px close (the scrim tap alone isn't
            discoverable on touch). */}
        {variant === 'sheet' && (
          <button
            type="button"
            onClick={onClose}
            aria-label={tCommon('actions.close', { defaultValue: 'Close' })}
            className="inline-flex size-[var(--tap-min)] items-center justify-center rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-sidebar-hover)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-9 max-sm:rounded-[8px]"
          >
            <X size={18} aria-hidden />
          </button>
        )}
      </div>

      {/* Actions — every entry is one SidebarNavItem so hover, selection and
          shortcut hints stay identical across the rail. */}
      <div className="flex flex-col gap-px px-2">
        <SidebarNavItem
          icon={Plus}
          iconClassName="text-[var(--color-accent)]"
          label={t('sidebar.newChat')}
          collapsed={collapsed}
          active={newChatActive}
          shortcut={formatShortcut('O', { shift: true })}
          onClick={startNewChat}
        />
        <SidebarNavItem
          icon={Search}
          label={t('sidebar.search')}
          collapsed={collapsed}
          shortcut={formatShortcut('K')}
          onClick={() => setOpen(true)}
        />

        {/* §4.20 Draw — opens a new conversation pre-set to an image model. */}
        {hasImageModels && (
          <SidebarNavItem
            icon={ImagePlus}
            label={tNav('draw', { defaultValue: 'Draw' })}
            collapsed={collapsed}
            active={drawActive}
            to="/?mode=draw"
            onClick={onClose}
          />
        )}

        <div
          aria-hidden
          className="my-1 h-1 shrink-0"
        />

        {/* § AI PPT — the Docmee iframe workbench. Rendered only when the
            deployment configured the integration (see useAiPPT). */}
        {aiPptEnabled && (
          <SidebarNavItem
            icon={Presentation}
            label={tNav('aiPpt', { defaultValue: 'AI PPT' })}
            collapsed={collapsed}
            active={aiPptActive}
            to="/ppt"
            onClick={onClose}
          />
        )}

        {/* § user files page — every upload (chat + KB) with the storage meter.
            The page is scoped to the user's PERSONAL uploads (GET /me/files),
            so it's hidden inside a workspace where files are shared, not owned. */}
        {!activeWorkspace && (
          <SidebarNavItem
            icon={FolderOpen}
            label={tNav('files', { defaultValue: 'Files' })}
            collapsed={collapsed}
            active={filesActive}
            to="/files"
            onClick={onClose}
          />
        )}

        {canUseKnowledgeBases && (
          <SidebarNavItem
            icon={Database}
            label={tNav('knowledgeBases', { defaultValue: 'Knowledge' })}
            collapsed={collapsed}
            active={knowledgeBasesActive}
            to="/kb"
            onClick={onClose}
          />
        )}

        {resourceLibraryVisible && (
          <SidebarNavItem
            icon={LibraryBig}
            label={tNav('resources', { defaultValue: 'Library' })}
            collapsed={collapsed}
            active={skillsActive}
            to="/skills"
            onClick={onClose}
          />
        )}
      </div>

      {/* History has a loading indicator from first mount through hydration,
          and while changing spaces; resolved rows fade in at the same place. */}
      {!compact && (
        <div
          className={cn(
            'relative mt-1 flex-1 min-h-0 transition-opacity duration-[var(--duration-fast)] ease-[var(--ease-out)]',
            collapsed ? 'pointer-events-none opacity-0' : 'animate-[fade-in_var(--duration-base)_var(--ease-out)]',
          )}
        >
          <div
            ref={listScrollRef}
            onScroll={measureListEdges}
            inert={historyPending}
            aria-hidden={historyPending || undefined}
            className={cn(
              'h-full overflow-y-auto scrollbar-thin transition-opacity duration-200 motion-reduce:transition-none',
              historyPending && 'opacity-0 pointer-events-none',
            )}
          >
            <div ref={listContentRef} className="pb-2">
            {canUseKnowledgeBases ? <section className="pt-2">
              <div className="flex items-center pr-3.5">
                <SidebarSectionLabel className="min-w-0 flex-1">
                  <Link
                    to="/projects"
                    onClick={onClose}
                    aria-current={location.pathname === '/projects' ? 'page' : undefined}
                    className="rounded-[5px] interactive hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    {tNav('projects')}
                  </Link>
                </SidebarSectionLabel>
                {canCreateProject ? (
                  <Tooltip content={tProjects('nav.newProject')}>
                    <button
                      type="button"
                      onClick={() => setNewProjectOpen(true)}
                      aria-label={tProjects('nav.newProject')}
                      className="-mt-1 inline-flex size-6 shrink-0 items-center justify-center rounded-[6px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-sidebar-hover)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-lg:size-8"
                    >
                      <Plus size={13} aria-hidden />
                    </button>
                  </Tooltip>
                ) : null}
              </div>

              {sortedProjects.length === 0 ? (
                <p className="px-[18px] py-1.5 text-[12px] text-[var(--color-fg-subtle)]">
                  {tProjects('nav.empty')}
                </p>
              ) : (
                <ul>
                  {sortedProjects.map((project) => {
                    const accent = accentClasses(project.accent)
                    const expanded = expandedProjectIds.has(project.id)
                    const projectConversations = projectConversationsById.get(project.id) ?? []
                    const childListId = `${sidebarId}-project-${project.id}`
                    const projectActive = activeProjectId === project.id
                    const chip = (className?: string) => (
                      <span
                        className={cn(
                          'inline-flex size-5 shrink-0 items-center justify-center rounded-[6px] text-[11px] font-medium',
                          accent.chip,
                          className,
                        )}
                        aria-hidden
                      >
                        {project.emoji?.trim() || project.name.trim().slice(0, 1).toUpperCase()}
                      </span>
                    )
                    const chevron = (
                      <ChevronRight
                        size={13}
                        aria-hidden
                        className={cn(
                          'transition-transform duration-[var(--duration-base)] ease-[var(--ease-out)]',
                          expanded && 'rotate-90',
                        )}
                      />
                    )
                    return (
                      <li key={project.id}>
                        <div
                          className={cn(
                            'group/project relative mx-2 my-px flex min-h-8 items-center gap-2 rounded-[8px] pl-2.5 pr-1.5 interactive max-lg:min-h-[var(--tap-min)] max-sm:!min-h-9',
                            projectActive
                              ? 'bg-[var(--color-sidebar-active)] shadow-[var(--shadow-xs)]'
                              : 'hover:bg-[var(--color-sidebar-hover)]',
                          )}
                        >
                          {/* Desktop: the project chip doubles as the disclosure
                              toggle and turns into a chevron while the row is
                              hovered or focused, keeping the chip on the same
                              x-line as every other row icon. */}
                          <button
                            type="button"
                            aria-label={project.name}
                            aria-expanded={expanded}
                            aria-controls={childListId}
                            onClick={() => toggleProject(project.id, expanded)}
                            className="relative inline-flex size-5 shrink-0 items-center justify-center rounded-[6px] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-lg:hidden"
                          >
                            {chip('transition-opacity duration-[var(--duration-fast)] group-hover/project:opacity-0 group-focus-within/project:opacity-0')}
                            <span className="absolute inset-0 inline-flex items-center justify-center opacity-0 transition-opacity duration-[var(--duration-fast)] group-hover/project:opacity-100 group-focus-within/project:opacity-100">
                              {chevron}
                            </span>
                          </button>
                          {chip('lg:hidden')}
                          <Link
                            to={`/projects/${project.id}`}
                            onClick={onClose}
                            aria-current={location.pathname === `/projects/${project.id}` ? 'page' : undefined}
                            title={project.name}
                            className={cn(
                              'flex min-w-0 flex-1 items-center self-stretch rounded-[6px] text-[13px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                              projectActive
                                ? 'font-medium text-[var(--color-fg)]'
                                : 'text-[var(--color-fg-muted)] group-hover/project:text-[var(--color-fg)]',
                            )}
                          >
                            <span className="min-w-0 flex-1 truncate">{truncate(project.name, 30)}</span>
                          </Link>
                          {/* Touch has no hover to reveal the chip's chevron, so
                              the drawer keeps an explicit disclosure control. */}
                          <button
                            type="button"
                            aria-label={project.name}
                            aria-expanded={expanded}
                            aria-controls={childListId}
                            onClick={() => toggleProject(project.id, expanded)}
                            className="inline-flex size-8 shrink-0 items-center justify-center rounded-[6px] text-[var(--color-fg-faint)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] lg:hidden"
                          >
                            {chevron}
                          </button>
                          <ProjectActionsMenu
                            project={project}
                            canUseKnowledgeBases={canUseKnowledgeBases}
                            canManageProject={project.canDelete ?? (
                              project.userId === user?.id || activeWorkspace?.role === 'admin'
                            )}
                            canChangeProjectVisibility={Boolean(
                              project.workspaceId && (
                                project.userId === user?.id || activeWorkspace?.role === 'admin'
                              ),
                            )}
                            canDeleteConversations={userCan(user, 'allow_conversation_deletion') && (
                              !project.workspaceId || activeWorkspace?.can_delete_conversations === true
                            )}
                            placement="sidebar"
                          />
                        </div>
                        <ProjectConversationDisclosure
                          id={childListId}
                          expanded={expanded}
                        >
                          {() => (
                            <ul>
                              {loadingProjectIds.has(project.id) && projectConversations.length === 0 ? (
                                <li
                                  role="status"
                                  aria-label={tCommon('common.loading')}
                                  className="flex min-h-8 items-center pl-[46px] text-[var(--color-fg-subtle)]"
                                >
                                  <Loader2 size={12} className="animate-spin" aria-hidden />
                                </li>
                              ) : null}
                              {projectConversations.map((conversation) => (
                                <ConversationItem
                                  key={conversation.id}
                                  conversation={conversation}
                                  active={conversation.id === currentId}
                                  onSelect={onClose}
                                  t={t}
                                  nested
                                  dense={variant === 'sheet'}
                                  reorder={conversationReorder}
                                />
                              ))}
                              {!loadingProjectIds.has(project.id) &&
                                loadedProjectIdsRef.current.has(project.id) &&
                                projectConversations.length === 0 ? (
                                <li className="flex min-h-8 items-center pl-[46px] pr-2 text-[12px] text-[var(--color-fg-subtle)]">
                                  {tProjects('detail.chatsEmpty')}
                                </li>
                              ) : null}
                            </ul>
                          )}
                        </ProjectConversationDisclosure>
                      </li>
                    )
                  })}
                </ul>
              )}
            </section> : null}

            {starred.length > 0 && (
              <Group
                label={t('sidebar.starred')}
                items={starred}
                currentId={currentId}
                onSelect={onClose}
                t={t}
                dense={variant === 'sheet'}
                reorder={conversationReorder}
              />
            )}
            {groupOrder.map(
              (g) =>
                grouped[g].length > 0 && (
                  <Group
                    key={g}
                    label={t(`buckets.${g}`)}
                    items={grouped[g]}
                    currentId={currentId}
                    onSelect={onClose}
                    t={t}
                    dense={variant === 'sheet'}
                    reorder={conversationReorder}
                  />
                ),
            )}
            <span className="sr-only" role="status" aria-live="polite">
              {conversationReorder.saving
                ? t('sidebar.reorderSaving')
                : conversationReorder.dropTarget
                  ? t(conversationReorder.dropTarget.position === 'before' ? 'sidebar.dropBefore' : 'sidebar.dropAfter', {
                    title: activeConversations.find((conversation) => conversation.id === conversationReorder.dropTarget?.id)?.title,
                  })
                  : ''}
            </span>
            {hasMore && (
              <div
                ref={loadMoreRef}
                className="flex items-center justify-center py-3 text-[11px] text-[var(--color-fg-subtle)]"
              >
                {loadingMore ? <Loader2 size={13} className="animate-spin" aria-hidden /> : null}
              </div>
            )}
            {!historyPending && conversations.length === 0 && (conversationsError ? (
              <div className="flex flex-col items-center gap-2 px-[18px] py-6 text-center text-xs text-[var(--color-fg-muted)]">
                <p role="status">{t('empty.recentLoadFailed')}</p>
                <button type="button" onClick={() => void loadConversations()}
                  className="rounded-[5px] px-2 py-1 underline underline-offset-4 hover:bg-[var(--color-sidebar-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]">
                  {t('imageEdit.retry')}
                </button>
              </div>
            ) : (
              <p className="px-[18px] py-6 text-xs text-[var(--color-fg-subtle)] text-center">
                {t('sidebar.empty')}
              </p>
            ))}
            </div>
          </div>
          {/* Edge fades only appear while more rows sit beyond that edge, so a
              row is never cut hard against the header actions or the footer. */}
          <div
            aria-hidden
            className={cn(
              'pointer-events-none absolute inset-x-0 top-0 z-10 h-5',
              reducedMotion ? 'transition-none' : 'transition-opacity duration-150',
              listEdges.top && !historyPending ? 'opacity-100' : 'opacity-0',
            )}
            style={{ background: 'linear-gradient(to bottom, var(--color-sidebar-bg), transparent)' }}
          />
          <div
            aria-hidden
            className={cn(
              'pointer-events-none absolute inset-x-0 bottom-0 z-10 h-8',
              reducedMotion ? 'transition-none' : 'transition-opacity duration-150',
              listEdges.bottom && !historyPending ? 'opacity-100' : 'opacity-0',
            )}
            style={{ background: 'linear-gradient(to top, var(--color-sidebar-bg), transparent)' }}
          />
          {historyPending && (
            <div className="absolute inset-0 flex items-center justify-center">
              <PanelFallback scope="fill" />
            </div>
          )}
        </div>
      )}

      {/* Footer — the avatar plus a space switcher beside it. The switcher is a
          flat picker (personal + every workspace) shown whenever the user has
          any workspace, so it works both in the personal space (pick one to
          enter) and inside a workspace (§workspaces 头像旁切换按钮). */}
      {/* The avatar keeps one x-position (10px) in both layouts. */}
      <div className="mt-auto p-1">
        <div className={cn('flex items-center gap-1', compact && 'flex-col')}>
          <div className={cn('min-w-0', !compact && 'flex-1')}>
            <UserMenu collapsed={compact} />
          </div>
          <SpaceSwitcherButton />
        </div>
      </div>
      </div>
      </div>

      <NewProjectDialog open={newProjectOpen && canCreateProject} onOpenChange={setNewProjectOpen} />

      {/* Keep the visual separator at the edge, but place it last in DOM order so
          keyboard focus reaches the sidebar's navigation before its resize control. */}
      {variant === 'desktop' && !collapsed ? (
        <SidebarResizeHandle
          label={t('sidebar.resize')}
          controlsId={sidebarId}
          targetRef={sidebarRef}
          width={sidebarWidth}
          onCommit={setSidebarWidth}
        />
      ) : null}
    </aside>
  )
}

function SidebarSectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  // Sentence case at a readable size: uppercase + wide tracking does nothing
  // for CJK labels and made 10px Latin labels hard to scan.
  return (
    <h3 className={cn('px-[18px] pb-1 text-[12px] font-medium leading-5 text-[var(--color-fg-subtle)]', className)}>
      {children}
    </h3>
  )
}

interface SidebarNavItemProps {
  icon: LucideIcon
  label: string
  collapsed: boolean
  active?: boolean
  /** Shown as one key chip only while the row is hovered or focused. */
  shortcut?: string
  /** Renders a router link; without it the row is a button. */
  to?: string
  onClick?: () => void
  iconClassName?: string
}

function SidebarNavItem({
  icon: Icon,
  label,
  collapsed,
  active = false,
  shortcut,
  to,
  onClick,
  iconClassName,
}: SidebarNavItemProps) {
  const className = cn(
    'group/nav inline-flex h-8 items-center gap-2 rounded-[8px] text-[13px] interactive max-lg:h-[var(--tap-min)] max-sm:!h-9 max-sm:gap-1.5',
    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
    active
      ? 'bg-[var(--color-sidebar-active)] font-medium text-[var(--color-fg)] shadow-[var(--shadow-xs)]'
      : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-sidebar-hover)] hover:text-[var(--color-fg)]',
    // Same box in both rail states: the icon keeps its x while the label fades
    // and the rail's clip edge closes over it.
    'w-full overflow-hidden px-2.5',
  )
  const content = (
    <>
      <Icon size={15} aria-hidden className={cn('shrink-0', iconClassName)} />
      <span
        className={cn(
          'min-w-0 flex-1 truncate text-left transition-opacity duration-[var(--duration-fast)] ease-[var(--ease-out)]',
          collapsed && 'opacity-0',
        )}
      >
        {label}
      </span>
      {!collapsed && shortcut ? (
        <Kbd className="opacity-0 transition-opacity duration-[var(--duration-fast)] group-hover/nav:opacity-100 group-focus-visible/nav:opacity-100 max-lg:hidden">
          {shortcut}
        </Kbd>
      ) : null}
    </>
  )
  return (
    <Tooltip content={collapsed ? label : ''} shortcut={collapsed ? shortcut : undefined} side="right">
      {to ? (
        <Link
          to={to}
          onClick={onClick}
          aria-current={active ? 'page' : undefined}
          aria-label={collapsed ? label : undefined}
          className={className}
        >
          {content}
        </Link>
      ) : (
        <button
          type="button"
          onClick={onClick}
          aria-current={active ? 'page' : undefined}
          aria-label={collapsed ? label : undefined}
          className={className}
        >
          {content}
        </button>
      )}
    </Tooltip>
  )
}

function Group({
  label,
  items,
  currentId,
  onSelect,
  t,
  dense = false,
  reorder,
}: {
  label: string
  items: ReturnType<typeof useConversations.getState>['conversations']
  currentId: string | undefined
  onSelect?: () => void
  t: TFunction<'chat'>
  dense?: boolean
  reorder?: ConversationReorderController
}) {
  return (
    <div className="pt-3">
      <SidebarSectionLabel>{label}</SidebarSectionLabel>
      <ul>
        {items.map((c) => (
          <ConversationItem
            key={c.id}
            conversation={c}
            active={c.id === currentId}
            onSelect={onSelect}
            t={t}
            dense={dense}
            reorder={reorder}
          />
        ))}
      </ul>
    </div>
  )
}

function ProjectConversationDisclosure({
  id,
  expanded,
  children,
}: {
  id: string
  expanded: boolean
  children: () => ReactNode
}) {
  const renderedRef = useRef(expanded)
  const [rendered, setRendered] = useState(expanded)
  const [visible, setVisible] = useState(expanded)
  // Mount on the opening render so the parent's active-row locator can find
  // the conversation; retain it only until the closing transition finishes.
  const shouldRender = expanded || rendered

  useEffect(() => {
    let firstFrame = 0
    let secondFrame = 0
    let unmountTimer: number | undefined

    if (expanded) {
      if (renderedRef.current) {
        setVisible(true)
      } else {
        renderedRef.current = true
        setRendered(true)
        setVisible(false)
        firstFrame = window.requestAnimationFrame(() => {
          secondFrame = window.requestAnimationFrame(() => setVisible(true))
        })
      }
    } else {
      setVisible(false)
      unmountTimer = window.setTimeout(() => {
        renderedRef.current = false
        setRendered(false)
      }, duration.base + duration.instant)
    }

    return () => {
      window.cancelAnimationFrame(firstFrame)
      window.cancelAnimationFrame(secondFrame)
      if (unmountTimer !== undefined) window.clearTimeout(unmountTimer)
    }
  }, [expanded])

  return (
    <div
      id={id}
      aria-hidden={!expanded}
      inert={!expanded}
      onTransitionEnd={(event) => {
        if (event.currentTarget !== event.target || expanded) return
        renderedRef.current = false
        setRendered(false)
      }}
      className={cn(
        'grid transition-[grid-template-rows,opacity] duration-[var(--duration-base)] ease-[var(--ease-out)]',
        visible
          ? 'grid-rows-[1fr] opacity-100'
          : 'pointer-events-none grid-rows-[0fr] opacity-0',
      )}
    >
      {shouldRender ? <div className="min-h-0 overflow-hidden">{children()}</div> : null}
    </div>
  )
}

function ConversationItem({
  conversation,
  active,
  onSelect,
  t,
  nested = false,
  dense = false,
  reorder,
}: {
  conversation: ReturnType<typeof useConversations.getState>['conversations'][number]
  active: boolean
  onSelect?: () => void
  t: TFunction<'chat'>
  nested?: boolean
  dense?: boolean
  reorder?: ConversationReorderController
}) {
  const user = useAuth((s) => s.user)
  const meId = user?.id
  const canShare = userCan(user, 'allow_sharing')
  const canExportConversations = userCan(user, 'allow_conversation_export')
  const conversationWorkspace = useWorkspaces((s) =>
    conversation.workspaceId
      ? s.workspaces.find((workspace) => workspace.id === conversation.workspaceId)
      : undefined,
  )
  const workspaceRole = conversationWorkspace?.role
  const isWorkspaceGuest = workspaceRole === 'guest'
  const canManageConversation = !conversation.workspaceId || conversation.creatorId === meId || workspaceRole === 'admin'
  const canDeleteConversations =
    userCan(user, 'allow_conversation_deletion') &&
    (!conversation.workspaceId || conversationWorkspace?.can_delete_conversations !== false)
  const remove = useConversations((s) => s.deleteConversation)
  const star = useConversations((s) => s.toggleStar)
  const archive = useConversations((s) => s.archiveConversation)
  const navigate = useNavigate()
  const [renaming, setRenaming] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const [exporting, setExporting] = useState(false)
  const displayTitle = conversation.title || t('untitled')
  const streaming = isConversationStreaming(conversation)
  const dragProps = isWorkspaceGuest ? undefined : reorder?.rowProps(conversation)
  const dragging = reorder?.draggedId === conversation.id
  const dropPosition = reorder?.dropTarget?.id === conversation.id ? reorder.dropTarget.position : null

  useEffect(() => {
    if (!canManageConversation) {
      setRenaming(false)
      setShareOpen(false)
      setConfirm(false)
      return
    }
    if (!canDeleteConversations) setConfirm(false)
  }, [canManageConversation, canDeleteConversations])

  async function handleExport() {
    if (exporting || !canExportConversations) return
    setExporting(true)
    try {
      await exportConversation(conversation.id)
      toast.success(t('sidebar.exported'))
    } catch (error) {
      toast.error(t('sidebar.exportFailed'), error instanceof Error ? error.message : undefined)
    } finally {
      setExporting(false)
    }
  }

  return (
    // data-conversation-id lets the sidebar scroll the active row into view when
    // the user arrives from outside the list (gallery, command menu, deep link).
    <li data-conversation-id={conversation.id} data-drop-position={dropPosition ?? undefined} {...dragProps}>
      <div
        className={cn(
          'group/conv relative my-px rounded-[8px] interactive',
          nested ? 'ml-9 mr-2' : 'mx-2',
          active
            ? 'bg-[var(--color-sidebar-active)] shadow-[var(--shadow-xs)]'
            : 'hover:bg-[var(--color-sidebar-hover)]',
          dragging && 'opacity-40',
          dropPosition && 'bg-[var(--color-accent-soft)]',
        )}
      >
        <Link
          to={`/chat/${conversation.id}`}
          draggable={false}
          title={dragProps?.draggable ? t('sidebar.reorderHint') : undefined}
          aria-keyshortcuts={dragProps?.draggable ? 'Alt+ArrowUp Alt+ArrowDown' : undefined}
          onClick={onSelect}
          className={cn(
            'flex items-center gap-2 rounded-[8px] px-2.5 py-1.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            dense ? 'min-h-9 pr-10' : 'min-h-8 pr-9',
            dragProps?.draggable && 'cursor-grab active:cursor-grabbing',
          )}
        >
          <ConversationTitle
            title={displayTitle}
            className={cn(
              'min-w-0 flex-1 text-[13px] leading-5',
              active ? 'text-[var(--color-fg)] font-medium' : 'text-[var(--color-fg-muted)] group-hover/conv:text-[var(--color-fg)]',
            )}
          />
          {/* The Starred group already says so; nested project rows keep a
              quiet filled star because they never appear in that group. */}
          {nested && conversation.starred ? (
            <Star size={11} aria-hidden className="shrink-0 fill-current text-[var(--color-fg-subtle)]" />
          ) : null}
          {streaming ? (
            <span
              role="status"
              aria-label={t('sidebar.replying')}
              className="inline-flex size-4 shrink-0 items-center justify-center text-[var(--color-fg-subtle)]"
            >
              <Loader2 size={12} className="animate-spin motion-reduce:animate-none" aria-hidden />
            </span>
          ) : null}
          {conversation.workspaceId && !conversation.isPublic ? (
            <Tooltip content={t('visibility.privateTooltip')}>
              <span
                className="inline-flex size-5 shrink-0 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]"
                aria-label={t('visibility.private')}
              >
                <UserRound size={12} aria-hidden />
              </span>
            </Tooltip>
          ) : conversation.workspaceId && conversation.creatorName ? (
            <span
              className="flex max-w-[45%] shrink-0 items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]"
              title={conversation.creatorName}
            >
              <Avatar size="xs">
                {conversation.creatorAvatar ? (
                  <AvatarImage src={conversation.creatorAvatar} alt={conversation.creatorName} />
                ) : null}
                <AvatarFallback>{initials(conversation.creatorName)}</AvatarFallback>
              </Avatar>
              <span className="truncate">{conversation.creatorName}</span>
            </span>
          ) : null}
        </Link>
        {dropPosition ? (
          <span className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-[var(--color-accent)]" aria-hidden>
            {dropPosition === 'before' ? <ArrowUp size={14} /> : <ArrowDown size={14} />}
          </span>
        ) : null}
        {!isWorkspaceGuest ? <div className={cn('absolute right-1.5 top-1/2 -translate-y-1/2', dropPosition && 'invisible')}>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label={t('sidebar.actions')}
                className={cn(
                  'inline-flex items-center justify-center rounded-[6px] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] data-[state=open]:bg-[var(--color-bg-muted)] data-[state=open]:text-[var(--color-fg)]',
                  // Desktop reveals the menu on hover/focus like every other
                  // row action. Touch has no hover, so the drawer keeps it
                  // visible but quiet enough that a column of dots recedes.
                  dense
                    ? cn('size-8', active ? 'text-[var(--color-fg-muted)]' : 'text-[var(--color-fg-faint)]')
                    : 'size-6 text-[var(--color-fg-muted)] opacity-0 group-hover/conv:opacity-100 group-focus-within/conv:opacity-100 data-[state=open]:opacity-100',
                )}
              >
                <MoreHorizontal size={14} aria-hidden />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[180px]">
              {canManageConversation ? (
                <DropdownMenuItem onClick={() => setRenaming(true)}>
                  <Pencil size={13} aria-hidden />
                  {t('sidebar.rename')}
                </DropdownMenuItem>
              ) : null}
              <DropdownMenuItem onClick={() => {
                void star(conversation.id)
                toast.success(conversation.starred ? t('common:actions.unstar') : t('common:actions.star'))
              }}>
                <Star size={13} aria-hidden />
                {conversation.starred ? t('common:actions.unstar') : t('common:actions.star')}
              </DropdownMenuItem>
              {canShare && canManageConversation ? (
                <DropdownMenuItem onClick={() => setShareOpen(true)}>
                  <Share2 size={13} aria-hidden />
                  {t('sidebar.share')}
                </DropdownMenuItem>
              ) : null}
              {canExportConversations ? (
                <DropdownMenuItem disabled={exporting} onClick={() => void handleExport()}>
                  <Download size={13} aria-hidden />
                  {exporting ? t('sidebar.exporting') : t('sidebar.export')}
                </DropdownMenuItem>
              ) : null}
              {canManageConversation ? (
                <MoveToProjectSub
                  conversationId={conversation.id}
                  currentProjectId={conversation.projectId}
                  workspaceId={conversation.workspaceId ?? null}
                  separatorBefore
                />
              ) : null}
              {!conversation.workspaceId ? (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onClick={() => {
                    archive(conversation.id)
                    toast.success(t('sidebar.archived'))
                  }}>
                    <Archive size={13} aria-hidden />
                    {t('sidebar.archive')}
                  </DropdownMenuItem>
                </>
              ) : null}
              {canManageConversation && canDeleteConversations ? (
                <DropdownMenuItem destructive onClick={() => setConfirm(true)}>
                  <Trash2 size={13} aria-hidden />
                  {t('sidebar.delete')}
                </DropdownMenuItem>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        </div> : null}
      </div>

      {canManageConversation ? (
        <RenameConversationDialog
          conversationId={conversation.id}
          currentTitle={conversation.title}
          open={renaming}
          onOpenChange={setRenaming}
        />
      ) : null}

      {canShare && canManageConversation ? (
        <ShareConversationDialog
          conversationId={conversation.id}
          open={shareOpen}
          onOpenChange={setShareOpen}
        />
      ) : null}

      {/* Delete confirm */}
      <Dialog open={confirm && canManageConversation && canDeleteConversations} onOpenChange={setConfirm}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('sidebar.deleteTitle')}</DialogTitle>
            <DialogDescription>
              {t('sidebar.deleteBody')}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirm(false)}>
              {t('actions.cancel', { ns: 'common' })}
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                remove(conversation.id)
                setConfirm(false)
                // Only leave for the blank chat when we just deleted the
                // conversation the user is actively viewing; deleting any other
                // row should remove it in place without hijacking the route.
                if (active) navigate('/chat')
                toast.success(t('sidebar.deleted'))
              }}
            >
              {t('actions.delete', { ns: 'common' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </li>
  )
}

/**
 * A long title fades out at its trailing edge instead of ending in "…" or
 * scrolling on hover; the full title is offered in a tooltip only when it is
 * actually clipped.
 */
function ConversationTitle({ title, className }: { title: string; className?: string }) {
  const ref = useRef<HTMLSpanElement>(null)
  const [overflowing, setOverflowing] = useState(false)

  useEffect(() => {
    const node = ref.current
    if (!node) return
    let disposed = false
    const measure = () => {
      if (disposed) return
      const next = node.scrollWidth > node.clientWidth + 1
      setOverflowing((current) => (current === next ? current : next))
    }
    measure()
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(measure)
    observer?.observe(node)
    void document.fonts?.ready.then(measure)
    return () => {
      disposed = true
      observer?.disconnect()
    }
  }, [title])

  return (
    // The Tooltip root stays mounted with empty content so the measured span
    // never remounts when the overflow state flips.
    <Tooltip
      content={overflowing ? <span className="block max-w-[20rem] whitespace-normal break-words">{title}</span> : ''}
      side="right"
      delayDuration={600}
    >
      <span ref={ref} data-overflowing={overflowing ? 'true' : undefined} className={cn('sidebar-row-title', className)}>
        {title}
      </span>
    </Tooltip>
  )
}

interface UserMenuProps {
  collapsed?: boolean
  /** Header placement keeps the same account actions while adapting the trigger
   * and popup direction for a top-right mobile toolbar. */
  placement?: 'sidebar' | 'header'
}

export function UserMenu({ collapsed = false, placement = 'sidebar' }: UserMenuProps) {
  const navigate = useNavigate()
  const openSettings = useOpenSettings()
  const { t } = useTranslation(['chat', 'common', 'settings'])
  const user = useAuth((s) => s.user)
  const logout = useAuth((s) => s.logout)
  const domainDataStatus = useDomainData((s) => s.status)
  const showDomainData = useDomainData((s) => s.show)
  const displayName = user?.name || user?.email?.split('@')[0] || 'Aivory'
  const avatarUrl = (user?.settings as Record<string, unknown> | undefined)?.avatar_url as string | undefined
  const isAdmin = user?.role === 'admin'
  const lang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const [wsCreateOpen, setWsCreateOpen] = useState(false)
  const [wsManageId, setWsManageId] = useState<string | null>(null)
  const activeWorkspace = useWorkspaces((s) => s.workspaces.find((w) => w.id === s.activeId))
  const canManageWorkspace = activeWorkspace?.is_owner || activeWorkspace?.role === 'admin'
  const [systemUpdateOpen, setSystemUpdateOpen] = useState(false)
  const [systemUpdateSummary, setSystemUpdateSummary] = useState<SystemUpdateSummary>({
    currentVersion: typeof __APP_VERSION__ !== 'undefined' ? __APP_VERSION__ : 'dev',
    updateAvailable: false,
    updating: false,
  })
  const inHeader = placement === 'header'
  return (
    <>
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('settings:user.menuAria')}
          className={cn(
            'flex items-center justify-center gap-2.5 rounded-[10px] interactive',
            inHeader
              ? 'hover:bg-[var(--color-bg-muted)] data-[state=open]:bg-[var(--color-bg-muted)]'
              : 'hover:bg-[var(--color-sidebar-hover)] data-[state=open]:bg-[var(--color-sidebar-hover)]',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            inHeader ? 'size-[var(--tap-min)]' : collapsed ? 'p-1.5' : 'w-full p-1.5',
          )}
        >
          <Avatar size="md" tone="clay">
            {avatarUrl ? <AvatarImage src={avatarUrl} alt={displayName} /> : null}
            <AvatarFallback>{initials(displayName)}</AvatarFallback>
          </Avatar>
          {!collapsed && !inHeader && (
            <div className="flex-1 min-w-0 text-left">
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-medium text-[var(--color-fg)] truncate">{displayName}</span>
                {user?.group_name && (
                  <span className="shrink-0 rounded-full bg-[var(--color-bg-muted)] px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
                    {user.group_name}
                  </span>
                )}
              </div>
              <span className="text-[11px] text-[var(--color-fg-subtle)] truncate block">{user?.email}</span>
            </div>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align={inHeader ? 'end' : 'start'}
        side={inHeader ? 'bottom' : 'top'}
        className={cn(
          inHeader ? 'min-w-[220px]' : 'min-w-[248px]',
          // The mobile header uses the same compact item density as the desktop
          // account menu. The avatar trigger itself remains tap-sized.
          inHeader && 'w-[min(17rem,calc(100vw-1rem))]',
        )}
      >
        <DropdownMenuItem onClick={() => openSettings('account')}>
          <Settings size={13} aria-hidden />
          {t('settings:user.settings')}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => navigate('/subscription')}>
          <Layers size={13} aria-hidden />
          {t('chat:userMenu.subscription', { defaultValue: 'Subscription' })}
        </DropdownMenuItem>
        {domainDataStatus?.needs_action ? (
          <DropdownMenuItem onClick={showDomainData}>
            <Database size={13} aria-hidden />
            {t('chat:userMenu.dataManagement')}
          </DropdownMenuItem>
        ) : null}
        {isAdmin && (
          <>
            <DropdownMenuItem onClick={() => navigate('/admin')}>
              <ShieldCheck size={13} aria-hidden />
              {t('chat:userMenu.admin', { defaultValue: 'Admin' })}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setSystemUpdateOpen(true)}>
              <PackageCheck size={13} aria-hidden />
              <span className="flex-1">{t('chat:userMenu.systemUpdate.version')}</span>
              <Badge
                size="xs"
                variant={systemUpdateSummary.updateAvailable ? 'warning' : systemUpdateSummary.updating ? 'info' : 'neutral'}
                className={systemUpdateSummary.updateAvailable ? 'font-semibold shadow-[0_0_0_2px_var(--color-warning-soft)]' : undefined}
              >
                {systemUpdateSummary.updating
                  ? t('chat:userMenu.systemUpdate.updating')
                  : `v${systemUpdateSummary.currentVersion}`}
              </Badge>
            </DropdownMenuItem>
          </>
        )}
        <WorkspaceMenuItems
          onManage={() => canManageWorkspace ? setWsManageId(activeWorkspace.id) : openSettings('work')}
          onCreate={() => setWsCreateOpen(true)}
        />
        <DropdownMenuSeparator />
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <Languages size={13} aria-hidden />
            {t('chat:userMenu.language', { defaultValue: 'Language' })}
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent>
            <DropdownMenuRadioGroup value={lang} onValueChange={(v) => setLang(v as typeof lang)}>
              {SUPPORTED_LANGUAGES.map((l) => (
                <DropdownMenuRadioItem key={l.code} value={l.code}>
                  {l.label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        <DropdownMenuSub>
          <DropdownMenuSubTrigger>
            <CircleHelp size={13} aria-hidden />
            {t('chat:userMenu.help', { defaultValue: 'Help' })}
          </DropdownMenuSubTrigger>
          <DropdownMenuSubContent>
            <DropdownMenuItem onClick={() => window.open('/terms', '_blank', 'noopener,noreferrer')}>
              <FileText size={13} aria-hidden />
              {t('chat:userMenu.terms', { defaultValue: 'Terms of Service' })}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => window.open('/privacy', '_blank', 'noopener,noreferrer')}>
              <ShieldCheck size={13} aria-hidden />
              {t('chat:userMenu.privacyPolicy', { defaultValue: 'Privacy Policy' })}
            </DropdownMenuItem>
          </DropdownMenuSubContent>
        </DropdownMenuSub>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={() =>
            void (async () => {
              // Immediate feedback while the backend sign-out is in flight;
              // the success toast + redirect below follow once it resolves.
              toast.info(t('chat:signingOut', { defaultValue: 'Signing out…' }))
              await logout()
              toast.success(t('chat:signedOut'))
              navigate('/login')
            })()
          }
        >
          {t('settings:user.signOut')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
    {isAdmin && (
      <SystemUpdateDialog
        open={systemUpdateOpen}
        onOpenChange={setSystemUpdateOpen}
        onSummaryChange={setSystemUpdateSummary}
      />
    )}
    <CreateWorkspaceDialog open={wsCreateOpen} onOpenChange={setWsCreateOpen} />
    {canManageWorkspace ? <WorkspaceMembersDialog key={activeWorkspace.id} open={wsManageId === activeWorkspace.id} onOpenChange={(open) => setWsManageId(open ? activeWorkspace.id : null)} /> : null}
    </>
  )
}
