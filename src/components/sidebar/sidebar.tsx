import { type CSSProperties, type ReactNode, useEffect, useId, useMemo, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import {
  Search,
  Plus,
  PanelLeftClose,
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
  Briefcase,
  FolderOpen,
  LibraryBig,
  CircleHelp,
  FileText,
  UserRound,
} from 'lucide-react'
import { Logo, LogoMark } from '@/components/brand/logo'
import { useWorkspaces } from '@/store/workspaces'
import {
  CreateWorkspaceDialog,
  SpaceSwitcherButton,
  WorkspaceMembersDialog,
  WorkspaceMenuItems,
} from '@/components/sidebar/workspace-menu'
import { SidebarResizeHandle } from '@/components/sidebar/sidebar-resize-handle'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { Tooltip } from '@/components/ui/tooltip'
import { KeyboardShortcut } from '@/components/ui/kbd'
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
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NewProjectDialog } from '@/components/projects/new-project-dialog'
import { MoveToProjectSub } from '@/components/projects/move-to-project-menu'
import { useConversations, sameConvListShape } from '@/store/conversations'
import { resetComposerForNewConversation } from '@/store/composer-prefs'
import { useProjects } from '@/store/projects'
import { useModels } from '@/store/models'
import { useSettings } from '@/store/settings'
import { useAuth } from '@/store/auth'
import { useLanguage } from '@/store/language'
import { SUPPORTED_LANGUAGES } from '@/i18n'
import { useCommandMenu } from '@/hooks/use-command-menu'
import { useOpenSettings } from '@/hooks/use-open-settings'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useCopy } from '@/hooks/use-clipboard'
import { conversationsApi, ApiError } from '@/api'
import { duration } from '@/lib/design-tokens'
import { accentClasses } from '@/lib/project-helpers'
import { partitionConversationNavigation } from '@/lib/conversation-navigation'
import { userCan } from '@/lib/user-permissions'
import { type DateBucket, bucketFor, modKey, cn, truncate } from '@/lib/utils'
import { toast } from '@/hooks/use-toast'
import { useTranslation } from 'react-i18next'
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
  const canDraw = userCan(user, 'allow_drawing')
  const canUseKnowledgeBases = userCan(user, 'allow_knowledge_bases')
  const activeWorkspace = useWorkspaces((s) => (s.activeId ? s.workspaces.find((w) => w.id === s.activeId) : undefined))
  const canCreateProject = canUseKnowledgeBases && (!activeWorkspace || activeWorkspace.can_create_projects)
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
  const activeWsId = useWorkspaces((s) => s.activeId)
  const switching = useWorkspaces((s) => s.switching)
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
    if (!hasMore) return
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
  }, [hasMore, loadMore])
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
          return b.updatedAt - a.updatedAt
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
  const newChatActive = location.pathname === '/' && !drawActive
  // Projects, Files, and Skills are their own routes — highlight their entry
  // when the current path is under them.
  const filesActive = location.pathname === '/files'
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
  const sidebarWidth = useSettings((s) => s.sidebarWidth)
  const setSidebarWidth = useSettings((s) => s.setSidebarWidth)
  const toggleSidebar = useSettings((s) => s.toggleSidebar)
  const sidebarRef = useRef<HTMLElement>(null)
  const sidebarId = useId()
  const [newProjectOpen, setNewProjectOpen] = useState(false)
  const [conversationListScrolled, setConversationListScrolled] = useState(false)
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
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const scrolledForIdRef = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (collapsed) {
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
  }, [activeConversations, collapsed, currentId, expandedProjectIds, reducedMotion])

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
      className={cn(
        'relative flex h-full shrink-0 flex-col bg-[var(--color-sidebar-bg)]',
        variant === 'desktop' && 'border-r border-[var(--color-sidebar-border)]',
        variant === 'desktop' && collapsed && 'w-[var(--layout-sidebar-w-collapsed)]',
        variant === 'sheet' && 'w-full',
        'transition-[width] duration-[var(--duration-base)] ease-[var(--ease-out)] data-[resizing=true]:transition-none',
      )}
    >
      {/* Header — inside a workspace the brand slot shows the WORKSPACE NAME
          (§workspaces spec: sidebar 上方原先显示 aivory 的地方显示工作空间名称).
          Keyed on the active space so switching replays a fade-in (§ workspace
          switch animation) instead of the name jump-cutting. */}
      <div className="flex h-[56px] shrink-0 items-center justify-between px-3 max-sm:h-12 max-sm:px-2">
        {!collapsed ? (
          activeWorkspace ? (
            <div key={activeWorkspace.id} className="page-enter flex min-w-0 items-center gap-1.5">
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
                <Briefcase size={16} aria-hidden className="shrink-0 text-[var(--color-secondary)]" />
                <span className="truncate text-[15px] text-[var(--color-fg)]">{activeWorkspace.name}</span>
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
            className="page-enter inline-flex items-center"
            aria-label={tCommon('aria.homeLink')}
          >
            <Logo size="sm" />
          </Link>
          )
        ) : (
          <Link to="/" onClick={resetComposerForNewConversation} className="mx-auto" aria-label={tCommon('aria.homeLink')}>
            <LogoMark size={22} />
          </Link>
        )}
        {!collapsed && variant === 'desktop' && (
          <Tooltip content={t('commandMenu.actions.toggleSidebar')} shortcut={`${modKey()}B`}>
            <button
              type="button"
              onClick={toggleSidebar}
              aria-label={t('commandMenu.actions.toggleSidebar')}
              className="inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive"
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
            className="inline-flex size-[var(--tap-min)] items-center justify-center rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-9 max-sm:rounded-[8px]"
          >
            <X size={18} aria-hidden />
          </button>
        )}
      </div>

      {/* Actions */}
      <div className={cn('flex flex-col gap-px px-2', collapsed && 'items-center')}>
        <Tooltip content={collapsed ? t('sidebar.newChat') : ''} side="right">
          <button
            type="button"
            onClick={() => void startNewChat()}
            aria-current={newChatActive ? 'page' : undefined}
            className={cn(
              'inline-flex h-8 items-center gap-2 rounded-[8px] text-[13px] font-medium max-lg:h-[var(--tap-min)] max-sm:!h-9 max-sm:gap-1.5',
              // Filled "selected" look ONLY on the new-chat home; a plain nav row
              // everywhere else so it never reads as selected on /chat, /projects…
              newChatActive
                ? 'bg-[var(--color-bg-muted)] border border-[var(--color-border-strong)] text-[var(--color-fg)] hover:bg-[var(--color-bg)] hover:border-[var(--color-border-strong)]'
                : 'border border-transparent text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              'interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-8 justify-center px-0' : 'w-full justify-between px-2.5',
            )}
          >
            <span className="inline-flex items-center gap-2">
              <Plus size={15} className="text-[var(--color-accent)]" aria-hidden />
              {!collapsed && <span>{t('sidebar.newChat')}</span>}
            </span>
            {!collapsed && <KeyboardShortcut combo={[modKey(), 'Shift', 'O']} className="max-lg:hidden" />}
          </button>
        </Tooltip>

        <Tooltip content={collapsed ? `${t('sidebar.search')} (${modKey()}K)` : ''} side="right">
          <button
            type="button"
            onClick={() => setOpen(true)}
            className={cn(
              'inline-flex h-8 items-center gap-2 rounded-[8px] text-[13px] max-lg:h-[var(--tap-min)] max-sm:!h-9 max-sm:gap-1.5',
              'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-8 justify-center px-0' : 'w-full justify-between px-2.5',
            )}
          >
            <span className="inline-flex items-center gap-2">
              <Search size={15} aria-hidden />
              {!collapsed && <span>{t('sidebar.search')}</span>}
            </span>
            {!collapsed && <KeyboardShortcut combo={[modKey(), 'K']} className="max-lg:hidden" />}
          </button>
        </Tooltip>

        {/* §4.20 Draw — opens a new conversation pre-set to an image model. */}
        {hasImageModels && (
          <Tooltip content={collapsed ? tNav('draw', { defaultValue: 'Draw' }) : ''} side="right">
            <Link
              to="/?mode=draw"
              onClick={onClose}
              aria-current={drawActive ? 'page' : undefined}
              className={cn(
                'inline-flex h-8 items-center gap-2 rounded-[8px] text-[13px] max-lg:h-[var(--tap-min)] max-sm:!h-9 max-sm:gap-1.5',
                drawActive
                  ? 'bg-[var(--color-bg-muted)] text-[var(--color-fg)] font-medium'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                'interactive',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                collapsed ? 'w-8 justify-center px-0' : 'w-full justify-start px-2.5',
              )}
            >
              <ImagePlus size={15} aria-hidden />
              {!collapsed && <span>{tNav('draw', { defaultValue: 'Draw' })}</span>}
            </Link>
          </Tooltip>
        )}

        <div
          aria-hidden
          className={cn(
            'my-1 h-px shrink-0 bg-[var(--color-divider)]/60',
            collapsed ? 'w-5' : 'mx-2',
          )}
        />

        {/* § user files page — every upload (chat + KB) with the storage meter.
            The page is scoped to the user's PERSONAL uploads (GET /me/files),
            so it's hidden inside a workspace where files are shared, not owned. */}
        {!activeWorkspace && (
          <Tooltip content={collapsed ? tNav('files', { defaultValue: 'Files' }) : ''} side="right">
            <Link
              to="/files"
              onClick={onClose}
              aria-current={filesActive ? 'page' : undefined}
              className={cn(
                'inline-flex h-8 items-center gap-2 rounded-[8px] text-[13px] interactive max-lg:h-[var(--tap-min)] max-sm:!h-9 max-sm:gap-1.5',
                filesActive
                  ? 'bg-[var(--color-bg-muted)] text-[var(--color-fg)] font-medium'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                collapsed ? 'w-8 justify-center px-0' : 'w-full justify-start px-2.5',
              )}
            >
              <FolderOpen size={15} aria-hidden />
              {!collapsed && <span>{tNav('files', { defaultValue: 'Files' })}</span>}
            </Link>
          </Tooltip>
        )}

        {canUseKnowledgeBases && (
          <Tooltip
            content={collapsed ? tNav('knowledgeBases', { defaultValue: 'Knowledge' }) : ''}
            side="right"
          >
            <Link
              to="/kb"
              onClick={onClose}
              aria-current={knowledgeBasesActive ? 'page' : undefined}
              className={cn(
                'inline-flex h-8 items-center gap-2 rounded-[8px] text-[13px] interactive max-lg:h-[var(--tap-min)] max-sm:!h-9 max-sm:gap-1.5',
                knowledgeBasesActive
                  ? 'bg-[var(--color-bg-muted)] text-[var(--color-fg)] font-medium'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                collapsed ? 'w-8 justify-center px-0' : 'w-full justify-start px-2.5',
              )}
            >
              <Database size={15} aria-hidden />
              {!collapsed && (
                <span>{tNav('knowledgeBases', { defaultValue: 'Knowledge' })}</span>
              )}
            </Link>
          </Tooltip>
        )}

        <Tooltip
          content={collapsed ? tNav('resources', { defaultValue: 'Library' }) : ''}
          side="right"
        >
          <Link
            to="/skills"
            onClick={onClose}
            aria-current={skillsActive ? 'page' : undefined}
            className={cn(
              'inline-flex h-8 items-center gap-2 rounded-[8px] text-[13px] interactive max-lg:h-[var(--tap-min)] max-sm:!h-9 max-sm:gap-1.5',
              skillsActive
                ? 'bg-[var(--color-bg-muted)] text-[var(--color-fg)] font-medium'
                : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-8 justify-center px-0' : 'w-full justify-start px-2.5',
            )}
          >
            <LibraryBig size={15} aria-hidden />
            {!collapsed && <span>{tNav('resources', { defaultValue: 'Library' })}</span>}
          </Link>
        </Tooltip>
      </div>

      {/* Conversation list — while a workspace switch is reloading data, the list
          fades out and a spinner takes its place instead of flashing the old
          (or momentarily empty) space's rows (§ workspace switch animation). */}
      {!collapsed && (
        <div className="relative mt-1 flex-1 min-h-0">
          <div
            ref={listScrollRef}
            onScroll={(event) => {
              const scrolled = event.currentTarget.scrollTop > 2
              setConversationListScrolled((current) => (current === scrolled ? current : scrolled))
            }}
            className={cn(
              'h-full overflow-y-auto scrollbar-thin transition-opacity duration-200',
              switching && 'opacity-0 pointer-events-none',
            )}
          >
            <section className="py-1.5 max-sm:py-1">
              <div className="flex items-center pr-2">
                <h3 className="min-w-0 flex-1 px-4 py-1 text-[10px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)] max-lg:py-1.5 max-lg:text-[11px] max-sm:px-3 max-sm:py-0.5">
                  <Link
                    to="/projects"
                    onClick={onClose}
                    aria-current={location.pathname === '/projects' ? 'page' : undefined}
                    className="rounded-[5px] interactive hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    {tNav('projects')}
                  </Link>
                </h3>
                {canCreateProject ? (
                  <Tooltip content={tProjects('nav.newProject')}>
                    <button
                      type="button"
                      onClick={() => setNewProjectOpen(true)}
                      aria-label={tProjects('nav.newProject')}
                      className="inline-flex size-6 shrink-0 items-center justify-center rounded-[6px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-lg:size-10 max-sm:!size-8"
                    >
                      <Plus size={12} aria-hidden />
                    </button>
                  </Tooltip>
                ) : null}
              </div>

              {sortedProjects.length === 0 ? (
                <p className="px-4 py-2 text-[11.5px] text-[var(--color-fg-subtle)]">
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
                    return (
                      <li key={project.id}>
                        <div
                          className={cn(
                            'group/project mx-1 flex min-h-8 items-center rounded-[8px] interactive max-lg:min-h-[var(--tap-min)] max-sm:!min-h-9',
                            projectActive ? 'bg-[var(--color-bg)]' : 'hover:bg-[var(--color-bg)]',
                          )}
                        >
                          <button
                            type="button"
                            aria-label={project.name}
                            aria-expanded={expanded}
                            aria-controls={childListId}
                            onClick={() => toggleProject(project.id, expanded)}
                            className="inline-flex size-8 shrink-0 items-center justify-center rounded-[7px] text-[var(--color-fg-faint)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-lg:size-[var(--tap-min)] max-sm:!size-9"
                          >
                            <ChevronRight
                              size={13}
                              aria-hidden
                              className={cn(
                                'transition-transform duration-[var(--duration-base)] ease-[var(--ease-out)]',
                                expanded && 'rotate-90',
                              )}
                            />
                          </button>
                          <Link
                            to={`/projects/${project.id}`}
                            onClick={onClose}
                            aria-label={project.name}
                            aria-current={location.pathname === `/projects/${project.id}` ? 'page' : undefined}
                            title={project.name}
                            className="flex min-h-8 min-w-0 flex-1 items-center gap-2 rounded-[7px] py-1.5 pl-0.5 pr-2 text-[13px] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-lg:min-h-[var(--tap-min)] max-sm:!min-h-9 max-sm:gap-1.5 max-sm:py-1"
                          >
                            <span
                              className={cn(
                                'inline-flex size-5 shrink-0 items-center justify-center rounded-[6px] text-[11px] font-medium',
                                accent.chip,
                              )}
                              aria-hidden
                            >
                              {project.emoji?.trim() || project.name.trim().slice(0, 1).toUpperCase()}
                            </span>
                            <span className={cn('min-w-0 flex-1 truncate', projectActive && 'font-medium text-[var(--color-fg)]')}>
                              {truncate(project.name, 30)}
                            </span>
                            {projectConversations.length > 0 ? (
                              <span className="shrink-0 text-[10.5px] tabular-nums text-[var(--color-fg-subtle)]">
                                {projectConversations.length}
                              </span>
                            ) : null}
                          </Link>
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
                                  className="ml-11 flex min-h-8 items-center text-[var(--color-fg-subtle)]"
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
                                  reducedMotion={reducedMotion}
                                  nested
                                  dense={variant === 'sheet'}
                                />
                              ))}
                              {!loadingProjectIds.has(project.id) &&
                                loadedProjectIdsRef.current.has(project.id) &&
                                projectConversations.length === 0 ? (
                                <li className="ml-11 min-h-8 py-1.5 pr-2 text-[11.5px] text-[var(--color-fg-subtle)]">
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
            </section>

            {starred.length > 0 && (
              <Group
                label={t('sidebar.starred')}
                items={starred}
                currentId={currentId}
                onSelect={onClose}
                t={t}
                reducedMotion={reducedMotion}
                dense={variant === 'sheet'}
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
                    reducedMotion={reducedMotion}
                    dense={variant === 'sheet'}
                  />
                ),
            )}
            {hasMore && (
              <div
                ref={loadMoreRef}
                className="flex items-center justify-center py-3 text-[11px] text-[var(--color-fg-subtle)]"
              >
                {loadingMore ? <Loader2 size={13} className="animate-spin" aria-hidden /> : null}
              </div>
            )}
            {conversations.length === 0 && (
              <p className="px-4 py-6 text-xs text-[var(--color-fg-subtle)] text-center">
                {t('sidebar.empty')}
              </p>
            )}
          </div>
          <div
            aria-hidden
            className={cn(
              'pointer-events-none absolute inset-x-0 top-0 z-10 h-5',
              reducedMotion ? 'transition-none' : 'transition-opacity duration-150',
              conversationListScrolled && !switching ? 'opacity-100' : 'opacity-0',
            )}
            style={{ background: 'linear-gradient(to bottom, var(--color-sidebar-bg), transparent)' }}
          />
          {switching && (
            <div className="absolute inset-0 flex items-center justify-center">
              <Loader2 size={16} className="animate-spin text-[var(--color-fg-subtle)]" aria-hidden />
            </div>
          )}
        </div>
      )}

      {/* Footer — the avatar plus a space switcher beside it. The switcher is a
          flat picker (personal + every workspace) shown whenever the user has
          any workspace, so it works both in the personal space (pick one to
          enter) and inside a workspace (§workspaces 头像旁切换按钮). */}
      <div className={cn('mt-auto p-2 max-sm:p-1.5', collapsed && 'flex items-center justify-center')}>
        <div className={cn('flex items-center gap-1', collapsed && 'flex-col')}>
          <div className="min-w-0 flex-1">
            <UserMenu collapsed={collapsed} />
          </div>
          <SpaceSwitcherButton />
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

function Group({
  label,
  items,
  currentId,
  onSelect,
  t,
  reducedMotion,
  dense = false,
}: {
  label: string
  items: ReturnType<typeof useConversations.getState>['conversations']
  currentId: string | undefined
  onSelect?: () => void
  t: TFunction<'chat'>
  reducedMotion: boolean
  dense?: boolean
}) {
  return (
    <div className={cn('py-1.5', dense && 'py-1')}>
      <h3 className={cn('px-4 py-1 max-lg:py-1.5 text-[10px] max-lg:text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]', dense && 'px-3 py-0.5')}>
        {label}
      </h3>
      <ul>
        {items.map((c) => (
          <ConversationItem
            key={c.id}
            conversation={c}
            active={c.id === currentId}
            onSelect={onSelect}
            t={t}
            reducedMotion={reducedMotion}
            dense={dense}
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
  reducedMotion,
  nested = false,
  dense = false,
}: {
  conversation: ReturnType<typeof useConversations.getState>['conversations'][number]
  active: boolean
  onSelect?: () => void
  t: TFunction<'chat'>
  reducedMotion: boolean
  nested?: boolean
  dense?: boolean
}) {
  const user = useAuth((s) => s.user)
  const meId = user?.id
  const canShare = userCan(user, 'allow_sharing')
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
  const rename = useConversations((s) => s.renameConversation)
  const remove = useConversations((s) => s.deleteConversation)
  const star = useConversations((s) => s.toggleStar)
  const archive = useConversations((s) => s.archiveConversation)
  const navigate = useNavigate()
  const { copy } = useCopy()
  const [renaming, setRenaming] = useState(false)
  const [draft, setDraft] = useState(conversation.title)
  const [confirm, setConfirm] = useState(false)
  const displayTitle = `${conversation.starred ? '☆ ' : ''}${conversation.title || t('untitled')}`

  useEffect(() => {
    if (!canManageConversation) {
      setRenaming(false)
      setConfirm(false)
      return
    }
    if (!canDeleteConversations) setConfirm(false)
  }, [canManageConversation, canDeleteConversations])

  // Create (or refresh) a public share and copy its link in one tap (§ sharing).
  // Managing / revoking the share lives in the conversation's Share dialog.
  async function shareConversation() {
    // Immediate feedback so a slow backend doesn't leave the click feeling dead;
    // the success toast below replaces this once the link is copied.
    toast.info(t('share.creatingLink', { defaultValue: 'Creating share link…' }))
    try {
      const s = await conversationsApi.createShare(conversation.id)
      await copy(`${window.location.origin}/share/${s.id}`)
      toast.success(t('share.linkCopied'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('share.failed'))
    }
  }

  return (
    // data-conversation-id lets the sidebar scroll the active row into view when
    // the user arrives from outside the list (gallery, command menu, deep link).
    <li data-conversation-id={conversation.id}>
      <div
        className={cn(
          'group/conv relative my-px rounded-[10px] interactive',
          nested ? (dense ? 'ml-7 mr-0.5' : 'ml-9 mr-1') : dense ? 'mx-1.5' : 'mx-2',
          active ? 'bg-[var(--color-surface)] shadow-[var(--shadow-xs)]' : 'hover:bg-[var(--color-bg)]',
        )}
      >
        <Link
          to={`/chat/${conversation.id}`}
          onClick={onSelect}
          className={cn(
            'conversation-title-link block rounded-[10px] px-2.5 pr-9 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            dense ? 'py-1.5 pr-8' : cn('max-lg:pr-12', nested ? 'py-1.5 max-lg:py-2.5' : 'py-2 max-lg:py-2.5'),
          )}
        >
          <span className="flex items-center gap-2">
            <OverflowingConversationTitle
              title={displayTitle}
              reducedMotion={reducedMotion}
              className={cn(
                'min-w-0 flex-1 leading-snug',
                dense ? 'text-[12.5px]' : cn('max-lg:text-[15px]', nested ? 'text-[12.5px]' : 'text-[13.5px]'),
                active ? 'text-[var(--color-fg)] font-medium' : 'text-[var(--color-fg-muted)]',
              )}
            />
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
          </span>
        </Link>
        {!isWorkspaceGuest ? <div className="absolute right-1.5 top-1/2 -translate-y-1/2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label={t('sidebar.actions')}
                className={cn(
                  'inline-flex items-center justify-center rounded-[6px] opacity-0 group-hover/conv:opacity-100 data-[state=open]:opacity-100 text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  dense ? 'size-7 opacity-100' : 'size-6 max-lg:size-10 max-lg:opacity-100',
                )}
              >
                <MoreHorizontal size={13} aria-hidden />
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
                <DropdownMenuItem onClick={() => void shareConversation()}>
                  <Share2 size={13} aria-hidden />
                  {t('sidebar.share')}
                </DropdownMenuItem>
              ) : null}
              {canManageConversation ? (
                <>
                  <DropdownMenuSeparator />
                  <MoveToProjectSub conversationId={conversation.id} currentProjectId={conversation.projectId} />
                </>
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

      {/* Rename dialog */}
      <Dialog open={renaming && canManageConversation} onOpenChange={setRenaming}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('sidebar.renameTitle')}</DialogTitle>
            <DialogDescription>{t('sidebar.renameHint')}</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <Input value={draft} onChange={(e) => setDraft(e.target.value)} autoFocus />
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenaming(false)}>
              {t('actions.cancel', { ns: 'common' })}
            </Button>
            <Button
              onClick={() => {
                rename(conversation.id, draft)
                setRenaming(false)
                toast.success(t('sidebar.renamed'))
              }}
            >
              {t('actions.save', { ns: 'common' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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

function OverflowingConversationTitle({
  title,
  reducedMotion,
  className,
}: {
  title: string
  reducedMotion: boolean
  className?: string
}) {
  const viewportRef = useRef<HTMLSpanElement>(null)
  const trackRef = useRef<HTMLSpanElement>(null)
  const [overflow, setOverflow] = useState(0)

  useEffect(() => {
    const viewport = viewportRef.current
    const track = trackRef.current
    if (!viewport || !track) return

    let disposed = false
    const measure = () => {
      if (disposed) return
      const next = Math.max(0, Math.ceil(track.getBoundingClientRect().width - viewport.clientWidth))
      setOverflow((current) => (current === next ? current : next))
    }

    measure()
    const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(measure)
    observer?.observe(viewport)
    observer?.observe(track)
    window.addEventListener('resize', measure)
    void document.fonts?.ready.then(measure)

    return () => {
      disposed = true
      observer?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [title])

  const scrollable = overflow > 0 && !reducedMotion
  const durationMs = Math.min(9000, Math.max(3000, Math.round(overflow * 18)))
  const style = {
    '--conversation-title-distance': `${overflow}px`,
    '--conversation-title-duration': `${durationMs}ms`,
  } as CSSProperties

  return (
    <span
      ref={viewportRef}
      data-scrollable={scrollable ? 'true' : undefined}
      title={overflow > 0 && reducedMotion ? title : undefined}
      className={cn('conversation-title-viewport', className)}
      style={style}
    >
      <span className="conversation-title-static">{title}</span>
      <span ref={trackRef} aria-hidden className="conversation-title-track">
        {title}
      </span>
    </span>
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
  const displayName = user?.name || user?.email?.split('@')[0] || 'Aivory'
  const avatarUrl = (user?.settings as Record<string, unknown> | undefined)?.avatar_url as string | undefined
  const isAdmin = user?.role === 'admin'
  const lang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const [archivedOpen, setArchivedOpen] = useState(false)
  const [wsMembersOpen, setWsMembersOpen] = useState(false)
  const [wsCreateOpen, setWsCreateOpen] = useState(false)
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
            'hover:bg-[var(--color-bg)] data-[state=open]:bg-[var(--color-bg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            inHeader ? 'size-[var(--tap-min)]' : collapsed ? 'p-1.5' : 'w-full p-2',
          )}
        >
          <Avatar size="md" tone="clay" className={cn(inHeader && 'ring-1 ring-[var(--color-border)]')}>
            {avatarUrl ? <AvatarImage src={avatarUrl} alt={displayName} /> : null}
            <AvatarFallback>{initials(displayName)}</AvatarFallback>
          </Avatar>
          {!collapsed && !inHeader && (
            <div className="flex-1 min-w-0 text-left">
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-medium text-[var(--color-fg)] truncate">{displayName}</span>
                {user?.group_name && (
                  <span className="shrink-0 rounded-full border border-[var(--color-border)] px-1.5 py-px text-[10px] font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
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
          'min-w-[220px]',
          inHeader && 'w-[min(17rem,calc(100vw-1rem))] [&_[role=menuitem]]:min-h-11',
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
        <DropdownMenuItem onClick={() => setArchivedOpen(true)}>
          <Archive size={13} aria-hidden />
          {t('chat:sidebar.archivedTitle')}
        </DropdownMenuItem>
        {isAdmin && (
          <DropdownMenuItem onClick={() => navigate('/admin')}>
            <ShieldCheck size={13} aria-hidden />
            {t('chat:userMenu.admin', { defaultValue: 'Admin' })}
          </DropdownMenuItem>
        )}
        <WorkspaceMenuItems onManage={() => setWsMembersOpen(true)} onCreate={() => setWsCreateOpen(true)} />
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
    <WorkspaceMembersDialog open={wsMembersOpen} onOpenChange={setWsMembersOpen} />
    <CreateWorkspaceDialog open={wsCreateOpen} onOpenChange={setWsCreateOpen} />
    <ArchivedDialog open={archivedOpen} onOpenChange={setArchivedOpen} />
    </>
  )
}

/**
 * ArchivedDialog — lists the user's archived conversations so they can be found
 * again, reopened, unarchived (back to the sidebar), or deleted. Archived chats
 * are fetched on open and live only in this dialog's local state.
 */
function ArchivedDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const { t } = useTranslation(['chat', 'common'])
  const navigate = useNavigate()
  const user = useAuth((s) => s.user)
  const canDeleteConversations = userCan(user, 'allow_conversation_deletion')
  const loadArchived = useConversations((s) => s.loadArchived)
  const unarchive = useConversations((s) => s.unarchiveConversation)
  const remove = useConversations((s) => s.deleteConversation)
  const [rows, setRows] = useState<Conversation[]>([])
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    setLoading(true)
    void loadArchived()
      .then(setRows)
      .finally(() => setLoading(false))
  }, [open, loadArchived])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <DialogTitle>{t('chat:sidebar.archivedTitle')}</DialogTitle>
          <DialogDescription>{t('chat:sidebar.archivedBody')}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {loading ? (
            <p className="py-4 text-sm text-[var(--color-fg-subtle)]">{t('common:common.loading')}</p>
          ) : rows.length === 0 ? (
            <p className="py-4 text-sm text-[var(--color-fg-muted)]">{t('chat:sidebar.archivedEmpty')}</p>
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-divider)]">
              {rows.map((c) => (
                <li key={c.id} className="flex items-center gap-2 py-2">
                  <button
                    type="button"
                    onClick={() => {
                      navigate(`/chat/${c.id}`)
                      onOpenChange(false)
                    }}
                    className="min-w-0 flex-1 truncate rounded-[6px] text-left text-sm text-[var(--color-fg)] interactive hover:text-[var(--color-accent)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    {truncate(c.title, 60)}
                  </button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      void unarchive(c.id)
                      setRows((r) => r.filter((x) => x.id !== c.id))
                      toast.success(t('chat:sidebar.unarchived'))
                    }}
                  >
                    {t('chat:sidebar.unarchive')}
                  </Button>
                  {canDeleteConversations ? (
                    <button
                      type="button"
                      aria-label={t('chat:sidebar.delete')}
                      onClick={() => {
                        void remove(c.id)
                        setRows((r) => r.filter((x) => x.id !== c.id))
                      }}
                      className="inline-flex size-7 items-center justify-center rounded-[7px] text-[var(--color-fg-subtle)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-danger)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <Trash2 size={13} aria-hidden />
                    </button>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('common:common.close', { defaultValue: 'Close' })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
