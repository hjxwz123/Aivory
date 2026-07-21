import { FileWarning } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { Skeleton } from '@/components/ui/skeleton'
import { MAX_OFFICE_PREVIEW_BYTES } from '@/lib/file-preview-kind'
import { preservePptxParagraphTextFills, removeExternalPptxResources } from '@/lib/pptx-security'

import type { PptxViewer as PptxViewerInstance } from '@aiden0z/pptx-renderer'

type PreviewStatus = 'loading' | 'ready' | 'error' | 'too-large'

export interface PptxNativePreviewLabels {
  loading: string
  error: string
  tooLarge: string
  slideError: string
}

export interface PptxNativePreviewProps {
  data: ArrayBuffer
  name: string
  labels: PptxNativePreviewLabels
  onError?: (error: Error) => void
}

function asError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error))
}

export function PptxNativePreview({ data, name, labels, onError }: PptxNativePreviewProps) {
  const scrollerRef = useRef<HTMLDivElement>(null)
  const hostRef = useRef<HTMLDivElement>(null)
  const viewerRef = useRef<PptxViewerInstance | null>(null)
  const onErrorRef = useRef(onError)
  const labelsRef = useRef(labels)
  const [status, setStatus] = useState<PreviewStatus>('loading')

  onErrorRef.current = onError
  labelsRef.current = labels

  useEffect(() => {
    const controller = new AbortController()
    const host = hostRef.current
    const scroller = scrollerRef.current
    let viewer: PptxViewerInstance | null = null

    viewerRef.current?.destroy()
    viewerRef.current = null
    host?.replaceChildren()
    setStatus('loading')

    if (data.byteLength > MAX_OFFICE_PREVIEW_BYTES) {
      const error = new Error(`PPTX preview exceeds the ${MAX_OFFICE_PREVIEW_BYTES}-byte limit: ${name}`)
      setStatus('too-large')
      onErrorRef.current?.(error)
      return () => {
        controller.abort()
        host?.replaceChildren()
      }
    }

    void (async () => {
      try {
        const {
          PptxViewer,
          RECOMMENDED_ZIP_LIMITS,
          buildPresentation,
          parseZipLazyMedia,
        } = await import(
          '@aiden0z/pptx-renderer/browser'
        )
        if (controller.signal.aborted || !host || !scroller) return

        const files = await parseZipLazyMedia(data.slice(0), RECOMMENDED_ZIP_LIMITS)
        if (controller.signal.aborted) return
        preservePptxParagraphTextFills(files)
        const presentation = buildPresentation(files, { lazySlides: true })
        removeExternalPptxResources(presentation)

        viewer = new PptxViewer(host, {
          fitMode: 'contain',
          lazyMedia: true,
          lazySlides: true,
          pdfjs: false,
          scrollContainer: scroller,
          zipLimits: RECOMMENDED_ZIP_LIMITS,
          onSlideError: (index, cause) => {
            onErrorRef.current?.(asError(cause))

            // The renderer has a built-in English fallback. Replace it after
            // its synchronous error handler finishes so all UI copy is local.
            queueMicrotask(() => {
              if (controller.signal.aborted) return
              const item = host.querySelector<HTMLElement>(`[data-slide-index="${index}"]`)
              const frame = item?.firstElementChild
              if (frame instanceof HTMLElement) {
                frame.textContent = labelsRef.current.slideError
                frame.style.background = 'var(--color-danger-soft)'
                frame.style.border = '1px solid var(--color-danger)'
                frame.style.boxShadow = 'var(--shadow-sm)'
                frame.style.color = 'var(--color-danger)'
                frame.style.lineHeight = '1.5'
                frame.style.padding = '1rem'
                frame.style.textAlign = 'center'
              }
            })
          },
          onSlideRendered: (_index, element) => {
            if (element.parentElement) element.parentElement.style.boxShadow = 'var(--shadow-sm)'
            for (const titledElement of element.querySelectorAll<HTMLElement>('[title]')) {
              if (/^Go to slide \d+$/.test(titledElement.title)) {
                titledElement.removeAttribute('title')
              }
            }
          },
        })
        viewerRef.current = viewer

        viewer.load(presentation)
        await viewer.renderList({
          batchSize: 4,
          initialSlides: 3,
          overscanViewport: 1.25,
          showSlideLabels: false,
          windowed: true,
        })

        if (!controller.signal.aborted) setStatus('ready')
      } catch (cause) {
        if (controller.signal.aborted) return
        const error = asError(cause)
        viewer?.destroy()
        host?.replaceChildren()
        setStatus(error.message.startsWith('PPTX zip limit exceeded:') ? 'too-large' : 'error')
        onErrorRef.current?.(error)
      }
    })()

    return () => {
      controller.abort()
      viewer?.destroy()
      if (viewerRef.current === viewer) viewerRef.current = null
      host?.replaceChildren()
    }
  }, [data, name])

  const failureLabel = status === 'too-large' ? labels.tooLarge : labels.error

  return (
    <div className="relative h-full min-h-[28rem] overflow-hidden bg-[var(--color-bg-muted)]">
      {status === 'loading' && (
        <div
          className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center gap-4 overflow-hidden px-4 py-8"
          role="status"
          aria-live="polite"
        >
          <span className="text-[13px] text-[var(--color-fg-muted)]">{labels.loading}</span>
          <Skeleton className="aspect-video w-full max-w-[52rem] shrink-0 rounded-[4px]" />
          <Skeleton className="aspect-video w-full max-w-[52rem] shrink-0 rounded-[4px]" />
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
        ref={scrollerRef}
        role="document"
        aria-label={name}
        aria-busy={status === 'loading'}
        inert={status !== 'ready'}
        className={[
          'h-full min-h-[28rem] overflow-auto overscroll-contain px-3 pt-3 sm:px-5 sm:pt-5',
          status === 'error' || status === 'too-large' ? 'hidden' : '',
          status === 'loading' ? 'pointer-events-none opacity-0' : '',
        ].join(' ')}
      >
        <div ref={hostRef} className="w-full min-w-0" />
      </div>
    </div>
  )
}
