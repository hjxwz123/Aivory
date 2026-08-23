import { memo, useState, useRef, useEffect, useId, useMemo, useCallback, type ReactNode } from 'react'
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
  Sparkles,
  BookText,
  Loader2,
  Coins,
  Flag,
  ImageOff,
  Zap,
  Sigma,
  Square,
} from 'lucide-react'
import { Link } from 'react-router-dom'
import {
  FEEDBACK_REASON_VALUES,
  GENERATION_INTERRUPTED_ERROR_CODE,
  type Attachment,
  type Citation,
  type FeedbackReason,
  type Message,
  type MessageFeedbackInput,
} from '@/types/chat'
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
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Sheet, SheetContent } from '@/components/ui/sheet'
import { useCopy } from '@/hooks/use-clipboard'
import { useAuth } from '@/store/auth'
import { initials } from '@/components/ui/avatar.utils'
import { useModels } from '@/store/models'
import { useMediaQuery } from '@/hooks/use-media-query'
import { mediaQuery } from '@/lib/design-tokens'
import { hasMathContent } from '@/lib/math-content'
import { Markdown } from './markdown'
import { MathText } from './math-text'
import {
  RichComposerEditor,
  type FormulaTarget,
  type RichComposerEditorHandle,
} from './rich-composer-editor'
import { FormulaEditorDialog } from './formula-editor-dialog'
import { ReasoningTrace } from './reasoning-trace'
import { ImageGenerating } from './image-generating'
import { ResearchPanel } from './research-panel'
import { CitationList } from './citation'
import { VerifyBadge } from './verify-badge'
import { ImageLightbox } from './image-lightbox'
import { FilePreview } from './file-preview'
import { toast } from '@/hooks/use-toast'
import { cn, safeHref } from '@/lib/utils'
import { isEmptyStoppedMessage, messageHasActions } from '@/lib/message-state'
import { documentCitationContentUrl } from '@/lib/citations'
import { userCan } from '@/lib/user-permissions'
import { attachmentKindLabel, attachmentTileClass, fileIconFor } from '@/lib/file-icon'
import {
  feedbackCommentLength,
  MAX_FEEDBACK_COMMENT_LENGTH,
  truncateFeedbackComment,
} from '@/lib/message-feedback'

const RUNTIME_PERMISSION_ERROR_CODES = new Set([
  'drawing_group_permission_required',
  'knowledge_base_group_permission_required',
])

/**
 * ThinkingLogo — the "still forming a reply" indicator shown before the first
 * token: the Aivory mark breathing inside a slow scan ring. The global
 * prefers-reduced-motion rule holds it static.
 */
function ThinkingLogo() {
  return (
    <div
      className="relative grid size-11 cursor-default select-none place-items-center caret-transparent"
      aria-hidden
      onMouseDown={(event) => event.preventDefault()}
    >
      <span className="absolute inset-0 rounded-full border border-[var(--color-border)] [border-top-color:var(--color-secondary)] animate-[spin_1200ms_cubic-bezier(0.6,0.1,0.4,0.9)_infinite]" />
      <LogoMark size={24} className="animate-[core-breathe_2400ms_ease-in-out_infinite]" />
    </div>
  )
}

