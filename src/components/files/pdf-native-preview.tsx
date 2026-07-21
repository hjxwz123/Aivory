import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent } from 'react'
import type { PDFDocumentLoadingTask, PDFDocumentProxy, RenderTask } from 'pdfjs-dist'
import { ChevronLeft, ChevronRight, FileWarning, Maximize2, ZoomIn, ZoomOut } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

export interface PdfNativePreviewLabels {
  loading: string
  previousPage: string
  nextPage: string
  page: (page: number) => string
  of: (total: number) => string
  zoomOut: string
  zoomIn: string
  fitWidth: string
  renderFailed: string
}

export interface PdfNativePreviewProps {
  data: ArrayBuffer
  name: string
  className?: string
  labels: PdfNativePreviewLabels
  onError?: (message: string) => void
}

type PdfJsModule = typeof import('pdfjs-dist')
type PreviewStatus = 'loading' | 'ready' | 'error'

const MIN_SCALE = 0.25
const MIN_FIT_SCALE = 0.05
const MAX_SCALE = 4
const SCALE_STEP = 0.25
const CANVAS_PADDING = 32
const MAX_CANVAS_PIXELS = 16_777_216

let pdfJsPromise: Promise<PdfJsModule> | null = null

function loadPdfJs(): Promise<PdfJsModule> {
  if (!pdfJsPromise) {
    pdfJsPromise = Promise.all([
      import('pdfjs-dist'),
      import('pdfjs-dist/build/pdf.worker.min.mjs?url'),
    ])
      .then(([pdfJs, worker]) => {
        pdfJs.GlobalWorkerOptions.workerSrc = worker.default
        return pdfJs
      })
      .catch((error: unknown) => {
        pdfJsPromise = null
        throw error
      })
  }
  return pdfJsPromise
}

function clampScale(scale: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, scale))
}

function isRenderingCancelled(error: unknown): boolean {
  return error instanceof Error && error.name === 'RenderingCancelledException'
}

