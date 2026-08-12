/**
 * Composer — the unified message-input surface used on both the empty home
 * screen and inside a conversation. Real concerns it owns:
 *
 *   1. Per-model param_controls (§2.3-G): rendered above the textarea via
 *      <ParamControls>; the picked values flow up via onSubmit().
 *   2. Real file upload (§4.6): when the user picks files we POST each one
 *      to /api/files immediately and surface a chip with the returned id, so
 *      the attachment that lands in the SSE request carries a real backend
 *      file_id, not just a local filename.
 *   3. Stop / send button states.
 *   4. IME-aware Enter handling for CJK input.
 */
import { useWorkspaces } from '@/store/workspaces'
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import {
  ArrowUp,
  AudioWaveform,
  Paperclip,
  Image as ImageIcon,
  StopCircle,
  Telescope,
  ShieldCheck,
  X,
  Loader2,
  RefreshCw,
  BookOpen,
  Check,
  AlertTriangle,
  Plus,
  Wrench,
  Globe,
  Sigma,
  ArrowLeft,
  ChevronRight,
  FileText,
} from 'lucide-react'
import type { Attachment } from '@/types/chat'
import {
  modelAllowsToolModeSelection,
  normalizeToolModeForCapabilities,
  resolveModelToolModeCapabilities,
  type ToolMode,
  type ToolModeCapabilities,
} from '@/lib/tool-mode'
import { Tooltip } from '@/components/ui/tooltip'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { kbsApi, audioApi, conversationsApi, libraryApi } from '@/api/endpoints'
import { ModelPicker } from './model-picker'
import { StylePicker } from './style-picker'
import { ParamControls } from './param-controls'
import { filterVisibleParams, parseControls } from './param-controls.utils'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useModels } from '@/store/models'
import { useAuth } from '@/store/auth'
import { useComposerPrefs } from '@/store/composer-prefs'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, apiUpload, ApiError } from '@/api/client'
import { blockReload } from '@/lib/sync-guards'
import { toastStorageQuotaFull } from '@/lib/quota-toast'
import type {
  ApiAttachment,
  ApiConversationFile,
  ApiDocument,
  ApiKnowledgeBase,
  ApiUserPrompt,
  ApiUserSkill,
} from '@/api/types'
import { toast } from '@/hooks/use-toast'
import { cn, uid, modKey } from '@/lib/utils'
import { attachmentKindLabel, attachmentTileClass, fileIconFor } from '@/lib/file-icon'
import {
  addSelectedUserSkill,
  selectedUserSkillIdsForRequest,
} from '@/lib/composer-commands'
import { skillDisplayDescription } from '@/lib/skill-description'
import { encodeWavFromBlob } from '@/lib/audio'
import { startVoiceStream, type VoiceStreamController } from '@/lib/audio-stream'
import { ProgressRing } from '@/components/ui/progress-ring'
import { SkillIcon } from '@/components/ui/skill-icon'
import { envNum } from '@/lib/env-config'
import {
  filterFilesForImageCapability,
  isImageFileLike,
  NON_IMAGE_ATTACHMENT_ACCEPT,
  resolveImageAttachmentCapability,
} from '@/lib/vision-capability'
import {
  RichComposerEditor,
  type ComposerCommandQuery,
  type FormulaTarget,
  type RichComposerEditorHandle,
} from './rich-composer-editor'
import { FormulaEditorDialog } from './formula-editor-dialog'
import { ToolSelectionDialog } from './tool-selection-dialog'
import { modelHasBuiltinTools, modelSupportsBuiltinTool } from '@/lib/builtin-tools'
import { hasImageAttachment, hasSendableMessageContent } from '@/lib/chat-message-input'
import {
  knowledgeBaseSelectionContext,
  knowledgeBasesHaveCompatibleEmbeddings,
} from '@/lib/knowledge-base-selection'

interface ComposerProps {
  modelId: string
  onModelChange: (id: string) => void
  /** §fast-mode: current 快速/进阶 selection + setter (per-conversation). */
  fast?: boolean
  onFastChange?: (fast: boolean) => void
  onSubmit: (
    text: string,
    attachments: Attachment[],
    options: {
      mode?: 'default' | 'deep-research' | 'canvas'
      params?: Record<string, unknown>
      /** §4.20 image mode: chosen style id (sent for an image-model turn). */
      imageStyleId?: string
      /** §verify: run the secondary-auditor pass on this turn. */
      verify?: boolean
      /** Three-state tool policy, always sent explicitly. */
      toolMode: ToolMode
      /** §4.4-B: forced non-tool web search (only in disabled mode). */
      webSearch?: boolean
      /** User-owned skills explicitly selected for this turn. */
      selectedUserSkillIds?: string[]
      /** Per-model candidate tool subset. undefined preserves the default-all policy. */
      selectedToolIds?: string[]
      /** §fast-mode: run this turn in fast mode. */
      fast?: boolean
    },
  ) => void
  onStop?: () => void
  streaming?: boolean
  initialValue?: string
  placeholder?: string
  /** When true, render compact (used inside landing hero CTA). */
  compact?: boolean
  /** Autofocus on mount. */
  autoFocus?: boolean
  /** Optional local draft cache scope. Used by the new-chat composer only. */
  draftScope?: string
  /** Conversation id (so uploads carry the right scope). */
  conversationId?: string
  /**
   * Home screen only: there is no conversation yet when the user attaches a
   * file. Provide this to lazily create one BEFORE the first upload so the file
   * is POSTed with `?conversation_id&rag=1` and actually gets ingested for
   * retrieval (instead of landing at a scope-less `/files` and being
   * unreachable). Should be idempotent — return the same id on repeat calls.
   */
  ensureConversationId?: () => Promise<string | undefined>
  /**
   * Fired when the LAST attachment leaves the composer (user removed it, or a
   * failed upload auto-removed it). Home / project pages use it to discard the
   * draft conversation that existed only to scope those uploads — otherwise it
   * lingers server-side and surfaces as an "Untitled" row in the sidebar.
   */
  onAttachmentsDrained?: () => void
  /** Knowledge bases bound to the conversation (§7.2-7 📚 selector). */
  kbIds?: string[]
  /** Project-owned KB, implicitly attached and therefore not user-removable. */
  projectKBId?: string
  /** Embedding signature for a project KB omitted from the ordinary KB list. */
  projectKBEmbeddingModelId?: string
  projectKBEmbeddingDim?: number
  /** When provided, the 📚 selector is shown and changes flow up here. */
  onKBChange?: (kbIds: string[]) => void
  /** True when a model picker already lives in the page header (e.g. ChatThread).
   *  On phones the composer then drops its own picker to avoid a redundant,
   *  cramped second selector — the header one is enough (§ mobile composer). */
  modelPickerInHeader?: boolean
}

const MAX_LEN = envNum('VITE_AIVORY_MAX_LEN', 12_000)
const EMPTY_PARAM_VALUES: Record<string, unknown> = {}
// Backend file ids already committed to a SENT message, shared across every
// composer instance (module scope, not a per-instance ref). Sending the FIRST
// message from the home screen navigates to the thread, which mounts a FRESH
// composer whose draft-file restore (listDraftFiles) can race the backend's
// commit of the just-sent file and re-add the image that was already sent. The
// existing per-instance `committedAttachmentIds` ref can't guard a different
// instance, so committed ids are tracked here too. Upload ids are unique, so a
// committed id never legitimately reappears as a fresh draft; the size cap only
// bounds memory across a long session.
const committedFileIds = new Set<string>()
function markFileCommitted(id: string) {
  committedFileIds.add(id)
  if (committedFileIds.size > 256) {
    const oldest = committedFileIds.values().next().value
    if (oldest !== undefined) committedFileIds.delete(oldest)
  }
}

// Speech-to-text capability is shared across composer instances. Besides the
// provider, the backend reports whether its required credentials exist so the
// prominent empty-draft action never records a clip it cannot transcribe.
interface SttCapability {
  provider: string
  enabled: boolean
}

let sttCapabilityPromise: Promise<SttCapability> | null = null
function loadSttCapability(): Promise<SttCapability> {
  if (!sttCapabilityPromise) {
    sttCapabilityPromise = audioApi
      .capabilities()
      .then((c) => ({ provider: c.provider || 'gpt', enabled: Boolean(c.enabled) }))
      .catch(() => ({ provider: 'gpt', enabled: false }))
  }
  return sttCapabilityPromise
}

// §4.6-A upload size caps. The /api/files handler is authoritative; we read the
// admin-configured per-kind caps from the upload policy once (module-level
// cache, shared across composer instances) so we can reject an oversize file up
// front instead of wasting an upload round-trip. Falls back to the seeded
// defaults if the fetch fails, so a transient error never blocks attaching.
const DEFAULT_UPLOAD_LIMITS = { max_image_bytes: 5 * 1024 * 1024, max_file_bytes: 50 * 1024 * 1024 }
let uploadLimitsCache: Promise<{ max_image_bytes: number; max_file_bytes: number }> | null = null

// INGEST_POLL_MS: status poll cadence. Do not fake-ready after a timer: the
// send button must stay blocked until parsing, embedding and vector upsert
// really finished, otherwise the model falls back to tool-side PDF parsing.
const INGEST_POLL_MS = envNum('VITE_AIVORY_INGEST_POLL_MS', 1200)
function getUploadLimits() {
  if (!uploadLimitsCache) {
    uploadLimitsCache = api<{ max_image_bytes?: number; max_file_bytes?: number }>('/me/upload-policy')
      .then((p) => ({
        max_image_bytes: p.max_image_bytes ?? DEFAULT_UPLOAD_LIMITS.max_image_bytes,
        max_file_bytes: p.max_file_bytes ?? DEFAULT_UPLOAD_LIMITS.max_file_bytes,
      }))
      .catch(() => DEFAULT_UPLOAD_LIMITS)
  }
  return uploadLimitsCache
}

interface PendingAttachment extends Attachment {
  /** true while POST /api/files is in flight. */
  uploading?: boolean
  /** Browser-reported upload progress, 0-100, while uploading is true. */
  uploadProgress?: number
  /** Conversation scope used for the uploaded file; needed for explicit removal. */
  uploadScopeId?: string
  /** Ingest progress of the conversation-scoped document. While 'parsing' or
   *  'embedding' the send button is blocked so the FIRST question always lands
   *  after the file is searchable (§ chat uploads). */
  ingest?: 'parsing' | 'embedding' | 'ready' | 'failed'
}

function restoredAttachmentKind(kind: string): Attachment['kind'] {
  switch (kind) {
    case 'image':
    case 'pdf':
    case 'doc':
    case 'sheet':
    case 'code':
      return kind
    default:
      return 'other'
  }
}

// classifyAttachmentKind mirrors the backend's kindOf (files_handlers.go) by
// leading with the file EXTENSION, then falling back to MIME. Extension-first is
// not cosmetic: an .xlsx's MIME is
// `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, whose
// "officedocument" substring trips a naive /doc/ MIME test and mislabels the
// spreadsheet as a 'doc'. The backend files .xlsx/.csv as 'sheet' — sandbox data
// with NO RAG document row — so a stale 'doc' claim makes the send preflight
// demand a document that was never created and rejects the turn with 409
// "attached document not found". Keeping client + server in agreement on kind is
// what prevents that.
function classifyAttachmentKind(name: string, type: string): Attachment['kind'] {
  const ext = name.toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] ?? ''
  if (isImageFileLike({ name, type })) return 'image'
  if (type === 'application/pdf' || ext === 'pdf') return 'pdf'
  // Spreadsheets BEFORE documents — the OOXML MIME matches both.
  if (['csv', 'tsv', 'xlsx', 'xls', 'xlsm', 'ods'].includes(ext) || /spreadsheet|ms-excel|csv/i.test(type))
    return 'sheet'
  if (['doc', 'docx', 'ppt', 'pptx', 'odt', 'odp', 'rtf'].includes(ext) || /word|wordprocessing|presentation/i.test(type))
    return 'doc'
  return 'other'
}

function restoredIngestStatus(status?: ApiDocument['status']): PendingAttachment['ingest'] {
  switch (status) {
    case 'ready':
    case 'failed':
    case 'embedding':
      return status
    case 'pending':
    case 'parsing':
      return 'parsing'
    default:
      return undefined
  }
}

function restoreConversationFile(file: ApiConversationFile, scopeId: string): PendingAttachment {
  return {
    id: file.id,
    name: file.filename,
    kind: restoredAttachmentKind(file.kind),
    size: file.size_bytes,
    previewUrl: file.url,
    uploadScopeId: scopeId,
    documentId: file.document_id,
    ingest: restoredIngestStatus(file.document_status),
  }
}

// One toggleable turn feature in the composer "+" menu (§4.13-B). Rendered as an
// icon + name with an optional one-line description. Unavailable features are omitted by
// the caller instead of being rendered as disabled rows.
interface FeatureItem {
  key: string
  icon: ReactNode
  label: string
  desc?: string
  active: boolean
  /** Row is revealed conditionally (e.g. web search only in disabled mode)
   *  — play a soft fade-in when it mounts. */
  enter?: boolean
  /** Accessible label for the active chip's clear action. */
  clearLabel?: string
  /** Optional compact text rendered beside the icon in the active chip. */
  chipText?: string
  toggle: () => void
}

type ComposerCommandItem =
  | { kind: 'skill'; id: string; name: string; description: string; skill: ApiUserSkill }
  | { kind: 'prompt'; id: string; name: string; description: string; prompt: ApiUserPrompt }
  | {
      kind: 'knowledge-base'
      id: string
      name: string
      description: string
      knowledgeBase: ApiKnowledgeBase
      disabled: boolean
    }

function FeatureRow({ item, onAfter }: { item: FeatureItem; onAfter?: () => void }) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={item.active}
      onClick={() => {
        item.toggle()
        onAfter?.()
      }}
      className={cn(
        'flex w-full min-w-0 max-w-full items-start gap-2.5 overflow-hidden rounded-[12px] border px-2.5 py-2 text-left interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        item.enter && 'animate-[fade-in_var(--duration-base)_var(--ease-out)]',
        item.active
          ? 'border-[var(--color-tool-selection-border)] bg-[var(--color-tool-selection-soft)]'
          : 'border-transparent hover:bg-[var(--color-bg-muted)]',
      )}
    >
      <span
        className={cn(
          'mt-0.5 inline-flex shrink-0',
          item.active ? 'text-[var(--color-tool-selection-text)]' : 'text-[var(--color-fg-muted)]',
        )}
        aria-hidden
      >
        {item.icon}
      </span>
      <span className="min-w-0 max-w-full flex-1">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="min-w-0 truncate text-[13px] font-medium text-[var(--color-fg)]">
            {item.label}
          </span>
          {item.active ? <Check size={13} className="text-[var(--color-tool-selection-text)]" aria-hidden /> : null}
        </span>
        {item.desc ? (
          <span className="mt-0.5 block max-w-full break-words [overflow-wrap:anywhere] text-[11.5px] leading-snug text-[var(--color-fg-subtle)]">
            {item.desc}
          </span>
        ) : null}
      </span>
    </button>
  )
}

