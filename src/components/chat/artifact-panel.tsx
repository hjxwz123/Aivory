/**
 * ArtifactPanel — the single right-edge surface for documents the user is
 * inspecting while chatting: live HTML produced by a code block, and files
 * (conversation attachments, sandbox outputs, knowledge-base documents).
 *
 * Desktop (≥1024px): an inline split panel on the right edge of the chat area,
 * so the conversation stays usable while markup streams in live or a PDF is
 * open. Mobile: the same content inside a right-side Sheet.
 *
 * The sandbox attributes, the injected `<base>`/CSP head and the reasoning
 * behind them live in SandboxedHtmlFrame, which is the app's single renderer
 * for untrusted HTML (the Files preview of a `.html` upload uses it too).
 */
import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Check, Download, ExternalLink, Eye, Link2, LoaderCircle, Maximize2, Minimize2, Pencil, RotateCw, Save } from 'lucide-react'
import { ChatSidePanel, ChatSidePanelHeader } from '@/components/chat/chat-side-panel'
import { DocumentPreview } from '@/components/files/document-preview'
import { DocumentEditor } from '@/components/files/editors/document-editor'
import { EditorErrorBoundary } from '@/components/files/editors/editor-error-boundary'
import { documentEditorFor, type EditedContent } from '@/components/files/editors/editor-types'
import { SandboxedHtmlFrame } from '@/components/html/sandboxed-html-frame'
import { Tooltip } from '@/components/ui/tooltip'
import { useHTMLPreviewShare } from '@/hooks/use-html-preview-share'
import { toast } from '@/hooks/use-toast'
import { assertNetworkOnline, getAccessToken } from '@/api/client'
import { authApi } from '@/api/endpoints'
import { documentPreviewKind } from '@/lib/file-preview-kind'
import { cn } from '@/lib/utils'
import { useArtifactPanel, type ArtifactSource } from '@/store/artifact-panel'

type FileSource = Extract<ArtifactSource, { type: 'file' }>

/**
 * HTML artifacts never offer "save a copy": the server upload allowlist rejects
 * `.html`/`.htm` by default because stored HTML served inline is an XSS vector
 * (see server/internal/api/upload_policy.go). The extension is the only reliable
 * signal — `Attachment['kind']` has no `html` member, so an `.html` sandbox
 * output arrives as `code`.
 */
function isHtmlArtifact(name: string): boolean {
  return /\.html?$/i.test(name)
}

export function ArtifactPanel() {
  const open = useArtifactPanel((s) => s.open)
  const source = useArtifactPanel((s) => s.source)
  const close = useArtifactPanel((s) => s.close)
  const { t } = useTranslation('chat')
  const { pathname } = useLocation()

  // Leaving the current page closes the panel — a drawer pinned to a
  // conversation shouldn't follow the user to the next one.
  const prevPath = useRef(pathname)
  useEffect(() => {
    if (prevPath.current === pathname) return
    prevPath.current = pathname
    close()
  }, [pathname, close])

  const file = source?.type === 'file' ? source : null
  const title = source?.type === 'file' ? source.name : t('code.previewTitle')

  return (
    <ChatSidePanel open={open} title={title} onClose={close}>
      {file ? (
        // Keyed per file so switching documents resets edit mode and any edits
        // rather than carrying one file's unsaved changes onto the next.
        <FileBody key={`${file.url}\n${file.name}`} file={file} onClose={close} />
      ) : (
        // `key` resets the debounced document only when a *different* HTML
        // source takes over — closing and reopening the same block still
        // re-renders its markup immediately (the debounce is skipped while
        // `doc` is empty), exactly like the pre-unification panel.
        <HtmlBody
          key={source?.type === 'html' ? source.sourceKey : 'html'}
          source={source}
          onClose={close}
        />
      )}
    </ChatSidePanel>
  )
}

interface HtmlBodyProps {
  source: ArtifactSource | null
  onClose: () => void
}

