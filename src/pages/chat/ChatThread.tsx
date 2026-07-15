import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { MoreHorizontal, Pencil, Share2, Star, Trash2, Archive, ArrowDown, FolderKanban, Copy, Check, Globe, Loader2, Menu, Files, GitBranch } from 'lucide-react'
import { Composer } from '@/components/chat/composer'
import { MessageList } from '@/components/chat/message-list'
import { InlineThreadLayer } from '@/components/chat/inline-thread-layer'
import { ModelPicker } from '@/components/chat/model-picker'
import { EmptyState } from '@/components/ui/empty-state'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tooltip } from '@/components/ui/tooltip'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { useConversations } from '@/store/conversations'
import { useModels } from '@/store/models'
import { useProjects } from '@/store/projects'
import { useUI } from '@/store/ui'
import { useWorkspaces } from '@/store/workspaces'
import { useConversationFiles } from '@/store/conversation-files'
import { useMediaQuery } from '@/hooks/use-media-query'
import { conversationsApi, ApiError } from '@/api'
import type { ApiShareInfo } from '@/api/types'
import { toast } from '@/hooks/use-toast'
import { useCopy } from '@/hooks/use-clipboard'
import { ConversationOutline } from '@/components/chat/conversation-outline'
import { ConversationMinimap } from '@/components/chat/conversation-minimap'
import { accentClasses } from '@/lib/project-helpers'
import { cn, truncate } from '@/lib/utils'
import type { Attachment } from '@/types/chat'
import type { ToolMode } from '@/lib/tool-mode'

