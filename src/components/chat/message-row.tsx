import { useState, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Copy,
  Check,
  RefreshCw,
  ThumbsUp,
  ThumbsDown,
  Pencil,
  MoreHorizontal,
  Volume2,
  Share2,
  ChevronLeft,
  ChevronRight,
  Download,
  GitBranchPlus,
} from 'lucide-react'
import type { Message } from '@/types/chat'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { ModelIcon } from '@/components/chat/model-icon'
import { Tooltip } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { useCopy } from '@/hooks/use-clipboard'
import { useModels } from '@/store/models'
import { useAutosizeTextarea } from '@/hooks/use-autosize-textarea'
import { Markdown } from './markdown'
import { ToolCallCard } from './tool-call-card'
import { CitationList } from './citation'
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'

interface MessageRowProps {
  message: Message
  userName?: string
  onRegenerate?: (id: string) => void
  onEdit?: (id: string, content: string) => void
  onLike?: (id: string, liked: boolean) => void
  onDislike?: (id: string, disliked: boolean) => void
  /** Called when the user clicks `<` / `>` to switch between sibling
   *  branches. Receives the target message id. */
  onBranchSwitch?: (leafId: string) => void
  /** Called when the user picks "Fork to new conversation" from the menu. */
  onFork?: (leafId: string) => void
}