function HtmlBody({ source, onClose }: HtmlBodyProps) {
  const { t } = useTranslation('chat')
  const html = source?.type === 'html' ? source.html : ''
  const shareable = source?.type === 'html' ? source.shareable : false

  // Re-setting srcDoc reloads the whole document, so streaming markup is
  // applied on a trailing debounce: live enough to feel real-time, calm
  // enough not to flicker on every token.
  const [doc, setDoc] = useState('')
  useEffect(() => {
    const timer = setTimeout(() => setDoc(html), doc ? 350 : 0)
    return () => clearTimeout(timer)
  }, [html, doc])

  const rootRef = useRef<HTMLDivElement>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const [isFullscreen, setIsFullscreen] = useState(false)
  const previewShare = useHTMLPreviewShare(doc)

  // Keep local state in sync with the native fullscreen lifecycle (Esc exits).
  useEffect(() => {
    const onChange = () => setIsFullscreen(document.fullscreenElement === rootRef.current)
    document.addEventListener('fullscreenchange', onChange)
    return () => document.removeEventListener('fullscreenchange', onChange)
  }, [])

  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement) await document.exitFullscreen()
      else if (rootRef.current) await rootRef.current.requestFullscreen()
    } catch {
      /* fullscreen can be denied (permissions / unsupported) — no-op */
    }
  }

  return (
    <div ref={rootRef} className="flex h-full flex-col bg-[var(--color-bg)]">
      <ChatSidePanelHeader
        title={t('code.previewTitle')}
        closeLabel={t('code.previewClose')}
        onClose={onClose}
      >
        {shareable && doc === html ? (
          <Tooltip content={previewShare.copied ? t('code.previewLinkCopied') : t('code.copyPreviewLink')}>
            <button
              type="button"
              onClick={() => void previewShare.copyLink()}
              disabled={previewShare.sharing}
              aria-label={previewShare.copied ? t('code.previewLinkCopied') : t('code.copyPreviewLink')}
              className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)] disabled:pointer-events-none disabled:opacity-50"
            >
              {previewShare.sharing ? <LoaderCircle className="animate-spin" size={14} aria-hidden /> : previewShare.copied ? <Check size={14} aria-hidden /> : <Link2 size={14} aria-hidden />}
            </button>
          </Tooltip>
        ) : null}
        <Tooltip content={t(isFullscreen ? 'code.previewExitFullscreen' : 'code.previewFullscreen', { defaultValue: isFullscreen ? 'Exit fullscreen' : 'Fullscreen' })}>
          <button
            type="button"
            onClick={() => void toggleFullscreen()}
            aria-label={t(isFullscreen ? 'code.previewExitFullscreen' : 'code.previewFullscreen', { defaultValue: isFullscreen ? 'Exit fullscreen' : 'Fullscreen' })}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            {isFullscreen ? <Minimize2 size={14} aria-hidden /> : <Maximize2 size={14} aria-hidden />}
          </button>
        </Tooltip>
        <Tooltip content={t('code.previewRefresh')}>
          <button
            type="button"
            onClick={() => setReloadKey((k) => k + 1)}
            aria-label={t('code.previewRefresh')}
            className="inline-flex items-center justify-center size-8 rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
          >
            <RotateCw size={14} aria-hidden />
          </button>
        </Tooltip>
      </ChatSidePanelHeader>

      <div className="min-h-0 flex-1 bg-[var(--color-preview-canvas)]">
        <SandboxedHtmlFrame doc={doc} title={t('code.previewTitle')} reloadKey={reloadKey} />
      </div>
    </div>
  )
}

interface LoadedPreview {
  data?: ArrayBuffer
  objectUrl?: string
  mimeType?: string
  loading: boolean
  error?: string
}

