import { memo, useState, useRef, useEffect, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Copy,
  Check,
  Clock,
  RefreshCw,
  ThumbsUp,
  ThumbsDown,
  Pencil,
  Trash2,
  MoreHorizontal,
  ChevronLeft,
  ChevronRight,
  Download,
  FileDown,
  GitBranchPlus,
  AlertTriangle,
  X,
  FileText,
  FileSpreadsheet,
  Sparkles,
  BookText,
  Coins, ImageOff } from 'lucide-react'
import { Link } from 'react-router-dom'
import type { Message, Attachment } from '@/types/chat'
import { Avatar, AvatarFallback, AvatarImage } from '@/components/ui/avatar'
import { LogoMark } from '@/components/brand/logo'
import { ModelIcon } from '@/components/chat/model-icon'
import { Tooltip } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
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
import { Textarea } from '@/components/ui/textarea'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { useCopy } from '@/hooks/use-clipboard'
import { useAuth } from '@/store/auth'
import { initials } from '@/components/ui/avatar.utils'
import { useModels } from '@/store/models'
import { useAutosizeTextarea } from '@/hooks/use-autosize-textarea'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { Markdown } from './markdown'
import { ReasoningTrace } from './reasoning-trace'
import { ImageGenerating } from './image-generating'
import { ResearchPanel } from './research-panel'
import { CitationList } from './citation'
import { VerifyBadge } from './verify-badge'
import { ImageLightbox } from './image-lightbox'
import { FilePreview } from './file-preview'
import { toast } from '@/hooks/use-toast'
import { cn, safeHref } from '@/lib/utils'

/**
 * ThinkingLogo — the "still forming a reply" indicator shown before the first
 * token: the Aivory mark breathing + glowing inside a slow scan ring. The ring
 * highlight and glow use the sage AI-status colour (§2.4); the global
 * prefers-reduced-motion rule holds it static.
 */
function ThinkingLogo() {
  return (
    <div className="relative grid size-11 place-items-center" aria-hidden>
      <span className="absolute inset-0 rounded-full border border-[var(--color-border)] [border-top-color:var(--color-secondary)] animate-[spin_1200ms_cubic-bezier(0.6,0.1,0.4,0.9)_infinite]" />
      <LogoMark size={24} className="animate-[core-breathe_2400ms_ease-in-out_infinite]" />
    </div>
  )
}

interface MessageRowProps {
  message: Message
  userName?: string
  onRegenerate?: (id: string) => void
  /**
   * "Save & resend" — edit a question into a NEW branch and regenerate.
   * `attachments` carries the surviving attachments (after the user removed any
   * in edit mode); when omitted the row keeps the original list.
   */
  onEdit?: (id: string, content: string, attachments?: Attachment[]) => void
  /** "Save" — overwrite the question text in place, no branch, no regenerate. */
  onSaveEdit?: (id: string, content: string) => void
  onLike?: (id: string, liked: boolean) => void
  onDislike?: (id: string, disliked: boolean) => void
  /** Called when the user clicks `<` / `>` to switch between sibling
   *  branches. Receives the target message id. */
  onBranchSwitch?: (leafId: string) => void
  /** Called when the user picks "Fork to new conversation" from the menu. */
  onFork?: (leafId: string) => void
  /** Delete this whole round (the question + all its answers). Branch-safe. */
  onDelete?: (id: string) => void
  /**
   * Read-only render (admin transcript inspection / triage). Renders the message
   * body identically to the live chat but suppresses the hover action bar and
   * edit affordances — there are no mutation callbacks to wire in that context.
   */
  readOnly?: boolean
  /** Render user-authored message text through the markdown renderer. */
  userMessageMarkdown?: boolean
}

// Compact generation time: "3.2s" under a minute, "1m4s" beyond.
function formatGenMs(ms: number): string {
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1000)
  return `${m}m${s}s`
}

// Credits charged for a turn — show up to 2 decimals, trimming trailing zeros so
// whole amounts read "12" not "12.00".
function formatCredits(credits: number): string {
  return credits.toLocaleString(undefined, { maximumFractionDigits: 2 })
}