type ToolUsePanel = 'root' | 'tools'

interface ToolSelectionAction {
  label: string
  description: string
  summary: string
  custom: boolean
  onOpen: () => void
}

/** A stable drill-down: root features -> tool use -> tool selection dialog. */
function ToolUseSelector({
  label,
  description,
  menuOpen,
  rootItems,
  toolSelection,
  onPanelChange,
  onAfter,
}: {
  label: string
  description: string
  menuOpen: boolean
  rootItems: FeatureItem[]
  toolSelection: ToolSelectionAction
  onPanelChange?: (panel: ToolUsePanel) => void
  onAfter?: () => void
}) {
  const [panel, setPanel] = useState<ToolUsePanel>('root')
  const previousPanelRef = useRef(panel)
  const rootToolRef = useRef<HTMLButtonElement>(null)
  const toolsBackRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    onPanelChange?.(panel)
  }, [onPanelChange, panel])

  useEffect(() => {
    if (!menuOpen) {
      previousPanelRef.current = 'root'
      setPanel('root')
      return
    }
    if (previousPanelRef.current === panel) return
    previousPanelRef.current = panel
    if (panel === 'tools') {
      toolsBackRef.current?.focus()
    } else {
      rootToolRef.current?.focus()
    }
  }, [menuOpen, panel])

  if (panel === 'root') {
    return (
      <div className="flex flex-col gap-0.5">
        {rootItems.map((item) => (
          <FeatureRow key={item.key} item={item} onAfter={onAfter} />
        ))}
        <button
          ref={rootToolRef}
          type="button"
          onClick={() => setPanel('tools')}
          aria-label={`${label}: ${toolSelection.summary}`}
          className={cn(
            'flex w-full min-w-0 max-w-full items-start gap-2.5 overflow-hidden rounded-[12px] border px-2.5 py-2 text-left interactive',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            toolSelection.custom
              ? 'border-[var(--color-tool-selection-border)] bg-[var(--color-tool-selection-soft)]'
              : 'border-transparent hover:bg-[var(--color-bg-muted)]',
          )}
        >
          <span
            className={cn(
              'mt-0.5 inline-flex shrink-0',
              toolSelection.custom
                ? 'text-[var(--color-tool-selection-text)]'
                : 'text-[var(--color-fg-muted)]',
            )}
            aria-hidden
          >
            <Wrench size={16} />
          </span>
          <span className="min-w-0 max-w-full flex-1">
            <span className="flex min-w-0 items-center gap-2">
              <span className="truncate text-[13px] font-medium text-[var(--color-fg)]">
                {label}
              </span>
              <span
                className={cn(
                  'ml-auto shrink-0 text-[11.5px]',
                  toolSelection.custom
                    ? 'text-[var(--color-tool-selection-text)]'
                    : 'text-[var(--color-fg-subtle)]',
                )}
              >
                {toolSelection.summary}
              </span>
            </span>
            <span className="mt-0.5 block max-w-full break-words [overflow-wrap:anywhere] text-[11.5px] leading-snug text-[var(--color-fg-subtle)]">
              {description}
            </span>
          </span>
          <ChevronRight size={14} className="mt-1 shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
        </button>
      </div>
    )
  }

  return (
    <div
      className="px-1 py-1"
      role="group"
      aria-label={label}
      aria-description={description}
      onKeyDown={(event) => {
        if (event.key !== 'ArrowLeft') return
        event.preventDefault()
        setPanel('root')
      }}
    >
      <button
        ref={toolsBackRef}
        type="button"
        onClick={() => setPanel('root')}
        className="flex min-h-9 w-full items-center gap-2 rounded-[8px] px-2 py-1.5 text-left text-[12.5px] font-medium text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <ArrowLeft size={14} aria-hidden />
        <span className="truncate">{label}</span>
        <span className="ml-auto text-[11px] text-[var(--color-fg-subtle)]">{toolSelection.summary}</span>
      </button>
      <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden />
      <button
        type="button"
        aria-haspopup="dialog"
        aria-label={`${toolSelection.label}: ${toolSelection.summary}`}
        onClick={() => {
          onAfter?.()
          toolSelection.onOpen()
        }}
        className={cn(
          'flex w-full min-w-0 max-w-full items-start gap-2.5 overflow-hidden rounded-[12px] border px-2.5 py-2 text-left interactive',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
          toolSelection.custom
            ? 'border-[var(--color-tool-selection-border)] bg-[var(--color-tool-selection-soft)]'
            : 'border-transparent hover:bg-[var(--color-bg-muted)]',
        )}
      >
        <span
          className={cn(
            'mt-0.5 inline-flex shrink-0',
            toolSelection.custom
              ? 'text-[var(--color-tool-selection-text)]'
              : 'text-[var(--color-fg-muted)]',
          )}
          aria-hidden
        >
          <Wrench size={16} />
        </span>
        <span className="min-w-0 max-w-full flex-1">
          <span className="flex min-w-0 items-center gap-2">
            <span className="truncate text-[13px] font-medium text-[var(--color-fg)]">
              {toolSelection.label}
            </span>
            <span
              className={cn(
                'ml-auto shrink-0 text-[11.5px]',
                toolSelection.custom
                  ? 'text-[var(--color-tool-selection-text)]'
                  : 'text-[var(--color-fg-subtle)]',
              )}
            >
              {toolSelection.summary}
            </span>
          </span>
          <span className="mt-0.5 block max-w-full break-words [overflow-wrap:anywhere] text-[11.5px] leading-snug text-[var(--color-fg-subtle)]">
            {toolSelection.description}
          </span>
        </span>
        <ChevronRight size={14} className="mt-1 shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
      </button>
    </div>
  )
}