function FileBody({ file, onClose }: { file: FileSource; onClose: () => void }) {
  const { t } = useTranslation(['chat', 'common', 'files'])
  const [attempt, setAttempt] = useState(0)
  const [preview, setPreview] = useState<LoadedPreview>({ loading: false })
  const objectUrlRef = useRef<string | null>(null)
  const [saving, setSaving] = useState(false)

  const { url, authenticated, kind, backendKind, name, onLoadError } = file

  useEffect(() => {
    if (!url) {
      setPreview({ loading: false })
      return
    }

    const controller = new AbortController()
    let disposed = false
    if (objectUrlRef.current) {
      URL.revokeObjectURL(objectUrlRef.current)
      objectUrlRef.current = null
    }
    setPreview({ loading: true })

    void (async () => {
      try {
        assertNetworkOnline()
        const token = authenticated ? getAccessToken() : null
        const response = await fetch(url, {
          credentials: 'include',
          headers: token ? { authorization: `Bearer ${token}` } : undefined,
          signal: controller.signal,
        })
        if (!response.ok) {
          onLoadError?.(response.status)
          throw new Error(`preview failed (${response.status})`)
        }
        const blob = await response.blob()
        const data = await blob.arrayBuffer()
        if (disposed) return
        const objectUrl = URL.createObjectURL(blob)
        objectUrlRef.current = objectUrl
        setPreview({ data, objectUrl, mimeType: blob.type, loading: false })
      } catch (error) {
        if (disposed || controller.signal.aborted) return
        setPreview({
          loading: false,
          error: error instanceof Error ? error.message : t('chat:filePreview.failed'),
        })
      }
    })()

    return () => {
      disposed = true
      controller.abort()
      if (objectUrlRef.current) {
        URL.revokeObjectURL(objectUrlRef.current)
        objectUrlRef.current = null
      }
    }
  }, [attempt, authenticated, url, onLoadError, t])

  const actionUrl = preview.objectUrl ?? (authenticated ? undefined : url)

  // `backendKind` (a tool artifact's real MIME type) wins when present; an
  // upload falls back to its backend `kind`, which is what it always passed.
  const previewKind = documentPreviewKind(name, preview.mimeType, backendKind || kind)
  const editor = documentEditorFor(name, previewKind)
  const [mode, setMode] = useState<'view' | 'edit'>('view')
  const [edited, setEdited] = useState<EditedContent | null>(null)
  /**
   * The serializing editors debounce, so `edited` trails the last keystroke.
   * Flushing here is what makes "edit then immediately save" persist the edit.
   */
  const editorFlushRef = useRef<(() => Promise<Blob | null>) | null>(null)

  // Markup shows its live preview beside the editor from the outset, seeded with
  // the original bytes until the first keystroke — otherwise the preview would
  // appear only after editing began, which reads as a glitch rather than a
  // preview.
  const originalMarkup = useMemo(() => {
    if (previewKind !== 'html' || !preview.data) return null
    return new TextDecoder('utf-8', { fatal: false }).decode(preview.data)
  }, [preview.data, previewKind])

  const liveMarkup = previewKind === 'html' ? (edited?.previewHtml ?? originalMarkup) : null

  /** Exactly what the user currently sees, edits included. */
  const currentBlob = useMemo(() => {
    if (edited) return edited.bytes
    if (!preview.data) return null
    return new Blob([preview.data], { type: preview.mimeType || 'application/octet-stream' })
  }, [edited, preview.data, preview.mimeType])

  // "Save a copy" re-uploads through POST /files. Stored HTML served inline is an
  // XSS vector, so the server allowlist rejects `.html`/`.htm` by default — that
  // action is therefore not offered for markup, which downloads the edited
  // result instead.
  async function saveCopy() {
    if (saving) return
    setSaving(true)
    try {
      const flushed = await editorFlushRef.current?.()
      const blob = flushed ?? currentBlob
      if (!blob) return
      const saved = await authApi.saveDocumentCopy(blob, name)
      toast.success(t('chat:filePreview.copySaved', { defaultValue: 'Saved as a copy', name: saved.filename }))
    } catch (error) {
      toast.error(
        t('chat:filePreview.copyFailed', { defaultValue: "Couldn't save a copy" }),
        error instanceof Error ? error.message : undefined,
      )
    } finally {
      setSaving(false)
    }
  }

  /** Downloads what the user currently has, edits included. */
  async function downloadCurrent() {
    const flushed = await editorFlushRef.current?.()
    const blob = flushed ?? currentBlob
    if (!blob) return
    const blobUrl = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = blobUrl
    anchor.download = name
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    // Give the click a tick before revoking so the download starts reliably.
    window.setTimeout(() => URL.revokeObjectURL(blobUrl), 4000)
  }

  return (
    <div className="flex h-full flex-col bg-[var(--color-bg)]">
      <ChatSidePanelHeader
        title={name}
        closeLabel={t('chat:code.previewClose')}
        onClose={onClose}
      >
        {editor ? (
          <Tooltip
            content={t(mode === 'edit' ? 'chat:filePreview.exitEdit' : 'chat:filePreview.enterEdit', {
              defaultValue: mode === 'edit' ? 'Preview' : 'Edit',
            })}
          >
            <button
              type="button"
              onClick={() => setMode((current) => (current === 'edit' ? 'view' : 'edit'))}
              aria-pressed={mode === 'edit'}
              aria-label={t(mode === 'edit' ? 'chat:filePreview.exitEdit' : 'chat:filePreview.enterEdit', {
                defaultValue: mode === 'edit' ? 'Preview' : 'Edit',
              })}
              className="interactive inline-flex size-8 items-center justify-center rounded-[8px] text-[var(--color-fg-muted)] hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
            >
              {mode === 'edit' ? <Eye size={14} aria-hidden /> : <Pencil size={14} aria-hidden />}
            </button>
          </Tooltip>
        ) : null}
      </ChatSidePanelHeader>

      <div className="min-h-0 flex-1 overflow-hidden border-t border-[var(--color-divider)] px-0 pb-0">
        {mode === 'edit' && editor && preview.data ? (
          // The editing surface is wrapped so a throwing editor degrades to a
          // readable notice instead of blanking the panel, and it is keyed by
          // file so switching documents rebuilds the editor rather than handing
          // a fresh document to an instance still holding the previous one.
          <EditorErrorBoundary
            name={name}
            title={t('chat:filePreview.editorFailed', {
              defaultValue: "This document couldn't be opened for editing.",
            })}
            retryLabel={t('common:actions.tryAgain', { defaultValue: 'Try again' })}
          >
            <div key={`${name}:edit`} className="flex h-full min-h-0 flex-col">
              {liveMarkup !== null ? (
                <div className="h-[45%] min-h-0 shrink-0 border-b border-[var(--color-divider)] bg-[var(--color-preview-canvas)]">
                  <SandboxedHtmlFrame doc={liveMarkup} title={name} />
                </div>
              ) : null}
              <div className="min-h-0 flex-1">
                <DocumentEditor
                  editor={editor}
                  name={name}
                  mimeType={preview.mimeType}
                  data={preview.data}
                  onChange={setEdited}
                  flushRef={editorFlushRef}
                />
              </div>
            </div>
          </EditorErrorBoundary>
        ) : (
          <EditorErrorBoundary
            name={name}
            title={t('chat:filePreview.previewFailed', {
              defaultValue: "This document couldn't be previewed.",
            })}
            retryLabel={t('common:actions.tryAgain', { defaultValue: 'Try again' })}
          >
            <DocumentPreview
              name={name}
              mimeType={preview.mimeType}
              backendKind={kind}
              data={preview.data}
              objectUrl={preview.objectUrl}
              loading={preview.loading}
              error={preview.error}
              onRetry={() => setAttempt((value) => value + 1)}
            />
          </EditorErrorBoundary>
        )}
      </div>

      <div className="flex shrink-0 items-center justify-end gap-2 border-t border-[var(--color-divider)] px-4 py-3">
        {actionUrl || edited ? (
          <>
            {actionUrl ? (
              <a
                href={actionUrl}
                target="_blank"
                rel="noreferrer"
                aria-label={t('chat:filePreview.open')}
                className={cn(
                  'inline-flex h-9 items-center gap-1.5 rounded-[10px] border border-[var(--color-border)] px-3.5 text-sm font-medium text-[var(--color-fg-muted)] interactive',
                  'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <ExternalLink size={14} aria-hidden />
                {t('chat:filePreview.open')}
              </a>
            ) : null}
            {isHtmlArtifact(name) ? null : (
              <button
                type="button"
                onClick={() => void saveCopy()}
                disabled={saving || !currentBlob}
                aria-label={t('chat:filePreview.saveCopy', { defaultValue: 'Save a copy' })}
                className={cn(
                  'inline-flex h-9 items-center gap-1.5 rounded-[10px] border border-[var(--color-border)] px-3.5 text-sm font-medium text-[var(--color-fg-muted)] interactive',
                  'hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                  'disabled:pointer-events-none disabled:opacity-50',
                )}
              >
                {saving ? <LoaderCircle className="animate-spin" size={14} aria-hidden /> : <Save size={14} aria-hidden />}
                {t('chat:filePreview.saveCopy', { defaultValue: 'Save a copy' })}
              </button>
            )}
            {edited ? (
              // With edits in hand the fetched object URL no longer describes what
              // the user sees, so download from the edited bytes instead.
              <button
                type="button"
                onClick={() => void downloadCurrent()}
                aria-label={t('chat:filePreview.download')}
                className={cn(
                  'inline-flex h-9 items-center gap-1.5 rounded-[10px] bg-[var(--color-fg)] px-3.5 text-sm font-medium text-[var(--color-fg-inverted)] interactive',
                  'hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <Download size={14} aria-hidden />
                {t('chat:filePreview.download')}
              </button>
            ) : actionUrl ? (
              <a
                href={actionUrl}
                download={name}
                aria-label={t('chat:filePreview.download')}
                className={cn(
                  'inline-flex h-9 items-center gap-1.5 rounded-[10px] bg-[var(--color-fg)] px-3.5 text-sm font-medium text-[var(--color-fg-inverted)] interactive',
                  'hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
                )}
              >
                <Download size={14} aria-hidden />
                {t('chat:filePreview.download')}
              </a>
            ) : null}
          </>
        ) : null}
      </div>
    </div>
  )
}