function MessageRowImpl({ message, userName, onRegenerate, onEdit, onSaveEdit, onLike, onDislike, onBranchSwitch, onFork, onDelete, readOnly = false, userMessageMarkdown = false }: MessageRowProps) {
  const isUser = message.role === 'user'
  // §workspaces: in a shared conversation "own" = authored by ME — other
  // members' questions render LEFT like the assistant, with the author's
  // name + avatar. Personal conversations (no author) keep role semantics.
  const meId = useAuth((s) => s.user?.id)
  // readOnly (admin transcript viewer): there is no "me" perspective — keep the
  // classic role-based layout so mixed legacy/authored turns don't zigzag.
  const isOwn = isUser && (readOnly ? true : message.authorId ? message.authorId === meId : true)
  const isForeignUser = isUser && !isOwn
  // §7.2-6: assistant 气泡标注生成它的模型名 + 图标。
  const model = useModels((s) => (message.modelId ? s.getById(message.modelId) : undefined))
  const { t } = useTranslation('chat')
  const displayUserName = message.authorName ?? userName ?? t('common.you', { ns: 'common' })
  const [hovered, setHovered] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  // Phone: the per-message actions live in a bottom Sheet (a clean thread reveals
  // them on tap) instead of an always-on row of tiny icons (§ mobile redesign).
  const isPhone = useMediaQuery(mediaQuery.phone)
  const [actionSheetOpen, setActionSheetOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(message.content)
  // Attachments the user keeps in the edit dialog. Seeded from the original
  // message on entering edit mode; removing an item here does NOT touch the
  // original until the user clicks "Save & resend" (which opens a new branch
  // with this exact attachment list).
  const [draftAtts, setDraftAtts] = useState<Attachment[]>(message.attachments ?? [])
  // Attachment ids whose thumbnail 404'd — the file was deleted from the Files
  // page or the admin console; show a labelled placeholder, not a broken img.
  const [brokenAtts, setBrokenAtts] = useState<Set<string>>(new Set())
  // Lightbox: which image is being previewed (null = closed). Driven from the
  // attachment id so the Dialog re-mounts cleanly on each preview.
  const [lightbox, setLightbox] = useState<{ src: string; alt?: string } | null>(null)
  // Non-image attachment preview (pdf / docx / text / fallback) — opens a modal
  // instead of letting the click download the file.
  const [filePreview, setFilePreview] = useState<{ name: string; url?: string; kind: Attachment['kind'] } | null>(null)
  const editRef = useRef<HTMLTextAreaElement>(null)
  const { copied, copy } = useCopy()
  const [exportingDocx, setExportingDocx] = useState(false)

  // Export THIS reply as .docx: markdown -> formatted Word, LaTeX -> native
  // equations (lib/docx-export.ts). Lazy import keeps docx/katex-omml out of
  // the main bundle; failures surface as a toast, never an exception.
  const exportDocx = async () => {
    if (exportingDocx || !message.content) return
    setExportingDocx(true)
    try {
      const { exportMarkdownAsDocx } = await import('@/lib/docx-export')
      const stamp = new Date().toISOString().slice(0, 16).replace(/[T:]/g, '-')
      await exportMarkdownAsDocx(message.content, `aivory-reply-${stamp}`)
    } catch {
      toast.error(t('actions.exportDocxFailed', { defaultValue: 'Export failed' }))
    } finally {
      setExportingDocx(false)
    }
  }

  useAutosizeTextarea(editRef, draft, 14)

  // Seed the draft when entering edit mode — but only on the transition,
  // so streaming/external updates to message.content don't overwrite the user's typing.
  useEffect(() => {
    if (editing) {
      setDraft(message.content)
      setDraftAtts(message.attachments ?? [])
    }
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
    onEdit?.(message.id, next, draftAtts)
    setEditing(false)
  }

  // "Save" — overwrite the message text in place (no new branch / regenerate).
  function saveInPlace() {
    const next = draft.trim()
    if (!next) return
    onSaveEdit?.(message.id, next)
    setEditing(false)
  }

  function removeDraftAtt(id: string) {
    setDraftAtts((s) => s.filter((a) => a.id !== id))
  }

  const visible = hovered || menuOpen || message.liked || message.disliked

  return (
    <div
      data-message-id={message.id}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      className={cn(
        'group/msg w-full flex animate-[message-in_220ms_var(--ease-out)_both]',
        isOwn ? 'justify-end' : 'justify-start',
      )}
    >
      <div
        className={cn(
          'flex flex-col min-w-0',
          // A user bubble hugs its content (right-aligned, capped width); but
          // while editing it expands to the full message column — same width as
          // an assistant reply — so there's room to rework the question.
          isOwn && !editing
            ? 'items-end max-w-[88%] sm:max-w-[68%]'
            : isForeignUser
              ? 'items-start max-w-[88%] sm:max-w-[68%]'
              : 'items-start w-full',
        )}
      >
        {isForeignUser && (
          <div className="flex items-center gap-2 mb-1.5">
            <Avatar size="sm" tone="ink">
              {message.authorAvatar ? <AvatarImage src={message.authorAvatar} alt={displayUserName} /> : null}
              <AvatarFallback>{initials(displayUserName)}</AvatarFallback>
            </Avatar>
            <span className="text-[13px] font-medium text-[var(--color-fg-muted)]">{displayUserName}</span>
          </div>
        )}
        {!isUser && (
          <div className="flex items-center gap-2 mb-2">
            {model ? (
              <ModelIcon icon={model.icon} size={20} />
            ) : (
              <Avatar size="sm" tone="sage">
                <AvatarFallback>A</AvatarFallback>
              </Avatar>
            )}
            <span className="font-medium text-[15px] text-[var(--color-fg)]">
              {model?.label ?? message.modelLabel ?? t('assistant')}
            </span>
            {/* Per-reply generation time (§ 用时). Cost stays admin-only. */}
            {!message.streaming && message.genMs ? (
              <span className="inline-flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)] tabular-nums">
                <Clock size={11} aria-hidden />
                {formatGenMs(message.genMs)}
              </span>
            ) : null}
            {message.streaming ? (
              <span className="thinking-shimmer ml-1 text-[11px] font-medium tracking-[0.04em]">
                {t('thinking')}…
              </span>
            ) : null}
          </div>
        )}

        {/* Body */}
        {editing && isUser ? (
          // Full-width edit surface (spans the whole message column, like an AI
          // reply). One calm muted well holds the textarea AND the actions, with
          // the buttons docked bottom-right inside the box.
          <div className="w-full rounded-[18px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] px-4 py-3.5 transition-colors focus-within:border-[var(--color-border-strong)]">
            {/* Editable attachment strip — images preview as thumbnails with
                an X hover affordance; non-images render as compact chips. */}
            {draftAtts.length > 0 ? (
              <div className="mb-3 flex flex-wrap gap-2">
                {draftAtts.map((a) =>
                  a.kind === 'image' && a.previewUrl ? (
                    <EditableImageChip key={a.id} att={a} onRemove={() => removeDraftAtt(a.id)} />
                  ) : (
                    <EditableFileChip key={a.id} att={a} onRemove={() => removeDraftAtt(a.id)} />
                  ),
                )}
              </div>
            ) : null}
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
              className="min-h-[112px] resize-none border-none bg-transparent p-0 text-[length:var(--text-chat-body)] leading-relaxed focus:ring-0"
            />
            <div className="mt-2.5 flex items-center justify-end gap-2">
              <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
                {t('actions.cancelEdit', { defaultValue: 'Cancel' })}
              </Button>
              <Button size="sm" variant="secondary" onClick={saveInPlace}>
                {t('actions.saveInPlace', { defaultValue: 'Save' })}
              </Button>
              <Button size="sm" variant="primary" onClick={commitEdit}>
                {t('actions.saveEdit', { defaultValue: 'Save & resend' })}
              </Button>
            </div>
          </div>
        ) : isUser ? (
          <div
            className={cn(
              'rounded-[18px] px-4 py-2.5',
              'bg-[var(--color-user-bubble)] border border-[var(--color-user-bubble-border)]',
              'text-[var(--color-fg)] text-[length:var(--text-chat-body)] leading-relaxed',
              userMessageMarkdown ? 'min-w-0' : 'whitespace-pre-wrap break-words',
              'max-w-full',
            )}
          >
            {message.attachments && message.attachments.length > 0 ? (
              <div className="mb-2 flex flex-wrap gap-2">
                {message.attachments.map((a) =>
                  a.kind === 'image' && brokenAtts.has(a.id) ? (
                    <span
                      key={a.id}
                      className="inline-flex items-center gap-1.5 rounded-[8px] border border-dashed border-[var(--color-border)] bg-[var(--color-bg-muted)] px-2 py-1 text-[11.5px] text-[var(--color-fg-subtle)] max-w-[18rem]"
                      title={a.name}
                    >
                      <ImageOff size={13} aria-hidden />
                      <span className="truncate">{a.name}</span>
                      <span className="shrink-0">· {t('attachmentDeleted', { defaultValue: 'File deleted' })}</span>
                    </span>
                  ) : a.kind === 'image' && a.previewUrl ? (
                    <button
                      key={a.id}
                      type="button"
                      onClick={() => setLightbox({ src: a.previewUrl!, alt: a.name })}
                      aria-label={t('actions.viewImage', { defaultValue: 'View image' })}
                      className="block overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] hover:opacity-90"
                    >
                      <img
                        src={a.previewUrl}
                        alt={a.name}
                        className="max-h-56 max-w-[18rem] sm:max-w-[22rem] w-auto h-auto object-cover"
                        draggable={false}
                        onError={() => setBrokenAtts((prev) => new Set(prev).add(a.id))}
                      />
                    </button>
                  ) : (
                    <button
                      key={a.id}
                      type="button"
                      onClick={() => setFilePreview({ name: a.name, url: a.previewUrl, kind: a.kind })}
                      aria-label={t('actions.previewFile', { defaultValue: 'Preview file' })}
                      className={cn(
                        'inline-flex items-center gap-1.5 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 text-[11.5px] text-[var(--color-fg-muted)] max-w-[18rem]',
                        'interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      )}
                    >
                      <KindIcon kind={a.kind} />
                      <span className="truncate">{a.name}</span>
                    </button>
                  ),
                )}
                {/* TODO(#8): when this conversation belongs to a project, offer an
                    "Add to project library" action here that calls
                    conversationsApi.promoteDoc(convId, a.id) then refreshes the
                    project. Skipped for now — wiring it needs conversationId +
                    projectId threaded through MessageList (off-limits for this
                    change), so the clean path is blocked. */}
              </div>
            ) : null}
            {userMessageMarkdown ? (
              <Markdown content={message.content} blockKeyPrefix={`${message.id}-user`} className="prose-user" breaks />
            ) : (
              message.content
            )}
          </div>
        ) : (
          <div className="w-full text-[var(--color-fg)]">
            {/* Deep Research panel — plan checklist + sources, live while the
                report streams below (§ deep-research mode). */}
            {message.research ? (
              <ResearchPanel
                research={message.research}
                streaming={message.streaming}
                settled={Boolean(message.content)}
              />
            ) : null}

            {/* Unified reasoning trace — extended thinking + tool rounds in one
                live, collapsible panel (§7.1-4). Streams open with per-tool
                pulse + elapsed counters so long searches / sandbox runs never
                look frozen; collapses once the answer text begins. */}
            <ReasoningTrace
              reasoning={message.reasoning}
              streaming={message.streaming}
              settled={Boolean(message.content)}
            />

            {message.ragInjection ? (
              <div className="mb-2.5 inline-flex max-w-full items-center gap-1.5 text-[11.5px] text-[var(--color-fg-subtle)]">
                <BookText size={13} strokeWidth={1.5} aria-hidden className="shrink-0 text-[var(--color-secondary)]" />
                <span className="truncate">
                  <span className="text-[var(--color-fg-muted)]">
                    {message.ragInjection.strategy === 'indexing'
                      ? t('message.ragIndexing')
                      : message.ragInjection.strategy === 'indexing_done'
                        ? t('message.ragIndexingDone')
                        : message.ragInjection.strategy === 'warning'
                          ? t('message.ragWarning')
                          : message.ragInjection.strategy === 'full_text'
                            ? t('message.ragFullText')
                            : message.ragInjection.strategy === 'full_doc'
                              ? t('message.ragFullDoc')
                              : message.ragInjection.strategy === 'none'
                                ? t('message.ragNone')
                                : t('message.ragDefault')}
                  </span>
                  {message.ragInjection.summary ? (
                    <span className="text-[var(--color-fg-faint)]"> · {message.ragInjection.summary}</span>
                  ) : null}
                </span>
              </div>
            ) : null}

            {/* §4.20 image mode: dedicated drawing surface (distinct from the
                chat thinking/tool-call trace) while no image artifact exists yet. */}
            {message.imageStatus && (!message.artifacts || message.artifacts.length === 0) ? (
              <ImageGenerating phase={message.imageStatus} />
            ) : /* Streaming placeholder while empty — the brand thinking mark */
            message.streaming && !message.content && (!message.reasoning || message.reasoning.length === 0) ? (
              <div className="py-1">
                <ThinkingLogo />
              </div>
            ) : message.quotaExceeded ? (
              <div className="my-1 overflow-hidden rounded-xl border border-[var(--color-secondary)]/40 bg-[var(--color-secondary-soft)]/50 px-4 py-3.5">
                <div className="flex items-center gap-2 text-[var(--color-secondary)] font-medium text-sm">
                  <Sparkles size={16} aria-hidden />
                  {t('message.quota.title', { defaultValue: 'Quota reached' })}
                </div>
                <p className="mt-1.5 text-sm text-[var(--color-fg)] leading-relaxed">
                  {t('message.quota.body', {
                    defaultValue: "You've used up your group's quota for this model. Upgrade your plan to keep going.",
                  })}
                </p>
                <Button asChild size="sm" variant="secondary" className="mt-3" leadingIcon={<Sparkles size={13} aria-hidden />}>
                  <Link to="/subscription">{t('message.quota.cta', { defaultValue: 'Upgrade plan' })}</Link>
                </Button>
              </div>
            ) : message.moderation ? (
              <div
                role="alert"
                className="my-1 rounded-xl border border-[var(--color-danger)] bg-[var(--color-danger-soft)] px-4 py-3"
              >
                <div className="flex items-center gap-2 text-[var(--color-danger)] font-medium text-sm">
                  <AlertTriangle size={16} aria-hidden />
                  {t('message.moderation.title')}
                </div>
                <p className="mt-1.5 text-sm text-[var(--color-fg)] leading-relaxed">
                  {message.content || t('message.moderation.body')}
                </p>
                <p className="mt-1.5 text-[12.5px] text-[var(--color-danger)]">{t('message.moderation.cta')}</p>
              </div>
            ) : (
              <>
                {message.refused ? (
                  <div className="mb-2 inline-flex items-center gap-2 rounded-lg border border-[var(--color-warning)] bg-[var(--color-bg-subtle)] px-3 py-1.5 text-sm text-[var(--color-fg-muted)]">
                    {t('message.refused')}
                  </div>
                ) : null}
                <div data-inline-msg={message.id} data-inline-role={message.role}>
                  <Markdown content={message.content} live={Boolean(message.streaming)} blockKeyPrefix={message.id} citations={message.citations} className="prose-full" />
                </div>
                {message.streaming ? (
                  <span
                    aria-hidden
                    className="inline-block align-text-bottom w-[2px] h-[1.05em] bg-[var(--color-accent)] ml-0.5 animate-[fade-in_400ms_ease-in-out_infinite_alternate]"
                  />
                ) : null}
                {message.error && !message.streaming ? (
                  <div
                    role="alert"
                    className="mt-2 rounded-xl border border-[var(--color-danger)] bg-[var(--color-danger-soft)] px-4 py-3"
                  >
                    <div className="flex items-center gap-2 text-[var(--color-danger)] font-medium text-sm">
                      <AlertTriangle size={16} aria-hidden />
                      {t('message.error.title')}
                    </div>
                    <p className="mt-1 text-[12.5px] text-[var(--color-fg-subtle)] break-words">{message.error}</p>
                    <button
                      type="button"
                      onClick={() => onRegenerate?.(message.id)}
                      className="mt-2.5 inline-flex items-center gap-1.5 h-8 px-3 rounded-[9px] text-sm font-medium bg-[var(--color-danger)] text-[var(--color-fg-inverted)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <RefreshCw size={13} aria-hidden />
                      {t('message.error.retry')}
                    </button>
                  </div>
                ) : null}
                {message.citations && message.citations.length > 0 ? (
                  <CitationList citations={message.citations} />
                ) : null}
                {/* §verify: secondary-auditor trust badge + findings report. */}
                {message.verify ? <VerifyBadge verify={message.verify} /> : null}
                {/* Downloadable artifacts produced by tools (§4.5/§4.12) */}
                {message.artifacts && message.artifacts.length > 0 ? (
                  <div className="mt-3 flex flex-wrap gap-2">
                    {message.artifacts.map((a) => {
                      // Artifact URLs are tool/model-controlled (SSE) — vet the
                      // scheme before it reaches href/src (§ XSS E4).
                      const href = safeHref(a.url)
                      if (a.mimeType.startsWith('image/')) {
                        // No safe URL → render a placeholder chip instead of an
                        // <img> that could carry a javascript:/data: payload.
                        if (!href) {
                          return (
                            <span
                              key={a.id}
                              className="inline-flex items-center gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-subtle)] px-3 py-2 text-sm text-[var(--color-fg-muted)]"
                            >
                              <Download className="size-4 text-[var(--color-fg-subtle)]" />
                              {a.filename}
                            </span>
                          )
                        }
                        return (
                          <a key={a.id} href={href} target="_blank" rel="noreferrer" className="block">
                            <img
                              src={href}
                              alt={a.filename}
                              className="max-h-64 rounded-lg border border-[var(--color-border)]"
                            />
                          </a>
                        )
                      }
                      return (
                        <a
                          key={a.id}
                          href={href}
                          download={a.filename}
                          className="inline-flex items-center gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-subtle)] px-3 py-2 text-sm text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]"
                        >
                          <Download className="size-4 text-[var(--color-fg-muted)]" />
                          {a.filename}
                        </a>
                      )
                    })}
                  </div>
                ) : null}
              </>
            )}
          </div>
        )}

        {/* Branch picker during streaming — the action bar below is hidden while
            tokens arrive, but a freshly-retried answer should show its
            `< n/m >` immediately (§4.15 R2). */}
        {!isUser && message.streaming && message.branchCount && message.branchCount > 1 && typeof message.branchIndex === 'number' ? (
          <div className="mt-2 inline-flex items-center">
            <BranchSwitcher message={message} onSwitch={onBranchSwitch} t={t} />
          </div>
        ) : null}

        {/* Always-visible branch switcher (both roles, once settled). Sits under
            the USER bubble after an edit-branch (§4.15 R1) and under the AI reply
            after a retry (R2) — shown the instant the branch exists rather than
            waiting for a hover, and kept visible in read-only (admin) triage. The
            hover action bar below no longer carries its own copy. */}
        {!editing && !message.streaming && message.branchCount && message.branchCount > 1 && typeof message.branchIndex === 'number' ? (
          <div className="mt-2 inline-flex items-center">
            <BranchSwitcher message={message} onSwitch={onBranchSwitch} t={t} />
          </div>
        ) : null}

        {/* Actions — always rendered after streaming completes, so the layout
            never jumps when the user hovers in/out. Visibility is controlled
            via opacity + pointer-events so nothing below is pushed around.
            Also show the action bar when a message has an error but no content
            so the user can retry the failed message. */}
        {!readOnly && !editing && !message.streaming && (message.content || message.error || (message.artifacts && message.artifacts.length > 0)) ? (
          isPhone ? (
            <div className="mt-1.5 flex items-center gap-2">
              <button
                type="button"
                onClick={() => setActionSheetOpen(true)}
                aria-label={t('actions.more')}
                className="inline-flex items-center justify-center size-[var(--tap-min)] -ml-2 rounded-[10px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <MoreHorizontal size={18} aria-hidden />
              </button>
              {!isUser && message.credits && message.credits > 0 ? (
                <span className="inline-flex items-center gap-1 text-[11px] text-[var(--color-secondary)] tabular-nums">
                  <Coins size={11} aria-hidden />
                  {t('actions.creditsUsed', { credits: formatCredits(message.credits) })}
                </span>
              ) : null}
            </div>
          ) : (
          <div
            className={cn(
              'mt-2 inline-flex items-center gap-0.5 transition-opacity duration-[140ms] ease-out',
              visible
                ? 'opacity-100'
                : 'opacity-0 pointer-events-none max-sm:opacity-100 max-sm:pointer-events-auto',
            )}
          >
                {message.content ? (
                <Tooltip content={copied ? t('actions.copied') : t('actions.copy')}>
                  <button
                    type="button"
                    onClick={() => copy(message.content)}
                    aria-label={t('actions.copy')}
                    className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
                  </button>
                </Tooltip>
                ) : null}

                {!isUser && (
                  <>
                    {message.content ? (
                      <Tooltip content={t('actions.exportDocx', { defaultValue: 'Export as Word' })}>
                        <button
                          type="button"
                          onClick={exportDocx}
                          disabled={exportingDocx}
                          aria-label={t('actions.exportDocx', { defaultValue: 'Export as Word' })}
                          aria-busy={exportingDocx}
                          className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:opacity-50 disabled:pointer-events-none"
                        >
                          <FileDown size={13} aria-hidden />
                        </button>
                      </Tooltip>
                    ) : null}
                    <Tooltip content={t('actions.regenerate')}>
                      <button
                        type="button"
                        onClick={() => onRegenerate?.(message.id)}
                        aria-label={t('actions.regenerate')}
                        className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
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
                          'inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] interactive',
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
                          'inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] interactive',
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

                {isOwn && (
                  <Tooltip content={t('actions.edit')}>
                    <button
                      type="button"
                      onClick={() => setEditing(true)}
                      aria-label={t('actions.edit')}
                      className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <Pencil size={13} aria-hidden />
                    </button>
                  </Tooltip>
                )}

                {onDelete && (
                  <Tooltip content={t('actions.delete', { defaultValue: 'Delete' })}>
                    <button
                      type="button"
                      onClick={() => setConfirmDelete(true)}
                      aria-label={t('actions.delete', { defaultValue: 'Delete' })}
                      className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-danger-soft)] hover:text-[var(--color-danger)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <Trash2 size={13} aria-hidden />
                    </button>
                  </Tooltip>
                )}

                <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
                  <Tooltip content={t('actions.more')}>
                    <DropdownMenuTrigger asChild>
                      <button
                        type="button"
                        aria-label={t('actions.more')}
                        className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
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
                    {onFork ? (
                      // Feedback (forking… → forked/failed) is owned by handleFork
                      // in message-list — a success toast here would fire before
                      // the request even starts (§2.7).
                      <DropdownMenuItem onClick={() => onFork(message.id)}>
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

                {/* Credits spent on this turn — shown after the action icons for
                    credit-charged replies (§ credits). Sage = an AI-status moment. */}
                {!isUser && message.credits && message.credits > 0 ? (
                  <span className="ml-1.5 inline-flex items-center gap-1 text-[11px] text-[var(--color-secondary)] tabular-nums">
                    <Coins size={11} aria-hidden />
                    {t('actions.creditsUsed', { credits: formatCredits(message.credits) })}
                  </span>
                ) : null}
          </div>
          )
        ) : null}
        {isUser && (
          <span className="sr-only">
            {displayUserName}
          </span>
        )}
      </div>
      {/* Image lightbox — rendered once per row; opens via setLightbox(). */}
      <ImageLightbox
        open={lightbox !== null}
        onOpenChange={(o) => !o && setLightbox(null)}
        src={lightbox?.src ?? ''}
        alt={lightbox?.alt}
      />
      {/* Non-image attachment preview modal. */}
      <FilePreview
        open={filePreview !== null}
        onOpenChange={(o) => !o && setFilePreview(null)}
        file={filePreview}
      />
      {/* Phone: per-message actions as a bottom Sheet (§ mobile redesign). */}
      {isPhone && (
        <Sheet open={actionSheetOpen} onOpenChange={setActionSheetOpen}>
          <SheetContent side="bottom" size="sm" label={t('actions.more')} className="h-auto max-h-[85dvh] rounded-t-[20px]">
            <div className="flex flex-col px-2 py-2">
              {message.content ? (
                <MsgActionRow
                  icon={copied ? <Check size={18} aria-hidden /> : <Copy size={18} aria-hidden />}
                  label={copied ? t('actions.copied') : t('actions.copy')}
                  onClick={() => copy(message.content)}
                />
              ) : null}
              {!isUser ? (
                <>
                  {message.content ? (
                    <MsgActionRow
                      icon={<FileDown size={18} aria-hidden />}
                      label={t('actions.exportDocx', { defaultValue: 'Export as Word' })}
                      onClick={() => { setActionSheetOpen(false); void exportDocx() }}
                    />
                  ) : null}
                  <MsgActionRow
                    icon={<RefreshCw size={18} aria-hidden />}
                    label={t('actions.regenerate')}
                    onClick={() => { setActionSheetOpen(false); onRegenerate?.(message.id) }}
                  />
                  <MsgActionRow
                    icon={<ThumbsUp size={18} aria-hidden />}
                    label={t('actions.helpful')}
                    active={message.liked}
                    onClick={() => onLike?.(message.id, !message.liked)}
                  />
                  <MsgActionRow
                    icon={<ThumbsDown size={18} aria-hidden />}
                    label={t('actions.notHelpful')}
                    active={message.disliked}
                    onClick={() => onDislike?.(message.id, !message.disliked)}
                  />
                </>
              ) : isOwn ? (
                <MsgActionRow
                  icon={<Pencil size={18} aria-hidden />}
                  label={t('actions.edit')}
                  onClick={() => { setActionSheetOpen(false); setEditing(true) }}
                />
              ) : null}
              {onFork ? (
                <MsgActionRow
                  icon={<GitBranchPlus size={18} aria-hidden />}
                  label={t('actions.fork', { defaultValue: 'Fork to new conversation' })}
                  onClick={() => {
                    // handleFork owns the forking…/forked/failed toasts (§2.7).
                    setActionSheetOpen(false)
                    onFork(message.id)
                  }}
                />
              ) : null}
              {onDelete ? (
                <>
                  <div className="my-1.5 h-px bg-[var(--color-divider)]" aria-hidden />
                  <MsgActionRow
                    icon={<Trash2 size={18} aria-hidden />}
                    label={t('actions.delete', { defaultValue: 'Delete' })}
                    destructive
                    onClick={() => { setActionSheetOpen(false); setConfirmDelete(true) }}
                  />
                </>
              ) : null}
            </div>
          </SheetContent>
        </Sheet>
      )}
      {/* Delete-round confirmation — removes this question and all of its
          answers (branch-safe: earlier/later turns and other branches stay). */}
      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>{t('deleteRound.title', { defaultValue: 'Delete this exchange?' })}</DialogTitle>
            <DialogDescription>
              {t('deleteRound.body', {
                defaultValue:
                  'This removes this question and its answer from the conversation. Earlier and later messages are kept.',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
              {t('actions.cancel', { ns: 'common', defaultValue: 'Cancel' })}
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                setConfirmDelete(false)
                onDelete?.(message.id)
              }}
            >
              {t('actions.delete', { defaultValue: 'Delete' })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

// Memoised: with stable callback props from MessageList, only the row whose
// `message` object actually changed (the streaming one) re-renders per token —
// the rest of the visible window bails out. Default shallow prop comparison is
// exactly right here (message is a fresh object only when it truly changed).
export const MessageRow = memo(MessageRowImpl)

/** A 44px icon+label row inside the phone message action Sheet. */
function MsgActionRow({
  icon,
  label,
  onClick,
  destructive = false,
  active = false,
}: {
  icon: ReactNode
  label: string
  onClick: () => void
  destructive?: boolean
  active?: boolean
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
          : active
            ? 'text-[var(--color-accent)] bg-[var(--color-accent-soft)]'
            : 'text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]',
      )}
    >
      <span
        className={cn(
          'shrink-0',
          destructive ? 'text-[var(--color-danger)]' : active ? 'text-[var(--color-accent)]' : 'text-[var(--color-fg-muted)]',
        )}
      >
        {icon}
      </span>
      <span className="truncate">{label}</span>
    </button>
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
        className="inline-flex items-center justify-center size-4 max-sm:p-3 max-sm:-m-3 rounded-[4px] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] interactive disabled:opacity-40 disabled:cursor-not-allowed"
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
        className="inline-flex items-center justify-center size-4 max-sm:p-3 max-sm:-m-3 rounded-[4px] hover:bg-[var(--color-surface)] hover:text-[var(--color-fg)] interactive disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <ChevronRight size={9} aria-hidden />
      </button>
    </span>
  )
}

/* ───────────────────────── attachment chips ─────────────────────────── */

/**
 * EditableImageChip — image thumbnail (~64px square) shown inside the edit
 * surface. A small ✕ button (top-right, fades in on hover) removes the image
 * from the resend payload. Tap target is large enough on mobile to avoid
 * misclicks on the underlying preview.
 */
function EditableImageChip({ att, onRemove }: { att: Attachment; onRemove: () => void }) {
  const { t } = useTranslation('chat')
  return (
    <span className="group/att relative inline-block">
      <img
        src={att.previewUrl}
        alt={att.name}
        className="size-16 rounded-[10px] border border-[var(--color-border-subtle)] object-cover"
        draggable={false}
      />
      <button
        type="button"
        aria-label={t('actions.removeAttachment', { defaultValue: 'Remove attachment' })}
        onClick={onRemove}
        className="absolute -right-1.5 -top-1.5 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-sm)] opacity-0 interactive group-hover/att:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <X size={11} aria-hidden />
      </button>
    </span>
  )
}

/**
 * EditableFileChip — non-image attachment chip with a remove button. Wider
 * than the inline bubble chip so the filename has breathing room.
 */
function EditableFileChip({ att, onRemove }: { att: Attachment; onRemove: () => void }) {
  const { t } = useTranslation('chat')
  return (
    <span className="inline-flex items-center gap-1.5 rounded-[10px] bg-[var(--color-bg-muted)] border border-[var(--color-border-subtle)] px-2 py-1 text-[11.5px] text-[var(--color-fg-muted)] max-w-[18rem]">
      <KindIcon kind={att.kind} />
      <span className="truncate">{att.name}</span>
      <button
        type="button"
        aria-label={t('actions.removeAttachment', { defaultValue: 'Remove attachment' })}
        onClick={onRemove}
        className="inline-flex items-center justify-center rounded-full hover:text-[var(--color-fg)] interactive"
      >
        <X size={11} aria-hidden />
      </button>
    </span>
  )
}

/** KindIcon — small icon for non-image attachment chips. */
function KindIcon({ kind }: { kind: Attachment['kind'] }) {
  const iconClass = 'shrink-0 text-[var(--color-fg-subtle)]'
  switch (kind) {
    case 'sheet':
      return <FileSpreadsheet size={12} className={iconClass} aria-hidden />
    case 'pdf':
    case 'doc':
    case 'code':
    case 'other':
    default:
      return <FileText size={12} className={iconClass} aria-hidden />
  }
}
