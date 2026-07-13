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
import { activeWorkspaceId } from '@/store/workspaces'
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ArrowUp,
  Paperclip,
  Image as ImageIcon,
  Mic,
  StopCircle,
  Telescope,
  ShieldCheck,
  X,
  Loader2,
  BookOpen,
  Check,
  AlertTriangle,
  Plus,
  Ban,
  Globe,
} from 'lucide-react'
import type { Attachment } from '@/types/chat'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip } from '@/components/ui/tooltip'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { kbsApi, audioApi, conversationsApi } from '@/api/endpoints'
import { ModelPicker } from './model-picker'
import { StylePicker } from './style-picker'
import { ParamControls } from './param-controls'
import { filterVisibleParams } from './param-controls.utils'
import { useAutosizeTextarea } from '@/hooks/use-autosize-textarea'
import { useMediaQuery } from '@/hooks/use-media-query'
import { useModels } from '@/store/models'
import { useAuth } from '@/store/auth'
import { useComposerPrefs } from '@/store/composer-prefs'
import { useNavigate } from 'react-router-dom'
import { api, apiUpload, ApiError } from '@/api/client'
import { toastStorageQuotaFull } from '@/lib/quota-toast'
import type { ApiAttachment, ApiConversationFile, ApiDocument } from '@/api/types'
import { toast } from '@/hooks/use-toast'
import { cn, uid, modKey } from '@/lib/utils'
import { fileIconFor } from '@/lib/file-icon'
import { ProgressRing } from '@/components/ui/progress-ring'
import { envNum } from '@/lib/env-config'

interface ComposerProps {
  modelId: string
  onModelChange: (id: string) => void
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
      /** §4.13-B: run this turn with NO tool calling. */
      noTools?: boolean
      /** §4.4-B: forced non-tool web search (only with noTools). */
      webSearch?: boolean
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
  /** Knowledge bases bound to the conversation (§7.2-7 📚 selector). */
  kbIds?: string[]
  /** When provided, the 📚 selector is shown and changes flow up here. */
  onKBChange?: (kbIds: string[]) => void
  /** True when a model picker already lives in the page header (e.g. ChatThread).
   *  On phones the composer then drops its own picker to avoid a redundant,
   *  cramped second selector — the header one is enough (§ mobile composer). */
  modelPickerInHeader?: boolean
}

const MAX_LEN = envNum('VITE_AIVORY_MAX_LEN', 12_000)
const EMPTY_PARAM_VALUES: Record<string, unknown> = {}

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

function attachmentKindLabel(a: Pick<Attachment, 'kind' | 'name'>): string {
  const ext = a.name.includes('.') ? a.name.split('.').pop()?.toUpperCase() : ''
  if (ext) return ext
  switch (a.kind) {
    case 'pdf':
      return 'PDF'
    case 'doc':
      return 'DOC'
    case 'sheet':
      return 'SHEET'
    case 'code':
      return 'CODE'
    case 'image':
      return 'IMAGE'
    default:
      return 'FILE'
  }
}

