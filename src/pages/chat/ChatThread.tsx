import { useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { MoreHorizontal, Pencil, Share2, Star, Trash2, Archive, ArrowDown, FolderKanban } from 'lucide-react'
import { Composer } from '@/components/chat/composer'
import { MessageList } from '@/components/chat/message-list'
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
import { useConversations } from '@/store/conversations'
import { useModels } from '@/store/models'
import { useProjects } from '@/store/projects'
import { toast } from '@/hooks/use-toast'
import { accentClasses } from '@/lib/project-helpers'
import { cn, truncate } from '@/lib/utils'
import type { Attachment } from '@/types/chat'

export default function ChatThread() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const { t } = useTranslation(['chat', 'common', 'projects'])
  const conversation = useConversations((s) => s.conversations.find((c) => c.id === id))
  const loadOne = useConversations((s) => s.loadOne)
  const setModel = useConversations((s) => s.setModel)
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

  const scrollRef = useRef<HTMLDivElement>(null)
  const [autoFollow, setAutoFollow] = useState(true)
  const [showJump, setShowJump] = useState(false)

  const [renaming, setRenaming] = useState(false)
  const [renameDraft, setRenameDraft] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)

  const streaming = useMemo(
    () => conversation?.messages.some((m) => m.streaming),
    [conversation?.messages],
  )

  // Hydrate the active conversation + its messages from the backend whenever
  // the id changes.
  useEffect(() => {
    if (!id) return
    void loadOne(id)
  }, [id, loadOne])

  useEffect(() => {
    setAutoFollow(true)
    setShowJump(false)
  }, [id])

  useEffect(() => {
    if (!autoFollow) return
    const el = scrollRef.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior: streaming ? 'auto' : 'smooth' })
  }, [conversation?.messages, autoFollow, streaming])

  function handleScroll(e: React.UIEvent<HTMLDivElement>) {
    const el = e.currentTarget
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
    const atBottom = distanceFromBottom < 80
    setAutoFollow(atBottom)
    setShowJump(!atBottom && el.scrollHeight - el.clientHeight > 200)
  }

  if (!conversation) {
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

  function submit(text: string, attachments: Attachment[], opts: { params?: Record<string, unknown> }) {
    if (!conversation) return
    void sendMessage({
      conversationId: conversation.id,
      text,
      modelId: conversation.modelId,
      attachments,
      params: opts.params,
    })
    setAutoFollow(true)
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

  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* Topbar */}
      <header className="flex items-center gap-3 h-[var(--layout-topbar-h)] px-4 sm:px-6 border-b border-[var(--color-divider)] bg-[var(--color-bg)]/85 backdrop-blur-sm">
        <div className="flex-1 min-w-0 flex flex-col">
          <h1 className="font-serif tracking-tight text-[var(--color-fg)] text-[15px] sm:text-[17px] truncate">
            {truncate(conversation.title, 80)}
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
        <ModelPicker
          value={conversation.modelId}
          onChange={(id) => {
            // §2.3-D cross-vendor downgrade: only warn the user when switching
            // provider type. Same-provider model swaps (Sonnet → Opus) keep
            // raw replay and full fidelity — they shouldn't trigger the
            // "tool process compressed" notice.
            const all = useModels.getState().models
            const cur = all.find((m) => m.id === conversation.modelId)
            const next = all.find((m) => m.id === id)
            // ApiChannel.type is the provider; resolve through channels next
            // refresh if needed. For now compare by channel_id which is a 1:1
            // proxy for provider type in our schema.
            const sameProvider = cur && next && cur.channel_id === next.channel_id
            void setModel(conversation.id, id)
            if (!sameProvider) {
              toast.success(t('chat:thread.modelSwitched'), t('chat:thread.modelSwitchedBody'))
            }
          }}
        />
        <Tooltip content={t('chat:topbar.shareTooltip')}>
          <button
            type="button"
            onClick={() => toast.info(t('chat:actions.shareMocked'))}
            aria-label={t('chat:sidebar.share')}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <Share2 size={14} aria-hidden />
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
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => { void archive(conversation.id); toast.success(t('chat:sidebar.archived')); navigate('/chat') }}>
              <Archive size={13} aria-hidden /> {t('chat:sidebar.archive')}
            </DropdownMenuItem>
            <DropdownMenuItem destructive onSelect={() => setConfirmDelete(true)}>
              <Trash2 size={13} aria-hidden /> {t('chat:sidebar.delete')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </header>

      {/* Messages */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="flex-1 min-h-0 overflow-y-auto scrollbar-thin"
      >
        <MessageList conversation={conversation} />
      </div>

      {/* Composer */}
      <div className="relative">
        {showJump && (
          <button
            type="button"
            onClick={jumpToBottom}
            aria-label={t('chat:thread.jumpToLatest')}
            className={cn(
              'absolute -top-12 left-1/2 -translate-x-1/2 inline-flex items-center justify-center',
              'size-9 rounded-full bg-[var(--color-surface)] border border-[var(--color-border)] text-[var(--color-fg-muted)]',
              'shadow-[var(--shadow-md)] hover:text-[var(--color-fg)] interactive',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            )}
          >
            <ArrowDown size={14} aria-hidden />
          </button>
        )}
        <div className="mx-auto w-full max-w-[44rem] px-4 sm:px-6 lg:px-8 pb-5 pt-2">
          <Composer
            modelId={conversation.modelId}
            onModelChange={(id) => void setModel(conversation.id, id)}
            onSubmit={submit}
            onStop={stopAll}
            streaming={Boolean(streaming)}
            autoFocus
            conversationId={conversation.id}
            kbIds={conversation.kbIds}
            onKBChange={(ids) => void setKBs(conversation.id, ids)}
          />
        </div>
      </div>

      <Dialog open={renaming} onOpenChange={setRenaming}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('chat:sidebar.renameTitle')}</DialogTitle>
          </DialogHeader>
          <DialogBody>
            <Input
              value={renameDraft}
              onChange={(e) => setRenameDraft(e.target.value)}
              autoFocus
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  e.stopPropagation()
                  void rename(conversation.id, renameDraft)
                  setRenaming(false)
                }
              }}
            />
          </DialogBody>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setRenaming(false)}>
              {t('common:actions.cancel')}
            </Button>
            <Button onClick={() => { void rename(conversation.id, renameDraft); setRenaming(false); toast.success(t('chat:thread.renamed')) }}>
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
    </div>
  )
}