function RagInjectionStatus({ injection }: { injection: NonNullable<Message['ragInjection']> }) {
  const { t } = useTranslation('chat')
  const active = injection.strategy === 'searching' || injection.strategy === 'expanding'
  const failed = injection.strategy === 'error'
  const kbSettled =
    injection.strategy === 'found' ||
    injection.strategy === 'partial' ||
    injection.strategy === 'no_hit'
  const knowledgeBaseLifecycle = active || failed || kbSettled
  const label = (() => {
    switch (injection.strategy) {
      case 'indexing':
        return t('message.ragIndexing')
      case 'indexing_done':
        return t('message.ragIndexingDone')
      case 'warning':
        return t('message.ragWarning')
      case 'full_text':
        return t('message.ragFullText')
      case 'full_doc':
        return t('message.ragFullDoc')
      case 'none':
        return t('message.ragNone')
      case 'searching':
        return t('message.ragSearching')
      case 'expanding':
        return t('message.ragExpanding')
      case 'found':
        return t('message.ragFound')
      case 'partial':
        return t('message.ragPartial')
      case 'no_hit':
        return t('message.ragNoHit')
      case 'error':
        return t('message.ragError')
      default:
        return t('message.ragDefault')
    }
  })()
  const Icon = active ? Loader2 : failed ? AlertTriangle : BookText

  return (
    <div
      role={failed ? 'alert' : active || kbSettled ? 'status' : undefined}
      aria-live={active ? 'polite' : undefined}
      className={cn(
        'mb-2.5 inline-flex max-w-full items-center gap-1.5 text-[11.5px] text-[var(--color-fg-subtle)]',
        failed && 'text-[var(--color-danger)]',
      )}
    >
      <Icon
        size={13}
        strokeWidth={1.5}
        aria-hidden
        className={cn(
          'shrink-0',
          active ? 'animate-spin text-[var(--color-secondary)]' : failed ? 'text-[var(--color-danger)]' : 'text-[var(--color-secondary)]',
        )}
      />
      <span
        className={cn(
          failed ? 'text-[var(--color-danger)]' : 'text-[var(--color-fg-muted)]',
          knowledgeBaseLifecycle ? 'min-w-0 truncate' : 'shrink-0',
        )}
      >
        {label}
      </span>
      {knowledgeBaseLifecycle && injection.sourceCount !== undefined ? (
        <span
          className={cn(
            'shrink-0 whitespace-nowrap',
            failed ? 'text-[var(--color-danger)]/80' : 'text-[var(--color-fg-faint)]',
          )}
        >
          · {t('message.ragSourceCount', { count: injection.sourceCount })}
        </span>
      ) : null}
      {!knowledgeBaseLifecycle && injection.summary ? (
        <span className="min-w-0 truncate text-[var(--color-fg-faint)]">
          · {injection.summary}
        </span>
      ) : null}
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
  /** "Save" — overwrite the visible Markdown text in place. */
  onSaveEdit?: (id: string, content: string) => void | Promise<void>
  onFeedback?: (id: string, input: MessageFeedbackInput) => Promise<void>
  /** Called when the user clicks `<` / `>` to switch between sibling
   *  branches. Receives the target message id. */
  onBranchSwitch?: (leafId: string) => void
  /** Called when the user picks "Fork to new conversation" from the menu. */
  onFork?: (leafId: string) => void
  /** Delete this whole round (the question + all its answers). Branch-safe. */
  onDelete?: (id: string) => void
  /** Open the product issue reporter for this message. */
  onReport?: (id: string) => void
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

function formatTurnTime(timestamp: number, locale: string): string {
  return new Intl.DateTimeFormat(locale || undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(timestamp)
}

// Credits charged for a turn — show up to 2 decimals, trimming trailing zeros so
// whole amounts read "12" not "12.00".
function formatCredits(credits: number): string {
  return credits.toLocaleString(undefined, { maximumFractionDigits: 2 })
}

function MessageRowImpl({ message, userName, onRegenerate, onEdit, onSaveEdit, onFeedback, onBranchSwitch, onFork, onDelete, onReport, readOnly = false, userMessageMarkdown = false }: MessageRowProps) {
  const isUser = message.role === 'user'
  const userHasMath = useMemo(() => isUser && hasMathContent(message.content), [isUser, message.content])
  // §workspaces: in a shared conversation "own" = authored by ME — other
  // members' questions render LEFT like the assistant, with the author's
  // name + avatar. Personal conversations (no author) keep role semantics.
  const user = useAuth((s) => s.user)
  const meId = user?.id
  const canExportConversations = userCan(user, 'allow_conversation_export')
  // readOnly (admin transcript viewer): there is no "me" perspective — keep the
  // classic role-based layout so mixed legacy/authored turns don't zigzag.
  const isOwn = isUser && (readOnly ? true : message.authorId ? message.authorId === meId : true)
  const isForeignUser = isUser && !isOwn
  // §7.2-6: assistant 气泡标注生成它的模型名 + 图标。
  const model = useModels((s) => (message.modelId ? s.getById(message.modelId) : undefined))
  const { t, i18n } = useTranslation('chat')
  const displayUserName = message.authorName ?? userName ?? t('common.you', { ns: 'common' })
  const [hovered, setHovered] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  // Phone: the per-message actions live in a bottom Sheet (a clean thread reveals
  // them on tap) instead of an always-on row of tiny icons (§ mobile redesign).
  const isPhone = useMediaQuery(mediaQuery.phone)
  const [actionSheetOpen, setActionSheetOpen] = useState(false)
  const mobileActionsRef = useRef<HTMLButtonElement>(null)
  const dislikeButtonRef = useRef<HTMLButtonElement>(null)
  const feedbackPanelId = useId()
  const [feedbackPanelOpen, setFeedbackPanelOpen] = useState(false)
  const [feedbackReasons, setFeedbackReasons] = useState<FeedbackReason[]>(message.feedbackReasons ?? [])
  const [feedbackComment, setFeedbackComment] = useState(message.feedbackComment ?? '')
  const [feedbackPending, setFeedbackPending] = useState(false)
  const [feedbackSubmitted, setFeedbackSubmitted] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [editing, setEditing] = useState(false)
  const [savingEdit, setSavingEdit] = useState(false)
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
  const [lightbox, setLightbox] = useState<{
    src: string
    alt?: string
    downloadUrl?: string
    filename?: string
  } | null>(null)
  // Non-image attachment preview (pdf / docx / text / fallback) — opens a modal
  // instead of letting the click download the file.
  const [filePreview, setFilePreview] = useState<{
    name: string
    url?: string
    kind: Attachment['kind']
    authenticated?: boolean
  } | null>(null)
  const openDocumentCitation = useCallback((citation: Citation) => {
    const url = documentCitationContentUrl(citation)
    if (!url) return
    setFilePreview({ name: citation.title, url, kind: 'other', authenticated: true })
  }, [])
  const editRef = useRef<RichComposerEditorHandle>(null)
  const assistantEditRef = useRef<HTMLTextAreaElement>(null)
  const [editFormulaOpen, setEditFormulaOpen] = useState(false)
  const [editFormulaTarget, setEditFormulaTarget] = useState<FormulaTarget | null>(null)
  const editFormulaSelectionRef = useRef<{ from: number; to: number } | null>(null)
  const { copied, copy } = useCopy()
  const [exportingDocx, setExportingDocx] = useState(false)
  const exportAttemptRef = useRef(0)
  const emptyStopped = isEmptyStoppedMessage(message)
  const turnTime = useMemo(
    () => ({
      label: formatTurnTime(message.createdAt, i18n.resolvedLanguage || i18n.language),
      iso: new Date(message.createdAt).toISOString(),
    }),
    [message.createdAt, i18n.language, i18n.resolvedLanguage],
  )

  // Export THIS reply as .docx: markdown -> formatted Word, LaTeX -> native
  // equations (lib/docx-export.ts). Lazy import keeps docx/katex-omml out of
  // the main bundle; failures surface as a toast, never an exception.
  const exportDocx = async () => {
    if (!canExportConversations || exportingDocx || !message.content) return
    const userID = user?.id
    if (!userID) return
    const attempt = exportAttemptRef.current + 1
    exportAttemptRef.current = attempt
    setExportingDocx(true)
    try {
      const { exportMarkdownAsDocx } = await import('@/lib/docx-export')
      const stamp = new Date().toISOString().slice(0, 16).replace(/[T:]/g, '-')
      await exportMarkdownAsDocx(message.content, `aivory-reply-${stamp}`, () => {
        const latestUser = useAuth.getState().user
        return exportAttemptRef.current === attempt && latestUser?.id === userID &&
          userCan(latestUser, 'allow_conversation_export')
      })
    } catch {
      if (exportAttemptRef.current === attempt) {
        toast.error(t('actions.exportDocxFailed', { defaultValue: 'Export failed' }))
      }
    } finally {
      if (exportAttemptRef.current === attempt) setExportingDocx(false)
    }
  }

  useEffect(() => {
    if (!canExportConversations) {
      exportAttemptRef.current += 1
      setExportingDocx(false)
    }
  }, [canExportConversations, user?.id])

  useEffect(() => () => {
    exportAttemptRef.current += 1
  }, [])

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
    const t = setTimeout(() => {
      if (isUser) editRef.current?.focus('end')
      else {
        assistantEditRef.current?.focus()
        const end = assistantEditRef.current?.value.length ?? 0
        assistantEditRef.current?.setSelectionRange(end, end)
      }
    }, 60)
    return () => clearTimeout(t)
  }, [editing, isUser])

  // A phone dislike starts in the bottom action Sheet. Wait for that Sheet to
  // release focus before moving focus into the newly revealed inline panel.
  useEffect(() => {
    if (!feedbackPanelOpen || feedbackPending) return
    const timer = window.setTimeout(() => {
      const panel = document.getElementById(feedbackPanelId)
      panel?.querySelector<HTMLButtonElement>('[data-feedback-reason]')?.focus()
    }, isPhone ? 180 : 0)
    return () => window.clearTimeout(timer)
  }, [feedbackPanelId, feedbackPanelOpen, feedbackPending, isPhone])

  useEffect(() => {
    if (!feedbackSubmitted) return
    const timer = window.setTimeout(() => setFeedbackSubmitted(false), 4500)
    return () => window.clearTimeout(timer)
  }, [feedbackSubmitted])

  // Workspace roles may change without remounting the transcript. A callback
  // disappearing means this row is now read-only, so discard any transient
  // editor or feedback UI that was opened before the permission change.
  useEffect(() => {
    if (onFeedback) return
    setFeedbackPanelOpen(false)
  }, [onFeedback])

  useEffect(() => {
    if (isUser ? onEdit : onSaveEdit) return
    setEditing(false)
  }, [isUser, onEdit, onSaveEdit])

  // An in-place edit (or any authoritative external refresh) invalidates an
  // old dislike. Clear the row-local draft only after the feedback mutation
  // itself has settled, so the first optimistic dislike is not closed early.
  useEffect(() => {
    if (message.disliked || feedbackPending) return
    if (feedbackPanelOpen) setFeedbackPanelOpen(false)
    if (feedbackReasons.length > 0) setFeedbackReasons([])
    if (feedbackComment) setFeedbackComment('')
    if (feedbackSubmitted) setFeedbackSubmitted(false)
  }, [
    feedbackComment,
    feedbackPanelOpen,
    feedbackPending,
    feedbackReasons.length,
    feedbackSubmitted,
    message.disliked,
  ])

  function commitEdit() {
    const next = draft.trim()
    if (!next) return
    onEdit?.(message.id, next, draftAtts)
    setEditing(false)
  }

  // "Save" — overwrite the message text in place. Assistant replies keep the
  // exact Markdown source, including intentional leading/trailing whitespace.
  async function saveInPlace() {
    const next = isUser ? draft.trim() : draft
    if (!next.trim() || savingEdit || !onSaveEdit) return
    setSavingEdit(true)
    try {
      await onSaveEdit(message.id, next)
      setEditing(false)
    } catch {
      toast.error(t('actions.editSaveFailed'))
    } finally {
      setSavingEdit(false)
    }
  }

  function removeDraftAtt(id: string) {
    setDraftAtts((s) => s.filter((a) => a.id !== id))
  }

  function openNewEditFormula() {
    editFormulaSelectionRef.current = editRef.current?.captureSelection() ?? null
    setEditFormulaTarget(null)
    setEditFormulaOpen(true)
  }

  function openExistingEditFormula(target: FormulaTarget) {
    editFormulaSelectionRef.current = null
    setEditFormulaTarget(target)
    setEditFormulaOpen(true)
  }

  async function persistFeedback(input: MessageFeedbackInput): Promise<boolean> {
    if (feedbackPending || !onFeedback) return false
    setFeedbackPending(true)
    try {
      await onFeedback(message.id, input)
      return true
    } catch {
      toast.error(t('feedback.saveFailed'))
      return false
    } finally {
      setFeedbackPending(false)
    }
  }

  function restoreFeedbackTriggerFocus() {
    window.requestAnimationFrame(() => {
      const target = isPhone ? mobileActionsRef.current : dislikeButtonRef.current
      target?.focus({ preventScroll: true })
    })
  }

  async function toggleLike() {
    setActionSheetOpen(false)
    setFeedbackPanelOpen(false)
    setFeedbackSubmitted(false)
    const feedback = message.liked ? '' : 'like'
    const saved = await persistFeedback({ feedback, reasons: [], comment: '' })
    if (saved && feedback === 'like') toast.success(t('feedback.thanks'))
  }

  async function toggleDislike() {
    // On phones this must happen before the inline panel is made visible; the
    // modal Sheet would otherwise cover it and retain focus.
    setActionSheetOpen(false)
    setFeedbackSubmitted(false)

    if (message.disliked) {
      setFeedbackPanelOpen(false)
      setFeedbackReasons([])
      setFeedbackComment('')
      await persistFeedback({ feedback: '', reasons: [], comment: '' })
      return
    }

    setFeedbackReasons([])
    setFeedbackComment('')
    setFeedbackPanelOpen(true)
    const saved = await persistFeedback({ feedback: 'dislike' })
    if (!saved) setFeedbackPanelOpen(false)
  }

  async function submitFeedbackDetail() {
    const saved = await persistFeedback({
      feedback: 'dislike',
      reasons: feedbackReasons,
      comment: feedbackComment.trim(),
    })
    if (!saved) return
    setFeedbackPanelOpen(false)
    setFeedbackSubmitted(true)
    restoreFeedbackTriggerFocus()
  }

  function skipFeedbackDetail() {
    setFeedbackPanelOpen(false)
    setFeedbackSubmitted(true)
    restoreFeedbackTriggerFocus()
  }

  function toggleFeedbackReason(reason: FeedbackReason) {
    setFeedbackReasons((current) =>
      current.includes(reason)
        ? current.filter((item) => item !== reason)
        : [...current, reason],
    )
  }

  const visible = hovered || menuOpen || message.liked || message.disliked || feedbackPanelOpen || feedbackSubmitted
  const attachments = message.attachments ?? []
  const imageAttachments = attachments.filter(
    (attachment) => attachment.kind === 'image' && attachment.previewUrl && !brokenAtts.has(attachment.id),
  )
  const otherAttachments = attachments.filter(
    (attachment) => attachment.kind !== 'image' || !attachment.previewUrl || brokenAtts.has(attachment.id),
  )

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
            {/* §fast-mode: a fast turn's real model is hidden — show a bolt + 快速. */}
            {message.fast ? (
              <span className="flex size-5 items-center justify-center rounded-full bg-[var(--color-bg-muted)] text-[var(--color-fg-muted)]">
                <Zap size={13} aria-hidden />
              </span>
            ) : model ? (
              <ModelIcon icon={model.icon} size={20} />
            ) : (
              <Avatar size="sm" tone="sage">
                <AvatarFallback>A</AvatarFallback>
              </Avatar>
            )}
            <span className="font-medium text-[15px] text-[var(--color-fg)]">
              {message.fast
                ? t('fastMode.label', { defaultValue: '快速' })
                : model?.label ?? message.modelLabel ?? t('assistant')}
            </span>
            {/* The turn timestamp is secondary context: reserve its space so the
                model label never jumps, but reveal it only while this row is
                hovered or keyboard-focused. Touch layouts keep the header clean. */}
            <time
              dateTime={turnTime.iso}
              className="max-sm:hidden text-[11px] text-[var(--color-fg-subtle)] tabular-nums opacity-0 transition-opacity duration-[140ms] ease-out group-hover/msg:opacity-100 group-focus-within/msg:opacity-100"
            >
              {turnTime.label}
            </time>
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
            <RichComposerEditor
              ref={editRef}
              value={draft}
              onChange={setDraft}
              onSubmit={commitEdit}
              submitOnEnter={false}
              onEscape={() => setEditing(false)}
              onFormulaClick={openExistingEditFormula}
              onPasteFiles={() => undefined}
              onLongPaste={(text) => setDraft((current) => (current ? `${current}\n${text}` : text))}
              canAttachLongText={false}
              maxLength={Number.MAX_SAFE_INTEGER}
              placeholder=""
              ariaLabel={t('composer.inputLabel', { defaultValue: 'Edit message' })}
              formulaEditLabel={t('composer.formula.editTitle')}
              className="message-edit-editor"
            />
            <div className="mt-2.5 flex flex-wrap items-center gap-2">
              <Tooltip content={t('composer.formula.action')}>
                <button
                  type="button"
                  onClick={openNewEditFormula}
                  aria-label={t('composer.formula.action')}
                  className="inline-flex size-8 max-sm:size-11 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <Sigma size={15} aria-hidden />
                </button>
              </Tooltip>
              <div className="ml-auto flex max-w-full flex-wrap justify-end gap-2 max-sm:w-full [&>button]:max-sm:min-h-11">
                <Button size="sm" variant="ghost" disabled={savingEdit} onClick={() => setEditing(false)}>
                  {t('actions.cancelEdit', { defaultValue: 'Cancel' })}
                </Button>
                <Button size="sm" variant="secondary" loading={savingEdit} onClick={() => void saveInPlace()}>
                  {t('actions.saveInPlace', { defaultValue: 'Save' })}
                </Button>
                <Button size="sm" variant="primary" disabled={savingEdit} onClick={commitEdit}>
                  {t('actions.saveEdit', { defaultValue: 'Save & resend' })}
                </Button>
              </div>
            </div>
          </div>
        ) : editing ? (
          <div className="w-full">
            <Textarea
              ref={assistantEditRef}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Escape' && !savingEdit) setEditing(false)
                if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
                  event.preventDefault()
                  void saveInPlace()
                }
              }}
              readOnly={savingEdit}
              spellCheck={false}
              aria-label={t('actions.editReplySource')}
              className="min-h-64 resize-y bg-[var(--color-surface-sunken)] font-mono text-[13px] leading-relaxed"
            />
            <div className="mt-3 flex justify-end gap-2 max-sm:[&>button]:min-h-11">
              <Button
                size="sm"
                variant="ghost"
                disabled={savingEdit}
                onClick={() => setEditing(false)}
              >
                {t('actions.cancelEdit')}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                loading={savingEdit}
                disabled={!draft.trim() || draft === message.content}
                onClick={() => void saveInPlace()}
              >
                {t('actions.saveInPlace')}
              </Button>
            </div>
          </div>
        ) : isUser ? (
          <div
            className={cn(
              'rounded-[18px] px-4 py-2.5',
              'bg-[var(--color-user-bubble)] border border-[var(--color-user-bubble-border)]',
              'text-[var(--color-fg)] text-[length:var(--text-chat-body)] leading-relaxed',
              userMessageMarkdown || userHasMath ? 'min-w-0' : 'whitespace-pre-wrap break-words',
              'min-w-0 max-w-full',
            )}
          >
            {attachments.length > 0 ? (
              <div className="mb-2 grid min-w-0 gap-2">
                {imageAttachments.length > 0 ? (
                  <div
                    className={cn(
                      imageAttachments.length === 1
                        ? 'flex min-w-0 max-w-full'
                        : 'flex min-w-0 w-[min(30rem,72vw)] max-w-full flex-wrap gap-2',
                      imageAttachments.length > 1 && (isOwn ? 'justify-end' : 'justify-start'),
                    )}
                  >
                    {imageAttachments.map((attachment) => (
                      <button
                        key={attachment.id}
                        type="button"
                        onClick={() => setLightbox({
                          src: attachment.previewUrl!,
                          alt: attachment.name,
                          downloadUrl: attachment.previewUrl,
                          filename: attachment.name,
                        })}
                        aria-label={t('actions.viewImage', { defaultValue: 'View image' })}
                        className={cn(
                          'block max-w-full shrink-0 overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] hover:opacity-90',
                        )}
                      >
                        <img
                          src={attachment.previewUrl}
                          alt={attachment.name}
                          className={cn(
                            'object-cover',
                            imageAttachments.length === 1
                              ? 'h-auto max-h-56 w-auto max-w-[min(100%,18rem)] sm:max-w-[min(100%,22rem)]'
                              : 'size-24 sm:size-28',
                          )}
                          draggable={false}
                          onError={() => setBrokenAtts((previous) => new Set(previous).add(attachment.id))}
                        />
                      </button>
                    ))}
                  </div>
                ) : null}

                {otherAttachments.length > 0 ? (
                  <div className="grid min-w-0 gap-1.5">
                    {otherAttachments.map((attachment) => {
                      if (attachment.kind === 'image' && brokenAtts.has(attachment.id)) {
                        return (
                          <span
                            key={attachment.id}
                            className="inline-flex h-14 min-w-0 max-w-[22rem] items-center gap-2.5 rounded-[10px] border border-dashed border-[var(--color-border)] bg-[var(--color-surface-raised)] px-2.5 text-[var(--color-fg-subtle)]"
                            title={attachment.name}
                          >
                            <span className="grid size-9 shrink-0 place-items-center rounded-[9px] bg-[var(--color-danger-soft)] text-[var(--color-danger)]">
                              <ImageOff size={18} aria-hidden />
                            </span>
                            <span className="grid min-w-0 gap-0.5 text-left">
                              <span className="truncate text-[0.8125rem] font-semibold leading-tight">
                                {attachment.name}
                              </span>
                              <span className="text-[0.75rem] leading-tight">
                                {t('attachmentDeleted', { defaultValue: 'File deleted' })}
                              </span>
                            </span>
                          </span>
                        )
                      }

                      return (
                        <button
                          key={attachment.id}
                          type="button"
                          onClick={() =>
                            setFilePreview({
                              name: attachment.name,
                              url: attachment.previewUrl,
                              kind: attachment.kind,
                            })
                          }
                          aria-label={t('actions.previewFile', { defaultValue: 'Preview file' })}
                          className="flex h-14 min-w-0 max-w-[22rem] items-center gap-2.5 rounded-[10px] border border-[var(--color-border)] bg-[var(--color-surface-raised)] py-2 pl-2.5 pr-3 text-left shadow-[var(--shadow-xs)] interactive hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                        >
                          <span
                            className={cn(
                              'grid size-9 shrink-0 place-items-center rounded-[9px]',
                              attachmentTileClass(attachment),
                            )}
                            aria-hidden
                          >
                            <AttachmentTypeIcon attachment={attachment} />
                          </span>
                          <span className="grid min-w-0 flex-1 gap-0.5">
                            <span className="truncate text-[0.8125rem] font-semibold leading-tight text-[var(--color-fg)]">
                              {attachment.name}
                            </span>
                            <span className="truncate text-[0.75rem] leading-tight text-[var(--color-fg-subtle)]">
                              {attachmentKindLabel(attachment)}
                            </span>
                          </span>
                        </button>
                      )
                    })}
                  </div>
                ) : null}
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
            ) : userHasMath ? (
              <MathText content={message.content} />
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

            {message.ragInjection ? <RagInjectionStatus injection={message.ragInjection} /> : null}

            {/* §4.20 image mode: dedicated drawing surface (distinct from the
                chat thinking/tool-call trace) while no image artifact exists yet. */}
            {message.imageStatus && (!message.artifacts || message.artifacts.length === 0) ? (
              <ImageGenerating phase={message.imageStatus} />
            ) : /* Streaming placeholder while empty — the brand thinking mark */
            message.streaming && !message.content && (!message.reasoning || message.reasoning.length === 0) ? (
              <div className="py-1">
                <ThinkingLogo />
              </div>
            ) : emptyStopped ? (
              <div role="status" className="my-1 inline-flex items-center gap-2 text-sm text-[var(--color-fg-muted)]">
                <Square size={11} className="shrink-0 fill-current" aria-hidden />
                <span>{t('message.stopped', { defaultValue: 'Generation stopped.' })}</span>
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
                  <Markdown
                    content={message.content}
                    live={Boolean(message.streaming)}
                    blockKeyPrefix={message.id}
                    citations={message.citations}
                    onOpenDocumentCitation={readOnly ? undefined : openDocumentCitation}
                    className="prose-full"
                  />
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
                      {message.errorCode === GENERATION_INTERRUPTED_ERROR_CODE
                        ? t('message.error.interrupted')
                        : message.errorCode && RUNTIME_PERMISSION_ERROR_CODES.has(message.errorCode)
                          ? t('message.error.permissionChanged')
                          : t('message.error.title')}
                    </div>
                    {message.errorCode !== GENERATION_INTERRUPTED_ERROR_CODE ? (
                      <p className="mt-1 text-[12.5px] text-[var(--color-fg-subtle)] break-words">
                        {message.errorCode === 'drawing_group_permission_required'
                          ? t('message.error.drawingPermission')
                          : message.errorCode === 'knowledge_base_group_permission_required'
                            ? t('message.error.knowledgeBasePermission')
                            : message.error}
                      </p>
                    ) : null}
                    {onRegenerate && (!message.errorCode || !RUNTIME_PERMISSION_ERROR_CODES.has(message.errorCode)) ? (
                      <button
                        type="button"
                        onClick={() => onRegenerate?.(message.id)}
                        className="mt-2.5 inline-flex items-center gap-1.5 h-8 px-3 rounded-[9px] text-sm font-medium bg-[var(--color-danger)] text-[var(--color-fg-inverted)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                      >
                        <RefreshCw size={13} aria-hidden />
                        {t('message.error.retry')}
                      </button>
                    ) : null}
                  </div>
                ) : null}
                {message.citations && message.citations.length > 0 ? (
                  <CitationList
                    citations={message.citations}
                    onOpenDocument={readOnly ? undefined : openDocumentCitation}
                  />
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
                          <button
                            key={a.id}
                            type="button"
                            onClick={() => setLightbox({
                              src: href,
                              alt: a.filename,
                              downloadUrl: href,
                              filename: a.filename,
                            })}
                            aria-label={t('actions.viewImage', { defaultValue: 'View image' })}
                            className="block overflow-hidden rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                          >
                            <img
                              src={href}
                              alt={a.filename}
                              className="max-h-64 rounded-lg border border-[var(--color-border)] transition-opacity hover:opacity-90"
                            />
                          </button>
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
        {!readOnly && onBranchSwitch && !isUser && message.streaming && message.branchCount && message.branchCount > 1 && typeof message.branchIndex === 'number' ? (
          <div className="mt-2 inline-flex items-center">
            <BranchSwitcher message={message} onSwitch={onBranchSwitch} t={t} />
          </div>
        ) : null}

        {/* Always-visible branch switcher (both roles, once settled). Sits under
            the USER bubble after an edit-branch (§4.15 R1) and under the AI reply
            after a retry (R2) — shown the instant the branch exists rather than
            waiting for a hover, and kept visible in read-only (admin) triage. The
            hover action bar below no longer carries its own copy. */}
        {!readOnly && onBranchSwitch && !editing && !message.streaming && message.branchCount && message.branchCount > 1 && typeof message.branchIndex === 'number' ? (
          <div className="mt-2 inline-flex items-center">
            <BranchSwitcher message={message} onSwitch={onBranchSwitch} t={t} />
          </div>
        ) : null}

        {/* Actions — always rendered after streaming completes, so the layout
            never jumps when the user hovers in/out. Visibility is controlled
            via opacity + pointer-events so nothing below is pushed around.
            Also show the action bar when a message has an error but no content
            so the user can retry the failed message. */}
        {!readOnly && !editing && !message.streaming && messageHasActions(message) ? (
          isPhone ? (
            <div className="mt-1.5 flex items-center gap-2">
              <button
                ref={mobileActionsRef}
                type="button"
                onClick={() => setActionSheetOpen(true)}
                aria-label={t('actions.more')}
                className="inline-flex items-center justify-center size-[var(--tap-min)] -ml-2 rounded-[10px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <MoreHorizontal size={18} aria-hidden />
              </button>
              {!isUser && message.credits && message.credits > 0 ? (
                <Tooltip content={t('actions.viewSubscription')}>
                  <Link
                    to="/subscription"
                    className="inline-flex min-h-7 items-center gap-1 rounded-[6px] px-1 text-[11px] text-[var(--color-secondary)] tabular-nums interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <Coins size={11} aria-hidden />
                    {t('actions.creditsUsed', { credits: formatCredits(message.credits) })}
                  </Link>
                </Tooltip>
              ) : null}
            </div>
          ) : (
          <div
            className={cn(
              'mt-2 inline-flex items-center gap-0.5 transition-opacity duration-[140ms] ease-out focus-within:opacity-100 focus-within:pointer-events-auto',
              visible
                ? 'opacity-100'
                : 'opacity-0 pointer-events-none max-sm:opacity-100 max-sm:pointer-events-auto',
            )}
          >
                {/* Per-reply generation time (§ 用时). It belongs with reply
                    actions rather than model identity, immediately before Copy. */}
                {!isUser && message.genMs ? (
                  <span className="mr-1 inline-flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)] tabular-nums">
                    <Clock size={11} aria-hidden />
                    {formatGenMs(message.genMs)}
                  </span>
                ) : null}
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

                {!isUser && message.content && onSaveEdit ? (
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
                ) : null}

                {!isUser && (
                  <>
                    {message.content && canExportConversations ? (
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
                    {onRegenerate ? (
                      <Tooltip content={t('actions.regenerate')}>
                        <button
                          type="button"
                          onClick={() => onRegenerate(message.id)}
                          aria-label={t('actions.regenerate')}
                          className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                        >
                          <RefreshCw size={13} aria-hidden />
                        </button>
                      </Tooltip>
                    ) : null}
                    {onFeedback ? (
                      <>
                        <Tooltip content={t('actions.helpful')}>
                          <button
                            type="button"
                            onClick={() => void toggleLike()}
                            disabled={feedbackPending}
                            aria-label={t('actions.helpful')}
                            aria-pressed={message.liked}
                            className={cn(
                              'inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] interactive disabled:pointer-events-none disabled:opacity-50',
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
                            ref={dislikeButtonRef}
                            type="button"
                            onClick={() => void toggleDislike()}
                            disabled={feedbackPending}
                            aria-label={t('actions.notHelpful')}
                            aria-pressed={message.disliked}
                            aria-expanded={feedbackPanelOpen}
                            aria-controls={feedbackPanelId}
                            className={cn(
                              'inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] interactive disabled:pointer-events-none disabled:opacity-50',
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
                    ) : null}
                  </>
                )}

                {isOwn && onEdit && (
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

                {onReport ? (
                  <Tooltip content={t('actions.reportIssue')}>
                    <button
                      type="button"
                      onClick={() => onReport(message.id)}
                      aria-label={t('actions.reportIssue')}
                      className="inline-flex items-center justify-center size-7 max-sm:size-9 rounded-[7px] text-[var(--color-fg-subtle)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <Flag size={13} aria-hidden />
                    </button>
                  </Tooltip>
                ) : null}

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
                    {!isUser && onRegenerate && (
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
                  <Tooltip content={t('actions.viewSubscription')}>
                    <Link
                      to="/subscription"
                      className="ml-1.5 inline-flex min-h-7 items-center gap-1 rounded-[6px] px-1 text-[11px] text-[var(--color-secondary)] tabular-nums interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <Coins size={11} aria-hidden />
                      {t('actions.creditsUsed', { credits: formatCredits(message.credits) })}
                    </Link>
                  </Tooltip>
                ) : null}
          </div>
          )
        ) : null}
        {!isUser && !editing && feedbackPanelOpen ? (
          <FeedbackPanel
            id={feedbackPanelId}
            reasons={feedbackReasons}
            comment={feedbackComment}
            pending={feedbackPending}
            onToggleReason={toggleFeedbackReason}
            onCommentChange={setFeedbackComment}
            onSkip={skipFeedbackDetail}
            onSubmit={() => void submitFeedbackDetail()}
          />
        ) : null}
        {!isUser && !editing && feedbackSubmitted && message.disliked ? (
          <div
            role="status"
            className="mt-2 flex min-h-9 items-center gap-2 text-[12px] text-[var(--color-fg-muted)] max-sm:min-h-[var(--tap-min)]"
          >
            <Check size={14} className="text-[var(--color-success)]" aria-hidden />
            <span>{t('feedback.saved')}</span>
            {onRegenerate ? (
              <button
                type="button"
                onClick={() => {
                  setFeedbackSubmitted(false)
                  onRegenerate(message.id)
                }}
                className="ml-1 inline-flex min-h-8 items-center rounded-[7px] px-2 font-medium text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:min-h-[var(--tap-min)]"
              >
                <RefreshCw size={13} className="mr-1.5" aria-hidden />
                {t('actions.regenerate')}
              </button>
            ) : null}
          </div>
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
        downloadUrl={lightbox?.downloadUrl}
        filename={lightbox?.filename}
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
          <SheetContent side="bottom" size="sm" label={t('actions.more')} className="h-auto max-h-[85dvh]">
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
                  {message.content && onSaveEdit ? (
                    <MsgActionRow
                      icon={<Pencil size={18} aria-hidden />}
                      label={t('actions.edit')}
                      onClick={() => { setActionSheetOpen(false); setEditing(true) }}
                    />
                  ) : null}
                  {message.content && canExportConversations ? (
                    <MsgActionRow
                      icon={<FileDown size={18} aria-hidden />}
                      label={t('actions.exportDocx', { defaultValue: 'Export as Word' })}
                      onClick={() => { setActionSheetOpen(false); void exportDocx() }}
                    />
                  ) : null}
                  {onRegenerate ? (
                    <MsgActionRow
                      icon={<RefreshCw size={18} aria-hidden />}
                      label={t('actions.regenerate')}
                      onClick={() => { setActionSheetOpen(false); onRegenerate(message.id) }}
                    />
                  ) : null}
                  {onFeedback ? (
                    <>
                      <MsgActionRow
                        icon={<ThumbsUp size={18} aria-hidden />}
                        label={t('actions.helpful')}
                        active={message.liked}
                        disabled={feedbackPending}
                        onClick={() => {
                          setActionSheetOpen(false)
                          void toggleLike()
                        }}
                      />
                      <MsgActionRow
                        icon={<ThumbsDown size={18} aria-hidden />}
                        label={t('actions.notHelpful')}
                        active={message.disliked}
                        disabled={feedbackPending}
                        controls={feedbackPanelId}
                        expanded={feedbackPanelOpen}
                        onClick={() => {
                          setActionSheetOpen(false)
                          void toggleDislike()
                        }}
                      />
                    </>
                  ) : null}
                </>
              ) : isOwn && onEdit ? (
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
              {onReport ? (
                <MsgActionRow
                  icon={<Flag size={18} aria-hidden />}
                  label={t('actions.reportIssue')}
                  onClick={() => {
                    setActionSheetOpen(false)
                    window.setTimeout(() => onReport(message.id), 200)
                  }}
                />
              ) : null}
            </div>
          </SheetContent>
        </Sheet>
      )}
      <FormulaEditorDialog
        open={editFormulaOpen}
        initialLatex={editFormulaTarget?.latex ?? ''}
        editing={Boolean(editFormulaTarget)}
        onOpenChange={(open) => {
          setEditFormulaOpen(open)
          if (!open && editing) requestAnimationFrame(() => editRef.current?.focus())
        }}
        onConfirm={(latex) => {
          editRef.current?.setFormula(latex, editFormulaTarget, editFormulaSelectionRef.current)
          editFormulaSelectionRef.current = null
        }}
      />
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

function FeedbackPanel({
  id,
  reasons,
  comment,
  pending,
  onToggleReason,
  onCommentChange,
  onSkip,
  onSubmit,
}: {
  id: string
  reasons: FeedbackReason[]
  comment: string
  pending: boolean
  onToggleReason: (reason: FeedbackReason) => void
  onCommentChange: (comment: string) => void
  onSkip: () => void
  onSubmit: () => void
}) {
  const { t } = useTranslation('chat')
  const titleId = `${id}-title`

  return (
    <section
      id={id}
      aria-labelledby={titleId}
      className="mt-3 w-full max-w-[36rem] border-t border-[var(--color-divider)] pt-3"
    >
      <div className="mb-2.5 flex items-baseline gap-1.5">
        <h3 id={titleId} className="text-[13px] font-medium text-[var(--color-fg)]">
          {t('feedback.title')}
        </h3>
        <span className="text-[11px] text-[var(--color-fg-subtle)]">
          {t('feedback.optional')}
        </span>
      </div>

      <div
        role="group"
        aria-label={t('feedback.reasonsLabel')}
        className="flex flex-wrap gap-1.5"
      >
        {FEEDBACK_REASON_VALUES.map((reason) => {
          const selected = reasons.includes(reason)
          return (
            <button
              key={reason}
              type="button"
              data-feedback-reason
              aria-pressed={selected}
              disabled={pending}
              onClick={() => onToggleReason(reason)}
              className={cn(
                'inline-flex min-h-9 items-center rounded-[8px] border px-2.5 text-[12px] interactive max-sm:min-h-[var(--tap-min)] max-sm:px-3',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-55',
                selected
                  ? 'border-[var(--color-border-strong)] bg-[var(--color-bg-muted)] text-[var(--color-fg)]'
                  : 'border-[var(--color-border)] bg-transparent text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
              )}
            >
              <Check
                size={12}
                className={cn('mr-1.5 shrink-0', selected ? 'opacity-100' : 'opacity-0')}
                aria-hidden
              />
              {t(`feedback.reasons.${reason}`)}
            </button>
          )
        })}
      </div>

      <label htmlFor={`${id}-comment`} className="mt-3 block text-[12px] font-medium text-[var(--color-fg-muted)]">
        {t('feedback.commentLabel')}
      </label>
      <Textarea
        id={`${id}-comment`}
        value={comment}
        rows={2}
        disabled={pending}
        onChange={(event) => onCommentChange(truncateFeedbackComment(event.target.value))}
        placeholder={t('feedback.commentPlaceholder')}
        className="mt-1.5 min-h-[68px] text-[13px]"
      />

      <div className="mt-2 flex items-center justify-between gap-3">
        <span className="text-[11px] tabular-nums text-[var(--color-fg-subtle)]" aria-live="polite">
          {feedbackCommentLength(comment)}/{MAX_FEEDBACK_COMMENT_LENGTH}
        </span>
        <div className="flex items-center gap-1.5">
          <Button
            size="sm"
            variant="ghost"
            className="max-sm:h-[var(--tap-min)]"
            disabled={pending}
            onClick={onSkip}
          >
            {t('feedback.skip')}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            className="max-sm:h-[var(--tap-min)]"
            loading={pending}
            onClick={onSubmit}
          >
            {t('feedback.submit')}
          </Button>
        </div>
      </div>
    </section>
  )
}

/** A 44px icon+label row inside the phone message action Sheet. */
function MsgActionRow({
  icon,
  label,
  onClick,
  destructive = false,
  active,
  disabled = false,
  controls,
  expanded,
}: {
  icon: ReactNode
  label: string
  onClick: () => void
  destructive?: boolean
  active?: boolean
  disabled?: boolean
  controls?: string
  expanded?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-pressed={active}
      aria-controls={controls}
      aria-expanded={expanded}
      className={cn(
        'flex w-full items-center gap-3 min-h-[var(--tap-min)] px-3 text-left text-[15px] rounded-[10px] interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        'disabled:pointer-events-none disabled:opacity-50',
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
      <AttachmentTypeIcon attachment={att} size={12} className="text-[var(--color-fg-subtle)]" />
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

function AttachmentTypeIcon({
  attachment,
  size = 18,
  className,
}: {
  attachment: Pick<Attachment, 'kind' | 'name'>
  size?: number
  className?: string
}) {
  const Icon = fileIconFor(attachment.name, attachment.kind)
  return <Icon size={size} strokeWidth={2} className={cn('shrink-0', className)} aria-hidden />
}