export function Composer({
  modelId,
  onModelChange,
  fast,
  onFastChange,
  onSubmit,
  onStop,
  streaming,
  initialValue = '',
  placeholder,
  compact = false,
  autoFocus = false,
  draftScope,
  conversationId,
  ensureConversationId,
  onAttachmentsDrained,
  kbIds,
  projectKBId,
  projectKBEmbeddingModelId,
  projectKBEmbeddingDim,
  onKBChange,
  modelPickerInHeader = false,
}: ComposerProps) {
  const { t } = useTranslation(['chat', 'library'])
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const workspaceId = useWorkspaces((state) => state.activeId ?? undefined)
  const mode = useComposerPrefs((s) => s.mode)
  const setMode = useComposerPrefs((s) => s.setMode)
  // §verify: when on, the answer is fact-checked by a second model this turn.
  const verify = useComposerPrefs((s) => s.verify)
  const setVerify = useComposerPrefs((s) => s.setVerify)
  // The persisted tool policy still drives request routing, while the composer
  // presents tool selection as the single user-facing tool-use control.
  const toolMode = useComposerPrefs((s) => s.toolMode)
  const setToolMode = useComposerPrefs((s) => s.setToolMode)
  const forceWebSearch = useComposerPrefs((s) => s.forceWebSearch)
  const setForceWebSearch = useComposerPrefs((s) => s.setForceWebSearch)
  const selectedToolIds = useComposerPrefs((s) =>
    modelId ? s.selectedToolIdsByModel[modelId] : undefined,
  )
  const setSelectedToolIds = useComposerPrefs((s) => s.setSelectedToolIds)
  const cachedParamValues = useComposerPrefs((s) => (modelId ? s.paramValuesByModel[modelId] : undefined))
  const setCachedParamValues = useComposerPrefs((s) => s.setParamValues)
  const cachedDraft = useComposerPrefs((s) => (draftScope ? s.draftsByScope[draftScope] : undefined))
  const setCachedDraft = useComposerPrefs((s) => s.setDraft)
  const paramValues = cachedParamValues ?? EMPTY_PARAM_VALUES
  const [value, setValue] = useState(() => (draftScope ? cachedDraft ?? initialValue : initialValue))
  const valueRef = useRef(value)
  const draftScopeRef = useRef(draftScope)
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  const attachmentsRef = useRef<PendingAttachment[]>([])
  const attachmentScopeRef = useRef(conversationId)
  const [restoringAttachments, setRestoringAttachments] = useState(Boolean(conversationId))
  const [kbList, setKBList] = useState<ApiKnowledgeBase[]>([])
  const [kbLoading, setKBLoading] = useState(false)
  const [kbLoaded, setKBLoaded] = useState(false)
  const [kbLoadFailed, setKBLoadFailed] = useState(false)
  const [kbPopoverOpen, setKBPopoverOpen] = useState(false)
  const kbLoadRequestRef = useRef(0)
  const [librarySkills, setLibrarySkills] = useState<ApiUserSkill[]>([])
  const [libraryPrompts, setLibraryPrompts] = useState<ApiUserPrompt[]>([])
  const [libraryLoading, setLibraryLoading] = useState(true)
  const [selectedSkills, setSelectedSkills] = useState<ApiUserSkill[]>([])
  const [commandQuery, setCommandQuery] = useState<ComposerCommandQuery | null>(null)
  const [commandIndex, setCommandIndex] = useState(0)
  const [dismissedCommandKey, setDismissedCommandKey] = useState('')
  const commandMenuRef = useRef<HTMLDivElement>(null)
  const composerRootRef = useRef<HTMLDivElement>(null)
  const [commandPosition, setCommandPosition] = useState<{
    left: number
    top?: number
    bottom?: number
    width: number
    maxHeight: number
    placement: 'up' | 'down'
  } | null>(null)
  // Drag-and-drop file upload. `dragOver` is driven by WINDOW-level listeners
  // (see the effect below) so a file can be dropped ANYWHERE on the page, not
  // only onto the composer surface. dragDepthRef balances nested
  // dragenter/dragleave so the full-screen overlay doesn't flicker as the
  // pointer crosses child elements. handleAttachRef exposes the latest attach
  // handler to those window listeners without re-subscribing them each render.
  const [dragOver, setDragOver] = useState(false)
  const dragDepthRef = useRef(0)
  const handleAttachRef = useRef<(files: FileList | null) => Promise<number>>(() => Promise.resolve(0))
  // Narrow screens collapse every secondary action into a single "+" menu
  // (Gemini/ChatGPT-mobile pattern) so the row never overflows and tap targets
  // stay large. 639px = Tailwind's `sm` breakpoint minus 1.
  const isMobile = useMediaQuery('(max-width: 639px)')
  const [moreOpen, setMoreOpen] = useState(false)
  const [toolUsePanel, setToolUsePanel] = useState<ToolUsePanel>('root')
  // Turn-feature menu (the "+" left of attach): research / verify / tool use
  // / web-search in one shared mobile/desktop surface.
  const [featuresOpen, setFeaturesOpen] = useState(false)
  const [toolSelectionOpen, setToolSelectionOpen] = useState(false)
  const handleSelectedToolIdsChange = useCallback(
    (ids: string[] | undefined) => setSelectedToolIds(modelId, ids),
    [modelId, setSelectedToolIds],
  )
  const loadKBList = useCallback(async () => {
    const requestID = ++kbLoadRequestRef.current
    setKBLoading(true)
    setKBLoadFailed(false)
    try {
      const rows = await kbsApi.list(workspaceId)
      if (requestID !== kbLoadRequestRef.current) return
      setKBList(rows)
      setKBLoaded(true)
    } catch {
      if (requestID !== kbLoadRequestRef.current) return
      setKBLoadFailed(true)
      setKBLoaded(true)
    } finally {
      if (requestID === kbLoadRequestRef.current) setKBLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    // KB visibility is workspace-scoped. Invalidate both cached rows and any
    // older request so a late response from the previous space cannot populate
    // the mention menu after a workspace switch.
    kbLoadRequestRef.current += 1
    setKBList([])
    setKBLoading(false)
    setKBLoaded(false)
    setKBLoadFailed(false)
    setKBPopoverOpen(false)
    setCommandQuery(null)
  }, [workspaceId])
  const ref = useRef<RichComposerEditorHandle>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const imageFileRef = useRef<HTMLInputElement>(null)
  const [formulaOpen, setFormulaOpen] = useState(false)
  const [formulaTarget, setFormulaTarget] = useState<FormulaTarget | null>(null)
  const formulaSelectionRef = useRef<{ from: number; to: number } | null>(null)
  const submittingRef = useRef(false)
  // §2.7 re-entry guard for the long-draft → .txt conversion in handleSubmit.
  const convertingRef = useRef(false)
  // Active ingest-status pollers, keyed by attachment id, so they can be
  // cancelled on remove / submit / unmount.
  const pollTimers = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map())
  // Local ids explicitly removed while an upload is still in flight. If the
  // request completes after removal, immediately delete the backend file/doc.
  const removedAttachmentIds = useRef<Set<string>>(new Set())
  // Attachments already committed to a sent message. A draft-file restore fetch
  // fired at conversation-creation time (e.g. pasting an image on the home
  // screen lazily creates the scope) can resolve AFTER the send cleared the
  // composer, re-adding the just-sent file. These ids are filtered out of any
  // late restore so a sent attachment never bounces back into the input.
  const committedAttachmentIds = useRef<Set<string>>(new Set())
  // Latest onAttachmentsDrained, readable from async upload callbacks without
  // capturing a stale closure.
  const onAttachmentsDrainedRef = useRef(onAttachmentsDrained)
  onAttachmentsDrainedRef.current = onAttachmentsDrained
  // True only after listDraftFiles has SUCCESSFULLY enumerated this scope's
  // server-side draft files at least once. The drain signal (discard the draft
  // conversation) is gated on it: while the restore is in flight or after it
  // failed, the composer's local chips may not reflect files the conversation
  // still holds (e.g. a recovered previous-session draft), and discarding then
  // would delete those files. Reset whenever the scope changes.
  const draftFilesLoadedRef = useRef(false)
  // Fire the drain signal only when we're certain the conversation holds no
  // remaining server-side files (all local chips gone AND the server set is known).
  const maybeSignalDrained = useCallback(() => {
    if (draftFilesLoadedRef.current) onAttachmentsDrainedRef.current?.()
  }, [])
  // The horizontal attachment rail (chips). A plain mouse wheel only scrolls
  // vertically, so translate dominant vertical wheel deltas into horizontal
  // scroll when the rail overflows — trackpads already pan natively. Native
  // listener because React root-delegates wheel as passive (preventDefault
  // would be ignored via onWheel).
  const chipsRailRef = useRef<HTMLDivElement | null>(null)
  const hasAttachments = attachments.length > 0

  useEffect(() => {
    let current = true
    setLibraryLoading(true)
    void Promise.all([libraryApi.skills(), libraryApi.prompts()])
      .then(([skills, prompts]) => {
        if (!current) return
        setLibrarySkills(skills)
        setLibraryPrompts(prompts)
      })
      .catch(() => {
        if (!current) return
        setLibrarySkills([])
        setLibraryPrompts([])
      })
      .finally(() => {
        if (current) setLibraryLoading(false)
      })
    return () => {
      current = false
    }
  }, [])

  useEffect(() => {
    setSelectedSkills([])
    setCommandQuery(null)
  }, [conversationId, draftScope])
  useEffect(() => {
    const el = chipsRailRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (el.scrollWidth <= el.clientWidth) return
      if (Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return
      // deltaMode 1 = lines (Firefox wheel) — normalise to pixels.
      el.scrollLeft += e.deltaMode === 1 ? e.deltaY * 24 : e.deltaY
      e.preventDefault()
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [hasAttachments])
  // §full-screen drop: accept image/file drops anywhere in the window, not just
  // on the composer. Window-level listeners raise a full-viewport overlay while
  // a file is dragged in and route the drop through the same handleAttach path.
  // Only ONE composer is ever mounted (home / thread / project routes are
  // mutually exclusive), so a single window listener set never double-handles.
  useEffect(() => {
    const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes('Files')
    const onDragEnter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      dragDepthRef.current += 1
      setDragOver(true)
    }
    const onDragOver = (e: DragEvent) => {
      if (!hasFiles(e)) return
      // preventDefault marks the whole window a valid drop target and stops the
      // browser from navigating to a file dropped outside the composer.
      e.preventDefault()
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
      // Self-heal the overlay if a dragenter was missed (some browsers skip it
      // when the drag originates outside the document).
      if (dragDepthRef.current === 0) dragDepthRef.current = 1
      setDragOver(true)
    }
    const onDragLeave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
      if (dragDepthRef.current === 0) setDragOver(false)
    }
    const onDrop = (e: DragEvent) => {
      if (!hasFiles(e)) return
      e.preventDefault()
      dragDepthRef.current = 0
      setDragOver(false)
      if (e.dataTransfer?.files?.length) void handleAttachRef.current(e.dataTransfer.files)
    }
    window.addEventListener('dragenter', onDragEnter)
    window.addEventListener('dragover', onDragOver)
    window.addEventListener('dragleave', onDragLeave)
    window.addEventListener('drop', onDrop)
    return () => {
      window.removeEventListener('dragenter', onDragEnter)
      window.removeEventListener('dragover', onDragOver)
      window.removeEventListener('dragleave', onDragLeave)
      window.removeEventListener('drop', onDrop)
    }
  }, [])
  useEffect(() => {
    const timers = pollTimers.current
    return () => {
      timers.forEach((tm) => clearTimeout(tm))
      timers.clear()
    }
  }, [])
  useEffect(() => {
    attachmentsRef.current = attachments
  }, [attachments])
  // Voice input (§ whisper). Record via MediaRecorder, then transcribe through
  // the admin-configured /audio/transcriptions endpoint and insert the text.
  const [recording, setRecording] = useState(false)
  const [transcribing, setTranscribing] = useState(false)
  const recorderRef = useRef<MediaRecorder | null>(null)
  const chunksRef = useRef<Blob[]>([])
  // Volcano live-streaming voice (§ Volcano ASR). `sttProvider` picks the flow;
  // `streamConnecting` covers the mic-acquire + socket-connect gap before audio
  // flows. streamBaseRef holds the composer text the live transcript appends to.
  const [sttProvider, setSttProvider] = useState('gpt')
  const [voiceEnabled, setVoiceEnabled] = useState(false)
  const [voiceStarting, setVoiceStarting] = useState(false)
  const [streamConnecting, setStreamConnecting] = useState(false)
  const streamCtlRef = useRef<VoiceStreamController | null>(null)
  const recordingStreamRef = useRef<MediaStream | null>(null)
  const streamBaseRef = useRef('')
  const streamAttemptRef = useRef(0)
  // getUserMedia resolves after an arbitrary permission delay. A monotonically
  // increasing attempt id lets cancel/unmount invalidate a late stream before it
  // can create an orphan MediaRecorder or retain the microphone.
  const voiceStartAttemptRef = useRef(0)
  const voiceStartingRef = useRef(false)

  useEffect(() => {
    let live = true
    void loadSttCapability().then((capability) => {
      if (live) {
        setSttProvider(capability.provider)
        setVoiceEnabled(capability.enabled)
      }
    })
    return () => {
      live = false
    }
  }, [])

  // Never leave the mic hot / a socket open when the composer unmounts. The
  // recorded-clip path needs explicit track cleanup too; stopping only the live
  // WebSocket path leaves the browser's microphone indicator active.
  useEffect(
    () => () => {
      voiceStartAttemptRef.current += 1
      voiceStartingRef.current = false
      streamAttemptRef.current += 1
      streamCtlRef.current?.cancel()
      const recorder = recorderRef.current
      if (recorder && recorder.state !== 'inactive') {
        recorder.ondataavailable = null
        recorder.onstop = null
        recorder.stop()
      }
      recordingStreamRef.current?.getTracks().forEach((track) => track.stop())
      recordingStreamRef.current = null
      recorderRef.current = null
    },
    [],
  )
  const updateValue = useCallback(
    (next: string) => {
      valueRef.current = next
      setValue(next)
      if (draftScope) setCachedDraft(draftScope, next)
    },
    [draftScope, setCachedDraft],
  )

  useEffect(() => {
    valueRef.current = value
  }, [value])

  // §23: the invisible-upgrade reload must never destroy unsent work. Register
  // as a reload blocker while the composer holds anything the user would lose:
  // typed text (thread composers keep it in React state only), staged
  // attachments, or an active / still-transcribing voice recording.
  const hasUnsentWork =
    value.trim().length > 0 ||
    attachments.length > 0 ||
    selectedSkills.length > 0 ||
    (kbIds ?? []).some((id) => id && id !== projectKBId) ||
    recording ||
    transcribing ||
    streamConnecting ||
    voiceStarting
  useEffect(() => {
    if (!hasUnsentWork) return
    return blockReload()
  }, [hasUnsentWork])

  useEffect(() => {
    if (draftScopeRef.current === draftScope) return
    draftScopeRef.current = draftScope
    updateValue(draftScope ? cachedDraft ?? initialValue : initialValue)
  }, [cachedDraft, draftScope, initialValue, updateValue])

  // Mic toggle. Volcano streams live; every other provider records a clip and
  // transcribes it. A second click always stops whatever is active.
  async function toggleVoice() {
    if (!voiceEnabled || transcribing) return
    if (voiceStartingRef.current || recording || streamConnecting || streamCtlRef.current) {
      stopVoice()
      return
    }
    if (sttProvider === 'volcano') {
      await startStreaming()
      return
    }
    await startRecording()
  }

  function stopVoice() {
    if (voiceStartingRef.current) {
      voiceStartAttemptRef.current += 1
      voiceStartingRef.current = false
      setVoiceStarting(false)
      return
    }
    if (streamCtlRef.current) {
      // Listening → stop the mic and wait for the final transcript.
      setTranscribing(true)
      streamCtlRef.current.stop()
      return
    }
    if (streamConnecting) {
      // Still connecting — invalidate this attempt. If its WebSocket resolves
      // later, startStreaming() tears it down without touching a newer attempt.
      streamAttemptRef.current += 1
      setStreamConnecting(false)
      return
    }
    const recorder = recorderRef.current
    if (recorder && recorder.state !== 'inactive') recorder.stop()
  }

  // Volcano live path: append the incremental transcript to whatever text the
  // composer already held when recording started.
  async function startStreaming() {
    const attempt = streamAttemptRef.current + 1
    streamAttemptRef.current = attempt
    setStreamConnecting(true)
    const base = valueRef.current
    streamBaseRef.current = base.trim() ? base.trimEnd() + ' ' : ''
    try {
      const ctl = await startVoiceStream({
        onReady: () => {
          if (streamAttemptRef.current !== attempt) return
          setStreamConnecting(false)
          setRecording(true)
        },
        onPartial: (text) => {
          if (streamAttemptRef.current === attempt) updateValue(streamBaseRef.current + text)
        },
        onFinal: (text) => {
          if (streamAttemptRef.current === attempt && text) {
            updateValue(streamBaseRef.current + text)
            requestAnimationFrame(() => ref.current?.focus('end'))
          }
        },
        onError: () => {
          if (streamAttemptRef.current === attempt) toast.error(t('composer.voiceFailed'))
        },
        onClose: () => {
          if (streamAttemptRef.current !== attempt) return
          streamCtlRef.current = null
          setRecording(false)
          setStreamConnecting(false)
          setTranscribing(false)
        },
      })
      if (streamAttemptRef.current !== attempt) {
        ctl.cancel()
        return
      }
      streamCtlRef.current = ctl
    } catch (e) {
      if (streamAttemptRef.current !== attempt) return
      setStreamConnecting(false)
      const msg = e instanceof Error ? e.message : ''
      toast.error(msg === 'unsupported' ? t('composer.voiceUnsupported') : t('composer.voicePermission'))
    }
  }

  // Whisper / OpenAI-compatible path: record a clip via MediaRecorder, then POST.
  async function startRecording() {
    if (typeof MediaRecorder === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
      toast.error(t('composer.voiceUnsupported'))
      return
    }
    const attempt = voiceStartAttemptRef.current + 1
    voiceStartAttemptRef.current = attempt
    voiceStartingRef.current = true
    setVoiceStarting(true)
    let stream: MediaStream | undefined
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true })
      if (voiceStartAttemptRef.current !== attempt) {
        stream.getTracks().forEach((track) => track.stop())
        return
      }
    } catch {
      if (voiceStartAttemptRef.current === attempt) toast.error(t('composer.voicePermission'))
      return
    } finally {
      if (voiceStartAttemptRef.current === attempt) {
        voiceStartingRef.current = false
        setVoiceStarting(false)
      }
    }
    if (!stream || voiceStartAttemptRef.current !== attempt) return

    try {
      const rec = new MediaRecorder(stream)
      recordingStreamRef.current = stream
      chunksRef.current = []
      rec.ondataavailable = (e) => {
        if (e.data.size > 0) chunksRef.current.push(e.data)
      }
      rec.onstop = () => {
        stream.getTracks().forEach((tr) => tr.stop())
        if (recordingStreamRef.current === stream) recordingStreamRef.current = null
        if (recorderRef.current === rec) recorderRef.current = null
        setRecording(false)
        const blob = new Blob(chunksRef.current, { type: rec.mimeType || 'audio/webm' })
        if (blob.size === 0) return
        setTranscribing(true)
        void transcribeRecording(blob)
      }
      recorderRef.current = rec
      rec.start()
      setRecording(true)
    } catch {
      stream.getTracks().forEach((track) => track.stop())
      if (recordingStreamRef.current === stream) recordingStreamRef.current = null
      if (voiceStartAttemptRef.current === attempt) toast.error(t('composer.voiceUnsupported'))
    }
  }

  // Send a recording for transcription. MediaRecorder's streaming WebM often has
  // no parseable duration, which makes some transcription upstreams 500 ("webm
  // duration parsing requires full EBML parser"). Re-encode to a 16 kHz mono WAV
  // (explicit duration, speech-model-native rate); fall back to the raw recording
  // if the browser can't decode it.
  async function transcribeRecording(blob: Blob) {
    try {
      let sendBlob = blob
      let filename = blob.type.includes('mp4') ? 'audio.mp4' : 'audio.webm'
      try {
        sendBlob = await encodeWavFromBlob(blob)
        filename = 'audio.wav'
      } catch {
        /* browser can't decode — send the original recording as-is */
      }
      const { text } = await audioApi.transcribe(sendBlob, filename)
      if (text) {
        const current = valueRef.current
        updateValue((current.trim() ? current.trimEnd() + ' ' : '') + text)
        requestAnimationFrame(() => ref.current?.focus('end'))
      }
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : t('composer.voiceFailed'))
    } finally {
      setTranscribing(false)
    }
  }
  const currentModel = useModels(
    (s) => s.models.find((m) => m.id === modelId) ?? s.imageModels.find((m) => m.id === modelId),
  )
  // §4.20 image mode: when the selected model draws, the composer shows a style
  // picker and hides chat-only controls (research / knowledge bases).
  const isImageMode = currentModel?.kind === 'image'
  const hasDraftImage = hasImageAttachment(attachments)
  const imagePromptRequired = isImageMode && hasDraftImage && value.trim().length === 0
  const effectivePlaceholder =
    placeholder ??
    (imagePromptRequired ? t('composer.imagePromptRequired') : t('composer.placeholder'))
  const [imageStyleId, setImageStyleId] = useState('')
  // Deep Research is both a per-group capability and a per-model exposure flag.
  // Admins bypass the group feature but still respect the current model's flag.
  const groupResearchEnabled = useAuth(
    (s) => s.user?.role === 'admin' || Boolean(s.user?.features?.includes('research')),
  )
  const builtinToolsAvailable = !isImageMode && modelHasBuiltinTools(currentModel)
  const configuredToolsAvailable = Boolean(
    !isImageMode &&
      (currentModel?.tools_available ??
        (builtinToolsAvailable || (Array.isArray(currentModel?.official_tools) && currentModel.official_tools.length > 0))),
  )
  const toolModeSelectionAllowed = Boolean(
    currentModel && configuredToolsAvailable && modelAllowsToolModeSelection(currentModel.tool_mode),
  )
  const supportsWebSearch = !isImageMode && modelSupportsBuiltinTool(currentModel, 'aivory_web_search')
  const toolModeCapabilities = useMemo<ToolModeCapabilities>(
    () =>
      resolveModelToolModeCapabilities(currentModel?.tool_mode, {
        available: configuredToolsAvailable,
      }),
    [configuredToolsAvailable, currentModel?.tool_mode],
  )
  const modelResearchEnabled = currentModel?.research_enabled ?? true
  const researchEnabled = groupResearchEnabled && modelResearchEnabled && supportsWebSearch
  // §verify: only offer the toggle when an admin has configured an auditor model.
  const verifyAvailable = useModels((s) => s.verifyAvailable)
  const paramControls = currentModel?.param_controls
  // §fast-mode: a fast turn disallows Verify / Deep Research / tool policy / the "+"
  // menu (the picker is a chat model, so isImageMode is already false). When fast
  // is active every other feature is forced off.
  const fastAvailable = useModels((s) => s.fastAvailable)
  const fastVision = useModels((s) => s.fastVision)
  const effectiveFast = Boolean(fast) && fastAvailable
  const imageAttachmentCapability = resolveImageAttachmentCapability(currentModel, {
    fast: effectiveFast,
    fastVision,
  })
  const canAttachImages = imageAttachmentCapability === 'allowed'
  // A fast turn resolves to the platform-selected model, while modelId stays on
  // the last advanced choice so switching back can restore it. Never surface or
  // submit that advanced model's parameter controls during the fast turn.
  const visibleParamControls = effectiveFast ? undefined : paramControls
  const effectiveMode = !effectiveFast && !isImageMode && researchEnabled ? mode : 'default'
  const effectiveVerify = !effectiveFast && verify && verifyAvailable && !isImageMode
  const availableToolMode = normalizeToolModeForCapabilities(toolMode, toolModeCapabilities)
  // Fast and image turns retain the prior fixed enabled behavior. Deep Research
  // also requires enabled mode and bypasses the automatic task classifier.
  const effectiveToolMode: ToolMode =
    effectiveFast || isImageMode || effectiveMode === 'deep-research'
      ? 'enabled'
      : availableToolMode
  const effectiveWebSearch = effectiveToolMode === 'disabled' && supportsWebSearch && forceWebSearch

  // A global mode can outlive a model switch. Concrete modes unavailable on the
  // new model always return to the product default instead of silently arming a
  // different tool family.
  useEffect(() => {
    if (currentModel && availableToolMode !== toolMode) setToolMode('auto')
  }, [availableToolMode, currentModel, setToolMode, toolMode])
  const handleParamValuesChange = useCallback(
    (next: Record<string, unknown>) => {
      setCachedParamValues(modelId, next)
    },
    [modelId, setCachedParamValues],
  )

  useEffect(() => {
    if (autoFocus) ref.current?.focus('end')
  }, [autoFocus])

  const openNewFormula = () => {
    formulaSelectionRef.current = ref.current?.captureSelection() ?? null
    setFormulaTarget(null)
    setFormulaOpen(true)
  }

  const openExistingFormula = (target: FormulaTarget) => {
    formulaSelectionRef.current = null
    setFormulaTarget(target)
    setFormulaOpen(true)
  }

  const handleFormulaOpenChange = (open: boolean) => {
    setFormulaOpen(open)
    if (!open) requestAnimationFrame(() => ref.current?.focus())
  }

  const uploading = useMemo(() => attachments.some((a) => a.uploading), [attachments])
  // A document attachment must be fully RAG-ready before it can be sent. Failed
  // docs block too; removing the chip is the explicit "send without it" action.
  const documentNotReady = useMemo(
    () => attachments.some((a) => a.documentId && a.ingest !== 'ready'),
    [attachments],
  )
  const hasUnsupportedImageAttachment = useMemo(
    () =>
      !canAttachImages &&
      attachments.some(
        (attachment) =>
          attachment.kind === 'image' ||
          isImageFileLike({
            name: attachment.name,
            // kind=image is handled by the preceding branch; an extension check
            // catches restored legacy rows whose kind metadata was incomplete.
            type: '',
          }),
      ),
    [attachments, canAttachImages],
  )
  const unsupportedImageNotifiedRef = useRef(false)
  useEffect(() => {
    if (!hasUnsupportedImageAttachment) {
      unsupportedImageNotifiedRef.current = false
      return
    }
    if (unsupportedImageNotifiedRef.current) return
    unsupportedImageNotifiedRef.current = true
    toast.warning(
      t('composer.imageUnsupported', {
        defaultValue: 'The current model does not support image input. Choose a vision-capable model.',
      }),
    )
  }, [hasUnsupportedImageAttachment, t])
  const voiceActive = recording || streamConnecting || transcribing || voiceStarting
  const canSubmit =
    hasSendableMessageContent(value, attachments, isImageMode) &&
    !voiceActive &&
    !streaming &&
    !uploading &&
    !restoringAttachments &&
    !documentNotReady &&
    !hasUnsupportedImageAttachment

  async function handleSubmit() {
    if (submittingRef.current) return
    const text = value.trim()
    if (voiceActive || streaming || uploading || restoringAttachments || documentNotReady) return
    if (!text && isImageMode && hasImageAttachment(attachments)) {
      toast.warning(t('composer.imagePromptRequired'))
      return
    }
    if (!hasSendableMessageContent(text, attachments, isImageMode)) return
    if (hasUnsupportedImageAttachment) {
      toast.error(
        t('composer.imageUnsupported', {
          defaultValue: 'The current model does not support image input. Choose a vision-capable model.',
        }),
      )
      return
    }
    if (text.length > MAX_LEN) {
      // Overflow fallback (multi-paste / dictation can pass the paste hook):
      // move the whole draft into a .txt attachment; the user adds a short
      // prompt and sends. The draft is only cleared once the upload actually
      // succeeded (quota/allowlist failures must not eat the text), and a ref
      // guard stops a second Enter from converting the same draft twice (§2.7).
      if (canAttachLongText) {
        if (convertingRef.current) return
        convertingRef.current = true
        const original = value
        void attachTextAsFile(text)
          .then((ok) => {
            if (ok && valueRef.current === original) updateValue('')
          })
          .finally(() => {
            convertingRef.current = false
          })
      } else {
        toast.warning(
          t('composer.tooLongTitle'),
          t('composer.tooLongBody', { max: MAX_LEN.toLocaleString() }),
        )
      }
      return
    }
    submittingRef.current = true
    try {
      // Uploads happen on attach now (so parsing starts immediately and the send is
      // gated until 'ready'); by here every attachment is already a real backend id.
      const params = effectiveFast ? {} : filterVisibleParams(visibleParamControls, paramValues)
      onSubmit(text, attachments, {
        mode: effectiveMode === 'default' ? undefined : effectiveMode,
        params: Object.keys(params).length > 0 ? params : undefined,
        imageStyleId: isImageMode && imageStyleId ? imageStyleId : undefined,
        verify: effectiveVerify ? true : undefined,
        toolMode: effectiveToolMode,
        webSearch: effectiveWebSearch ? true : undefined,
        selectedUserSkillIds: selectedUserSkillIdsForRequest(selectedSkills),
        selectedToolIds,
        fast: effectiveFast ? true : undefined,
      })
      updateValue('')
      // Stop any leftover pollers and revoke blob: URLs — uploadAttachment already
      // swapped its own. Persistent /api/files/… URLs stay so the bubble can render.
      pollTimers.current.forEach((tm) => clearTimeout(tm))
      pollTimers.current.clear()
      attachments.forEach((a) => {
        committedAttachmentIds.current.add(a.id)
        // Also record at module scope so the freshly-mounted thread composer
        // (first send from home) filters this id out of its draft-file restore.
        markFileCommitted(a.id)
        if (a.previewUrl && a.previewUrl.startsWith('blob:')) URL.revokeObjectURL(a.previewUrl)
      })
      setAttachments([])
      setSelectedSkills([])
    } finally {
      submittingRef.current = false
    }
  }

  // Upload one held file into the given conversation scope (rag=1 for doc-like
  // files), returning the chip updated with the server id, or null on failure.
  // A blank scopeId falls back to a scope-less upload (attachment only, no RAG).
  //
  // On success we swap the local blob URL for the persistent backend URL
  // (`/api/files/<id>`) BEFORE revoking the blob — otherwise the user-bubble
  // image preview later renders a dead URL once handleSubmit clears the draft.
  async function uploadAttachment(file: File, local: PendingAttachment, scopeId?: string): Promise<PendingAttachment | null> {
    try {
      const form = new FormData()
      form.append('file', file)
      // §4.11.2 session-scoped temp docs: ingest doc-like uploads (or anything
      // when a KB is bound) as conversation-scoped RAG so the user can ask over
      // what they just shared, without polluting any project KB.
      // Anything that isn't an image is treated as a readable document so the
      // model can use it (the backend reads unknown types as plain text and
      // routes spreadsheets to the sandbox). Images don't need RAG.
      const isDocLike = local.kind !== 'image'
      if (isDocLike && !scopeId) {
        throw new Error(t('composer.documentScopeRequired', { defaultValue: 'Create a conversation before uploading documents.' }))
      }
      const ragFlag = (kbIds && kbIds.length > 0) || isDocLike
      const query = new URLSearchParams()
      if (scopeId) {
        query.set('conversation_id', scopeId)
        query.set('draft', '1')
        if (ragFlag) query.set('rag', '1')
      }
      if (modelId) query.set('model_id', modelId)
      // Always send an explicit value so a concurrent mode switch cannot make
      // the upload inherit a different conversation-level Fast setting.
      query.set('fast', effectiveFast ? '1' : '0')
      const url = `/files?${query.toString()}`
      const res = await apiUpload<ApiAttachment & { id: string; url?: string; document_id?: string }>(url, form, {
        onProgress: (progress) => {
          if (typeof progress.percent !== 'number') return
          setAttachments((items) =>
            items.map((item) => (item.id === local.id ? { ...item, uploadProgress: progress.percent } : item)),
          )
        },
      })
      // Persistent URL replaces the blob URL. Fall back to /api/files/<id>
      // when the response omits `url` (older backends).
      const persistentUrl = res.url || `/api/files/${encodeURIComponent(res.id)}`
      // Revoke the blob URL ONLY now that we have a persistent replacement.
      if (local.previewUrl && local.previewUrl.startsWith('blob:')) {
        URL.revokeObjectURL(local.previewUrl)
      }
      const updated: PendingAttachment = {
        ...local,
        id: res.id,
        uploading: false,
        uploadProgress: 100,
        uploadScopeId: scopeId,
        previewUrl: persistentUrl,
        documentId: res.document_id,
        // A conversation doc was created → it's being parsed/embedded; track it
        // so the send stays blocked until it's searchable.
        ingest: res.document_id ? 'parsing' : undefined,
      }
      if (removedAttachmentIds.current.has(local.id)) {
        removedAttachmentIds.current.delete(local.id)
        setAttachments((items) => items.filter((item) => item.id !== local.id && item.id !== res.id))
        if (scopeId) {
          void conversationsApi.removeFile(scopeId, res.id).catch(() => {})
        }
        return null
      }
      // The conversation id becomes visible before this request finishes. A
      // simultaneous draft-restore query may therefore have already inserted
      // the server row; replace the local chip and collapse that duplicate.
      setAttachments((items) =>
        items
          .filter((item) => item.id !== res.id)
          .map((item) => (item.id === local.id ? updated : item)),
      )
      if (res.document_id && scopeId) {
        startIngestPoll(scopeId, res.document_id, res.id)
      }
      return updated
    } catch (e) {
      setAttachments((s) => {
        const next = s.filter((a) => a.id !== local.id)
        // Failure auto-removed the last chip → same draft-conversation cleanup
        // as a manual removal (gated on Fix7's confirmed-empty check). Microtask:
        // never call parent handlers from inside a state updater.
        if (next.length === 0 && s.length > 0) {
          queueMicrotask(() => maybeSignalDrained())
        }
        return next
      })
      // The user already removed this chip mid-upload — the failure (often the
      // now-discarded scope) is expected noise, not something to alarm about.
      if (removedAttachmentIds.current.has(local.id)) {
        removedAttachmentIds.current.delete(local.id)
        return null
      }
      if (e instanceof ApiError && e.status === 507) {
        // § user files page: group storage quota exhausted — link to /files.
        toastStorageQuotaFull(navigate)
      } else {
        toast.error(t('composer.uploadFailed', { defaultValue: 'Upload failed' }), e instanceof Error ? e.message : undefined)
      }
      return null
    }
  }

  const startIngestPoll = useCallback((scopeId: string, docId: string, attId: string) => {
    const previous = pollTimers.current.get(attId)
    if (previous) clearTimeout(previous)
    const tick = async () => {
      pollTimers.current.delete(attId)
      let done = false
      try {
        const docs = await conversationsApi.listDocs(scopeId)
        const doc = docs.find((dd) => dd.id === docId)
        if (doc) {
          if (doc.status === 'ready') {
            setAttachments((s) => s.map((a) => (a.id === attId ? { ...a, ingest: 'ready' } : a)))
            done = true
          } else if (doc.status === 'failed') {
            setAttachments((s) => s.map((a) => (a.id === attId ? { ...a, ingest: 'failed' } : a)))
            toast.error(t('composer.ingestFailed', { defaultValue: 'Could not read this file' }), doc.error || undefined)
            done = true
          } else {
            const ing: 'embedding' | 'parsing' = doc.status === 'embedding' ? 'embedding' : 'parsing'
            setAttachments((s) => s.map((a) => (a.id === attId ? { ...a, ingest: ing } : a)))
          }
        }
      } catch {
        /* transient network error — keep polling */
      }
      if (done) return
      pollTimers.current.set(attId, setTimeout(() => void tick(), INGEST_POLL_MS))
    }
    pollTimers.current.set(attId, setTimeout(() => void tick(), INGEST_POLL_MS))
  }, [t])

  // The backend is authoritative for unsent attachments. Rehydrate composer
  // drafts after refresh and resume status polling; committed historical files
  // are excluded by the endpoint and remain available in the files drawer only.
  useEffect(() => {
    const previousScope = attachmentScopeRef.current
    attachmentScopeRef.current = conversationId
    if (previousScope && previousScope !== conversationId) {
      pollTimers.current.forEach((tm) => clearTimeout(tm))
      pollTimers.current.clear()
      committedAttachmentIds.current.clear()
      setAttachments([])
    }
    // New (or absent) scope: the server file set is not yet known, so block the
    // drain signal until listDraftFiles confirms it below.
    draftFilesLoadedRef.current = false
    if (!conversationId) {
      setRestoringAttachments(false)
      return
    }

    let cancelled = false
    setRestoringAttachments(true)
    void conversationsApi
      .listDraftFiles(conversationId)
      .then((files) => {
        if (cancelled) return
        // Server draft set now known for this scope → the drain signal is armed.
        draftFilesLoadedRef.current = true
        const restored = files.map((file) => restoreConversationFile(file, conversationId))
        setAttachments((current) => {
          const present = new Set(current.map((item) => item.id))
          return [
            ...current,
            // Skip anything already present OR already sent — a stale restore
            // fetch must never resurrect a just-committed attachment (checked
            // both per-instance and at module scope so a first send from the home
            // screen doesn't bounce the image into the new thread's composer).
            ...restored.filter(
              (item) =>
                !present.has(item.id) &&
                !committedAttachmentIds.current.has(item.id) &&
                !committedFileIds.has(item.id),
            ),
          ]
        })
        for (const file of files) {
          if (
            file.document_id &&
            file.document_status !== 'ready' &&
            file.document_status !== 'failed'
          ) {
            const existing = pollTimers.current.get(file.id)
            if (existing) clearTimeout(existing)
            startIngestPoll(conversationId, file.document_id, file.id)
          }
        }
      })
      .catch((error) => {
        if (!cancelled) {
          toast.error(
            t('composer.restoreAttachmentsFailed', { defaultValue: 'Could not restore pending attachments' }),
            error instanceof Error ? error.message : undefined,
          )
        }
      })
      .finally(() => {
        if (!cancelled) setRestoringAttachments(false)
      })
    return () => {
      cancelled = true
    }
  }, [conversationId, startIngestPoll, t])

  async function retryAttachmentIngest(a: PendingAttachment) {
    if (!a.uploadScopeId || !a.documentId) return
    const tm = pollTimers.current.get(a.id)
    if (tm) {
      clearTimeout(tm)
      pollTimers.current.delete(a.id)
    }
    setAttachments((s) => s.map((x) => (x.id === a.id ? { ...x, ingest: 'parsing' } : x)))
    try {
      await conversationsApi.retryDoc(a.uploadScopeId, a.documentId)
      if (!attachmentsRef.current.some((x) => x.id === a.id)) return
      startIngestPoll(a.uploadScopeId, a.documentId, a.id)
    } catch (e) {
      if (!attachmentsRef.current.some((x) => x.id === a.id)) return
      setAttachments((s) => s.map((x) => (x.id === a.id ? { ...x, ingest: 'failed' } : x)))
      toast.error(
        t('composer.ingestRetryFailed', { defaultValue: 'Retry failed' }),
        e instanceof Error ? e.message : undefined,
      )
    }
  }

  // Returns how many files actually uploaded, so callers that transform user
  // input into an attachment (attachTextAsFile) can tell success from failure.
  async function handleAttach(files: FileList | null): Promise<number> {
    if (!files || !files.length) return 0
    const filtered = filterFilesForImageCapability(Array.from(files), imageAttachmentCapability)
    if (filtered.rejectedImages.length > 0) {
      toast.error(
        t('composer.imageUnsupported', {
          defaultValue: 'The current model does not support image input. Choose a vision-capable model.',
        }),
        filtered.rejectedImages.map((file) => file.name).join(', '),
      )
    }
    const all = filtered.accepted
    if (!all.length) return 0
    // A local preview is the acknowledgement for paste / picker / drop. Add it
    // before any network work so a cold upload-policy request cannot leave the
    // composer looking unchanged and tempt the user to attach the same file
    // repeatedly. Oversize candidates are removed again after policy validation.
    const candidates = all.map((file) => ({
      file,
      attachment: {
        id: uid('att'),
        name: file.name,
        size: file.size,
        kind: classifyAttachmentKind(file.name, file.type),
        previewUrl: isImageFileLike(file) ? URL.createObjectURL(file) : undefined,
        uploading: true,
        uploadProgress: 0,
      } satisfies PendingAttachment,
    }))
    setAttachments((current) => [...current, ...candidates.map(({ attachment }) => attachment)])

    // §4.6 reject oversize files BEFORE uploading — images and other files have
    // separate admin-set caps. Rejected images would otherwise upload fine but be
    // silently dropped at chat time (base64 inline cap); documents would fail the
    // server cap after a wasted upload.
    const limits = await getUploadLimits()
    const overImage = candidates.filter(
      ({ file }) => isImageFileLike(file) && file.size > limits.max_image_bytes,
    )
    const overFile = candidates.filter(
      ({ file }) => !isImageFileLike(file) && file.size > limits.max_file_bytes,
    )
    if (overImage.length) {
      toast.error(
        t('composer.imageTooLarge', {
          defaultValue: 'Images must be under {{mb}} MB',
          mb: Math.floor(limits.max_image_bytes / (1024 * 1024)),
        }),
        overImage.map(({ file }) => file.name).join(', '),
      )
    }
    if (overFile.length) {
      toast.error(
        t('composer.fileTooLarge', {
          defaultValue: 'Files must be under {{mb}} MB',
          mb: Math.floor(limits.max_file_bytes / (1024 * 1024)),
        }),
        overFile.map(({ file }) => file.name).join(', '),
      )
    }

    const rejectedIds = new Set([...overImage, ...overFile].map(({ attachment }) => attachment.id))
    if (rejectedIds.size > 0) {
      candidates.forEach(({ attachment }) => {
        if (rejectedIds.has(attachment.id) && attachment.previewUrl?.startsWith('blob:')) {
          URL.revokeObjectURL(attachment.previewUrl)
        }
      })
      setAttachments((current) => {
        const next = current.filter((attachment) => !rejectedIds.has(attachment.id))
        if (next.length === 0 && current.length > 0) queueMicrotask(() => maybeSignalDrained())
        return next
      })
    }

    // Removal can happen while upload-policy validation is in flight. Do not
    // resurrect or upload a candidate the user has already dismissed.
    const accepted = candidates.filter(({ attachment }) => {
      if (rejectedIds.has(attachment.id)) {
        removedAttachmentIds.current.delete(attachment.id)
        return false
      }
      if (!removedAttachmentIds.current.has(attachment.id)) return true
      removedAttachmentIds.current.delete(attachment.id)
      return false
    })
    if (!accepted.length) return 0
    toast.success(
      t(accepted.length === 1 ? 'composer.attachedSingle' : 'composer.attachedMultiple', { count: accepted.length }),
    )
    // Upload immediately so parsing/ingestion starts the moment the file is
    // attached (the user sees progress and can't send until it's ready). On the
    // home screen ensureConversationId lazily creates the scope WITHOUT navigating
    // away; without a scope we fall back to a plain (non-RAG) attachment upload.
    let scopeId = conversationId
    if (!scopeId && ensureConversationId) {
      try {
        scopeId = await ensureConversationId()
      } catch {
        scopeId = undefined
      }
    }
    const done = await Promise.all(
      accepted.map(({ file, attachment }) => uploadAttachment(file, attachment, scopeId)),
    )
    return done.filter(Boolean).length
  }
  // Keep the window-level drag listeners calling the current closure (it reads
  // conversationId / kbIds / ensureConversationId), without re-subscribing them.
  handleAttachRef.current = handleAttach

  // Codex-style long-text overflow (§4.11-B3): text past MAX_LEN becomes a .txt
  // attachment instead of being blocked. The backend line-gates it — small
  // files are injected in full, huge ones are embedded and retrieved — so the
  // model still sees the content either way. Document uploads need a
  // conversation scope, so this is only offered where one exists or can be
  // lazily created (all current composer mounts qualify).
  const canAttachLongText = Boolean(conversationId || ensureConversationId)
  async function attachTextAsFile(text: string): Promise<boolean> {
    const d = new Date()
    const pad = (n: number, w = 2) => String(n).padStart(w, '0')
    // Millisecond suffix so two conversions in the same second don't share a name.
    const name = `pasted-${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}${pad(d.getMilliseconds(), 3)}.txt`
    // Phrased as in-progress, not success — the upload can still fail (quota,
    // allowlist), in which case the error toast + draft restore tell the truth.
    toast.info(
      t('composer.longTextAttachedTitle', { defaultValue: 'Attaching long text as a file' }),
      t('composer.longTextAttachedBody', {
        defaultValue: 'Text over {{max}} characters is being uploaded as {{name}}.',
        max: MAX_LEN.toLocaleString(),
        name,
      }),
    )
    const dt = new DataTransfer()
    dt.items.add(new File([text], name, { type: 'text/plain' }))
    return (await handleAttach(dt.files)) > 0
  }

  function removeAttachment(id: string) {
    const tm = pollTimers.current.get(id)
    if (tm) {
      clearTimeout(tm)
      pollTimers.current.delete(id)
    }
    const target = attachments.find((a) => a.id === id)
    if (target?.uploading) {
      removedAttachmentIds.current.add(id)
    }
    setAttachments((s) => {
      if (target?.previewUrl && target.previewUrl.startsWith('blob:')) URL.revokeObjectURL(target.previewUrl)
      return s.filter((a) => a.id !== id)
    })
    if (target?.uploadScopeId && !target.uploading) {
      void conversationsApi.removeFile(target.uploadScopeId, target.id).catch(() => {
        // Removal is best-effort from the composer. The conversation-files drawer
        // still exposes retryable deletion for already-sent attachments.
      })
    }
    // Removed the last chip → let the page discard a draft conversation that
    // existed only to scope these uploads (the "Untitled ghost" fix), but only
    // once the server's draft file set is confirmed empty (Fix7 gate).
    if (!attachments.some((a) => a.id !== id)) {
      maybeSignalDrained()
    }
  }

  // Turn-feature list (composer "+" menu). Fast/image turns retain their existing
  // fixed behavior and hide it. Deep Research forces enabled; selecting it from
  // any other policy updates both values atomically in the preference store.
  const researchActive = effectiveMode === 'deep-research'
  const featureItems: FeatureItem[] = []
  const showTurnFeatures = !isImageMode && !effectiveFast
  const showToolUseSelector = showTurnFeatures && toolModeSelectionAllowed
  if (showTurnFeatures) {
    if (researchEnabled) {
      featureItems.push({
        key: 'deep-research',
        icon: <Telescope size={16} aria-hidden />,
        label: t('composer.research'),
        desc: t('composer.features.researchDesc', { defaultValue: 'Plan, search the web across rounds, and write a cited report.' }),
        active: researchActive,
        toggle: () => setMode(researchActive ? 'default' : 'deep-research'),
      })
    }
    if (verifyAvailable) {
      featureItems.push({
        key: 'verify',
        icon: <ShieldCheck size={16} aria-hidden />,
        label: t('composer.verify', { defaultValue: 'Verify' }),
        desc: t('composer.features.verifyDesc', { defaultValue: 'A second model fact-checks the answer after it is written.' }),
        active: verify,
        toggle: () => setVerify(!verify),
      })
    }
  }

  const toolModeLabel = t('composer.features.toolMode', { defaultValue: 'Tool use' })
  const hasCustomToolSelection = selectedToolIds !== undefined
  const toolSelectionSummary = hasCustomToolSelection
    ? t('composer.toolSelection.summaryCount', {
        count: selectedToolIds.length,
        defaultValue: '{{count}} selected',
      })
    : t('composer.toolSelection.summaryAll', { defaultValue: 'All' })

  const webSearchItem: FeatureItem | undefined =
    showToolUseSelector && supportsWebSearch && availableToolMode === 'disabled'
      ? {
          key: 'web-search',
          icon: <Globe size={16} aria-hidden />,
          label: t('composer.features.webSearch', { defaultValue: 'Web search' }),
          active: forceWebSearch,
          enter: true,
          toggle: () => setForceWebSearch(!forceWebSearch),
        }
      : undefined

  // Tool policy and tool selection are one composer concept. Keep a single
  // removable chip even when an older persisted policy is still non-default.
  const toolUseOverride: FeatureItem | undefined =
    showToolUseSelector &&
    (hasCustomToolSelection || (availableToolMode !== 'auto' && !researchActive))
      ? {
          key: 'tool-use',
          icon: <Wrench size={16} aria-hidden />,
          label: toolModeLabel,
          chipText: hasCustomToolSelection ? String(selectedToolIds.length) : undefined,
          desc: t('composer.features.toolModeDesc', {
            defaultValue: 'Configure the tools available for this turn.',
          }),
          active: true,
          clearLabel: t('composer.toolSelection.resetToolUse', { defaultValue: 'Reset tool use' }),
          toggle: () => {
            if (!researchActive && availableToolMode !== 'auto') setToolMode('auto')
            if (hasCustomToolSelection) handleSelectedToolIdsChange(undefined)
          },
        }
      : undefined

  const featureList = (onAfter?: () => void) => (
    <div className="flex flex-col gap-0.5">
      {showToolUseSelector ? (
        <ToolUseSelector
          label={toolModeLabel}
          description={t('composer.features.toolModeDesc', {
            defaultValue: 'Configure the tools available for this turn.',
          })}
          menuOpen={isMobile ? moreOpen : featuresOpen}
          rootItems={[...featureItems, ...(webSearchItem ? [webSearchItem] : [])]}
          toolSelection={{
            label: t('composer.toolSelection.entry', { defaultValue: 'Choose tools' }),
            description: t('composer.toolSelection.entryDescription', {
              defaultValue: 'Choose which tools are available for this turn.',
            }),
            summary: toolSelectionSummary,
            custom: hasCustomToolSelection,
            onOpen: () => {
              setMoreOpen(false)
              setFeaturesOpen(false)
              setToolSelectionOpen(true)
            },
          }}
          onPanelChange={setToolUsePanel}
          onAfter={onAfter}
        />
      ) : (
        featureItems.map((item) => <FeatureRow key={item.key} item={item} onAfter={onAfter} />)
      )}
    </div>
  )

  // Active-feature chips shown right before the model picker: icon + name + an
  // × to turn the feature off. Unavailable overrides never enter this list.
  const activeChips = [
    ...featureItems.filter((it) => it.active),
    ...(webSearchItem?.active ? [webSearchItem] : []),
    ...(toolUseOverride ? [toolUseOverride] : []),
  ]
  const anyFeatureActive = activeChips.length > 0
  const featureMenuAvailable = showToolUseSelector || featureItems.length > 0
  const hasActiveParamControl = useMemo(
    () =>
      parseControls(visibleParamControls).some((control) => {
        if (
          control.show_if &&
          Object.entries(control.show_if).some(([key, expected]) => paramValues[key] !== expected)
        ) {
          return false
        }
        if (control.type === 'toggle') {
          return Boolean(paramValues[control.key] ?? control.default ?? false)
        }
        const current = String(
          paramValues[control.key] ?? control.default ?? control.options?.[0]?.value ?? '',
        )
        return control.default !== undefined ? current !== String(control.default) : Boolean(current)
      }),
    [paramValues, visibleParamControls],
  )

  // The mobile "+" turns blue whenever a persistent choice inside it is active.
  // Hidden KB/tool policies must not leak into image or fast turns.
  const knowledgeBaseSelection = useMemo(
    () =>
      knowledgeBaseSelectionContext(
        kbList,
        kbIds ?? [],
        projectKBId,
        projectKBEmbeddingModelId !== undefined && projectKBEmbeddingDim !== undefined
          ? {
              embedding_model_id: projectKBEmbeddingModelId,
              embedding_dim: projectKBEmbeddingDim,
            }
          : undefined,
      ),
    [
      kbIds,
      kbList,
      projectKBEmbeddingDim,
      projectKBEmbeddingModelId,
      projectKBId,
    ],
  )
  const {
    options: selectableKnowledgeBases,
    anchors: selectedKnowledgeBases,
    selectedIds: selectedKnowledgeBaseIds,
  } = knowledgeBaseSelection
  const selectedKnowledgeBaseRows = useMemo(
    () =>
      selectedKnowledgeBaseIds
        .map((id) => kbList.find((knowledgeBase) => knowledgeBase.id === id))
        .filter((knowledgeBase): knowledgeBase is ApiKnowledgeBase => Boolean(knowledgeBase)),
    [kbList, selectedKnowledgeBaseIds],
  )
  const hasActiveTool =
    anyFeatureActive ||
    hasActiveParamControl ||
    (!isImageMode && Boolean(onKBChange) && selectedKnowledgeBaseIds.length > 0)

  const openKnowledgeBaseManager = () => {
    setMoreOpen(false)
    setKBPopoverOpen(false)
    navigate('/kb')
  }

  // KB checklist — shared by the desktop popover and the mobile "+" menu.
  // Keep selected rows actionable even if legacy data is incompatible so the
  // user can always recover by deselecting one of them.
  const kbChecklist =
    kbLoading || !kbLoaded ? (
      <div
        role="status"
        aria-live="polite"
        className="flex min-h-16 items-center gap-2 px-2 py-3 text-sm text-[var(--color-fg-muted)]"
      >
        <Loader2 size={15} aria-hidden className="shrink-0 animate-spin" />
        <span>{t('composer.knowledgeBasesLoading')}</span>
      </div>
    ) : kbLoadFailed ? (
      <div role="alert" className="px-2 py-2">
        <div className="flex items-start gap-2 text-sm leading-snug text-[var(--color-danger)]">
          <AlertTriangle size={15} aria-hidden className="mt-0.5 shrink-0" />
          <span>{t('composer.knowledgeBasesLoadFailed')}</span>
        </div>
        <button
          type="button"
          onClick={() => void loadKBList()}
          className="mt-2 inline-flex h-8 items-center gap-1.5 rounded-[8px] px-2 text-xs font-medium text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <RefreshCw size={13} aria-hidden />
          {t('composer.retryKnowledgeBases')}
        </button>
      </div>
    ) : selectableKnowledgeBases.length === 0 ? (
      <div className="px-2 py-2">
        <p className="text-sm text-[var(--color-fg-muted)]">{t('composer.noKnowledgeBases')}</p>
        <button
          type="button"
          onClick={openKnowledgeBaseManager}
          className="mt-2 inline-flex h-8 items-center gap-1.5 rounded-[8px] px-2 text-xs font-medium text-[var(--color-accent)] interactive hover:bg-[var(--color-accent-soft)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <Plus size={13} aria-hidden />
          {t('composer.manageKnowledgeBases')}
        </button>
      </div>
    ) : (
      <div className="max-h-64 overflow-y-auto overscroll-contain scrollbar-thin">
        {selectableKnowledgeBases.map((kb) => {
          const checked = selectedKnowledgeBaseIds.includes(kb.id)
          const incompatible =
            !checked &&
            selectedKnowledgeBases.some(
              (selected) => !knowledgeBasesHaveCompatibleEmbeddings(selected, kb),
            )
          const incompatibilityReason = t('composer.incompatibleKnowledgeBase')
          return (
            <button
              key={kb.id}
              type="button"
              role="checkbox"
              aria-checked={checked}
              aria-label={incompatible ? `${kb.name}. ${incompatibilityReason}` : kb.name}
              disabled={incompatible}
              onClick={() =>
                onKBChange?.(
                  checked
                    ? selectedKnowledgeBaseIds.filter((id) => id !== kb.id)
                    : [...selectedKnowledgeBaseIds, kb.id],
                )
              }
              className={cn(
                'flex w-full items-start gap-2 rounded-[8px] px-2 py-1.5 text-left text-sm interactive',
                incompatible
                  ? 'cursor-not-allowed text-[var(--color-fg-subtle)] opacity-60'
                  : 'text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
              )}
            >
              <span
                className={cn(
                  'mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded border',
                  checked
                    ? 'border-[var(--color-tool-selection)] bg-[var(--color-tool-selection)] text-[var(--color-tool-selection-fg)]'
                    : 'border-[var(--color-border-strong)]',
                )}
              >
                {checked ? <Check size={11} aria-hidden /> : null}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate">{kb.name}</span>
                {incompatible ? (
                  <span className="mt-0.5 block text-[11px] leading-snug text-[var(--color-fg-subtle)]">
                    {incompatibilityReason}
                  </span>
                ) : null}
              </span>
            </button>
          )
        })}
      </div>
    )

  const commandKey = commandQuery
    ? `${commandQuery.trigger}:${commandQuery.from}:${commandQuery.to}:${commandQuery.query}`
    : ''
  const commandOpen = Boolean(
    commandQuery &&
      commandKey !== dismissedCommandKey &&
      (commandQuery.trigger === '/' || (!isImageMode && Boolean(onKBChange))),
  )
  const commandItems = useMemo<ComposerCommandItem[]>(() => {
    if (!commandQuery) return []
    const query = commandQuery.query.trim().toLocaleLowerCase()
    const matches = (name: string, description: string) =>
      !query || name.toLocaleLowerCase().includes(query) || description.toLocaleLowerCase().includes(query)
    if (commandQuery.trigger === '@') {
      return selectableKnowledgeBases
        .filter((knowledgeBase) => !selectedKnowledgeBaseIds.includes(knowledgeBase.id))
        .filter((knowledgeBase) => matches(knowledgeBase.name, knowledgeBase.description))
        .map((knowledgeBase) => {
          const disabled = selectedKnowledgeBases.some(
            (selected) => !knowledgeBasesHaveCompatibleEmbeddings(selected, knowledgeBase),
          )
          return {
            kind: 'knowledge-base' as const,
            id: knowledgeBase.id,
            name: knowledgeBase.name,
            description: disabled
              ? t('composer.incompatibleKnowledgeBase')
              : knowledgeBase.description,
            knowledgeBase,
            disabled,
          }
        })
        .sort((left, right) => Number(left.disabled) - Number(right.disabled))
        .slice(0, 10)
    }
    const skillItems: ComposerCommandItem[] = librarySkills
      .filter((skill) => !selectedSkills.some((selected) => selected.id === skill.id))
      .filter((skill) => matches(skill.name, skillDisplayDescription(skill)))
      .map((skill) => ({
        kind: 'skill' as const,
        id: skill.id,
        name: skill.name,
        description: skillDisplayDescription(skill),
        skill,
      }))
    const promptItems: ComposerCommandItem[] = libraryPrompts
      .filter((prompt) => matches(prompt.name, prompt.description))
      .map((prompt) => ({
        kind: 'prompt' as const,
        id: prompt.id,
        name: prompt.name,
        description: prompt.description,
        prompt,
      }))
    const limit = 10
    if (skillItems.length > 0 && promptItems.length > 0) {
      const perKind = Math.floor(limit / 2)
      return [...skillItems.slice(0, perKind), ...promptItems.slice(0, perKind)]
    }
    return [...skillItems, ...promptItems].slice(0, limit)
  }, [
    commandQuery,
    libraryPrompts,
    librarySkills,
    selectableKnowledgeBases,
    selectedKnowledgeBaseIds,
    selectedKnowledgeBases,
    selectedSkills,
    t,
  ])

  const enabledCommandIndices = useMemo(
    () =>
      commandItems
        .map((item, index) => ({ item, index }))
        .filter(({ item }) => item.kind !== 'knowledge-base' || !item.disabled)
        .map(({ index }) => index),
    [commandItems],
  )

  useEffect(() => {
    setCommandIndex(enabledCommandIndices[0] ?? -1)
  }, [commandKey, enabledCommandIndices])

  useEffect(() => {
    const shouldLoadForMention = commandQuery?.trigger === '@' && !isImageMode && Boolean(onKBChange)
    const shouldLoadSelectedNames = !isImageMode && Boolean(onKBChange) && selectedKnowledgeBaseIds.length > 0
    if ((shouldLoadForMention || shouldLoadSelectedNames) && !kbLoaded && !kbLoading) {
      void loadKBList()
    }
  }, [
    commandQuery?.trigger,
    isImageMode,
    kbLoaded,
    kbLoading,
    loadKBList,
    onKBChange,
    selectedKnowledgeBaseIds.length,
  ])

  useEffect(() => {
    if (!commandOpen) {
      setCommandPosition(null)
      return
    }
    const opensDownward = pathname === '/' || pathname === '/chat'
    const update = () => {
      const root = composerRootRef.current
      if (!root) return
      const rect = root.getBoundingClientRect()
      const gutter = 12
      const gap = 8
      const viewportWidth = document.documentElement.clientWidth || window.innerWidth
      const viewportHeight = document.documentElement.clientHeight || window.innerHeight
      const width = Math.min(rect.width, Math.max(0, viewportWidth - gutter * 2))
      const maxLeft = Math.max(gutter, viewportWidth - width - gutter)
      const left = Math.min(Math.max(rect.left, gutter), maxLeft)
      if (opensDownward) {
        const top = rect.bottom + gap
        setCommandPosition({
          left,
          top,
          width,
          maxHeight: Math.min(280, Math.max(0, viewportHeight - top - gutter)),
          placement: 'down',
        })
        return
      }
      setCommandPosition({
        left,
        bottom: Math.max(gutter, viewportHeight - rect.top + gap),
        width,
        maxHeight: Math.min(280, Math.max(0, rect.top - gutter - gap)),
        placement: 'up',
      })
    }
    update()
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    return () => {
      window.removeEventListener('resize', update)
      window.removeEventListener('scroll', update, true)
    }
  }, [commandOpen, pathname])

  useEffect(() => {
    const active = commandMenuRef.current?.querySelector<HTMLElement>(`[data-command-index="${commandIndex}"]`)
    active?.scrollIntoView({ block: 'nearest' })
  }, [commandIndex])

  function chooseCommand(item: ComposerCommandItem) {
    const query = commandQuery
    if (!query) return
    if (item.kind === 'knowledge-base' && item.disabled) return
    if (item.kind === 'skill') {
      ref.current?.replaceRange(query.from, query.to, '')
      setSelectedSkills((current) => addSelectedUserSkill(current, item.skill))
      setDismissedCommandKey('')
    } else if (item.kind === 'prompt') {
      ref.current?.replaceRange(query.from, query.to, item.prompt.content)
      setDismissedCommandKey(commandKey)
    } else {
      ref.current?.replaceRange(query.from, query.to, '')
      onKBChange?.([...selectedKnowledgeBaseIds, item.knowledgeBase.id])
      setDismissedCommandKey('')
    }
    setCommandQuery(null)
  }

  function handleCommandKeyDown(event: KeyboardEvent): boolean {
    if (!commandOpen) return false
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setCommandIndex((current) => {
        if (enabledCommandIndices.length === 0) return 0
        const position = enabledCommandIndices.indexOf(current)
        return enabledCommandIndices[(position + 1 + enabledCommandIndices.length) % enabledCommandIndices.length]
      })
      return true
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      setCommandIndex((current) => {
        if (enabledCommandIndices.length === 0) return 0
        const position = enabledCommandIndices.indexOf(current)
        const previous = position < 0 ? enabledCommandIndices.length - 1 : position - 1
        return enabledCommandIndices[(previous + enabledCommandIndices.length) % enabledCommandIndices.length]
      })
      return true
    }
    if (event.key === 'Enter' || event.key === 'Tab') {
      const item = commandItems[commandIndex]
      if (!item) {
        if (event.key === 'Enter') {
          event.preventDefault()
          return true
        }
        setDismissedCommandKey(commandKey)
        return false
      }
      event.preventDefault()
      chooseCommand(item)
      return true
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      setDismissedCommandKey(commandKey)
      return true
    }
    return false
  }

  // One primary action owns the right edge, matching the familiar ChatGPT
  // composer pattern: stop while generating, voice while the draft is empty,
  // and send once text or a chat image exists. Keep voice in control until live/recorded speech
  // fully settles; a streaming transcript can populate `value` before the mic
  // has actually stopped, and must not strand the user without a stop action.
  const hasDraftText = value.trim().length > 0
  const showVoiceAction = voiceActive || (!hasDraftText && !hasDraftImage)
  const voiceStatusLabel = transcribing
    ? t('composer.voiceTranscribing')
    : recording
      ? t('composer.voiceStop')
      : streamConnecting || voiceStarting
        ? t('composer.voiceConnecting')
        : voiceEnabled
          ? t('composer.voice')
          : t('composer.voiceUnavailable')
  // Keep a 44px touch target on phones while reducing the visible circle to
  // 32px. The hit area preserves mobile ergonomics without making the action
  // visually dominate the composer.
  const primaryActionHitArea = cn(
    'inline-flex shrink-0 items-center justify-center size-9 max-sm:size-11 rounded-full interactive',
    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
  )
  const primaryActionSurface = 'inline-flex size-8 items-center justify-center rounded-full'

  const primaryAction = streaming ? (
    <Tooltip content={t('composer.stop')}>
      <button
        type="button"
        onClick={onStop}
        aria-label={t('composer.stop')}
        data-composer-action="stop"
        className={cn(primaryActionHitArea, 'hover:opacity-90')}
      >
        <span className={cn(primaryActionSurface, 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]')}>
          <StopCircle size={15} aria-hidden />
        </span>
      </button>
    </Tooltip>
  ) : showVoiceAction ? (
    <Tooltip content={voiceStatusLabel}>
      <button
        type="button"
        onClick={() => void toggleVoice()}
        disabled={transcribing || !voiceEnabled}
        aria-label={
          transcribing
            ? voiceStatusLabel
            : recording || streamConnecting || voiceStarting
              ? t('composer.voiceStop')
              : voiceStatusLabel
        }
        aria-pressed={recording || streamConnecting || voiceStarting}
        aria-busy={transcribing || streamConnecting || voiceStarting || undefined}
        data-composer-action={transcribing ? 'transcribing' : recording ? 'voice-recording' : streamConnecting ? 'voice-connecting' : voiceStarting ? 'voice-starting' : voiceEnabled ? 'voice' : 'voice-disabled'}
        className={cn(
          primaryActionHitArea,
          voiceEnabled && !recording && !streamConnecting && !voiceStarting && 'hover:opacity-90',
          !voiceEnabled && 'cursor-not-allowed',
          transcribing && 'cursor-wait opacity-60',
        )}
      >
        <span
          className={cn(
            primaryActionSurface,
            recording
              ? 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]'
              : streamConnecting || voiceStarting
                ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
                : voiceEnabled
                  ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]'
                  : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-faint)]',
          )}
        >
          {transcribing ? (
            <Loader2 size={15} className="animate-spin" aria-hidden />
          ) : (
            <AudioWaveform
              size={16}
              strokeWidth={2.1}
              className={cn((recording || streamConnecting || voiceStarting) && 'animate-[streaming-pulse_1600ms_ease-in-out_infinite]')}
              aria-hidden
            />
          )}
        </span>
      </button>
    </Tooltip>
  ) : (
    <Tooltip content={imagePromptRequired ? t('composer.imagePromptRequired') : t('composer.send', { kbd: modKey() })}>
      <button
        type="button"
        onClick={handleSubmit}
        disabled={!canSubmit}
        aria-label={t('actions.send', { ns: 'common' })}
        data-composer-action="send"
        className={cn(
          primaryActionHitArea,
          canSubmit ? 'hover:opacity-90' : 'cursor-not-allowed',
        )}
      >
        <span
          className={cn(
            primaryActionSurface,
            canSubmit
              ? 'bg-[var(--color-fg)] text-[var(--color-fg-inverted)]'
              : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-faint)]',
          )}
        >
          {uploading ? <Loader2 size={15} className="animate-spin" aria-hidden /> : <ArrowUp size={15} aria-hidden />}
        </span>
      </button>
    </Tooltip>
  )

  return (
    <div
      ref={composerRootRef}
      className={cn(
        'group/composer relative min-w-0 w-full max-w-full',
        'rounded-popup border-0 bg-[var(--color-surface)]',
        'shadow-[var(--shadow-sm)]',
        dragOver && 'ring-2 ring-[var(--color-tool-selection)] shadow-[var(--shadow-md)]',
      )}
    >
      {/* Full-screen drag-and-drop overlay — shown while a file is dragged
          anywhere over the window. Portalled to <body> so it stays viewport-fixed
          even when an ancestor (e.g. ChatHome's GSAP-animated wrapper) carries an
          inline transform that would otherwise become the fixed containing block.
          pointer-events-none: the window listeners own the drop, and the overlay
          must never intercept it. */}
      {dragOver &&
        createPortal(
          <div className="pointer-events-none fixed inset-0 z-[var(--z-max)] grid place-items-center bg-[var(--color-overlay)] p-6 backdrop-blur-[2px] animate-[fade-in_var(--duration-base)_var(--ease-out)]">
            <div className="flex flex-col items-center gap-3 rounded-popup border-2 border-dashed border-[var(--color-accent)] bg-[var(--color-surface)]/95 px-8 py-7 shadow-[var(--shadow-lg)]">
              <span className="grid size-12 place-items-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
                <ImageIcon size={22} aria-hidden />
              </span>
              <span className="text-[15px] font-medium text-[var(--color-fg)]">
                {t('composer.dropAnywhere', { defaultValue: 'Drop anywhere to attach' })}
              </span>
            </div>
          </div>,
          document.body,
        )}

      {commandOpen && commandPosition
        ? createPortal(
            <div
              ref={commandMenuRef}
              role="listbox"
              aria-label={
                commandQuery?.trigger === '@'
                  ? t('composer.knowledgeBaseMentionLabel')
                  : t('library:title')
              }
              data-command-placement={commandPosition.placement}
              className={cn(
                'fixed z-[var(--z-popover)] overflow-y-auto overscroll-contain rounded-popup bg-[var(--color-surface-raised)] p-1 shadow-[var(--shadow-md)] scrollbar-thin',
                commandPosition.placement === 'down'
                  ? 'animate-[slide-down_160ms_var(--ease-out)]'
                  : 'animate-[slide-up_160ms_var(--ease-out)]',
              )}
              style={{
                left: commandPosition.left,
                top: commandPosition.top,
                bottom: commandPosition.bottom,
                width: commandPosition.width,
                maxHeight: commandPosition.maxHeight,
              }}
            >
              {commandQuery?.trigger === '@' && (kbLoading || !kbLoaded) ? (
                <div
                  role="status"
                  aria-live="polite"
                  className="flex items-center gap-2 px-2.5 py-2 text-[13px] text-[var(--color-fg-muted)]"
                >
                  <Loader2 size={14} className="animate-spin" aria-hidden />
                  {t('composer.knowledgeBasesLoading')}
                </div>
              ) : commandQuery?.trigger === '@' && kbLoadFailed ? (
                <div role="alert" className="px-2.5 py-2">
                  <div className="flex items-start gap-2 text-[13px] leading-snug text-[var(--color-danger)]">
                    <AlertTriangle size={14} aria-hidden className="mt-0.5 shrink-0" />
                    <span>{t('composer.knowledgeBasesLoadFailed')}</span>
                  </div>
                  <button
                    type="button"
                    onMouseDown={(event) => event.preventDefault()}
                    onClick={() => void loadKBList()}
                    className="mt-2 inline-flex h-8 items-center gap-1.5 rounded-[8px] px-2 text-xs font-medium text-[var(--color-fg-muted)] interactive hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <RefreshCw size={13} aria-hidden />
                    {t('composer.retryKnowledgeBases')}
                  </button>
                </div>
              ) : commandQuery?.trigger === '/' && libraryLoading ? (
                <div className="flex items-center gap-2 px-2.5 py-2 text-[13px] text-[var(--color-fg-muted)]">
                  <Loader2 size={14} className="animate-spin" aria-hidden />
                  {t('library:command.loading')}
                </div>
              ) : commandItems.length === 0 ? (
                <p className="px-2.5 py-2 text-[13px] text-[var(--color-fg-muted)]">
                  {commandQuery?.trigger === '@'
                    ? t('composer.knowledgeBaseMentionEmpty')
                    : t('library:command.empty')}
                </p>
              ) : (
                (commandQuery?.trigger === '@'
                  ? (['knowledge-base'] as const)
                  : (['skill', 'prompt'] as const)
                ).map((kind) => {
                  const items = commandItems
                    .map((item, index) => ({ item, index }))
                    .filter(({ item }) => item.kind === kind)
                  if (items.length === 0) return null
                  const labelId = `composer-command-${kind}`
                  return (
                    <div key={kind} role="group" aria-labelledby={labelId} className="mt-0.5 first:mt-0">
                      <p
                        id={labelId}
                        className="px-2.5 pb-0.5 pt-1 text-[10.5px] font-medium tracking-normal text-[var(--color-fg-subtle)]"
                      >
                        {kind === 'knowledge-base'
                          ? t('composer.knowledgeBaseMentionLabel')
                          : t(`library:command.${kind === 'skill' ? 'skills' : 'prompts'}`)}
                      </p>
                      {items.map(({ item, index }) => {
                        const active = index === commandIndex
                        return (
                          <button
                            key={`${item.kind}:${item.id}`}
                            type="button"
                            role="option"
                            aria-selected={active}
                            aria-disabled={item.kind === 'knowledge-base' ? item.disabled : undefined}
                            disabled={item.kind === 'knowledge-base' && item.disabled}
                            data-command-index={index}
                            onMouseDown={(event) => event.preventDefault()}
                            onMouseEnter={() => {
                              if (item.kind !== 'knowledge-base' || !item.disabled) {
                                setCommandIndex(index)
                              }
                            }}
                            onClick={() => chooseCommand(item)}
                            className={cn(
                              'flex min-h-9 w-full min-w-0 items-center gap-2 rounded-[10px] px-2.5 py-1.5 text-left interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:min-h-10',
                              item.kind === 'knowledge-base' && item.disabled
                                ? 'cursor-not-allowed opacity-55'
                                : active
                                  ? 'bg-[var(--color-bg-muted)]'
                                  : 'hover:bg-[var(--color-bg-muted)]',
                            )}
                          >
                            <span
                              className={cn(
                                'inline-flex size-5 shrink-0 items-center justify-center',
                                item.kind === 'skill'
                                  ? 'text-[var(--color-accent)]'
                                  : item.kind === 'knowledge-base'
                                    ? 'text-[var(--color-tool-selection-text)]'
                                  : 'text-[var(--color-secondary)]',
                              )}
                              data-command-icon
                            >
                              {item.kind === 'skill' ? (
                                <SkillIcon name={item.skill.icon} size={16} aria-hidden />
                              ) : item.kind === 'knowledge-base' ? (
                                <BookOpen size={16} aria-hidden />
                              ) : (
                                <FileText size={16} aria-hidden />
                              )}
                            </span>
                            <span className="flex min-w-0 flex-1 items-baseline gap-2 overflow-hidden">
                              <span
                                className="max-w-[55%] shrink-0 truncate text-[13px] font-medium text-[var(--color-fg)]"
                                data-command-name
                              >
                                {item.name}
                              </span>
                              {item.description ? (
                                <span
                                  className="min-w-0 flex-1 truncate text-[11.5px] text-[var(--color-fg-subtle)]"
                                  data-command-description
                                >
                                  {item.description}
                                </span>
                              ) : null}
                            </span>
                            {active ? <Check size={14} className="shrink-0 text-[var(--color-accent)]" aria-hidden /> : null}
                          </button>
                        )
                      })}
                    </div>
                  )
                })
              )}
            </div>,
            document.body,
          )
        : null}

      {/* Attachments preview. The armed-mode (research) state is shown by the
          toolbar button below, so we don't repeat a chip above the input.
          ≤4 chips share the row (they may shrink); >4 stop shrinking — the rail
          scrolls horizontally (wheel deltas translated via chipsRailRef) and
          chip width is sized to ~4.5 per row so a half chip peeks at the edge
          as the scroll affordance. */}
      {attachments.length > 0 && (
        <div ref={chipsRailRef} className="flex items-stretch gap-1.5 overflow-x-auto px-3 pb-1 pt-2.5 scrollbar-none">
          {attachments.map((a) => {
            const manyAttachments = attachments.length > 4
            const busy = a.uploading || a.ingest === 'parsing' || a.ingest === 'embedding'
            const failed = a.ingest === 'failed'
            const uploadPercent = Math.max(0, Math.min(100, Math.round(a.uploadProgress ?? 0)))
            // Browser progress hits 100% when the bytes are handed to the socket,
            // but the request isn't done until the server has received + written
            // the file (and any reverse proxy has finished buffering it). Show a
            // neutral "processing" state so a parked 100% doesn't read as frozen.
            const serverProcessing = a.uploading && uploadPercent >= 100
            const status =
              serverProcessing
                ? t('composer.processing', { defaultValue: 'Processing…' })
                : a.uploading
                ? t('composer.uploadingPercent', { defaultValue: 'Uploading {{percent}}%', percent: uploadPercent })
                : a.ingest === 'embedding'
                ? t('composer.indexing')
                : a.ingest === 'parsing'
                  ? t('composer.parsing')
                  : attachmentKindLabel(a)

            if (a.kind === 'image' && a.previewUrl) {
              return (
                <span key={a.id} className="group/att relative inline-block shrink-0">
                  <img
                    src={a.previewUrl}
                    alt={a.name}
                    className="size-14 rounded-[10px] border border-[var(--color-border-subtle)] bg-[var(--color-bg-muted)] object-cover"
                  />
                  {busy ? (
                    <span className="absolute inset-0 grid place-items-center rounded-[10px] bg-[var(--color-overlay)]">
                      {a.uploading && !serverProcessing ? (
                        <ProgressRing
                          value={uploadPercent}
                          size={34}
                          strokeWidth={3}
                          showValue
                          label={status}
                          className="text-[var(--color-fg-inverted)]"
                        />
                      ) : (
                        <Loader2 size={13} className="animate-spin text-[var(--color-fg-inverted)]" aria-hidden />
                      )}
                    </span>
                  ) : null}
                  <button
                    type="button"
                    aria-label={`Remove ${a.name}`}
                    onClick={() => removeAttachment(a.id)}
                    className="absolute -right-1.5 -top-1.5 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-sm)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <X size={13} aria-hidden />
                  </button>
                </span>
              )
            }

            const Icon = fileIconFor(a.name, a.kind)
            return (
              <span
                key={a.id}
                className={cn(
                  'group/att relative flex h-14 items-center gap-2.5 rounded-[10px] border bg-[var(--color-surface-raised)] py-2 pl-2.5 pr-8 shadow-[var(--shadow-xs)]',
                  manyAttachments
                    ? // >4 chips: stop shrinking. Fixed width targets ~4.5 chips per
                      // row (half chip visible = "there's more" affordance), floored
                      // so names stay readable on narrow rails.
                      'w-[clamp(10rem,calc((100%_-_1.5rem)/4.5),15rem)] flex-none'
                    : 'min-w-0 max-w-[min(28rem,calc(100vw-6rem))] flex-[1_1_15rem]',
                  failed ? 'border-[var(--color-danger)]/50' : 'border-[var(--color-border)]',
                )}
              >
                <span
                  className={cn(
                    'grid size-9 shrink-0 place-items-center rounded-[9px]',
                    failed
                      ? 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]'
                      : attachmentTileClass(a),
                  )}
                  aria-hidden
                >
                  {busy ? (
                    a.uploading && !serverProcessing ? (
                      <ProgressRing value={uploadPercent} size={30} strokeWidth={3} showValue label={status} />
                    ) : (
                      <Loader2 size={17} className="animate-spin" />
                    )
                  ) : failed ? (
                    <AlertTriangle size={17} />
                  ) : (
                    <Icon size={18} strokeWidth={2} />
                  )}
                </span>
                <span className="grid min-w-0 flex-1 gap-0.5 text-left">
                  <span className="truncate text-[0.8125rem] font-semibold leading-tight text-[var(--color-fg)]">
                    {a.name}
                  </span>
                  <span
                    className={cn(
                      'min-w-0 text-[0.75rem] leading-tight',
                      !failed && 'truncate',
                      failed
                        ? 'text-[var(--color-danger)]'
                        : busy
                          ? 'text-[var(--color-fg-muted)]'
                          : 'text-[var(--color-fg-subtle)]',
                    )}
                  >
                    {failed ? (
                      <span className="flex min-w-0 items-center gap-1">
                        <span className="truncate">
                          {t('composer.ingestFailedAction', { defaultValue: 'Parsing failed. Remove it or' })}
                        </span>
                        <button
                          type="button"
                          onClick={(e) => {
                            e.stopPropagation()
                            void retryAttachmentIngest(a)
                          }}
                          className="shrink-0 font-semibold underline underline-offset-2 hover:text-[var(--color-danger)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                        >
                          {t('composer.retry', { defaultValue: 'Retry' })}
                        </button>
                      </span>
                    ) : (
                      status
                    )}
                  </span>
                </span>
                <button
                  type="button"
                  aria-label={`Remove ${a.name}`}
                  onClick={() => removeAttachment(a.id)}
                  className="absolute right-1.5 top-1.5 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-xs)] interactive hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <X size={13} aria-hidden />
                </button>
              </span>
            )
          })}
        </div>
      )}

      {selectedSkills.length > 0 || selectedKnowledgeBaseRows.length > 0 ? (
        <div className="flex items-center gap-1.5 overflow-x-auto px-3 pb-1 pt-2.5 scrollbar-none">
          {selectedSkills.map((skill) => (
            <span
              key={`skill:${skill.id}`}
              className="inline-flex h-7 max-w-[15rem] shrink-0 items-center gap-1.5 px-0.5 text-[12px] font-medium text-[var(--color-accent)]"
            >
              <SkillIcon name={skill.icon} size={13} className="shrink-0" aria-hidden />
              <span className="truncate">{skill.name}</span>
              <button
                type="button"
                onClick={() => setSelectedSkills((current) => current.filter((item) => item.id !== skill.id))}
                aria-label={t('library:command.removeSkill', { name: skill.name })}
                className="inline-flex size-6 shrink-0 items-center justify-center rounded-full text-[var(--color-fg-faint)] interactive hover:text-[var(--color-fg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-8"
              >
                <X size={12} aria-hidden />
              </button>
            </span>
          ))}
          {selectedKnowledgeBaseRows.map((knowledgeBase) => (
            <span
              key={`knowledge-base:${knowledgeBase.id}`}
              className="inline-flex h-7 max-w-[15rem] shrink-0 items-center gap-1.5 px-0.5 text-[12px] font-medium text-[var(--color-tool-selection-text)]"
            >
              <BookOpen size={13} className="shrink-0" aria-hidden />
              <span className="truncate">{knowledgeBase.name}</span>
              <button
                type="button"
                onClick={() =>
                  onKBChange?.(
                    selectedKnowledgeBaseIds.filter((id) => id !== knowledgeBase.id),
                  )
                }
                aria-label={t('composer.removeKnowledgeBase', { name: knowledgeBase.name })}
                className="inline-flex size-6 shrink-0 items-center justify-center rounded-full text-[var(--color-fg-faint)] interactive hover:text-[var(--color-fg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] max-sm:size-8"
              >
                <X size={12} aria-hidden />
              </button>
            </span>
          ))}
        </div>
      ) : null}

      {/* Plain text and atomic KaTeX nodes share one ProseMirror surface. The
          canonical value remains a string so drafts/API/provider behavior is
          unchanged, while users never have to edit raw LaTeX in the composer. */}
      <RichComposerEditor
        ref={ref}
        value={value}
        onChange={updateValue}
        onSubmit={handleSubmit}
        onFormulaClick={openExistingFormula}
        onCommandQueryChange={setCommandQuery}
        onCommandKeyDown={handleCommandKeyDown}
        onPasteFiles={(files) => {
          const dt = new DataTransfer()
          files.forEach((file) => dt.items.add(file))
          void handleAttach(dt.files)
        }}
        onLongPaste={(pasted) => {
          void attachTextAsFile(pasted).then((ok) => {
            // Upload failed (quota / allowlist) — put the text back into the
            // draft so nothing is lost; the error toast explains what happened.
            if (!ok) {
              const current = valueRef.current
              updateValue(current ? `${current}\n${pasted}` : pasted)
            }
          })
        }}
        canAttachLongText={canAttachLongText}
        maxLength={MAX_LEN}
        placeholder={effectivePlaceholder}
        ariaLabel={t('composer.inputLabel', { defaultValue: 'Type a message' })}
        formulaEditLabel={t('composer.formula.editTitle')}
        compact={compact}
        mobile={isMobile}
      />

      {/* Toolbar row. The file input is shared by both layouts. On phones every
          secondary action collapses into a single "+" menu (Gemini/ChatGPT-mobile
          pattern); on ≥sm screens they sit inline in a scrollable left zone. */}
      <input
        type="file"
        ref={fileRef}
        hidden
        multiple
        accept={canAttachImages ? undefined : NON_IMAGE_ATTACHMENT_ACCEPT}
        onChange={(e) => {
          void handleAttach(e.currentTarget.files)
          e.currentTarget.value = ''
        }}
      />
      <input
        type="file"
        ref={imageFileRef}
        hidden
        multiple
        accept="image/*,.heic,.heif,.avif"
        onChange={(e) => {
          void handleAttach(e.currentTarget.files)
          e.currentTarget.value = ''
        }}
      />

      {isMobile ? (
        /* ── Mobile: fixed [+] + scroll-safe context + fixed primary action ── */
        <div className="flex min-w-0 items-center gap-1 px-2 pb-2.5 pt-1">
          <Popover
            open={moreOpen}
            onOpenChange={(o) => {
              setMoreOpen(o)
              if (!o) setToolUsePanel('root')
              if (o && onKBChange) void loadKBList()
            }}
          >
            <PopoverTrigger asChild>
              <button
                type="button"
                aria-label={t('composer.more', { defaultValue: 'More' })}
                className={cn(
                  'relative inline-flex size-11 shrink-0 items-center justify-center rounded-full interactive',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <span
                  className={cn(
                    'relative inline-flex size-8 items-center justify-center rounded-full',
                    hasActiveTool
                      ? 'bg-[var(--color-tool-selection)] text-[var(--color-tool-selection-fg)] ring-4 ring-[var(--color-tool-selection-soft)] hover:bg-[var(--color-tool-selection-hover)]'
                      : 'bg-[var(--color-tool-idle)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                  )}
                  >
                  <Plus size={16} aria-hidden />
                </span>
              </button>
            </PopoverTrigger>
            <PopoverContent
              side="top"
              align="start"
              sideOffset={10}
              collisionPadding={12}
              className="min-w-0 overflow-y-auto overscroll-contain p-1.5 scrollbar-thin"
              style={{
                width: 'min(20rem, calc(100vw - var(--safe-left) - var(--safe-right) - 1.5rem))',
                maxWidth: 'calc(100vw - var(--safe-left) - var(--safe-right) - 1.5rem)',
                maxHeight: 'min(62dvh, var(--radix-popover-content-available-height), calc(100dvh - var(--safe-top) - var(--safe-bottom) - 1.5rem))',
              }}
            >
              {toolUsePanel === 'root' ? (
                <>
                  <button
                    type="button"
                    onClick={() => {
                      setMoreOpen(false)
                      fileRef.current?.click()
                    }}
                    className="flex w-full items-center gap-3 rounded-[10px] px-3 py-2.5 text-left text-[15px] text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] active:bg-[var(--color-bg-muted)]"
                  >
                    <Paperclip size={18} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden />
                    {t('composer.attach')}
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setMoreOpen(false)
                      openNewFormula()
                    }}
                    className="flex w-full items-center gap-3 rounded-[10px] px-3 py-2.5 text-left text-[15px] text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] active:bg-[var(--color-bg-muted)]"
                  >
                    <Sigma size={18} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden />
                    {t('composer.formula.action')}
                  </button>
                  {canAttachImages ? (
                    <button
                      type="button"
                      onClick={() => {
                        setMoreOpen(false)
                        imageFileRef.current?.click()
                      }}
                      className="flex w-full items-center gap-3 rounded-[10px] px-3 py-2.5 text-left text-[15px] text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)] active:bg-[var(--color-bg-muted)]"
                    >
                      <ImageIcon size={18} className="shrink-0 text-[var(--color-fg-muted)]" aria-hidden />
                      {t('composer.addImage')}
                    </button>
                  ) : null}
                </>
              ) : null}
              {featureMenuAvailable ? (
                <>
                  {toolUsePanel === 'root' ? <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden /> : null}
                  {featureList()}
                </>
              ) : null}

              {toolUsePanel === 'root' && onKBChange && !isImageMode ? (
                <>
                  <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden />
                  <p className="px-2.5 pb-1 pt-0.5 text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
                    {t('composer.knowledgeBases')}
                  </p>
                  {kbChecklist}
                </>
              ) : null}

              {toolUsePanel === 'root' && visibleParamControls ? (
                <>
                  <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden />
                  <div className="px-1.5 py-1">
                    <ParamControls
                      key={modelId || 'default-model'}
                      controls={visibleParamControls}
                      values={paramValues}
                      onChange={handleParamValuesChange}
                    />
                  </div>
                </>
              ) : null}
            </PopoverContent>
          </Popover>

          {/* Context controls can outgrow a 320px phone (image style + a long
              model name). They get the only shrinkable/scrollable lane so the
              two 44px edge actions never leave the viewport. */}
          <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto overscroll-x-contain scrollbar-none">
            {isImageMode ? <StylePicker value={imageStyleId} onChange={setImageStyleId} className="min-w-0 max-w-[42vw] shrink" /> : null}

            {/* On phones the header already carries the model picker (ChatThread),
                so we drop the composer's to keep the row uncluttered. New-chat
                (ChatHome) has no header picker, so it keeps this one. */}
            {!modelPickerInHeader ? (
              <ModelPicker value={modelId} onChange={onModelChange} fast={fast} onFastChange={onFastChange} className="min-w-0 max-w-[42vw] shrink" />
            ) : null}
          </div>

          {primaryAction}
        </div>
      ) : (
        /* ── Desktop: inline scrollable left zone + pinned right zone ── */
        <div className="flex items-center gap-1 px-2.5 pb-2.5 pt-1">
          <div className="-my-1.5 flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto py-1.5 scrollbar-none">
            {featureMenuAvailable ? (
              <Popover open={featuresOpen} onOpenChange={setFeaturesOpen}>
                <Tooltip content={t('composer.features.title', { defaultValue: 'Turn features' })}>
                  <PopoverTrigger asChild>
                    <button
                      type="button"
                      aria-label={t('composer.features.title', { defaultValue: 'Turn features' })}
                      className={cn(
                        'relative mx-1 inline-flex size-8 items-center justify-center rounded-[8px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                        anyFeatureActive
                          ? 'bg-[var(--color-tool-selection)] text-[var(--color-tool-selection-fg)] ring-4 ring-[var(--color-tool-selection-soft)] hover:bg-[var(--color-tool-selection-hover)]'
                          : 'bg-[var(--color-tool-idle)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]',
                      )}
                    >
                      <Plus size={16} aria-hidden />
                    </button>
                  </PopoverTrigger>
                </Tooltip>
                <PopoverContent
                  align="start"
                  side="top"
                  sideOffset={10}
                  className="max-h-[min(70vh,var(--radix-popover-content-available-height))] w-72 overflow-y-auto overscroll-contain p-1.5 scrollbar-thin"
                >
                  {featureList()}
                </PopoverContent>
              </Popover>
            ) : null}

            <Tooltip content={t('composer.attach')}>
              <button
                type="button"
                onClick={() => fileRef.current?.click()}
                aria-label={t('composer.attach')}
                className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <Paperclip size={15} aria-hidden />
              </button>
            </Tooltip>

            <Tooltip content={t('composer.formula.action')}>
              <button
                type="button"
                onClick={openNewFormula}
                aria-label={t('composer.formula.action')}
                className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <Sigma size={15} aria-hidden />
              </button>
            </Tooltip>

            {canAttachImages ? (
              <Tooltip content={t('composer.addImage')}>
                <button
                  type="button"
                  onClick={() => imageFileRef.current?.click()}
                  aria-label={t('composer.addImage')}
                  className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <ImageIcon size={15} aria-hidden />
                </button>
              </Tooltip>
            ) : null}

            <div className="mx-1 h-5 w-px bg-[var(--color-divider)]" aria-hidden />

            {isImageMode ? <StylePicker value={imageStyleId} onChange={setImageStyleId} /> : null}

            {/* Per-model param_controls (§2.3-G). Picked values flow up via onSubmit(). */}
            {visibleParamControls ? (
              <div className="flex flex-wrap items-center gap-1.5">
                <ParamControls
                  key={modelId || 'default-model'}
                  controls={visibleParamControls}
                  values={paramValues}
                  onChange={handleParamValuesChange}
                />
              </div>
            ) : null}

            {/* §7.2-7 📚 知识库选择器 — 绑定 kb_ids 到当前会话 */}
            {onKBChange && !isImageMode ? (
              <Popover
                open={kbPopoverOpen}
                onOpenChange={(open) => {
                  setKBPopoverOpen(open)
                  if (open) void loadKBList()
                }}
              >
                <Tooltip content={t('composer.knowledgeBases')}>
                  <PopoverTrigger asChild>
                    <button
                      type="button"
                      aria-label={t('composer.knowledgeBases')}
                      className={cn(
                        'inline-flex items-center gap-1.5 h-8 px-2 rounded-[8px] text-[12px] font-medium interactive',
                        selectedKnowledgeBaseIds.length > 0
                          ? 'bg-[var(--color-tool-selection-soft)] text-[var(--color-tool-selection-text)]'
                          : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      )}
                    >
                      <BookOpen size={14} aria-hidden />
                      {selectedKnowledgeBaseIds.length > 0 ? (
                        <span className="text-[11px]">{selectedKnowledgeBaseIds.length}</span>
                      ) : null}
                    </button>
                  </PopoverTrigger>
                </Tooltip>
                <PopoverContent align="start" className="w-72 p-2">
                  <p className="px-2 pb-1 pt-0.5 text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
                    {t('composer.knowledgeBases')}
                  </p>
                  {kbChecklist}
                </PopoverContent>
              </Popover>
            ) : null}
          </div>

          {/* Active-feature chips — icon-only (name on hover). In their own
              shrinkable, horizontally-scrollable track so that even many chips
              (or a long model name) can never push the model picker / send
              button off the composer edge. */}
          {activeChips.length > 0 ? (
            <div className="flex min-w-0 shrink items-center gap-1 overflow-x-auto scrollbar-none pl-1">
              {activeChips.map((chip) => (
                <Tooltip key={chip.key} content={chip.label}>
                  <span className="group inline-flex h-7 shrink-0 items-center gap-0.5 rounded-full bg-[var(--color-tool-selection-soft)] pl-1.5 pr-1 text-[var(--color-tool-selection-text)] interactive hover:bg-[var(--color-tool-selection)]/15">
                    <span className="inline-flex shrink-0" aria-hidden>
                      {chip.icon}
                    </span>
                    {chip.chipText ? (
                      <span className="min-w-[1ch] pr-0.5 text-[11px] font-semibold tabular-nums">
                        {chip.chipText}
                      </span>
                    ) : null}
                    <button
                      type="button"
                      onClick={chip.toggle}
                      aria-label={
                        chip.clearLabel ??
                        t('composer.features.disable', { defaultValue: 'Turn off {{name}}', name: chip.label })
                      }
                      className="inline-flex size-5 items-center justify-center rounded-full text-[var(--color-tool-selection-text)] hover:bg-[var(--color-tool-selection)]/15 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                    >
                      <X size={13} aria-hidden />
                    </button>
                  </span>
                </Tooltip>
              ))}
            </div>
          ) : null}
          {/* Pinned — model picker + send/stop, always visible (never shrinks). */}
          <div className="flex shrink-0 items-center gap-1.5 pl-1">
            <ModelPicker value={modelId} onChange={onModelChange} fast={fast} onFastChange={onFastChange} />
            {primaryAction}
          </div>
        </div>
      )}

      <FormulaEditorDialog
        open={formulaOpen}
        initialLatex={formulaTarget?.latex ?? ''}
        editing={Boolean(formulaTarget)}
        onOpenChange={handleFormulaOpenChange}
        onConfirm={(latex) => {
          ref.current?.setFormula(latex, formulaTarget, formulaSelectionRef.current)
          formulaSelectionRef.current = null
        }}
      />
      <ToolSelectionDialog
        open={toolSelectionOpen}
        onOpenChange={setToolSelectionOpen}
        modelId={modelId}
        selectedIds={selectedToolIds}
        onChange={handleSelectedToolIdsChange}
      />
    </div>
  )
}