export function PdfNativePreview({ data, name, className, labels, onError }: PdfNativePreviewProps) {
  const [status, setStatus] = useState<PreviewStatus>('loading')
  const [document, setDocument] = useState<PDFDocumentProxy | null>(null)
  const [pageCount, setPageCount] = useState(0)
  const [pageNumber, setPageNumber] = useState(1)
  const [pageInput, setPageInput] = useState('1')
  const [manualScale, setManualScale] = useState(1)
  const [renderedScale, setRenderedScale] = useState(1)
  const [fitWidth, setFitWidth] = useState(true)
  const [availableWidth, setAvailableWidth] = useState(0)
  const [isRendering, setIsRendering] = useState(false)
  const [renderError, setRenderError] = useState(false)

  const canvasRef = useRef<HTMLCanvasElement>(null)
  const scrollAreaRef = useRef<HTMLDivElement>(null)
  const renderTaskRef = useRef<RenderTask | null>(null)
  const renderGenerationRef = useRef(0)
  const labelsRef = useRef(labels)
  const onErrorRef = useRef(onError)
  const pageInputId = useId()

  useEffect(() => {
    labelsRef.current = labels
    onErrorRef.current = onError
  }, [labels, onError])

  const reportError = useCallback(() => {
    const message = labelsRef.current.renderFailed
    onErrorRef.current?.(message)
  }, [])

  useEffect(() => {
    const node = scrollAreaRef.current
    if (!node) return

    let frame = 0
    const updateWidth = () => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        setAvailableWidth(Math.max(1, node.clientWidth - CANVAS_PADDING))
      })
    }

    updateWidth()
    const observer = new ResizeObserver(updateWidth)
    observer.observe(node)
    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    let loadingTask: PDFDocumentLoadingTask | null = null
    let loadedDocument: PDFDocumentProxy | null = null

    renderTaskRef.current?.cancel()
    renderGenerationRef.current += 1
    setStatus('loading')
    setDocument(null)
    setPageCount(0)
    setPageNumber(1)
    setPageInput('1')
    setManualScale(1)
    setRenderedScale(1)
    setFitWidth(true)
    setIsRendering(false)
    setRenderError(false)

    void (async () => {
      try {
        const pdfJs = await loadPdfJs()
        if (cancelled) return

        // PDF.js transfers typed-array ownership to its worker, so keep the
        // caller's ArrayBuffer intact for cache reuse and subsequent previews.
        loadingTask = pdfJs.getDocument({ data: new Uint8Array(data.slice(0)) })
        loadedDocument = await loadingTask.promise
        if (cancelled) return

        setDocument(loadedDocument)
        setPageCount(loadedDocument.numPages)
        setStatus('ready')
      } catch (error: unknown) {
        if (cancelled || isRenderingCancelled(error)) return
        if (loadingTask && !loadingTask.destroyed) {
          void loadingTask.destroy().catch(() => undefined)
        }
        setStatus('error')
        reportError()
      }
    })()

    return () => {
      cancelled = true
      renderTaskRef.current?.cancel()
      renderTaskRef.current = null
      renderGenerationRef.current += 1
      const destroy = loadedDocument?.destroy() ??
        (loadingTask && !loadingTask.destroyed ? loadingTask.destroy() : null)
      if (destroy) void destroy.catch(() => undefined)
    }
  }, [data, name, reportError])

  useLayoutEffect(() => {
    if (!document || status !== 'ready' || availableWidth <= 0) return

    const canvas = canvasRef.current
    if (!canvas) return

    const generation = ++renderGenerationRef.current
    let cancelled = false
    let renderTask: RenderTask | null = null

    renderTaskRef.current?.cancel()
    setIsRendering(true)
    setRenderError(false)

    void (async () => {
      try {
        const page = await document.getPage(pageNumber)
        if (cancelled || generation !== renderGenerationRef.current) return

        const baseViewport = page.getViewport({ scale: 1 })
        const scale = fitWidth
          ? Math.min(MAX_SCALE, Math.max(MIN_FIT_SCALE, availableWidth / baseViewport.width))
          : clampScale(manualScale)
        const viewport = page.getViewport({ scale })
        const deviceScale = window.devicePixelRatio || 1
        const pixelBudgetScale = Math.sqrt(MAX_CANVAS_PIXELS / Math.max(1, viewport.width * viewport.height))
        const outputScale = Math.max(0.1, Math.min(deviceScale, pixelBudgetScale))

        canvas.width = Math.max(1, Math.floor(viewport.width * outputScale))
        canvas.height = Math.max(1, Math.floor(viewport.height * outputScale))
        canvas.style.width = `${viewport.width}px`
        canvas.style.height = `${viewport.height}px`
        setRenderedScale(scale)

        renderTask = page.render({
          canvas,
          viewport,
          transform: outputScale === 1 ? undefined : [outputScale, 0, 0, outputScale, 0, 0],
        })
        renderTaskRef.current = renderTask
        await renderTask.promise

        if (cancelled || generation !== renderGenerationRef.current) return
        setIsRendering(false)
      } catch (error: unknown) {
        if (cancelled || generation !== renderGenerationRef.current || isRenderingCancelled(error)) return
        setIsRendering(false)
        setRenderError(true)
        reportError()
      } finally {
        if (renderTaskRef.current === renderTask) renderTaskRef.current = null
      }
    })()

    return () => {
      cancelled = true
      renderTask?.cancel()
      if (renderTaskRef.current === renderTask) renderTaskRef.current = null
    }
  }, [availableWidth, document, fitWidth, manualScale, name, pageNumber, reportError, status])

  useEffect(() => {
    setPageInput(String(pageNumber))
  }, [pageNumber])

  const goToPage = useCallback(
    (nextPage: number) => {
      if (pageCount <= 0) return
      setPageNumber(Math.min(pageCount, Math.max(1, nextPage)))
    },
    [pageCount],
  )

  const commitPageInput = useCallback(() => {
    const parsed = Number.parseInt(pageInput, 10)
    if (Number.isFinite(parsed)) {
      const nextPage = Math.min(pageCount, Math.max(1, parsed))
      setPageNumber(nextPage)
      setPageInput(String(nextPage))
      return
    }
    setPageInput(String(pageNumber))
  }, [pageCount, pageInput, pageNumber])

  const handlePageInputKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter') {
      event.preventDefault()
      commitPageInput()
      event.currentTarget.select()
    } else if (event.key === 'Escape') {
      event.preventDefault()
      setPageInput(String(pageNumber))
      event.currentTarget.blur()
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      goToPage(pageNumber + 1)
    } else if (event.key === 'ArrowDown') {
      event.preventDefault()
      goToPage(pageNumber - 1)
    } else if (event.key === 'Home') {
      event.preventDefault()
      goToPage(1)
    } else if (event.key === 'End') {
      event.preventDefault()
      goToPage(pageCount)
    }
  }

  const changeZoom = useCallback(
    (direction: -1 | 1) => {
      const nextScale = clampScale(renderedScale + direction * SCALE_STEP)
      setFitWidth(false)
      setManualScale(nextScale)
    },
    [renderedScale],
  )

  const controlsDisabled = status !== 'ready' || pageCount <= 0
  const zoomPercent = Math.round(renderedScale * 100)

  return (
    <section
      className={cn('flex min-h-0 min-w-0 flex-col overflow-hidden bg-[var(--color-surface)]', className)}
      aria-busy={status === 'loading' || isRendering || undefined}
    >
      <div
        className="flex min-h-12 flex-wrap items-center justify-center gap-x-2 gap-y-1 border-b border-[var(--color-border)] px-2 py-1.5 sm:justify-between"
        role="group"
        aria-label={name}
      >
        <div className="flex items-center gap-1">
          <span className="sr-only" aria-live="polite">
            {labels.page(pageNumber)}, {labels.of(pageCount || 1)}
          </span>
          <Tooltip content={labels.previousPage} side="bottom">
            <Button
              variant="ghost"
              size="icon"
              className="[@media(pointer:coarse)]:size-11"
              aria-label={labels.previousPage}
              disabled={controlsDisabled || pageNumber <= 1}
              onClick={() => goToPage(pageNumber - 1)}
            >
              <ChevronLeft className="size-4" aria-hidden />
            </Button>
          </Tooltip>

          <div className="flex h-9 items-center gap-1 rounded-[8px] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 text-sm tabular-nums [@media(pointer:coarse)]:h-11">
            <label className="sr-only" htmlFor={pageInputId}>
              {labels.page(pageNumber)}
            </label>
            <input
              id={pageInputId}
              className="w-10 rounded-[5px] bg-transparent text-center text-[var(--color-fg)] outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
              type="text"
              inputMode="numeric"
              pattern="[0-9]*"
              value={pageInput}
              role="spinbutton"
              aria-label={labels.page(pageNumber)}
              aria-valuemin={1}
              aria-valuemax={pageCount || 1}
              aria-valuenow={pageNumber}
              disabled={controlsDisabled}
              onChange={(event) => {
                if (/^\d*$/.test(event.target.value)) setPageInput(event.target.value)
              }}
              onBlur={commitPageInput}
              onFocus={(event) => event.currentTarget.select()}
              onKeyDown={handlePageInputKeyDown}
            />
            <span className="whitespace-nowrap text-[var(--color-fg-muted)]" aria-hidden>
              {labels.of(pageCount || 1)}
            </span>
          </div>

          <Tooltip content={labels.nextPage} side="bottom">
            <Button
              variant="ghost"
              size="icon"
              className="[@media(pointer:coarse)]:size-11"
              aria-label={labels.nextPage}
              disabled={controlsDisabled || pageNumber >= pageCount}
              onClick={() => goToPage(pageNumber + 1)}
            >
              <ChevronRight className="size-4" aria-hidden />
            </Button>
          </Tooltip>
        </div>

        <div className="flex items-center gap-1">
          <Tooltip content={labels.zoomOut} side="bottom">
            <Button
              variant="ghost"
              size="icon"
              className="[@media(pointer:coarse)]:size-11"
              aria-label={labels.zoomOut}
              disabled={controlsDisabled || renderedScale <= MIN_SCALE}
              onClick={() => changeZoom(-1)}
            >
              <ZoomOut className="size-4" aria-hidden />
            </Button>
          </Tooltip>

          <output
            className="w-12 text-center text-xs tabular-nums text-[var(--color-fg-muted)]"
            aria-live="polite"
          >
            {zoomPercent}%
          </output>

          <Tooltip content={labels.zoomIn} side="bottom">
            <Button
              variant="ghost"
              size="icon"
              className="[@media(pointer:coarse)]:size-11"
              aria-label={labels.zoomIn}
              disabled={controlsDisabled || renderedScale >= MAX_SCALE}
              onClick={() => changeZoom(1)}
            >
              <ZoomIn className="size-4" aria-hidden />
            </Button>
          </Tooltip>

          <Tooltip content={labels.fitWidth} side="bottom">
            <Button
              variant={fitWidth ? 'secondary' : 'ghost'}
              size="icon"
              className="[@media(pointer:coarse)]:size-11"
              aria-label={labels.fitWidth}
              aria-pressed={fitWidth}
              disabled={controlsDisabled}
              onClick={() => setFitWidth(true)}
            >
              <Maximize2 className="size-4" aria-hidden />
            </Button>
          </Tooltip>
        </div>
      </div>

      <div
        ref={scrollAreaRef}
        className="relative min-h-0 flex-1 overflow-auto bg-[var(--color-preview-canvas)]"
        tabIndex={status === 'ready' ? 0 : -1}
        aria-label={name}
      >
        {status === 'loading' ? (
          <div className="flex min-h-full items-start justify-center p-4" role="status" aria-label={labels.loading}>
            <span className="sr-only">{labels.loading}</span>
            <Skeleton className="aspect-[3/4] h-auto max-h-full w-full max-w-[38rem]" />
          </div>
        ) : status === 'error' ? (
          <div className="flex min-h-full items-center justify-center p-6" role="alert">
            <div className="flex max-w-sm flex-col items-center gap-3 text-center text-[var(--color-fg-muted)]">
              <FileWarning className="size-8 text-[var(--color-danger)]" aria-hidden />
              <p className="text-sm leading-6">{labels.renderFailed}</p>
            </div>
          </div>
        ) : (
          <div className="min-h-full w-max min-w-full p-4">
            <canvas
              ref={canvasRef}
              className={cn(
                'mx-auto block bg-white shadow-[var(--shadow-sm)]',
                (isRendering || renderError) && 'invisible',
              )}
              role="img"
              aria-label={`${name}, ${labels.page(pageNumber)}, ${labels.of(pageCount)}`}
            />
            {isRendering && !renderError ? (
              <div className="absolute inset-4 flex items-start justify-center" role="status" aria-label={labels.loading}>
                <span className="sr-only">{labels.loading}</span>
                <Skeleton className="aspect-[3/4] h-auto max-h-full w-full max-w-[38rem]" />
              </div>
            ) : null}
            {renderError ? (
              <div className="absolute inset-0 flex items-center justify-center p-6" role="alert">
                <div className="flex max-w-sm flex-col items-center gap-3 text-center text-[var(--color-fg-muted)]">
                  <FileWarning className="size-8 text-[var(--color-danger)]" aria-hidden />
                  <p className="text-sm leading-6">{labels.renderFailed}</p>
                </div>
              </div>
            ) : null}
          </div>
        )}
      </div>
    </section>
  )
}
