import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
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
  FolderKanban,
  ChevronRight,
  BookText,
  Wand2,
  ImagePlus,
  ShieldCheck,
  Layers,
  Languages,
  Loader2,
  X,
  ArrowLeftRight,
  Briefcase,
} from 'lucide-react'
import { Logo, LogoMark } from '@/components/brand/logo'
import { useWorkspaces } from '@/store/workspaces'
import {
  CreateWorkspaceDialog,
  SpaceSwitcherButton,
  WorkspaceMembersDialog,
  WorkspaceMenuItems,
} from '@/components/sidebar/workspace-menu'
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
import { useProjects } from '@/store/projects'
import { useModels } from '@/store/models'
import { useSettings } from '@/store/settings'
import { useAuth } from '@/store/auth'
import { useLanguage } from '@/store/language'
import { SUPPORTED_LANGUAGES } from '@/i18n'
import { useCommandMenu } from '@/hooks/use-command-menu'
import { useOpenSettings } from '@/hooks/use-open-settings'
import { useCopy } from '@/hooks/use-clipboard'
import { conversationsApi, ApiError } from '@/api'
import { accentClasses } from '@/lib/project-helpers'
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
  const activeWorkspace = useWorkspaces((s) => (s.activeId ? s.workspaces.find((w) => w.id === s.activeId) : undefined))
  const navigate = useNavigate()
  const { id: currentId } = useParams<{ id?: string }>()
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
  const conversations = useMemo(
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
  const projects = useProjects((s) => s.projects)
  // §4.20: show the Draw entry only when an image model is configured.
  const hasImageModels = useModels((s) => s.imageModels.length > 0)
  const recentProjects = useMemo(
    () =>
      projects
        .slice()
        .sort((a, b) => {
          if ((a.pinned ? 1 : 0) !== (b.pinned ? 1 : 0)) return a.pinned ? -1 : 1
          return b.updatedAt - a.updatedAt
        })
        .slice(0, 5),
    [projects],
  )
  const setOpen = useCommandMenu((s) => s.setOpen)
  const collapsed = useSettings((s) => s.sidebarCollapsed) && variant === 'desktop'
  const toggleSidebar = useSettings((s) => s.toggleSidebar)
  const [newProjectOpen, setNewProjectOpen] = useState(false)

  function startNewChat() {
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
      data-variant={variant}
      data-collapsed={collapsed ? 'true' : 'false'}
      aria-label={t('sidebar.navAria', { defaultValue: 'Conversation navigation' })}
      className={cn(
        'flex flex-col h-full bg-[var(--color-sidebar-bg)]',
        variant === 'desktop' && (collapsed ? 'w-[3.5rem]' : 'w-[17.5rem]'),
        variant === 'sheet' && 'w-full',
        'transition-[width] duration-[220ms] ease-[var(--ease-out)]',
      )}
    >
      {/* Header — inside a workspace the brand slot shows the WORKSPACE NAME
          (§workspaces spec: sidebar 上方原先显示 aurelia 的地方显示工作空间名称).
          Keyed on the active space so switching replays a fade-in (§ workspace
          switch animation) instead of the name jump-cutting. */}
      <div className="flex items-center justify-between px-3 h-[56px] shrink-0">
        {!collapsed ? (
          activeWorkspace ? (
            <div key={activeWorkspace.id} className="page-enter flex min-w-0 items-center gap-1.5">
              <Link
                to="/"
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
          <Link key="personal" to="/" className="page-enter inline-flex items-center" aria-label={tCommon('aria.homeLink')}>
            <Logo size="sm" />
          </Link>
          )
        ) : (
          <Link to="/" className="mx-auto" aria-label={tCommon('aria.homeLink')}>
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
            className="inline-flex items-center justify-center size-[var(--tap-min)] rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <X size={18} aria-hidden />
          </button>
        )}
      </div>

      {/* Actions */}
      <div className={cn('px-3 flex flex-col gap-1', collapsed && 'items-center')}>
        <Tooltip content={collapsed ? t('sidebar.newChat') : ''} side="right">
          <button
            type="button"
            onClick={() => void startNewChat()}
            className={cn(
              'inline-flex items-center gap-2 h-9 max-lg:h-[var(--tap-min)] rounded-[10px] text-sm font-medium',
              'bg-[var(--color-bg-muted)] border border-[var(--color-border-strong)] text-[var(--color-fg)]',
              'hover:bg-[var(--color-bg)] hover:border-[var(--color-border-strong)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-9 justify-center px-0' : 'w-full justify-between px-3',
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
              'inline-flex items-center gap-2 h-9 max-lg:h-[var(--tap-min)] rounded-[10px] text-sm',
              'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-9 justify-center px-0' : 'w-full justify-between px-3',
            )}
          >
            <span className="inline-flex items-center gap-2">
              <Search size={15} aria-hidden />
              {!collapsed && <span>{t('sidebar.search')}</span>}
            </span>
            {!collapsed && <KeyboardShortcut combo={[modKey(), 'K']} className="max-lg:hidden" />}
          </button>
        </Tooltip>

        <Tooltip content={collapsed ? tNav('projects') : ''} side="right">
          <Link
            to="/projects"
            onClick={onClose}
            className={cn(
              'inline-flex items-center gap-2 h-9 max-lg:h-[var(--tap-min)] rounded-[10px] text-sm',
              'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-9 justify-center px-0' : 'w-full justify-between px-3',
            )}
          >
            <span className="inline-flex items-center gap-2">
              <FolderKanban size={15} aria-hidden />
              {!collapsed && <span>{tNav('projects')}</span>}
            </span>
            {!collapsed && projects.length > 0 && (
              <span className="text-[10.5px] tabular-nums text-[var(--color-fg-subtle)]">
                {projects.length}
              </span>
            )}
          </Link>
        </Tooltip>

        {/* §4.20 Draw — opens a new conversation pre-set to an image model. */}
        {hasImageModels && (
          <Tooltip content={collapsed ? tNav('draw', { defaultValue: 'Draw' }) : ''} side="right">
            <Link
              to="/?mode=draw"
              onClick={onClose}
              className={cn(
                'inline-flex items-center gap-2 h-9 max-lg:h-[var(--tap-min)] rounded-[10px] text-sm',
                'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                collapsed ? 'w-9 justify-center px-0' : 'w-full justify-start px-3',
              )}
            >
              <ImagePlus size={15} aria-hidden />
              {!collapsed && <span>{tNav('draw', { defaultValue: 'Draw' })}</span>}
            </Link>
          </Tooltip>
        )}
      </div>

      {/* Projects (expanded only) */}
      {!collapsed && recentProjects.length > 0 && (
        <div className="mt-3 px-1">
          <div className="flex items-center justify-between px-3 py-1">
            <h3 className="text-[10px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
              {tNav('projects')}
            </h3>
            <Tooltip content={tProjects('nav.newProject')}>
              <button
                type="button"
                onClick={() => setNewProjectOpen(true)}
                aria-label={tProjects('nav.newProject')}
                className="inline-flex items-center justify-center size-5 max-lg:size-9 rounded-[5px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <Plus size={11} aria-hidden />
              </button>
            </Tooltip>
          </div>
          <ul className="px-1">
            {recentProjects.map((p) => {
              const accent = accentClasses(p.accent)
              return (
                <li key={p.id}>
                  <Link
                    to={`/projects/${p.id}`}
                    onClick={onClose}
                    className={cn(
                      'group/p flex items-center gap-2 px-2 py-1.5 rounded-[8px] interactive',
                      'text-[13px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)]',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    )}
                  >
                    <span
                      className={cn(
                        'inline-flex items-center justify-center size-5 rounded-[6px] shrink-0 text-[11px] font-medium',
                        accent.chip,
                      )}
                      aria-hidden
                    >
                      {p.emoji ?? p.name.trim().slice(0, 1).toUpperCase()}
                    </span>
                    <span className="flex-1 truncate">{truncate(p.name, 30)}</span>
                    <ChevronRight
                      size={11}
                      aria-hidden
                      className="text-[var(--color-fg-faint)] opacity-0 group-hover/p:opacity-100"
                    />
                  </Link>
                </li>
              )
            })}
          </ul>
        </div>
      )}

      {/* Conversation list — while a workspace switch is reloading data, the list
          fades out and a spinner takes its place instead of flashing the old
          (or momentarily empty) space's rows (§ workspace switch animation). */}
      {!collapsed && (
        <div className="relative mt-2 flex-1 min-h-0">
          <div
            ref={listScrollRef}
            className={cn(
              'h-full overflow-y-auto scrollbar-thin transition-opacity duration-200',
              switching && 'opacity-0 pointer-events-none',
            )}
          >
            {starred.length > 0 && (
              <Group label={t('sidebar.starred')} items={starred} currentId={currentId} onSelect={onClose} t={t} />
            )}
            {groupOrder.map(
              (g) =>
                grouped[g].length > 0 && (
                  <Group key={g} label={t(`buckets.${g}`)} items={grouped[g]} currentId={currentId} onSelect={onClose} t={t} />
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
      <div className={cn('mt-auto p-2', collapsed && 'flex items-center justify-center')}>
        <div className={cn('flex items-center gap-1', collapsed && 'flex-col')}>
          <div className="min-w-0 flex-1">
            <UserMenu collapsed={collapsed} />
          </div>
          <SpaceSwitcherButton />
        </div>
      </div>

      <NewProjectDialog open={newProjectOpen} onOpenChange={setNewProjectOpen} />
    </aside>
  )
}

function Group({
  label,
  items,
  currentId,
  onSelect,
  t,
}: {
  label: string
  items: ReturnType<typeof useConversations.getState>['conversations']
  currentId: string | undefined
  onSelect?: () => void
  t: TFunction<'chat'>
}) {
  return (
    <div className="py-1.5">
      <h3 className="px-4 py-1 max-lg:py-1.5 text-[10px] max-lg:text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
        {label}
      </h3>
      <ul>
        {items.map((c) => (
          <ConversationItem key={c.id} conversation={c} active={c.id === currentId} onSelect={onSelect} t={t} />
        ))}
      </ul>
    </div>
  )
}

function ConversationItem({
  conversation,
  active,
  onSelect,
  t,
}: {
  conversation: ReturnType<typeof useConversations.getState>['conversations'][number]
  active: boolean
  onSelect?: () => void
  t: TFunction<'chat'>
}) {
  const meId = useAuth((s) => s.user?.id)
  const rename = useConversations((s) => s.renameConversation)
  const remove = useConversations((s) => s.deleteConversation)
  const star = useConversations((s) => s.toggleStar)
  const archive = useConversations((s) => s.archiveConversation)
  const navigate = useNavigate()
  const { copy } = useCopy()
  const [renaming, setRenaming] = useState(false)
  const [draft, setDraft] = useState(conversation.title)
  const [confirm, setConfirm] = useState(false)

  // Create (or refresh) a public share and copy its link in one tap (§ sharing).
  // Managing / revoking the share lives in the conversation's Share dialog.
  async function shareConversation() {
    try {
      const s = await conversationsApi.createShare(conversation.id)
      await copy(`${window.location.origin}/share/${s.id}`)
      toast.success(t('share.linkCopied'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('share.failed'))
    }
  }

  return (
    <li>
      <div
        className={cn(
          'group/conv relative mx-2 my-px rounded-[10px] interactive',
          active ? 'bg-[var(--color-surface)] shadow-[var(--shadow-xs)]' : 'hover:bg-[var(--color-bg)]',
        )}
      >
        <Link
          to={`/chat/${conversation.id}`}
          onClick={onSelect}
          className="block px-2.5 py-2 pr-9 max-lg:py-2.5 max-lg:pr-12 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[10px]"
        >
          <span className="flex items-center gap-2">
            <span
              className={cn(
                'min-w-0 flex-1 truncate text-[13.5px] max-lg:text-[15px] leading-snug',
                active ? 'text-[var(--color-fg)] font-medium' : 'text-[var(--color-fg-muted)]',
              )}
            >
              {conversation.starred ? '☆ ' : ''}
              {conversation.title || t('untitled')}
            </span>
            {conversation.workspaceId && conversation.creatorName ? (
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
        <div className="absolute right-1.5 top-1/2 -translate-y-1/2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label={t('sidebar.actions')}
                className="inline-flex items-center justify-center size-6 max-lg:size-10 max-lg:opacity-100 rounded-[6px] opacity-0 group-hover/conv:opacity-100 data-[state=open]:opacity-100 text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <MoreHorizontal size={13} aria-hidden />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[180px]">
              <DropdownMenuItem onClick={() => setRenaming(true)}>
                <Pencil size={13} aria-hidden />
                {t('sidebar.rename')}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => {
                void star(conversation.id)
                toast.success(conversation.starred ? t('common:actions.unstar') : t('common:actions.star'))
              }}>
                <Star size={13} aria-hidden />
                {conversation.starred ? t('common:actions.unstar') : t('common:actions.star')}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => void shareConversation()}>
                <Share2 size={13} aria-hidden />
                {t('sidebar.share')}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <MoveToProjectSub conversationId={conversation.id} currentProjectId={conversation.projectId} />
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => {
                archive(conversation.id)
                toast.success(t('sidebar.archived'))
              }}>
                <Archive size={13} aria-hidden />
                {t('sidebar.archive')}
              </DropdownMenuItem>
              {!conversation.workspaceId || conversation.creatorId === meId ? (
                <DropdownMenuItem destructive onClick={() => setConfirm(true)}>
                  <Trash2 size={13} aria-hidden />
                  {t('sidebar.delete')}
                </DropdownMenuItem>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {/* Rename dialog */}
      <Dialog open={renaming} onOpenChange={setRenaming}>
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
      <Dialog open={confirm} onOpenChange={setConfirm}>
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
                navigate('/chat')
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

function UserMenu({ collapsed }: { collapsed: boolean }) {
  const navigate = useNavigate()
  const openSettings = useOpenSettings()
  const { t } = useTranslation(['chat', 'common', 'settings'])
  const user = useAuth((s) => s.user)
  const logout = useAuth((s) => s.logout)
  const displayName = user?.name || user?.email?.split('@')[0] || 'Aurelia'
  const avatarUrl = (user?.settings as Record<string, unknown> | undefined)?.avatar_url as string | undefined
  const isAdmin = user?.role === 'admin'
  const lang = useLanguage((s) => s.lang)
  const setLang = useLanguage((s) => s.setLang)
  const [archivedOpen, setArchivedOpen] = useState(false)
  const [wsMembersOpen, setWsMembersOpen] = useState(false)
  const [wsCreateOpen, setWsCreateOpen] = useState(false)
  return (
    <>
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={t('settings:user.menuAria')}
          className={cn(
            'flex items-center gap-2.5 rounded-[10px] interactive',
            'hover:bg-[var(--color-bg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            collapsed ? 'p-1' : 'w-full p-2',
          )}
        >
          <Avatar size="md" tone="clay">
            {avatarUrl ? <AvatarImage src={avatarUrl} alt={displayName} /> : null}
            <AvatarFallback>{initials(displayName)}</AvatarFallback>
          </Avatar>
          {!collapsed && (
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
      <DropdownMenuContent align="start" side="top" className="min-w-[220px]">
        <DropdownMenuItem onClick={() => openSettings('account')}>
          <Settings size={13} aria-hidden />
          {t('settings:user.settings')}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => openSettings('personalization')}>
          <Wand2 size={13} aria-hidden />
          {t('chat:userMenu.personalization', { defaultValue: 'Personalization' })}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => navigate('/kb')}>
          <BookText size={13} aria-hidden />
          {t('chat:userMenu.knowledge', { defaultValue: 'Knowledge' })}
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
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={() =>
            void (async () => {
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