function attachmentTileClass(a: Pick<Attachment, 'kind' | 'name'>): string {
  const ext = a.name.includes('.') ? a.name.split('.').pop()?.toLowerCase() : ''
  if (a.kind === 'pdf' || ext === 'pdf') return 'bg-[var(--color-danger)] text-[var(--color-fg-inverted)]'
  if (a.kind === 'sheet' || ['xls', 'xlsx', 'csv', 'tsv'].includes(ext ?? '')) {
    return 'bg-[var(--color-success)] text-[var(--color-fg-inverted)]'
  }
  if (a.kind === 'doc' || ['doc', 'docx', 'ppt', 'pptx'].includes(ext ?? '')) {
    return 'bg-[var(--color-info)] text-[var(--color-fg-inverted)]'
  }
  return 'bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
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
// icon + name + one-line description row; `dimmed` marks a mutually-excluded
// feature (shown but not clickable).
interface FeatureItem {
  key: string
  icon: ReactNode
  label: string
  desc: string
  active: boolean
  dimmed: boolean
  /** Row is revealed conditionally (e.g. web search only inside a no-tools turn)
   *  — play a soft fade-in when it mounts. */
  enter?: boolean
  toggle: () => void
}

function FeatureRow({ item, onAfter }: { item: FeatureItem; onAfter?: () => void }) {
  return (
    <button
      type="button"
      role="menuitemcheckbox"
      aria-checked={item.active}
      disabled={item.dimmed}
      onClick={() => {
        item.toggle()
        onAfter?.()
      }}
      className={cn(
        'flex w-full items-start gap-2.5 rounded-[10px] px-2.5 py-2 text-left interactive',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        item.enter && 'animate-[fade-in_var(--duration-base)_var(--ease-out)]',
        item.dimmed
          ? 'opacity-40 cursor-not-allowed'
          : item.active
            ? 'bg-[var(--color-secondary-soft)]'
            : 'hover:bg-[var(--color-bg-muted)]',
      )}
    >
      <span
        className={cn(
          'mt-0.5 inline-flex shrink-0',
          item.active && !item.dimmed ? 'text-[var(--color-secondary)]' : 'text-[var(--color-fg-muted)]',
        )}
        aria-hidden
      >
        {item.icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-1.5">
          <span
            className={cn(
              'text-[13px] font-medium',
              item.active && !item.dimmed ? 'text-[var(--color-secondary)]' : 'text-[var(--color-fg)]',
            )}
          >
            {item.label}
          </span>
          {item.active && !item.dimmed ? <Check size={13} className="text-[var(--color-secondary)]" aria-hidden /> : null}
        </span>
        <span className="mt-0.5 block text-[11.5px] leading-snug text-[var(--color-fg-subtle)]">{item.desc}</span>
      </span>
    </button>
  )
}

export function Composer({
  modelId,
  onModelChange,
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
  kbIds,
  onKBChange,
  modelPickerInHeader = false,
}: ComposerProps) {
  const { t } = useTranslation('chat')
  const navigate = useNavigate()
  const mode = useComposerPrefs((s) => s.mode)
  const setMode = useComposerPrefs((s) => s.setMode)
  // §verify: when on, the answer is fact-checked by a second model this turn.
  const verify = useComposerPrefs((s) => s.verify)
  const setVerify = useComposerPrefs((s) => s.setVerify)
  // §4.13-B disable-tools + forced non-tool web search (mutually exclusive with
  // deep-research; web search only inside no-tools).
  const noTools = useComposerPrefs((s) => s.noTools)
  const setNoTools = useComposerPrefs((s) => s.setNoTools)
  const forceWebSearch = useComposerPrefs((s) => s.forceWebSearch)
  const setForceWebSearch = useComposerPrefs((s) => s.setForceWebSearch)
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
  const [kbList, setKBList] = useState<{ id: string; name: string }[]>([])
  // Drag-and-drop file upload over the composer surface.
  const [dragOver, setDragOver] = useState(false)
  // Narrow screens collapse every secondary action into a single "+" menu
  // (Gemini/ChatGPT-mobile pattern) so the row never overflows and tap targets
  // stay large. 639px = Tailwind's `sm` breakpoint minus 1.
  const isMobile = useMediaQuery('(max-width: 639px)')
  const [moreOpen, setMoreOpen] = useState(false)
  // §4.13-B feature menu (the "+" left of attach): research / verify / no-tools
  // / web-search as an icon+name+description list.
  const [featuresOpen, setFeaturesOpen] = useState(false)
  const loadKBList = async () => {
    try {
      const rows = await kbsApi.list(activeWorkspaceId())
      setKBList(rows.map((kb) => ({ id: kb.id, name: kb.name })))
    } catch {
      /* ignore */
    }
  }
  const ref = useRef<HTMLTextAreaElement>(null)
  const fileRef = useRef<HTMLInputElement>(null)
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

  useEffect(() => {
    if (draftScopeRef.current === draftScope) return
    draftScopeRef.current = draftScope
    updateValue(draftScope ? cachedDraft ?? initialValue : initialValue)
  }, [cachedDraft, draftScope, initialValue, updateValue])

  async function toggleVoice() {
    if (transcribing) return
    if (recording) {
      recorderRef.current?.stop()
      return
    }
    if (typeof MediaRecorder === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
      toast.error(t('composer.voiceUnsupported'))
      return
    }
    let stream: MediaStream
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    } catch {
      toast.error(t('composer.voicePermission'))
      return
    }
    const rec = new MediaRecorder(stream)
    chunksRef.current = []
    rec.ondataavailable = (e) => {
      if (e.data.size > 0) chunksRef.current.push(e.data)
    }
    rec.onstop = () => {
      stream.getTracks().forEach((tr) => tr.stop())
      setRecording(false)
      const blob = new Blob(chunksRef.current, { type: rec.mimeType || 'audio/webm' })
      if (blob.size === 0) return
      setTranscribing(true)
      void audioApi
        .transcribe(blob, 'audio.webm')
        .then(({ text }) => {
          if (text) {
            const current = valueRef.current
            updateValue((current.trim() ? current.trimEnd() + ' ' : '') + text)
            requestAnimationFrame(() => ref.current?.focus())
          }
        })
        .catch((e) => toast.error(e instanceof ApiError ? e.message : t('composer.voiceFailed')))
        .finally(() => setTranscribing(false))
    }
    recorderRef.current = rec
    rec.start()
    setRecording(true)
  }
  const effectivePlaceholder = placeholder ?? t('composer.placeholder')

  const currentModel = useModels(
    (s) => s.models.find((m) => m.id === modelId) ?? s.imageModels.find((m) => m.id === modelId),
  )
  // §4.20 image mode: when the selected model draws, the composer shows a style
  // picker and hides chat-only controls (research / knowledge bases).
  const isImageMode = currentModel?.kind === 'image'
  const [imageStyleId, setImageStyleId] = useState('')
  // Deep Research is both a per-group capability and a per-model exposure flag.
  // Admins bypass the group feature but still respect the current model's flag.
  const groupResearchEnabled = useAuth(
    (s) => s.user?.role === 'admin' || Boolean(s.user?.features?.includes('research')),
  )
  const modelResearchEnabled = currentModel?.research_enabled ?? true
  const researchEnabled = groupResearchEnabled && modelResearchEnabled
  // §verify: only offer the toggle when an admin has configured an auditor model.
  const verifyAvailable = useModels((s) => s.verifyAvailable)
  const paramControls = currentModel?.param_controls
  const effectiveMode = !isImageMode && researchEnabled ? mode : 'default'
  const effectiveVerify = verify && verifyAvailable && !isImageMode
  // §4.13-B disable-tools + forced web search — inapplicable to image models.
  const effectiveNoTools = noTools && !isImageMode
  const effectiveWebSearch = effectiveNoTools && forceWebSearch
  const handleParamValuesChange = useCallback(
    (next: Record<string, unknown>) => {
      setCachedParamValues(modelId, next)
    },
    [modelId, setCachedParamValues],
  )

  // Cap textarea growth lower on phones so a long draft can't eat the viewport.
  useAutosizeTextarea(ref, value, compact || isMobile ? 6 : 12)

  useEffect(() => {
    if (autoFocus) ref.current?.focus()
  }, [autoFocus])

  const uploading = useMemo(() => attachments.some((a) => a.uploading), [attachments])
  // A document attachment must be fully RAG-ready before it can be sent. Failed
  // docs block too; removing the chip is the explicit "send without it" action.
  const documentNotReady = useMemo(
    () => attachments.some((a) => a.documentId && a.ingest !== 'ready'),
    [attachments],
  )
  const canSubmit = value.trim().length > 0 && !streaming && !uploading && !restoringAttachments && !documentNotReady

  async function handleSubmit() {
    if (submittingRef.current) return
    const text = value.trim()
    if (!text || streaming || uploading || restoringAttachments || documentNotReady) return
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
      const params = filterVisibleParams(paramControls, paramValues)
      onSubmit(text, attachments, {
        mode: effectiveMode === 'default' ? undefined : effectiveMode,
        params: Object.keys(params).length > 0 ? params : undefined,
        imageStyleId: isImageMode && imageStyleId ? imageStyleId : undefined,
        verify: effectiveVerify ? true : undefined,
        noTools: effectiveNoTools ? true : undefined,
        webSearch: effectiveWebSearch ? true : undefined,
      })
      updateValue('')
      // Stop any leftover pollers and revoke blob: URLs — uploadAttachment already
      // swapped its own. Persistent /api/files/… URLs stay so the bubble can render.
      pollTimers.current.forEach((tm) => clearTimeout(tm))
      pollTimers.current.clear()
      attachments.forEach((a) => {
        committedAttachmentIds.current.add(a.id)
        if (a.previewUrl && a.previewUrl.startsWith('blob:')) URL.revokeObjectURL(a.previewUrl)
      })
      setAttachments([])
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
      const url = `/files${scopeId ? `?conversation_id=${encodeURIComponent(scopeId)}&draft=1${ragFlag ? '&rag=1' : ''}` : ''}`
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
      setAttachments((s) => s.filter((a) => a.id !== local.id))
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
        const restored = files.map((file) => restoreConversationFile(file, conversationId))
        setAttachments((current) => {
          const present = new Set(current.map((item) => item.id))
          return [
            ...current,
            // Skip anything already present OR already sent — a stale restore
            // fetch must never resurrect a just-committed attachment.
            ...restored.filter((item) => !present.has(item.id) && !committedAttachmentIds.current.has(item.id)),
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
    const all = Array.from(files)
    // §4.6 reject oversize files BEFORE uploading — images and other files have
    // separate admin-set caps. Rejected images would otherwise upload fine but be
    // silently dropped at chat time (base64 inline cap); documents would fail the
    // server cap after a wasted upload.
    const limits = await getUploadLimits()
    const overImage = all.filter((f) => f.type.startsWith('image/') && f.size > limits.max_image_bytes)
    const overFile = all.filter((f) => !f.type.startsWith('image/') && f.size > limits.max_file_bytes)
    if (overImage.length) {
      toast.error(
        t('composer.imageTooLarge', {
          defaultValue: 'Images must be under {{mb}} MB',
          mb: Math.floor(limits.max_image_bytes / (1024 * 1024)),
        }),
        overImage.map((f) => f.name).join(', '),
      )
    }
    if (overFile.length) {
      toast.error(
        t('composer.fileTooLarge', {
          defaultValue: 'Files must be under {{mb}} MB',
          mb: Math.floor(limits.max_file_bytes / (1024 * 1024)),
        }),
        overFile.map((f) => f.name).join(', '),
      )
    }
    const list = all.filter((f) => !overImage.includes(f) && !overFile.includes(f))
    if (!list.length) return 0
    const additions: PendingAttachment[] = list.map((f) => ({
      id: uid('att'),
      name: f.name,
      size: f.size,
      kind: f.type.startsWith('image/')
        ? 'image'
        : /pdf/i.test(f.type)
          ? 'pdf'
          : /word|doc/i.test(f.type)
            ? 'doc'
            : /sheet|csv|xls/i.test(f.type)
              ? 'sheet'
              : 'other',
      // Local thumbnail so images preview instantly; revoked on remove/submit.
      previewUrl: f.type.startsWith('image/') ? URL.createObjectURL(f) : undefined,
      uploading: true,
      uploadProgress: 0,
    }))
    setAttachments((s) => [...s, ...additions])
    toast.success(
      t(additions.length === 1 ? 'composer.attachedSingle' : 'composer.attachedMultiple', { count: additions.length }),
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
    const done = await Promise.all(list.map((file, idx) => uploadAttachment(file, additions[idx], scopeId)))
    return done.filter(Boolean).length
  }

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
  }

  // §4.13-B turn-feature list (composer "+" menu). Only chat models expose them.
  // Mutual exclusion: deep-research ↔ no-tools (each dims the other); web search
  // appears only inside a no-tools turn. Deep-research isDim when noTools is on
  // and vice-versa; the store setters enforce the same rules on toggle.
  const researchActive = effectiveMode === 'deep-research'
  const featureItems: FeatureItem[] = []
  if (!isImageMode) {
    if (researchEnabled) {
      featureItems.push({
        key: 'deep-research',
        icon: <Telescope size={16} aria-hidden />,
        label: t('composer.research'),
        desc: t('composer.features.researchDesc', { defaultValue: 'Plan, search the web across rounds, and write a cited report.' }),
        active: researchActive,
        dimmed: noTools,
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
        dimmed: false,
        toggle: () => setVerify(!verify),
      })
    }
    featureItems.push({
      key: 'no-tools',
      icon: <Ban size={16} aria-hidden />,
      label: t('composer.features.noTools', { defaultValue: 'Disable tools' }),
      desc: t('composer.features.noToolsDesc', { defaultValue: "Answer directly without calling any tool — faster and cheaper." }),
      active: noTools,
      dimmed: researchActive,
      toggle: () => setNoTools(!noTools),
    })
    if (noTools) {
      featureItems.push({
        key: 'web-search',
        icon: <Globe size={16} aria-hidden />,
        label: t('composer.features.webSearch', { defaultValue: 'Web search' }),
        desc: t('composer.features.webSearchDesc', { defaultValue: 'Search the web every turn and add the results to the prompt (no tool call).' }),
        active: forceWebSearch,
        dimmed: false,
        enter: true,
        toggle: () => setForceWebSearch(!forceWebSearch),
      })
    }
  }
  const featureList = (onAfter?: () => void) => (
    <div className="flex flex-col gap-0.5">
      {featureItems.map((it) => (
        <FeatureRow key={it.key} item={it} onAfter={onAfter} />
      ))}
    </div>
  )
  const anyFeatureActive = featureItems.some((it) => it.active && !it.dimmed)

  // Active-feature chips shown right before the model picker: icon + name + an
  // × to turn the feature off. Only genuinely active (non-dimmed) ones.
  const activeChips = featureItems.filter((it) => it.active && !it.dimmed)

  // A tool is "armed" while collapsed → the mobile "+" trigger (which now also
  // holds the feature menu) shows an accent dot so the user knows a feature or a
  // KB is active without expanding the menu.
  const hasActiveTool = anyFeatureActive || (kbIds?.length ?? 0) > 0

  // KB checklist — shared by the desktop popover and the mobile "+" menu.
  const kbChecklist =
    kbList.length === 0 ? (
      <p className="px-2 py-2 text-sm text-[var(--color-fg-muted)]">{t('composer.noKnowledgeBases')}</p>
    ) : (
      kbList.map((kb) => {
        const checked = kbIds?.includes(kb.id) ?? false
        return (
          <button
            key={kb.id}
            type="button"
            onClick={() =>
              onKBChange?.(checked ? (kbIds ?? []).filter((x) => x !== kb.id) : [...(kbIds ?? []), kb.id])
            }
            className="flex w-full items-center gap-2 rounded-[8px] px-2 py-1.5 text-left text-sm text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]"
          >
            <span
              className={cn(
                'inline-flex size-4 items-center justify-center rounded border',
                checked
                  ? 'border-[var(--color-accent)] bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
                  : 'border-[var(--color-border-strong)]',
              )}
            >
              {checked ? <Check size={11} aria-hidden /> : null}
            </span>
            <span className="truncate">{kb.name}</span>
          </button>
        )
      })
    )

  // Send / stop button — shared by both layouts. On mobile it's a larger,
  // thumb-friendly circle pinned to the right edge.
  const sendBtn = streaming ? (
    <Tooltip content={t('composer.stop')}>
      <button
        type="button"
        onClick={onStop}
        aria-label={t('composer.stop')}
        className="inline-flex items-center justify-center size-9 max-sm:size-10 rounded-[10px] max-sm:rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] hover:opacity-90 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
      >
        <StopCircle size={16} aria-hidden />
      </button>
    </Tooltip>
  ) : (
    <Tooltip content={t('composer.send', { kbd: modKey() })}>
      <button
        type="button"
        onClick={handleSubmit}
        disabled={!canSubmit}
        aria-label={t('actions.send', { ns: 'common' })}
        className={cn(
          'inline-flex items-center justify-center size-9 max-sm:size-10 rounded-[10px] max-sm:rounded-full interactive',
          canSubmit
            ? 'bg-[var(--color-accent)] text-[var(--color-accent-fg)] hover:bg-[var(--color-accent-hover)] shadow-[var(--shadow-xs)]'
            : 'bg-[var(--color-bg-muted)] text-[var(--color-fg-faint)] cursor-not-allowed',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
        )}
      >
        {uploading ? <Loader2 size={16} className="animate-spin" aria-hidden /> : <ArrowUp size={16} aria-hidden />}
      </button>
    </Tooltip>
  )

  return (
    <div
      onDragOver={(e) => {
        // Only react to file drags; let text/selection drags pass through.
        if (!Array.from(e.dataTransfer.types).includes('Files')) return
        e.preventDefault()
        setDragOver(true)
      }}
      onDragLeave={(e) => {
        // Ignore leave events fired when crossing into a child element.
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragOver(false)
      }}
      onDrop={(e) => {
        if (!Array.from(e.dataTransfer.types).includes('Files')) return
        e.preventDefault()
        setDragOver(false)
        if (e.dataTransfer.files?.length) void handleAttach(e.dataTransfer.files)
      }}
      className={cn(
        'group/composer relative w-full',
        // Calm Gemini-style pill on phones (rounder, no resting shadow); the
        // editorial card look is kept on ≥sm.
        'rounded-[16px] max-sm:rounded-[22px] border border-[var(--color-border)] bg-[var(--color-surface)]',
        'shadow-[var(--shadow-sm)] max-sm:shadow-none transition-[border-color,box-shadow] duration-200',
        'focus-within:border-[var(--color-border-strong)] focus-within:shadow-[var(--shadow-md)]',
        dragOver && 'border-[var(--color-accent)] shadow-[var(--shadow-md)]',
      )}
    >
      {/* Drag-and-drop overlay — shown while a file is dragged over the composer. */}
      {dragOver && (
        <div className="pointer-events-none absolute inset-0 z-[var(--z-raised)] grid place-items-center rounded-[16px] bg-[var(--color-accent-soft)]/80 backdrop-blur-[1px]">
          <span className="inline-flex items-center gap-2 text-sm font-medium text-[var(--color-accent)]">
            <Paperclip size={15} aria-hidden />
            {t('composer.dropHint', { defaultValue: 'Drop files to attach' })}
          </span>
        </div>
      )}

      {/* Attachments preview. The armed-mode (research) state is shown by the
          toolbar button below, so we don't repeat a chip above the input. */}
      {attachments.length > 0 && (
        <div className="flex items-stretch gap-1.5 overflow-x-auto px-3 pb-1 pt-2.5 scrollbar-none">
          {attachments.map((a) => {
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
                  'group/att relative flex h-14 min-w-0 max-w-[min(28rem,calc(100vw-6rem))] flex-[1_1_15rem] items-center gap-2.5 rounded-[10px] border bg-[var(--color-surface-raised)] py-2 pl-2.5 pr-8 shadow-[var(--shadow-xs)]',
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

      {/* Textarea */}
      <Textarea
        ref={ref}
        value={value}
        onChange={(e) => updateValue(e.target.value)}
        onKeyDown={(e) => {
          // Don't intercept while user is mid-IME composition (CJK).
          if (e.nativeEvent.isComposing || e.keyCode === 229) return
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault()
            handleSubmit()
            return
          }
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            handleSubmit()
          }
        }}
        onPaste={(e) => {
          // Support pasting an image from the clipboard (screenshot / copied
          // picture) — upload it as an attachment just like a file pick.
          const imgs = Array.from(e.clipboardData?.items ?? [])
            .filter((it) => it.kind === 'file' && it.type.startsWith('image/'))
            .map((it) => it.getAsFile())
            .filter((f): f is File => f !== null)
          if (imgs.length > 0) {
            e.preventDefault()
            const dt = new DataTransfer()
            imgs.forEach((f) => dt.items.add(f))
            void handleAttach(dt.files)
            return
          }
          // Codex-style long paste: inserting would push the draft past
          // MAX_LEN, so attach the pasted text as a .txt file instead of
          // flooding the textarea (and later hitting the length wall). The
          // resulting length accounts for the selection the paste replaces.
          const pasted = e.clipboardData?.getData('text/plain') ?? ''
          const el = e.currentTarget
          const replaced = (el.selectionEnd ?? 0) - (el.selectionStart ?? 0)
          if (canAttachLongText && pasted && el.value.length - replaced + pasted.length > MAX_LEN) {
            e.preventDefault()
            void attachTextAsFile(pasted).then((ok) => {
              // Upload failed (quota / allowlist) — put the text back into the
              // draft so nothing is lost; the error toast explains what happened.
              if (!ok) {
                const cur = valueRef.current
                updateValue(cur ? cur + '\n' + pasted : pasted)
              }
            })
          }
        }}
        placeholder={effectivePlaceholder}
        rows={compact || isMobile ? 1 : 2}
        className={cn(
          'border-none bg-transparent focus:bg-transparent focus:ring-0',
          // ≥16px on phones (--text-input-mobile) so iOS Safari doesn't zoom on focus.
          'px-4 pt-3 pb-1 text-[0.9375rem] max-sm:text-[length:var(--text-input-mobile)]',
          'placeholder:text-[var(--color-fg-faint)]',
          compact && 'min-h-[40px]',
        )}
        aria-label={t('composer.inputLabel', { defaultValue: 'Type a message' })}
      />

      {/* Toolbar row. The file input is shared by both layouts. On phones every
          secondary action collapses into a single "+" menu (Gemini/ChatGPT-mobile
          pattern); on ≥sm screens they sit inline in a scrollable left zone. */}
      <input
        type="file"
        ref={fileRef}
        hidden
        multiple
        onChange={(e) => {
          void handleAttach(e.currentTarget.files)
          e.currentTarget.value = ''
        }}
      />

      {isMobile ? (
        /* ── Mobile: [+ menu] [model] … [send] ── */
        <div className="flex items-center gap-1.5 px-2 pb-2.5 pt-1">
          <Popover
            open={moreOpen}
            onOpenChange={(o) => {
              setMoreOpen(o)
              if (o && onKBChange) void loadKBList()
            }}
          >
            <PopoverTrigger asChild>
              <button
                type="button"
                aria-label={t('composer.more', { defaultValue: 'More' })}
                className={cn(
                  'relative inline-flex shrink-0 items-center justify-center size-10 rounded-full interactive',
                  'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <Plus size={18} aria-hidden />
                {hasActiveTool ? (
                  <span
                    className="absolute right-1 top-1 size-2 rounded-full bg-[var(--color-accent)] ring-2 ring-[var(--color-surface)]"
                    aria-hidden
                  />
                ) : null}
              </button>
            </PopoverTrigger>
            <PopoverContent
              side="top"
              align="start"
              sideOffset={10}
              className="w-60 max-sm:w-[calc(100vw-2*var(--layout-gutter-mobile))] max-h-[60dvh] overflow-y-auto scrollbar-thin p-1.5"
            >
              <button
                type="button"
                onClick={() => {
                  setMoreOpen(false)
                  fileRef.current?.click()
                }}
                className="flex w-full items-center gap-2.5 rounded-[8px] px-2.5 py-2 text-left text-sm text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]"
              >
                <Paperclip size={16} className="text-[var(--color-fg-muted)]" aria-hidden />
                {t('composer.attach')}
              </button>
              <button
                type="button"
                onClick={() => {
                  setMoreOpen(false)
                  const input = fileRef.current
                  if (!input) return
                  input.accept = 'image/*'
                  input.click()
                  input.accept = ''
                }}
                className="flex w-full items-center gap-2.5 rounded-[8px] px-2.5 py-2 text-left text-sm text-[var(--color-fg)] hover:bg-[var(--color-bg-muted)]"
              >
                <ImageIcon size={16} className="text-[var(--color-fg-muted)]" aria-hidden />
                {t('composer.addImage')}
              </button>
              <button
                type="button"
                onClick={() => {
                  setMoreOpen(false)
                  void toggleVoice()
                }}
                disabled={transcribing}
                className={cn(
                  'flex w-full items-center gap-2.5 rounded-[8px] px-2.5 py-2 text-left text-sm hover:bg-[var(--color-bg-muted)]',
                  recording ? 'text-[var(--color-danger)]' : 'text-[var(--color-fg)]',
                  transcribing && 'cursor-not-allowed opacity-60',
                )}
              >
                <Mic size={16} className={cn(!recording && 'text-[var(--color-fg-muted)]')} aria-hidden />
                {recording ? t('composer.voiceStop') : t('composer.voice')}
              </button>

              {featureItems.length > 0 ? (
                <>
                  <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden />
                  {featureList()}
                </>
              ) : null}

              {onKBChange && !isImageMode ? (
                <>
                  <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden />
                  <p className="px-2.5 pb-1 pt-0.5 text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
                    {t('composer.knowledgeBases')}
                  </p>
                  {kbChecklist}
                </>
              ) : null}

              {paramControls ? (
                <>
                  <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden />
                  <div className="px-1.5 py-1">
                    <ParamControls
                      key={modelId || 'default-model'}
                      controls={paramControls}
                      values={paramValues}
                      onChange={handleParamValuesChange}
                    />
                  </div>
                </>
              ) : null}
            </PopoverContent>
          </Popover>

          {isImageMode ? <StylePicker value={imageStyleId} onChange={setImageStyleId} /> : null}

          {/* On phones the header already carries the model picker (ChatThread),
              so we drop the composer's to keep the row uncluttered. New-chat
              (ChatHome) has no header picker, so it keeps this one. */}
          {!modelPickerInHeader ? (
            <ModelPicker value={modelId} onChange={onModelChange} className="min-w-0 max-w-[40vw]" />
          ) : null}

          <div className="ml-auto">{sendBtn}</div>
        </div>
      ) : (
        /* ── Desktop: inline scrollable left zone + pinned right zone ── */
        <div className="flex items-center gap-1 px-2.5 pb-2.5 pt-1">
          <div className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto scrollbar-none">
            {featureItems.length > 0 ? (
              <Popover open={featuresOpen} onOpenChange={setFeaturesOpen}>
                <Tooltip content={t('composer.features.title', { defaultValue: 'Turn features' })}>
                  <PopoverTrigger asChild>
                    <button
                      type="button"
                      aria-label={t('composer.features.title', { defaultValue: 'Turn features' })}
                      className={cn(
                        'relative inline-flex items-center justify-center size-8 rounded-[8px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                        anyFeatureActive
                          ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
                          : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                      )}
                    >
                      <Plus size={16} aria-hidden />
                    </button>
                  </PopoverTrigger>
                </Tooltip>
                <PopoverContent align="start" side="top" sideOffset={10} className="w-72 p-1.5">
                  <p className="px-2.5 pb-1 pt-0.5 text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
                    {t('composer.features.title', { defaultValue: 'Turn features' })}
                  </p>
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

            <Tooltip content={t('composer.addImage')}>
              <button
                type="button"
                onClick={() => {
                  const input = fileRef.current
                  if (!input) return
                  input.accept = 'image/*'
                  input.click()
                  input.accept = ''
                }}
                aria-label={t('composer.addImage')}
                className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              >
                <ImageIcon size={15} aria-hidden />
              </button>
            </Tooltip>

            <Tooltip content={recording ? t('composer.voiceStop') : t('composer.voice')}>
              <button
                type="button"
                onClick={() => void toggleVoice()}
                disabled={transcribing}
                aria-label={t('composer.voice')}
                aria-pressed={recording}
                className={cn(
                  'inline-flex items-center justify-center size-8 rounded-[8px] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  recording
                    ? 'bg-[var(--color-danger-soft)] text-[var(--color-danger)]'
                    : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                  transcribing && 'opacity-60 cursor-not-allowed',
                )}
              >
                <Mic
                  size={15}
                  aria-hidden
                  className={cn(recording && 'animate-[streaming-pulse_1600ms_ease-in-out_infinite]')}
                />
              </button>
            </Tooltip>

            <div className="mx-1 h-5 w-px bg-[var(--color-divider)]" aria-hidden />

            {isImageMode ? <StylePicker value={imageStyleId} onChange={setImageStyleId} /> : null}

            {/* Per-model param_controls (§2.3-G). Picked values flow up via onSubmit(). */}
            {paramControls ? (
              <div className="flex flex-wrap items-center gap-1.5">
                <ParamControls
                  key={modelId || 'default-model'}
                  controls={paramControls}
                  values={paramValues}
                  onChange={handleParamValuesChange}
                />
              </div>
            ) : null}

            {/* §7.2-7 📚 知识库选择器 — 绑定 kb_ids 到当前会话 */}
            {onKBChange && !isImageMode ? (
              <Popover>
                <Tooltip content={t('composer.knowledgeBases')}>
                  <PopoverTrigger asChild>
                    <button
                      type="button"
                      aria-label={t('composer.knowledgeBases')}
                      className={cn(
                        'inline-flex items-center gap-1.5 h-8 px-2 rounded-[8px] text-[12px] font-medium interactive',
                        (kbIds?.length ?? 0) > 0
                          ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
                          : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                      )}
                    >
                      <BookOpen size={14} aria-hidden />
                      {(kbIds?.length ?? 0) > 0 ? <span className="text-[11px]">{kbIds?.length}</span> : null}
                    </button>
                  </PopoverTrigger>
                </Tooltip>
                <PopoverContent align="start" className="w-64 p-2" onOpenAutoFocus={() => void loadKBList()}>
                  <p className="px-2 pb-1 pt-0.5 text-[11px] font-medium uppercase tracking-wider text-[var(--color-fg-subtle)]">
                    {t('composer.knowledgeBases')}
                  </p>
                  {kbChecklist}
                </PopoverContent>
              </Popover>
            ) : null}
          </div>

          {/* Right zone — pinned, never wraps: active-feature chips + model picker + send/stop. */}
          <div className="flex shrink-0 items-center gap-1.5 pl-1">
            {activeChips.map((chip) => (
              <Tooltip key={chip.key} content={chip.desc}>
                <span className="group inline-flex items-center gap-1 h-7 pl-2 pr-1 rounded-full bg-[var(--color-secondary-soft)] text-[var(--color-secondary)] text-[12px] font-medium">
                  <span className="inline-flex shrink-0" aria-hidden>
                    {chip.icon}
                  </span>
                  <span className="max-w-[7rem] truncate">{chip.label}</span>
                  <button
                    type="button"
                    onClick={chip.toggle}
                    aria-label={t('composer.features.disable', { defaultValue: 'Turn off {{name}}', name: chip.label })}
                    className="inline-flex items-center justify-center size-5 rounded-full text-[var(--color-secondary)] hover:bg-[var(--color-secondary)]/15 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                  >
                    <X size={13} aria-hidden />
                  </button>
                </span>
              </Tooltip>
            ))}
            <ModelPicker value={modelId} onChange={onModelChange} />
            {sendBtn}
          </div>
        </div>
      )}
    </div>
  )
}
