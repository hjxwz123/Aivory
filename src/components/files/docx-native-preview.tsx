import { FileWarning } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { Skeleton } from '@/components/ui/skeleton'
import { MAX_OFFICE_PREVIEW_BYTES } from '@/lib/file-preview-kind'
import { OoxmlArchiveLimitError, validateOoxmlArchive } from '@/lib/ooxml-archive'

type PreviewStatus = 'loading' | 'ready' | 'error' | 'too-large'

export interface DocxNativePreviewLabels {
  loading: string
  error: string
  tooLarge: string
}

export interface DocxNativePreviewProps {
  data: ArrayBuffer
  name: string
  labels: DocxNativePreviewLabels
  onError?: (error: Error) => void
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

function nextPaint() {
  return new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
}

function sanitizeDocumentLinks(root: ParentNode) {
  for (const anchor of root.querySelectorAll<HTMLAnchorElement>('a[href]')) {
    const href = anchor.getAttribute('href')?.trim() ?? ''
    const isDocumentAnchor = href.startsWith('#')
    const isAllowedExternalLink = /^(?:https?:|mailto:|tel:)/i.test(href)

    if (!isDocumentAnchor && !isAllowedExternalLink) {
      anchor.removeAttribute('href')
      anchor.removeAttribute('target')
      anchor.removeAttribute('rel')
      continue
    }

    anchor.setAttribute('href', href)
    anchor.setAttribute('rel', 'noopener noreferrer')
    if (!isDocumentAnchor && !anchor.hasAttribute('target')) anchor.setAttribute('target', '_blank')
  }
}

export function DocxNativePreview({ data, name, labels, onError }: DocxNativePreviewProps) {
  const hostRef = useRef<HTMLDivElement>(null)
  const onErrorRef = useRef(onError)
  const [status, setStatus] = useState<PreviewStatus>('loading')

  onErrorRef.current = onError

  useEffect(() => {
    const host = hostRef.current
    let disposed = false

    host?.replaceChildren()

    if (data.byteLength > MAX_OFFICE_PREVIEW_BYTES) {
      const error = new Error(`DOCX preview exceeds the ${MAX_OFFICE_PREVIEW_BYTES}-byte limit: ${name}`)
      setStatus('too-large')
      onErrorRef.current?.(error)
      return () => {
        disposed = true
        host?.replaceChildren()
      }
    }

    try {
      validateOoxmlArchive(data)
    } catch (cause) {
      const error = asError(cause)
      setStatus(cause instanceof OoxmlArchiveLimitError ? 'too-large' : 'error')
      onErrorRef.current?.(error)
      return () => {
        disposed = true
        host?.replaceChildren()
      }
    }

    setStatus('loading')

    void (async () => {
      try {
        const { renderAsync } = await import('docx-preview')
        if (disposed) return

        // Render away from the live tree so a superseded async render can never
        // append stale pages to the current preview.
        const staging = document.createElement('div')
        await renderAsync(data.slice(0), staging, staging, {
          breakPages: true,
          className: 'aivory-docx',
          debug: false,
          experimental: false,
          hideWrapperOnPrint: false,
          ignoreFonts: false,
          ignoreHeight: false,
          ignoreLastRenderedPageBreak: true,
          ignoreWidth: false,
          inWrapper: true,
          renderAltChunks: false,
          renderComments: false,
          renderEndnotes: true,
          renderFooters: true,
          renderFootnotes: true,
          renderHeaders: true,
          renderChanges: false,
          trimXmlDeclaration: true,
          useBase64URL: true,
        })

        if (disposed) {
          staging.replaceChildren()
          return
        }

        sanitizeDocumentLinks(staging)
        host?.replaceChildren(staging)
        // Moving the detached render tree into the document is what registers
        // its embedded @font-face rules. Keep the skeleton up until those fonts
        // have reached the paint pipeline, otherwise the first frame can show
        // only borders and list markers with all document text missing.
        await nextPaint()
        await document.fonts.ready
        await nextPaint()
        await nextPaint()
        if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
          // Chromium can report embedded fonts as loaded before their glyphs
          // reach the compositor when global motion is reduced. Keeping the
          // skeleton for one short settle window prevents a textless first
          // frame for users with reduced-motion enabled.
          await new Promise<void>((resolve) => window.setTimeout(resolve, 150))
          await nextPaint()
        }
        if (disposed) return
        setStatus('ready')
      } catch (cause) {
        if (disposed) return
        host?.replaceChildren()
        setStatus('error')
        onErrorRef.current?.(asError(cause))
      }
    })()

    return () => {
      disposed = true
      host?.replaceChildren()
    }
  }, [data, name])

  const failureLabel = status === 'too-large' ? labels.tooLarge : labels.error

  return (
    <div className="relative h-full min-h-[28rem] overflow-hidden bg-[var(--color-bg-muted)]">
      {status === 'loading' && (
        <div
          className="absolute inset-0 z-10 flex flex-col items-center gap-4 overflow-hidden bg-[var(--color-bg-muted)] px-4 py-8"
          role="status"
          aria-live="polite"
        >
          <span className="text-[13px] text-[var(--color-fg-muted)]">{labels.loading}</span>
          <Skeleton className="h-[38rem] w-full max-w-[48rem] shrink-0 rounded-[4px]" />
        </div>
      )}

      {(status === 'error' || status === 'too-large') && (
        <div className="flex h-full min-h-[28rem] items-center justify-center p-6" role="alert">
          <div className="max-w-md text-center">
            <FileWarning
              size={24}
              strokeWidth={1.6}
              className="mx-auto mb-3 text-[var(--color-danger)]"
              aria-hidden
            />
            <p className="text-sm leading-relaxed text-[var(--color-fg-muted)]">{failureLabel}</p>
          </div>
        </div>
      )}

      <div
        ref={hostRef}
        role="document"
        aria-label={name}
        aria-busy={status === 'loading'}
        className={[
          'h-full min-h-[28rem] overflow-auto overscroll-contain',
          status === 'error' || status === 'too-large' ? 'hidden' : '',
          '[&_.aivory-docx-wrapper]:!min-h-full [&_.aivory-docx-wrapper]:!bg-transparent',
          '[&_.aivory-docx-wrapper]:!p-4 sm:[&_.aivory-docx-wrapper]:!p-7',
          '[&_section.aivory-docx]:!mx-auto [&_section.aivory-docx]:!mb-5',
          '[&_section.aivory-docx]:!shadow-[var(--shadow-sm)]',
        ].join(' ')}
      />
    </div>
  )
}
