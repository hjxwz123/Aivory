import { useEffect, useMemo, useState } from 'react'
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
  Sparkles,
  FolderKanban,
  ChevronRight,
  BookText,
  BrainCircuit,
  ShieldCheck,
} from 'lucide-react'
import { Logo, LogoMark } from '@/components/brand/logo'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { initials } from '@/components/ui/avatar.utils'
import { Tooltip } from '@/components/ui/tooltip'
import { Badge } from '@/components/ui/badge'
import { KeyboardShortcut } from '@/components/ui/kbd'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
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
import { useConversations } from '@/store/conversations'
import { useProjects } from '@/store/projects'
import { useSettings } from '@/store/settings'
import { useAuth } from '@/store/auth'
import { useCommandMenu } from '@/hooks/use-command-menu'
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

export function Sidebar({ variant = 'desktop', onClose }: SidebarProps) {
  const navigate = useNavigate()
  const { id: currentId } = useParams<{ id?: string }>()
  const { t } = useTranslation('chat')
  const { t: tCommon } = useTranslation('common')
  const { t: tProjects } = useTranslation('projects')
  const { t: tNav } = useTranslation('nav')
  const allConversations = useConversations((s) => s.conversations)
  const conversations = useMemo(
    () => allConversations.filter((c) => !c.archived),
    [allConversations],
  )
  const projects = useProjects((s) => s.projects)
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
  const create = useConversations((s) => s.createConversation)
  const setOpen = useCommandMenu((s) => s.setOpen)
  const collapsed = useSettings((s) => s.sidebarCollapsed) && variant === 'desktop'
  const toggleSidebar = useSettings((s) => s.toggleSidebar)
  const [newProjectOpen, setNewProjectOpen] = useState(false)

  async function startNewChat() {
    const conv = await create()
    if (conv) navigate(`/chat/${conv.id}`)
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
  for (const c of others) grouped[bucketFor(c.updatedAt)].push(c)

  return (
    <aside
      data-variant={variant}
      data-collapsed={collapsed ? 'true' : 'false'}
      className={cn(
        'flex flex-col h-full bg-[var(--color-bg-muted)] border-r border-[var(--color-divider)]',
        variant === 'desktop' && (collapsed ? 'w-[3.5rem]' : 'w-[17.5rem]'),
        variant === 'sheet' && 'w-full',
        'transition-[width] duration-[220ms] ease-[var(--ease-out)]',
      )}
    >
      {/* Header */}
      <div className="flex items-center justify-between px-3 h-[56px] shrink-0">
        {!collapsed ? (
          <Link to="/" className="inline-flex items-center" aria-label={tCommon('aria.homeLink')}>
            <Logo size="sm" />
          </Link>
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
      </div>

      {/* Actions */}
      <div className={cn('px-3 flex flex-col gap-1', collapsed && 'items-center')}>
        <Tooltip content={collapsed ? t('sidebar.newChat') : ''} side="right">
          <button
            type="button"
            onClick={() => void startNewChat()}
            className={cn(
              'inline-flex items-center gap-2 h-9 rounded-[10px] text-sm font-medium',
              'bg-[var(--color-surface)] border border-[var(--color-border)] text-[var(--color-fg)]',
              'hover:bg-[var(--color-bg)] hover:border-[var(--color-border-strong)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-9 justify-center px-0' : 'w-full justify-between px-3',
            )}
          >
            <span className="inline-flex items-center gap-2">
              <Plus size={15} aria-hidden />
              {!collapsed && <span>{t('sidebar.newChat')}</span>}
            </span>
            {!collapsed && <KeyboardShortcut combo={[modKey(), 'Shift', 'O']} />}
          </button>
        </Tooltip>

        <Tooltip content={collapsed ? `${t('sidebar.search')} (${modKey()}K)` : ''} side="right">
          <button
            type="button"
            onClick={() => setOpen(true)}
            className={cn(
              'inline-flex items-center gap-2 h-9 rounded-[10px] text-sm',
              'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              collapsed ? 'w-9 justify-center px-0' : 'w-full justify-between px-3',
            )}
          >
            <span className="inline-flex items-center gap-2">
              <Search size={15} aria-hidden />
              {!collapsed && <span>{t('sidebar.search')}</span>}
            </span>
            {!collapsed && <KeyboardShortcut combo={[modKey(), 'K']} />}
          </button>
        </Tooltip>

        <Tooltip content={collapsed ? tNav('projects') : ''} side="right">
          <Link
            to="/projects"
            onClick={onClose}
            className={cn(
              'inline-flex items-center gap-2 h-9 rounded-[10px] text-sm',
              'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive',
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
                className="inline-flex items-center justify-center size-5 rounded-[5px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
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

      {/* Conversation list */}
      {!collapsed && (
        <div className="mt-2 flex-1 min-h-0 overflow-y-auto scrollbar-thin">
          {starred.length > 0 && (
            <Group label={t('sidebar.starred')} items={starred} currentId={currentId} onSelect={onClose} t={t} />
          )}
          {groupOrder.map(
            (g) =>
              grouped[g].length > 0 && (
                <Group key={g} label={t(`buckets.${g}`)} items={grouped[g]} currentId={currentId} onSelect={onClose} t={t} />
              ),
          )}
          {conversations.length === 0 && (
            <p className="px-4 py-6 text-xs text-[var(--color-fg-subtle)] text-center">
              {t('sidebar.empty')}
            </p>
          )}
        </div>
      )}

      {/* Footer */}
      <div className={cn('mt-auto border-t border-[var(--color-divider)] p-2', collapsed && 'flex items-center justify-center')}>
        <UserMenu collapsed={collapsed} />
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
      <h3 className="px-4 py-1 text-[10px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
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
  const rename = useConversations((s) => s.renameConversation)
  const remove = useConversations((s) => s.deleteConversation)
  const star = useConversations((s) => s.toggleStar)
  const archive = useConversations((s) => s.archiveConversation)
  const navigate = useNavigate()
  const [renaming, setRenaming] = useState(false)
  const [draft, setDraft] = useState(conversation.title)
  const [confirm, setConfirm] = useState(false)

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
          className="block px-2.5 py-2 pr-9 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] rounded-[10px]"
        >
          <span
            className={cn(
              'block truncate text-[13.5px] leading-snug',
              active ? 'text-[var(--color-fg)] font-medium' : 'text-[var(--color-fg-muted)]',
            )}
          >
            {conversation.starred ? '☆ ' : ''}
            {truncate(conversation.title, 50)}
          </span>
        </Link>
        <div className="absolute right-1.5 top-1/2 -translate-y-1/2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label={t('sidebar.actions')}
                className="inline-flex items-center justify-center size-6 rounded-[6px] opacity-0 group-hover/conv:opacity-100 data-[state=open]:opacity-100 text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <MoreHorizontal size={13} aria-hidden />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[180px]">
              <DropdownMenuItem onClick={() => setRenaming(true)}>
                <Pencil size={13} aria-hidden />
                {t('sidebar.rename')}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => star(conversation.id)}>
                <Star size={13} aria-hidden />
                {conversation.starred ? t('common:actions.unstar') : t('common:actions.star')}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => toast.info(t('actions.shareMocked'))}>
                <Share2 size={13} aria-hidden />
                {t('sidebar.share')}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => archive(conversation.id)}>
                <Archive size={13} aria-hidden />
                {t('sidebar.archive')}
              </DropdownMenuItem>
              <DropdownMenuItem destructive onClick={() => setConfirm(true)}>
                <Trash2 size={13} aria-hidden />
                {t('sidebar.delete')}
              </DropdownMenuItem>
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
  const { t } = useTranslation(['chat', 'common', 'settings'])
  const user = useAuth((s) => s.user)
  const logout = useAuth((s) => s.logout)
  const displayName = user?.name || user?.email?.split('@')[0] || 'Aurelia'
  const isAdmin = user?.role === 'admin'
  const [archivedOpen, setArchivedOpen] = useState(false)
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
            <AvatarFallback>{initials(displayName)}</AvatarFallback>
          </Avatar>
          {!collapsed && (
            <div className="flex-1 min-w-0 text-left">
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-medium text-[var(--color-fg)] truncate">{displayName}</span>
                {isAdmin && (
                  <Badge variant="accent" size="xs" leadingIcon={<Sparkles size={9} aria-hidden />}>
                    ADMIN
                  </Badge>
                )}
              </div>
              <span className="text-[11px] text-[var(--color-fg-subtle)] truncate block">{user?.email}</span>
            </div>
          )}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="min-w-[220px]">
        <DropdownMenuItem onClick={() => navigate('/settings/account')}>
          <Settings size={13} aria-hidden />
          {t('settings:user.settings')}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => navigate('/memory')}>
          <BrainCircuit size={13} aria-hidden />
          {t('chat:userMenu.memory', { defaultValue: 'Memory' })}
        </DropdownMenuItem>
        <DropdownMenuItem onClick={() => navigate('/kb')}>
          <BookText size={13} aria-hidden />
          {t('chat:userMenu.knowledge', { defaultValue: 'Knowledge' })}
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