export default function ChatThread() {
  const { id } = useParams<{ id: string }>()
  // `?m=<messageId>` (set by the command-menu content search) asks the thread to
  // open scrolled to a specific message instead of pinned to the bottom.
  const [searchParams] = useSearchParams()
  const jumpTo = searchParams.get('m') || undefined
  // Nonce that changes when the user re-selects the same search result, so the
  // jump re-fires even though the message id (?m=) is unchanged.
  const jumpKey = searchParams.get('j') || undefined
  const navigate = useNavigate()
  const { t } = useTranslation(['chat', 'common', 'projects'])
  const conversation = useConversations((s) => s.conversations.find((c) => c.id === id))
  const [loadStatus, setLoadStatus] = useState<'idle' | 'loading' | 'done'>('idle')
  const loadOne = useConversations((s) => s.loadOne)
  const loadInlineThreads = useConversations((s) => s.loadInlineThreads)
  const setModel = useConversations((s) => s.setModel)
  const setFast = useConversations((s) => s.setFast)
  const setKBs = useConversations((s) => s.setKBs)
  const rename = useConversations((s) => s.renameConversation)
  const star = useConversations((s) => s.toggleStar)
  const remove = useConversations((s) => s.deleteConversation)
  const archive = useConversations((s) => s.archiveConversation)
  const sendMessage = useConversations((s) => s.sendMessage)
  const abortStream = useConversations((s) => s.abortStream)
  const project = useProjects((s) =>
    conversation?.projectId ? s.projects.find((p) => p.id === conversation.projectId) : undefined,
  )

  const isDesktop = useMediaQuery('(min-width: 1024px)')
  const openNav = useUI((s) => s.setNavOpen)
  // §workspaces: a space switch replaces the conversations cache with summary
  // rows (no messages) without changing the route id — re-hydrate when the
  // switch settles. switchTo() flips `switching` false only AFTER the new
  // space's list landed, so this loadOne can't be clobbered by that fetch.
  const wsSwitching = useWorkspaces((s) => s.switching)
  const openFilesDrawer = useConversationFiles((s) => s.openDrawer)
  const closeFilesDrawer = useConversationFiles((s) => s.close)
  const filesDrawerOpen = useConversationFiles((s) => s.open)
  // On mobile this page renders one combined bar (menu + title + controls), so
  // tell the layout to drop its standalone brand bar while we're mounted.
  useEffect(() => {
    useUI.getState().setPageOwnsTopBar(true)
    return () => useUI.getState().setPageOwnsTopBar(false)
  }, [])

  const scrollRef = useRef<HTMLDivElement>(null)
  // Tracks the conversation id we've already positioned at the bottom, so the
  // instant "jump to newest" only runs once per conversation (not on every
  // streaming token).
  const positionedFor = useRef<string | null>(null)
  const [autoFollow, setAutoFollow] = useState(true)
  const [showJump, setShowJump] = useState(false)

  const [renaming, setRenaming] = useState(false)
  const [renameDraft, setRenameDraft] = useState('')
  const [renameError, setRenameError] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [shareOpen, setShareOpen] = useState(false)
  const [share, setShare] = useState<ApiShareInfo | null>(null)
  const [shareLoading, setShareLoading] = useState(false)
  const [shareBusy, setShareBusy] = useState(false)
  const [outlineOpen, setOutlineOpen] = useState(false)
  // Mobile: the thread's secondary actions (outline / files / rename / share /
  // archive / delete) collapse into one trailing overflow that opens this bottom
  // action Sheet, keeping the header a calm three-zone bar (§ mobile redesign).
  const [actionsOpen, setActionsOpen] = useState(false)
  const { copied, copy } = useCopy()
  const shareUrl = share ? `${window.location.origin}/share/${share.id}` : ''

  // Load the current share state whenever the dialog opens.
  useEffect(() => {
    if (!shareOpen || !id) return
    let active = true
    setShareLoading(true)
    conversationsApi
      .getShare(id)
      .then((r) => active && setShare(r.share))
      .catch(() => active && setShare(null))
      .finally(() => active && setShareLoading(false))
    return () => {
      active = false
    }
  }, [shareOpen, id])

  async function createShare() {
    if (!id) return
    setShareBusy(true)
    try {
      setShare(await conversationsApi.createShare(id))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('chat:share.failed'))
    } finally {
      setShareBusy(false)
    }
  }

  async function revokeShare() {
    if (!id) return
    setShareBusy(true)
    try {
      await conversationsApi.deleteShare(id)
      setShare(null)
      toast.success(t('chat:share.revoked'))
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('chat:share.failed'))
    } finally {
      setShareBusy(false)
    }
  }

  const streaming = useMemo(
    () => conversation?.messages.some((m) => m.streaming),
    [conversation?.messages],
  )

  // Hydrate the active conversation + its messages from the backend whenever
  // the id changes — and again after a workspace switch settles (the switch
  // replaced this conversation's cache entry with a message-less summary row).
  useEffect(() => {
    if (!id || wsSwitching) return
    setLoadStatus('loading')
    // Jumping to a specific message needs the whole path loaded so the target is
    // present; a normal open paginates (latest page, older on scroll-up).
    Promise.all([loadOne(id, { full: Boolean(jumpTo) }), loadInlineThreads(id)]).finally(() => {
      setLoadStatus('done')
    })
  }, [id, jumpTo, wsSwitching, loadOne, loadInlineThreads])

  useEffect(() => {
    // When jumping to a specific message, don't auto-follow/pin to the bottom —
    // MessageList scrolls to the target instead. Mark the conversation as already
    // positioned so a later scroll-to-bottom follows the tail smoothly rather than
    // snapping (the bottom-pin first-load path is for fresh opens, not jumps).
    setAutoFollow(!jumpTo)
    setShowJump(false)
    setOutlineOpen(false)
    positionedFor.current = jumpTo ? (id ?? null) : null
  }, [id, jumpTo])

  // Hard-pin the scroller to the bottom across the next few frames. Late-laying-out
  // content (an empty assistant bubble that fills in, code blocks, math, images)
  // grows the transcript after the first jump, and a one-shot scroll would strand
  // the view part-way up — which, with the lazy window, reads as the oldest message.
  const pinToBottom = useCallback(() => {
    const el = scrollRef.current
    if (!el) return () => { }
    const pin = () => {
      el.scrollTop = el.scrollHeight
    }
    pin()
    const raf = requestAnimationFrame(() => {
      pin()
      requestAnimationFrame(pin)
    })
    const tmo = window.setTimeout(pin, 150)
    return () => {
      cancelAnimationFrame(raf)
      clearTimeout(tmo)
    }
  }, [])

  // Keep the newest message in view. The first load of a conversation pins
  // instantly; afterwards we follow the tail smoothly while the user is parked at
  // the bottom (autoFollow). Sending forces a pin in `submit` directly, so it
  // never depends on this effect's timing.
  useEffect(() => {
    if (!autoFollow) return
    const el = scrollRef.current
    if (!el || !conversation?.messages.length) return

    const firstLoad = positionedFor.current !== conversation.id
    if (firstLoad) {
      positionedFor.current = conversation.id
      return pinToBottom()
    }
    el.scrollTo({ top: el.scrollHeight, behavior: streaming ? 'auto' : 'smooth' })
  }, [conversation?.id, conversation?.messages, autoFollow, streaming, pinToBottom])

  function handleScroll(e: React.UIEvent<HTMLDivElement>) {
    const el = e.currentTarget
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
    const atBottom = distanceFromBottom < 80
    setAutoFollow(atBottom)
    setShowJump(!atBottom && el.scrollHeight - el.clientHeight > 200)
  }

  if (!conversation) {
    // A workspace switch transiently drops the open conversation from the
    // cache before the settle-refire re-hydrates it — show the spinner, not a
    // premature "conversation not found".
    if (loadStatus !== 'done' || wsSwitching) {
      return (
        <div className="flex-1 grid place-items-center">
          <div className="flex flex-col items-center gap-4 text-[var(--color-fg-muted)]">
            <Loader2 size={24} className="animate-spin" aria-hidden />
            <span className="text-sm">{t('common:common.loading')}</span>
          </div>
        </div>
      )
    }
    return (
      <div className="flex-1 grid place-items-center">
        <EmptyState
          title={t('chat:thread.notFoundTitle')}
          description={t('chat:thread.notFoundBody')}
          action={
            <Button onClick={() => navigate('/chat')}>{t('chat:thread.goToChat')}</Button>
          }
        />
      </div>
    )
  }

  function submit(
    text: string,
    attachments: Attachment[],
    opts: {
      mode?: 'default' | 'deep-research' | 'canvas'
      params?: Record<string, unknown>
      imageStyleId?: string
      verify?: boolean
      toolMode: ToolMode
      webSearch?: boolean
      fast?: boolean
    },
  ) {
    if (!conversation) return
    void sendMessage({
      conversationId: conversation.id,
      text,
      modelId: conversation.modelId,
      attachments,
      mode: opts.mode,
      params: opts.params,
      imageStyleId: opts.imageStyleId,
      verify: opts.verify,
      toolMode: opts.toolMode,
      webSearch: opts.webSearch,
      fast: opts.fast,
    })
    // Force the view to the freshly appended turn now — don't rely on the
    // auto-follow effect, whose scroll a content reflow or a stale autoFollow
    // can defeat (leaving the lazy list parked on the oldest message).
    setAutoFollow(true)
    setShowJump(false)
    pinToBottom()
  }

  function stopAll() {
    if (!conversation) return
    for (const m of conversation.messages) {
      if (m.streaming) abortStream(m.id)
    }
  }

  function jumpToBottom() {
    const el = scrollRef.current
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    setAutoFollow(true)
  }

  // §2.3-D cross-vendor downgrade: only warn when switching provider type.
  // Same-provider swaps (Sonnet → Opus) keep raw replay + full fidelity.
  // Shared by the desktop toolbar and the mobile header's model label.
  function handleModelChange(nextId: string) {
    if (!conversation) return
    const all = useModels.getState().models
    const cur = all.find((m) => m.id === conversation.modelId)
    const next = all.find((m) => m.id === nextId)
    const sameProvider = cur && next && cur.channel_id === next.channel_id
    void setModel(conversation.id, nextId)
    if (!sameProvider) {
      toast.success(t('chat:thread.modelSwitched'), t('chat:thread.modelSwitchedBody'))
    }
  }

  // §fast-mode: switch the conversation between 快速 and 进阶.
  function handleFastChange(next: boolean) {
    if (!conversation) return
    void setFast(conversation.id, next)
  }

  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* Topbar — desktop keeps the full inline toolbar; mobile is a calm
          three-zone bar (menu • title+model • one overflow) like ChatGPT/Gemini. */}
      {isDesktop ? (
        <header className="flex items-center gap-3 h-[var(--layout-topbar-h)] px-4 sm:px-6 bg-[var(--color-bg)]/85 backdrop-blur-sm">
          <div className="flex-1 min-w-0 flex flex-col">
            <h1 className="font-medium text-[var(--color-fg)] text-[15px] truncate">
              {truncate(conversation.title || t('untitled'), 80)}
            </h1>
            {project ? (
              <Link
                to={`/projects/${project.id}`}
                className={cn(
                  'mt-0.5 inline-flex items-center gap-1 self-start text-[11px] interactive rounded-[6px] px-1.5 py-0.5 -ml-1.5',
                  accentClasses(project.accent).chip,
                  'hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <FolderKanban size={10} aria-hidden />
                {t('projects:badge.in', { name: truncate(project.name, 28) })}
              </Link>
            ) : null}
          </div>
          <ModelPicker value={conversation.modelId} onChange={handleModelChange} fast={conversation.fast} onFastChange={handleFastChange} />
          <Tooltip content={t('chat:topbar.outlineTooltip', { defaultValue: 'Conversation outline' })}>
            <button
              type="button"
              onClick={() => setOutlineOpen((o) => !o)}
              aria-label={t('chat:topbar.outlineTooltip', { defaultValue: 'Conversation outline' })}
              aria-pressed={outlineOpen}
              className={cn(
                'inline-flex items-center justify-center size-8 rounded-[8px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                outlineOpen
                  ? 'bg-[var(--color-bg-muted)] text-[var(--color-fg)]'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <GitBranch size={14} aria-hidden />
            </button>
          </Tooltip>
          <Tooltip content={t('chat:files.tooltip')}>
            <button
              type="button"
              onClick={() => (filesDrawerOpen ? closeFilesDrawer() : openFilesDrawer(conversation.id))}
              aria-label={t('chat:files.title')}
              aria-pressed={filesDrawerOpen}
              className={cn(
                'inline-flex items-center justify-center size-8 rounded-[8px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                filesDrawerOpen
                  ? 'bg-[var(--color-bg-muted)] text-[var(--color-fg)]'
                  : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <Files size={14} aria-hidden />
            </button>
          </Tooltip>
          <DropdownMenu>
            <Tooltip content={t('chat:actions.more')}>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  aria-label={t('chat:sidebar.actions')}
                  className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <MoreHorizontal size={14} aria-hidden />
                </button>
              </DropdownMenuTrigger>
            </Tooltip>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={() => { setRenameDraft(conversation.title); setRenaming(true) }}>
                <Pencil size={13} aria-hidden /> {t('chat:sidebar.rename')}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => void star(conversation.id)}>
                <Star size={13} aria-hidden /> {conversation.starred ? t('common:actions.unstar') : t('common:actions.star')}
              </DropdownMenuItem>
              <DropdownMenuItem onSelect={() => setShareOpen(true)}>
                <Share2 size={13} aria-hidden /> {t('chat:sidebar.share')}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onSelect={async () => { toast.info(t('chat:sidebar.archiving', { defaultValue: 'Archiving…' })); await archive(conversation.id); toast.success(t('chat:sidebar.archived')); navigate('/chat') }}>
                <Archive size={13} aria-hidden /> {t('chat:sidebar.archive')}
              </DropdownMenuItem>
              <DropdownMenuItem destructive onSelect={() => setConfirmDelete(true)}>
                <Trash2 size={13} aria-hidden /> {t('chat:sidebar.delete')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </header>
      ) : (
        <header className="grid grid-cols-[var(--tap-min)_1fr_var(--tap-min)] items-center gap-1 h-[var(--layout-topbar-h-mobile)] px-2 bg-[var(--color-bg)]/85 backdrop-blur-sm">
          <button
            type="button"
            aria-label={t('chat:commandMenu.actions.toggleSidebar')}
            onClick={() => openNav(true)}
            className="inline-flex items-center justify-center size-[var(--tap-min)] rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <Menu size={18} aria-hidden />
          </button>
          <div className="min-w-0 flex flex-col items-center">
            <h1 className="max-w-full truncate text-[14px] font-medium text-[var(--color-fg)] leading-tight">
              {truncate(conversation.title || t('untitled'), 60)}
            </h1>
            {/* Model name as a tappable under-label (ChatGPT pattern) — opens the
                model list. The dropdown trigger is restyled small via className. */}
            <ModelPicker
              value={conversation.modelId}
              onChange={handleModelChange}
              fast={conversation.fast}
              onFastChange={handleFastChange}
              className="h-auto min-w-0 max-w-[62vw] gap-1 px-1.5 py-0.5 text-[11.5px] rounded-[7px]"
            />
          </div>
          <button
            type="button"
            aria-label={t('chat:actions.more')}
            onClick={() => setActionsOpen(true)}
            className="inline-flex items-center justify-center size-[var(--tap-min)] justify-self-end rounded-[10px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <MoreHorizontal size={18} aria-hidden />
          </button>
        </header>
      )}

      {/* Messages — wrapped in a relative box so the conversation minimap rail
          (§ minimap) can anchor to the right edge of the thread viewport. */}
      <div className="relative flex flex-1 min-h-0 flex-col">
        <div
          ref={scrollRef}
          onScroll={handleScroll}
          data-scroll-root
          // overflow-x-hidden: wide message content (code / tables / math)
          // scrolls inside its own block — the thread itself must never grow a
          // horizontal scrollbar.
          className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden scrollbar-thin"
        >
          {/* First load with nothing yet in the store (slow network / long thread):
              show a spinner instead of a blank thread. Once any message is present
              (incl. optimistic/streaming) we hand off to MessageList. */}
          {conversation.messages.length === 0 && loadStatus === 'loading' ? (
            <div className="flex h-full items-center justify-center text-[var(--color-fg-subtle)]">
              <Loader2 size={22} className="animate-spin" aria-hidden />
              <span className="sr-only">{t('common.loading', { ns: 'common', defaultValue: 'Loading…' })}</span>
            </div>
          ) : (
            <MessageList conversation={conversation} scrollToMessageId={jumpTo} jumpKey={jumpKey} />
          )}
        </div>
        <ConversationMinimap conversation={conversation} scrollContainerRef={scrollRef} />
      </div>
      <InlineThreadLayer conversationId={conversation.id} scrollRef={scrollRef} />

      {/* Composer — a hairline separates it from the thread on phones, where it's
          a bottom-anchored bar rather than a floating card. */}
      <div className="relative max-sm:border-t max-sm:border-[var(--color-divider)] bg-[var(--color-bg)]">
        {showJump && (
          <button
            type="button"
            onClick={jumpToBottom}
            aria-label={t('chat:thread.jumpToLatest')}
            className={cn(
              'absolute bottom-full left-1/2 mb-2 -translate-x-1/2 inline-flex items-center justify-center',
              'size-9 max-sm:size-10 rounded-full bg-[var(--color-surface)] border border-[var(--color-border)] text-[var(--color-fg-muted)]',
              'shadow-[var(--shadow-md)] hover:text-[var(--color-fg)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            )}
          >
            <ArrowDown size={14} aria-hidden />
          </button>
        )}
        <div className="mx-auto w-full max-w-[var(--layout-message-max-w)] px-3 sm:px-6 lg:px-8 pb-3 sm:pb-5 pt-1.5 sm:pt-2">
          <Composer
            modelId={conversation.modelId}
            onModelChange={(id) => void setModel(conversation.id, id)}
            fast={conversation.fast}
            onFastChange={handleFastChange}
            onSubmit={submit}
            onStop={stopAll}
            streaming={Boolean(streaming)}
            autoFocus
            conversationId={conversation.id}
            kbIds={conversation.kbIds}
            onKBChange={(ids) => void setKBs(conversation.id, ids)}
            modelPickerInHeader
          />
        </div>
      </div>

      <Dialog open={renaming} onOpenChange={(open) => { setRenaming(open); if (!open) setRenameError('') }}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('chat:sidebar.renameTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <Input
              value={renameDraft}
              onChange={(e) => { setRenameDraft(e.target.value); if (e.target.value.trim()) setRenameError('') }}
              autoFocus
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  e.stopPropagation()
                  const trimmed = renameDraft.trim()
                  if (!trimmed) { setRenameError(t('chat:thread.renameEmpty', { defaultValue: 'Title cannot be empty' })); return }
                  void rename(conversation.id, trimmed)
                  setRenaming(false)
                  setRenameError('')
                  toast.success(t('chat:thread.renamed'))
                }
              }}
            />
            {renameError ? (
              <p className="mt-1.5 text-[12px] text-[var(--color-danger)]">{renameError}</p>
            ) : null}
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => { setRenaming(false); setRenameError('') }}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => {
              const trimmed = renameDraft.trim()
              if (!trimmed) { setRenameError(t('chat:thread.renameEmpty', { defaultValue: 'Title cannot be empty' })); return }
              void rename(conversation.id, trimmed)
              setRenaming(false)
              setRenameError('')
              toast.success(t('chat:thread.renamed'))
            }}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('chat:sidebar.deleteTitle')}</DialogTitle>
            <DialogDescription>{t('chat:thread.deleteUndone')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button variant="destructive" onClick={() => { void remove(conversation.id); navigate('/chat'); toast.success(t('chat:thread.deleted')) }}>
              {t('common:actions.delete')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {outlineOpen ? (
        <ConversationOutline
          conversation={conversation}
          scrollContainerRef={scrollRef}
          onClose={() => setOutlineOpen(false)}
        />
      ) : null}

      {/* Mobile action sheet — outline / files / rename / star / share / archive /
          delete, collapsed out of the cramped header (§ mobile redesign). */}
      <Sheet open={actionsOpen} onOpenChange={setActionsOpen}>
        <SheetContent side="bottom" size="sm" label={t('chat:actions.more')} className="h-auto max-h-[85dvh] rounded-t-[20px]">
          <div className="flex flex-col px-2 py-2">
            <ThreadActionRow
              icon={<GitBranch size={18} aria-hidden />}
              label={t('chat:topbar.outlineTooltip', { defaultValue: 'Conversation outline' })}
              onClick={() => { setActionsOpen(false); setOutlineOpen(true) }}
            />
            <ThreadActionRow
              icon={<Files size={18} aria-hidden />}
              label={t('chat:files.title')}
              onClick={() => { setActionsOpen(false); openFilesDrawer(conversation.id) }}
            />
            <ThreadActionRow
              icon={<Pencil size={18} aria-hidden />}
              label={t('chat:sidebar.rename')}
              onClick={() => { setActionsOpen(false); setRenameDraft(conversation.title); setRenaming(true) }}
            />
            <ThreadActionRow
              icon={<Star size={18} aria-hidden />}
              label={conversation.starred ? t('common:actions.unstar') : t('common:actions.star')}
              onClick={() => { setActionsOpen(false); void star(conversation.id) }}
            />
            <ThreadActionRow
              icon={<Share2 size={18} aria-hidden />}
              label={t('chat:sidebar.share')}
              onClick={() => { setActionsOpen(false); setShareOpen(true) }}
            />
            {project ? (
              <ThreadActionRow
                icon={<FolderKanban size={18} aria-hidden />}
                label={t('projects:badge.in', { name: truncate(project.name, 28) })}
                onClick={() => { setActionsOpen(false); navigate(`/projects/${project.id}`) }}
              />
            ) : null}
            <div className="my-1.5 h-px bg-[var(--color-divider)]" aria-hidden />
            <ThreadActionRow
              icon={<Archive size={18} aria-hidden />}
              label={t('chat:sidebar.archive')}
              onClick={async () => { setActionsOpen(false); await archive(conversation.id); toast.success(t('chat:sidebar.archived')); navigate('/chat') }}
            />
            <ThreadActionRow
              icon={<Trash2 size={18} aria-hidden />}
              label={t('chat:sidebar.delete')}
              destructive
              onClick={() => { setActionsOpen(false); setConfirmDelete(true) }}
            />
          </div>
        </SheetContent>
      </Sheet>

      <Dialog open={shareOpen} onOpenChange={setShareOpen}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('chat:share.title')}</DialogTitle>
            <DialogDescription>
              {share ? t('chat:share.bodyShared') : t('chat:share.body')}
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            {shareLoading ? (
              <div className="flex items-center gap-2 text-sm text-[var(--color-fg-subtle)] py-2">
                <Loader2 size={14} className="animate-spin" aria-hidden />
                {t('common:common.loading')}
              </div>
            ) : share ? (
              <div className="flex flex-col gap-3">
                <div className="flex items-center gap-2">
                  <Input
                    readOnly
                    value={shareUrl}
                    onFocus={(e) => e.currentTarget.select()}
                    wrapperClassName="flex-1 min-w-0"
                    className="font-mono text-[12px]"
                  />
                  <Button
                    variant="secondary"
                    className="shrink-0"
                    leadingIcon={copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}
                    onClick={() => void copy(shareUrl)}
                  >
                    {copied ? t('common:actions.copied') : t('common:actions.copy')}
                  </Button>
                </div>
                <p className="inline-flex items-center gap-1.5 text-[12px] text-[var(--color-fg-subtle)]">
                  <Globe size={12} aria-hidden />
                  {t('chat:share.publicHint')}
                </p>
              </div>
            ) : (
              <div className="flex flex-col items-start gap-3 py-1">
                <Button
                  onClick={() => void createShare()}
                  loading={shareBusy}
                  leadingIcon={<Globe size={14} aria-hidden />}
                >
                  {t('chat:share.createCta')}
                </Button>
              </div>
            )}
          </DialogBody>
          {share ? (
            <DialogFooter>
              <Button variant="ghost" onClick={() => setShareOpen(false)}>
                {t('common:actions.close')}
              </Button>
              <Button
                variant="destructive"
                loading={shareBusy}
                leadingIcon={<Trash2 size={14} aria-hidden />}
                onClick={() => void revokeShare()}
              >
                {t('chat:share.revokeCta')}
              </Button>
            </DialogFooter>
          ) : null}
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** A 44px icon+label row inside the mobile thread action Sheet. */
function ThreadActionRow({
  icon,
  label,
  onClick,
  destructive = false,
}: {
  icon: ReactNode
  label: string
  onClick: () => void
  destructive?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        'flex w-full items-center gap-3 min-h-[var(--tap-min)] px-3 text-left text-[15px] rounded-[10px] interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        destructive
          ? 'text-[var(--color-danger)] hover:bg-[var(--color-danger-soft)]'
          : 'text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
      )}
    >
      <span className={cn('shrink-0', destructive ? 'text-[var(--color-danger)]' : 'text-[var(--color-fg-muted)]')}>
        {icon}
      </span>
      <span className="truncate">{label}</span>
    </button>
  )
}
