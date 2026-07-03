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
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
import { api, ApiError } from '@/api/client'
import type { ApiAttachment } from '@/api/types'
import { toast } from '@/hooks/use-toast'
import { cn, uid, modKey } from '@/lib/utils'
import { fileIconFor } from '@/lib/file-icon'

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

const MAX_LEN = 12_000
const EMPTY_PARAM_VALUES: Record<string, unknown> = {}

interface PendingAttachment extends Attachment {
  /** true while POST /api/files is in flight. */
  uploading?: boolean
  /** Server document id for a doc-like upload that is being RAG-ingested, so we
   *  can poll its status. Absent for images / scope-less uploads. */
  documentId?: string
  /** Ingest progress of the conversation-scoped document. While 'parsing' or
   *  'embedding' the send button is blocked so the FIRST question always lands
   *  after the file is searchable (§ chat uploads). */
  ingest?: 'parsing' | 'embedding' | 'ready' | 'failed'
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
  const mode = useComposerPrefs((s) => s.mode)
  const setMode = useComposerPrefs((s) => s.setMode)
  // §verify: when on, the answer is fact-checked by a second model this turn.
  const verify = useComposerPrefs((s) => s.verify)
  const setVerify = useComposerPrefs((s) => s.setVerify)
  const cachedParamValues = useComposerPrefs((s) => (modelId ? s.paramValuesByModel[modelId] : undefined))
  const setCachedParamValues = useComposerPrefs((s) => s.setParamValues)
  const cachedDraft = useComposerPrefs((s) => (draftScope ? s.draftsByScope[draftScope] : undefined))
  const setCachedDraft = useComposerPrefs((s) => s.setDraft)
  const paramValues = cachedParamValues ?? EMPTY_PARAM_VALUES
  const [value, setValue] = useState(() => (draftScope ? cachedDraft ?? initialValue : initialValue))
  const valueRef = useRef(value)
  const draftScopeRef = useRef(draftScope)
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  const [kbList, setKBList] = useState<{ id: string; name: string }[]>([])
  // Drag-and-drop file upload over the composer surface.
  const [dragOver, setDragOver] = useState(false)
  // Narrow screens collapse every secondary action into a single "+" menu
  // (Gemini/ChatGPT-mobile pattern) so the row never overflows and tap targets
  // stay large. 639px = Tailwind's `sm` breakpoint minus 1.
  const isMobile = useMediaQuery('(max-width: 639px)')
  const [moreOpen, setMoreOpen] = useState(false)
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
  // Active ingest-status pollers, keyed by attachment id, so they can be
  // cancelled on remove / submit / unmount.
  const pollTimers = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map())
  useEffect(() => {
    const timers = pollTimers.current
    return () => {
      timers.forEach((tm) => clearTimeout(tm))
      timers.clear()
    }
  }, [])
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
  // Deep Research is a per-group capability — only show the button when the
  // user's group is entitled (admins always are).
  const researchEnabled = useAuth(
    (s) => s.user?.role === 'admin' || Boolean(s.user?.features?.includes('research')),
  )
  // §verify: only offer the toggle when an admin has configured an auditor model.
  const verifyAvailable = useModels((s) => s.verifyAvailable)
  const paramControls = currentModel?.param_controls
  const effectiveMode = !isImageMode && researchEnabled ? mode : 'default'
  const effectiveVerify = verify && verifyAvailable && !isImageMode
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
  // A doc that is still parsing/embedding isn't searchable yet — block the send
  // so the FIRST question lands after the file is ready (§ chat uploads).
  const ingesting = useMemo(
    () => attachments.some((a) => a.ingest === 'parsing' || a.ingest === 'embedding'),
    [attachments],
  )
  const canSubmit = value.trim().length > 0 && !streaming && !uploading && !ingesting

  async function handleSubmit() {
    if (submittingRef.current) return
    const text = value.trim()
    if (!text || streaming || uploading || ingesting) return
    if (text.length > MAX_LEN) {
      toast.warning(
        t('composer.tooLongTitle'),
        t('composer.tooLongBody', { max: MAX_LEN.toLocaleString() }),
      )
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
      })
      updateValue('')
      // Stop any leftover pollers and revoke blob: URLs — uploadAttachment already
      // swapped its own. Persistent /api/files/… URLs stay so the bubble can render.
      pollTimers.current.forEach((tm) => clearTimeout(tm))
      pollTimers.current.clear()
      attachments.forEach((a) => {
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
      const ragFlag = (kbIds && kbIds.length > 0) || isDocLike
      const url = `/files${scopeId ? `?conversation_id=${encodeURIComponent(scopeId)}${ragFlag ? '&rag=1' : ''}` : ''}`
      const res = await api<ApiAttachment & { id: string; url?: string; document_id?: string }>(url, { method: 'POST', body: form })
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
        previewUrl: persistentUrl,
        documentId: res.document_id,
        // A conversation doc was created → it's being parsed/embedded; track it
        // so the send stays blocked until it's searchable.
        ingest: res.document_id ? 'parsing' : undefined,
      }
      setAttachments((s) => s.map((a) => (a.id === local.id ? updated : a)))
      if (res.document_id && scopeId) {
        startIngestPoll(scopeId, res.document_id, res.id)
      }
      return updated
    } catch (e) {
      setAttachments((s) => s.filter((a) => a.id !== local.id))
      toast.error(t('composer.uploadFailed', { defaultValue: 'Upload failed' }), e instanceof Error ? e.message : undefined)
      return null
    }
  }

  // INGEST_POLL_MS: status poll cadence. INGEST_MAX_MS: cap on how long the send
  // stays blocked — long enough for a normal parse+embed, but bounded so a slow
  // MinerU OCR job can't lock the composer; past it we unblock and let the
  // backend's own brief wait + RAG catch the doc up.
  const INGEST_POLL_MS = 1200
  const INGEST_MAX_MS = 90_000

  function startIngestPoll(scopeId: string, docId: string, attId: string) {
    const started = Date.now()
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
      if (Date.now() - started > INGEST_MAX_MS) {
        // Stop blocking; mark ready so the user can send (backend waits too).
        setAttachments((s) => s.map((a) => (a.id === attId && (a.ingest === 'parsing' || a.ingest === 'embedding') ? { ...a, ingest: 'ready' } : a)))
        return
      }
      pollTimers.current.set(attId, setTimeout(() => void tick(), INGEST_POLL_MS))
    }
    pollTimers.current.set(attId, setTimeout(() => void tick(), INGEST_POLL_MS))
  }

  async function handleAttach(files: FileList | null) {
    if (!files || !files.length) return
    const list = Array.from(files)
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
    await Promise.all(list.map((file, idx) => uploadAttachment(file, additions[idx], scopeId)))
  }

  function removeAttachment(id: string) {
    const tm = pollTimers.current.get(id)
    if (tm) {
      clearTimeout(tm)
      pollTimers.current.delete(id)
    }
    setAttachments((s) => {
      const target = s.find((a) => a.id === id)
      if (target?.previewUrl && target.previewUrl.startsWith('blob:')) URL.revokeObjectURL(target.previewUrl)
      return s.filter((a) => a.id !== id)
    })
  }

  // A tool is "armed" while collapsed → the "+" trigger shows an accent dot so
  // the user knows research / a KB is active without expanding the menu.
  const hasActiveTool = effectiveMode === 'deep-research' || (kbIds?.length ?? 0) > 0

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
        <div className="flex flex-wrap items-center gap-1.5 px-3.5 pt-3 pb-1">
          {attachments.map((a) =>
            a.kind === 'image' && a.previewUrl ? (
              <span key={a.id} className="group/att relative inline-block">
                <img
                  src={a.previewUrl}
                  alt={a.name}
                  className="size-16 rounded-[10px] border border-[var(--color-border-subtle)] object-cover"
                />
                {a.uploading ? (
                  <span className="absolute inset-0 grid place-items-center rounded-[10px] bg-[var(--color-overlay)]">
                    <Loader2 size={14} className="animate-spin text-[var(--color-fg-inverted)]" aria-hidden />
                  </span>
                ) : null}
                <button
                  type="button"
                  aria-label={`Remove ${a.name}`}
                  onClick={() => removeAttachment(a.id)}
                  className="absolute -right-1.5 -top-1.5 inline-flex size-5 items-center justify-center rounded-full bg-[var(--color-fg)] text-[var(--color-fg-inverted)] shadow-[var(--shadow-sm)] opacity-0 interactive group-hover/att:opacity-100 focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
                >
                  <X size={11} aria-hidden />
                </button>
              </span>
            ) : (
              <span
                key={a.id}
                className={cn(
                  'inline-flex items-center gap-1.5 rounded-[8px] bg-[var(--color-bg-muted)] border px-2 py-1 text-[11px] max-w-[240px]',
                  a.ingest === 'failed'
                    ? 'border-[var(--color-danger)]/40 text-[var(--color-danger)]'
                    : 'border-[var(--color-border-subtle)] text-[var(--color-fg-muted)]',
                )}
              >
                {a.uploading || a.ingest === 'parsing' || a.ingest === 'embedding' ? (
                  <Loader2 size={10} className="animate-spin shrink-0" aria-hidden />
                ) : a.ingest === 'failed' ? (
                  <AlertTriangle size={10} className="shrink-0" aria-hidden />
                ) : (
                  (() => {
                    const Icon = fileIconFor(a.name, a.kind)
                    return <Icon size={12} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
                  })()
                )}
                <span className="truncate">{a.name}</span>
                {a.ingest === 'parsing' || a.ingest === 'embedding' ? (
                  <span className="shrink-0 text-[10px] text-[var(--color-fg-subtle)]">
                    {a.ingest === 'embedding' ? t('composer.indexing') : t('composer.parsing')}
                  </span>
                ) : null}
                <button
                  type="button"
                  aria-label={`Remove ${a.name}`}
                  onClick={() => removeAttachment(a.id)}
                  className="inline-flex items-center justify-center rounded-full hover:text-[var(--color-fg)]"
                >
                  <X size={10} aria-hidden />
                </button>
              </span>
            ),
          )}
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
          if (imgs.length === 0) return
          e.preventDefault()
          const dt = new DataTransfer()
          imgs.forEach((f) => dt.items.add(f))
          void handleAttach(dt.files)
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

              {researchEnabled && !isImageMode ? (
                <>
                  <div className="my-1 h-px bg-[var(--color-divider)]" aria-hidden />
                  <button
                    type="button"
                    onClick={() => setMode(effectiveMode === 'deep-research' ? 'default' : 'deep-research')}
                    aria-pressed={effectiveMode === 'deep-research'}
                    className={cn(
                      'flex w-full items-center gap-2.5 rounded-[8px] px-2.5 py-2 text-left text-sm hover:bg-[var(--color-bg-muted)]',
                      effectiveMode === 'deep-research' ? 'text-[var(--color-secondary)]' : 'text-[var(--color-fg)]',
                    )}
                  >
                    <Telescope
                      size={16}
                      className={cn(effectiveMode !== 'deep-research' && 'text-[var(--color-fg-muted)]')}
                      aria-hidden
                    />
                    <span className="flex-1">{t('composer.research')}</span>
                    {effectiveMode === 'deep-research' ? <Check size={15} aria-hidden /> : null}
                  </button>
                </>
              ) : null}

              {verifyAvailable && !isImageMode ? (
                <button
                  type="button"
                  onClick={() => setVerify(!verify)}
                  aria-pressed={effectiveVerify}
                  className={cn(
                    'flex w-full items-center gap-2.5 rounded-[8px] px-2.5 py-2 text-left text-sm hover:bg-[var(--color-bg-muted)]',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                    effectiveVerify ? 'text-[var(--color-secondary)]' : 'text-[var(--color-fg)]',
                  )}
                >
                  <ShieldCheck size={16} className={cn(!effectiveVerify && 'text-[var(--color-fg-muted)]')} aria-hidden />
                  <span className="flex-1">{t('composer.verify', { defaultValue: 'Verify' })}</span>
                  {effectiveVerify ? <Check size={15} aria-hidden /> : null}
                </button>
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

            {researchEnabled && !isImageMode ? (
              <Tooltip content={t('composer.researchTooltip')}>
                <button
                  type="button"
                  onClick={() => setMode(effectiveMode === 'deep-research' ? 'default' : 'deep-research')}
                  aria-pressed={effectiveMode === 'deep-research'}
                  aria-label={t('composer.researchTooltip')}
                  className={cn(
                    'inline-flex items-center gap-1.5 h-8 px-2 rounded-[8px] text-[12px] font-medium interactive',
                    effectiveMode === 'deep-research'
                      ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
                      : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  )}
                >
                  <Telescope size={14} aria-hidden />
                  <span>{t('composer.research')}</span>
                </button>
              </Tooltip>
            ) : null}

            {verifyAvailable && !isImageMode ? (
              <Tooltip content={t('composer.verifyTooltip', { defaultValue: 'Have a second model fact-check the answer' })}>
                <button
                  type="button"
                  onClick={() => setVerify(!verify)}
                  aria-pressed={effectiveVerify}
                  aria-label={t('composer.verifyTooltip', { defaultValue: 'Have a second model fact-check the answer' })}
                  className={cn(
                    'inline-flex items-center gap-1.5 h-8 px-2 rounded-[8px] text-[12px] font-medium interactive',
                    effectiveVerify
                      ? 'bg-[var(--color-secondary-soft)] text-[var(--color-secondary)]'
                      : 'text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  )}
                >
                  <ShieldCheck size={14} aria-hidden />
                  <span>{t('composer.verify', { defaultValue: 'Verify' })}</span>
                </button>
              </Tooltip>
            ) : null}

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

          {/* Right zone — pinned, never wraps: model picker + send/stop. */}
          <div className="flex shrink-0 items-center gap-1.5 pl-1">
            <ModelPicker value={modelId} onChange={onModelChange} />
            {sendBtn}
          </div>
        </div>
      )}
    </div>
  )
}