export function MessageRow({ message, userName, onRegenerate, onEdit, onLike, onDislike, onBranchSwitch, onFork }: MessageRowProps) {
  const isUser = message.role === 'user'
  // §7.2-6: assistant 气泡标注生成它的模型名 + 图标。
  const model = useModels((s) => (message.modelId ? s.getById(message.modelId) : undefined))
  const { t } = useTranslation('chat')
  const displayUserName = userName ?? t('common.you', { ns: 'common' })
  const [hovered, setHovered] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(message.content)
  const editRef = useRef<HTMLTextAreaElement>(null)
  const { copied, copy } = useCopy()

  useAutosizeTextarea(editRef, draft, 14)

  // Seed the draft when entering edit mode — but only on the transition,
  // so streaming/external updates to message.content don't overwrite the user's typing.
  useEffect(() => {
    if (editing) setDraft(message.content)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing])

  // Focus the textarea shortly after entering edit mode. Cleanup cancels the
  // timer if the row unmounts or edit mode exits before it fires.
  useEffect(() => {
    if (!editing) return
    const t = setTimeout(() => editRef.current?.focus(), 60)
    return () => clearTimeout(t)
  }, [editing])

  function commitEdit() {
    const next = draft.trim()
    if (!next) return
    onEdit?.(message.id, next)
    setEditing(false)
  }

  const visible = hovered || menuOpen || message.liked || message.disliked

  return (
    <div
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      className={cn(
        'group/msg w-full flex animate-[message-in_220ms_var(--ease-out)_both]',
        isUser ? 'justify-end' : 'justify-start',
      )}
    >
      <div
        className={cn(
          'flex flex-col min-w-0',
          isUser ? 'items-end max-w-[80%] sm:max-w-[68%]' : 'items-start w-full',
        )}
      >
        {!isUser && (
          <div className="flex items-center gap-2 mb-2">
            {model ? (
              <ModelIcon icon={model.icon} size={16} />
            ) : (
              <Avatar size="sm" tone="sage">
                <AvatarFallback>A</AvatarFallback>
              </Avatar>
            )}
            <span className="font-serif text-[15px] tracking-tight text-[var(--color-fg)]">
              {model?.label ?? t('assistant')}
            </span>
            {!message.streaming && typeof message.cost === 'number' && message.cost > 0 ? (
              <span
                className="text-[11px] text-[var(--color-fg-subtle)] tabular-nums"
                title={t('actions.costTooltip', { defaultValue: 'Approximate cost of this reply' })}
              >
                · {formatCost(message.cost, message.currency)}
              </span>
            ) : null}
            {message.streaming ? (
              <span className="ml-1 inline-flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
                <span className="inline-block size-1.5 rounded-full bg-[var(--color-secondary)] animate-[streaming-pulse_1600ms_ease-in-out_infinite]" />
                {t('thinking')}
              </span>
            ) : null}
          </div>
        )}

        {/* Body */}
        {editing && isUser ? (
          <div className="flex flex-col gap-2 w-full max-w-[80%] sm:max-w-[68%]">
            <Textarea
              ref={editRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing || e.keyCode === 229) return
                if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
                  e.preventDefault()
                  commitEdit()
                }
                if (e.key === 'Escape') {
                  e.preventDefault()
                  setEditing(false)
                }
              }}
              className="min-h-[56px] text-sm"
            />
            <div className="flex items-center justify-end gap-2">
              <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
                {t('actions.cancelEdit', { defaultValue: 'Cancel' })}
              </Button>
              <Button size="sm" variant="secondary" onClick={commitEdit}>
                {t('actions.saveEdit', { defaultValue: 'Save & resend' })}
              </Button>
            </div>
          </div>
        ) : isUser ? (
          <div
            className={cn(
              'rounded-[18px] px-4 py-2.5',
              'bg-[var(--color-user-bubble)] border border-[var(--color-user-bubble-border)]',
              'text-[var(--color-fg)] text-[0.9375rem] leading-relaxed whitespace-pre-wrap',
              'max-w-full',
            )}
          >
            {message.attachments && message.attachments.length > 0 ? (
              <div className="flex flex-wrap gap-1.5 mb-2">
                {message.attachments.map((a) => (
                  <span
                    key={a.id}
                    className="inline-flex items-center gap-1 rounded-[6px] bg-[var(--color-surface)] border border-[var(--color-border)] px-1.5 py-0.5 text-[11px] text-[var(--color-fg-muted)]"
                  >
                    {a.name}
                  </span>
                ))}
              </div>
            ) : null}
            {message.content}
          </div>
        ) : (
          <div className="w-full text-[var(--color-fg)]">
            {/* Extended thinking — collapsible, shimmer not spinner (§1.1) */}
            {message.thinking && message.thinking.trim() ? (
              <details className="mb-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-subtle)] px-3 py-2 text-sm text-[var(--color-fg-muted)]">
                <summary className="cursor-pointer select-none text-[var(--color-fg-muted)]">
                  Thinking
                </summary>
                <div className="mt-2 whitespace-pre-wrap font-mono text-xs leading-relaxed text-[var(--color-fg-faint)]">
                  {message.thinking}
                </div>
              </details>
            ) : null}

            {/* Tool calls */}
            {message.ragInjection ? (
              <div className="mb-2 inline-flex items-center gap-1.5 rounded-[8px] border border-[var(--color-secondary)]/30 bg-[var(--color-secondary-soft)] px-2 py-1 text-[11px] text-[var(--color-secondary)]">
                <span aria-hidden>📚</span>
                <span>
                  {message.ragInjection.strategy === 'full_text'
                    ? `Injected full document(s)`
                    : message.ragInjection.strategy === 'full_doc'
                      ? `Whole-document context`
                      : message.ragInjection.strategy === 'none'
                        ? `Skipped retrieval`
                        : `Retrieved sources`}
                  {message.ragInjection.summary ? ` — ${message.ragInjection.summary}` : ''}
                </span>
              </div>
            ) : null}
            {message.toolCalls?.map((tc) => <ToolCallCard key={tc.id} toolCall={tc} />)}

            {/* Streaming placeholder while empty */}
            {message.streaming && !message.content && (!message.toolCalls || message.toolCalls.length === 0) ? (
              <div className="flex items-center gap-1.5 py-1">
                <span className="size-1.5 rounded-full bg-[var(--color-fg-faint)] animate-[typing_1400ms_ease-in-out_infinite] [animation-delay:0ms]" />
                <span className="size-1.5 rounded-full bg-[var(--color-fg-faint)] animate-[typing_1400ms_ease-in-out_infinite] [animation-delay:160ms]" />
                <span className="size-1.5 rounded-full bg-[var(--color-fg-faint)] animate-[typing_1400ms_ease-in-out_infinite] [animation-delay:320ms]" />
              </div>
            ) : (
              <>
                {message.refused ? (
                  <div className="mb-2 inline-flex items-center gap-2 rounded-lg border border-[var(--color-warning)] bg-[var(--color-bg-subtle)] px-3 py-1.5 text-sm text-[var(--color-fg-muted)]">
                    The model declined to answer this request.
                  </div>
                ) : null}
                <Markdown content={message.content} />
                {message.streaming ? (
                  <span
                    aria-hidden
                    className="inline-block align-text-bottom w-[2px] h-[1.05em] bg-[var(--color-accent)] ml-0.5 animate-[fade-in_400ms_ease-in-out_infinite_alternate]"
                  />
                ) : null}
                {message.citations && message.citations.length > 0 ? (
                  <CitationList citations={message.citations} />
                ) : null}
                {/* Downloadable artifacts produced by tools (§4.5/§4.12) */}
                {message.artifacts && message.artifacts.length > 0 ? (
                  <div className="mt-3 flex flex-wrap gap-2">
                    {message.artifacts.map((a) =>
                      a.mimeType.startsWith('image/') ? (
                        <a key={a.id} href={a.url} target="_blank" rel="noreferrer" className="block">
                          <img
                            src={a.url}
                            alt={a.filename}
                            className="max-h-64 rounded-lg border border-[var(--color-border)]"
                          />
                        </a>
                      ) : (
                        <a
                          key={a.id}
                          href={a.url}
                          download={a.filename}
                          className="inline-flex items-center gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-subtle)] px-3 py-2 text-sm text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]"
                        >
                          <Download className="size-4 text-[var(--color-fg-muted)]" />
                          {a.filename}
                        </a>
                      ),
                    )}
                  </div>
                ) : null}
              </>
            )}
          </div>
        )}

        {/* Actions — always rendered after streaming completes, so the layout
            never jumps when the user hovers in/out. Visibility is controlled
            via opacity + pointer-events so nothing below is pushed around. */}
        {!editing && !message.streaming && message.content ? (
          <div
            className={cn(
              'mt-2 inline-flex items-center gap-0.5 transition-opacity duration-[140ms] ease-out',
              visible
                ? 'opacity-100'
                : 'opacity-0 pointer-events-none max-sm:opacity-100 max-sm:pointer-events-auto',
            )}
          >
                {message.branchCount && message.branchCount > 1 && typeof message.branchIndex === 'number' ? (
                  <BranchSwitcher message={message} onSwitch={onBranchSwitch} t={t} />
                ) : null}
                <Tooltip content={copied ? t('actions.copied') : t('actions.copy')}>
                  <button
                    type="button"
                    onClick={() => copy(message.content)}
                    aria-label={t('actions.copy')}
                    className="inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
                  </button>
                </Tooltip>

                {!isUser && (
                  <>
                    <Tooltip content={t('actions.regenerate')}>
                      <button
                        type="button"
                        onClick={() => onRegenerate?.(message.id)}
                        aria-label={t('actions.regenerate')}
                        className="inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      >
                        <RefreshCw size={13} aria-hidden />
                      </button>
                    </Tooltip>
                    <Tooltip content={t('actions.helpful')}>
                      <button
                        type="button"
                        onClick={() => onLike?.(message.id, !message.liked)}
                        aria-label={t('actions.helpful')}
                        aria-pressed={message.liked}
                        className={cn(
                          'inline-flex items-center justify-center size-7 rounded-[7px] interactive',
                          message.liked
                            ? 'text-[var(--color-success)] bg-[var(--color-success-soft)]'
                            : 'text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                        )}
                      >
                        <ThumbsUp size={13} aria-hidden />
                      </button>
                    </Tooltip>
                    <Tooltip content={t('actions.notHelpful')}>
                      <button
                        type="button"
                        onClick={() => onDislike?.(message.id, !message.disliked)}
                        aria-label={t('actions.notHelpful')}
                        aria-pressed={message.disliked}
                        className={cn(
                          'inline-flex items-center justify-center size-7 rounded-[7px] interactive',
                          message.disliked
                            ? 'text-[var(--color-danger)] bg-[var(--color-danger-soft)]'
                            : 'text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                        )}
                      >
                        <ThumbsDown size={13} aria-hidden />
                      </button>
                    </Tooltip>
                  </>
                )}

                {isUser && (
                  <Tooltip content={t('actions.edit')}>
                    <button
                      type="button"
                      onClick={() => setEditing(true)}
                      aria-label={t('actions.edit')}
                      className="inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <Pencil size={13} aria-hidden />
                    </button>
                  </Tooltip>
                )}

                <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
                  <Tooltip content={t('actions.more')}>
                    <DropdownMenuTrigger asChild>
                      <button
                        type="button"
                        aria-label={t('actions.more')}
                        className="inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      >
                        <MoreHorizontal size={13} aria-hidden />
                      </button>
                    </DropdownMenuTrigger>
                  </Tooltip>
                  <DropdownMenuContent align={isUser ? 'end' : 'start'}>
                    <DropdownMenuItem onClick={() => copy(message.content)}>
                      <Copy size={13} aria-hidden />
                      {t('actions.copyMessage')}
                    </DropdownMenuItem>
                    {!isUser && (
                      <DropdownMenuItem onClick={() => toast.info(t('actions.readAloudMocked'))}>
                        <Volume2 size={13} aria-hidden />
                        {t('actions.readAloud')}
                      </DropdownMenuItem>
                    )}
                    <DropdownMenuItem onClick={() => toast.info(t('actions.shareMocked'))}>
                      <Share2 size={13} aria-hidden />
                      {t('sidebar.share')}
                    </DropdownMenuItem>
                    {onFork ? (
                      <DropdownMenuItem
                        onClick={() => {
                          onFork(message.id)
                          toast.success(t('actions.forked', { defaultValue: 'Forked to a new conversation' }))
                        }}
                      >
                        <GitBranchPlus size={13} aria-hidden />
                        {t('actions.fork', { defaultValue: 'Fork to new conversation' })}
                      </DropdownMenuItem>
                    ) : null}
                    {!isUser && (
                      <>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem onClick={() => onRegenerate?.(message.id)}>
                          <RefreshCw size={13} aria-hidden />
                          {t('actions.regenerate')}
                        </DropdownMenuItem>
                      </>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
          </div>
        ) : null}
        {isUser && (
          <span className="sr-only">
            {displayUserName}
          </span>
        )}
      </div>
    </div>
  )
}

/**
 * BranchSwitcher — the `<  2/3  >` chip shown when the current message has
 * sibling branches (§4.15). Clicking the arrows calls onSwitch with the
 * neighbour's id so the parent can flip conversations.active_leaf_id.
 */
function BranchSwitcher({
  message,
  onSwitch,
  t,
}: {
  message: Message
  onSwitch?: (leafId: string) => void
  t: (key: string) => string
}) {
  const siblings = message.siblings ?? []
  const idx = message.branchIndex ?? 0
  const total = message.branchCount ?? siblings.length
  if (total <= 1) return null
  function go(delta: number) {
    if (!onSwitch || siblings.length === 0) return
    const next = (idx + delta + siblings.length) % siblings.length
    const target = siblings[next]
    if (target) onSwitch(target)
  }
  return (
    <span
      className="mr-1 inline-flex items-center gap-0.5 rounded-[6px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] px-1 py-0.5 text-[10.5px] text-[var(--color-fg-subtle)] tabular-nums"
      aria-label={t('actions.branch')}
    >
      <button
        type="button"
        onClick={() => go(-1)}
        disabled={siblings.length === 0}
        aria-label={t('actions.prevBranch')}
        className="inline-flex items-center justify-center size-4 rounded-[4px] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] interactive disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <ChevronLeft size={9} aria-hidden />
      </button>
      <span className="px-0.5 select-none">
        {idx + 1}/{total}
      </span>
      <button
        type="button"
        onClick={() => go(1)}
        disabled={siblings.length === 0}
        aria-label={t('actions.nextBranch')}
        className="inline-flex items-center justify-center size-4 rounded-[4px] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] interactive disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <ChevronRight size={9} aria-hidden />
      </button>
    </span>
  )
}

/** formatCost renders a per-message cost line like "$0.0042" or "¥0.0042". */
function formatCost(cost: number, currency?: string): string {
  const symbol = currency === 'CNY' ? '¥' : '$'
  // Show 4 significant digits — almost every assistant reply is between $0.0001
  // and $1, so 4 dp is the smallest precision that doesn't display "$0.00".
  if (cost < 0.01) return `${symbol}${cost.toFixed(4)}`
  if (cost < 1) return `${symbol}${cost.toFixed(3)}`
  return `${symbol}${cost.toFixed(2)}`
}
