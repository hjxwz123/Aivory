import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ZoomIn, ZoomOut, RotateCcw, Maximize2, Download, X } from 'lucide-react'
import { useTheme } from '@/store/theme'
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { CodeBlock } from './code-block'
import { cn } from '@/lib/utils'

interface MermaidDiagramProps {
  code: string
  /** True while the owning message is still streaming (source incomplete). */
  live?: boolean
  className?: string
}

// One shared, lazily-loaded mermaid instance — keeps the ~500KB engine out of
// the main bundle (loaded only when a diagram actually appears).
let mermaidPromise: Promise<typeof import('mermaid').default> | null = null
function loadMermaid() {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then((m) => m.default)
  }
  return mermaidPromise
}

// Monotonic id for mermaid.render() targets (avoids Math.random; SSR-safe).
let renderSeq = 0

/**
 * MermaidDiagram renders a ```mermaid code block as an interactive SVG diagram.
 *
 * - Streams safely: while the message is still streaming the source is
 *   incomplete and would fail to parse, so we show the source code block until
 *   it settles, then render.
 * - Theme-aware: re-renders with mermaid's dark/default theme to match the app.
 * - Hostile-input safe: mermaid runs at securityLevel 'strict' (DOMPurify-
 *   sanitised SVG, no scripts/click handlers) — model output is untrusted.
 * - Degrades gracefully: a syntax error falls back to the source, never crashes
 *   the message.
 * - Interactive: zoom (buttons / ctrl+wheel), drag-to-pan, fit/reset, a
 *   fullscreen lightbox, and PNG / SVG export.
 */
export function MermaidDiagram({ code, live = false, className }: MermaidDiagramProps) {
  const { t } = useTranslation('chat')
  const theme = useTheme((s) => s.resolved)
  const [svg, setSvg] = useState('')
  const [failed, setFailed] = useState(false)
  const [fullscreen, setFullscreen] = useState(false)

  useEffect(() => {
    if (live || !code.trim()) {
      setSvg('')
      setFailed(false)
      return
    }
    let cancelled = false
    void (async () => {
      try {
        const mermaid = await loadMermaid()
        mermaid.initialize({
          startOnLoad: false,
          theme: theme === 'dark' ? 'dark' : 'default',
          securityLevel: 'strict',
        })
        renderSeq += 1
        const { svg: out } = await mermaid.render(`mermaid-${renderSeq}`, code)
        if (!cancelled) {
          setSvg(out)
          setFailed(false)
        }
      } catch {
        if (!cancelled) {
          setSvg('')
          setFailed(true)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [code, live, theme])

  // Streaming, render failure, or not yet rendered → show the source block.
  if (live || failed || !svg) {
    return (
      <div className={className}>
        <CodeBlock code={code} lang="mermaid" />
        {failed ? (
          <p className="mt-1 px-1 text-[11px] text-[var(--color-fg-subtle)]">
            {t('code.mermaidFailed', { defaultValue: 'Could not render this diagram — showing source.' })}
          </p>
        ) : null}
      </div>
    )
  }

  return (
    <div className={cn('my-3.5', className)}>
      <DiagramViewport svg={svg} onFullscreen={() => setFullscreen(true)} />
      <Dialog open={fullscreen} onOpenChange={setFullscreen}>
        <DialogContent
          size="full"
          showClose={false}
          className="h-[calc(100dvh-2rem)] p-0 overflow-hidden"
        >
          <DialogTitle className="sr-only">{t('diagram.title', { defaultValue: 'Diagram' })}</DialogTitle>
          <DialogDescription className="sr-only">
            {t('diagram.hint', { defaultValue: 'Scroll to zoom, drag to pan.' })}
          </DialogDescription>
          <DiagramViewport svg={svg} wheelZoom fullscreen onClose={() => setFullscreen(false)} className="h-full" />
        </DialogContent>
      </Dialog>
    </div>
  )
}

// ----- pan / zoom -----------------------------------------------------------

interface Transform {
  scale: number
  x: number
  y: number
}

const MIN_SCALE = 0.2
const MAX_SCALE = 12
const clampScale = (s: number) => Math.min(MAX_SCALE, Math.max(MIN_SCALE, s))

// "Fit to view" may upscale a small diagram up to this factor so it doesn't
// sit tiny in a wide viewport; larger diagrams still shrink to fit.
const FIT_MAX_SCALE = 4
// Breathing room (px) kept around a fitted diagram.
const FIT_PADDING = 24

function usePanZoom() {
  const [view, setView] = useState<Transform>({ scale: 1, x: 0, y: 0 })
  const viewRef = useRef(view)
  viewRef.current = view
  const drag = useRef<{ px: number; py: number; ox: number; oy: number } | null>(null)
  const [dragging, setDragging] = useState(false)

  // Zoom by `factor` keeping the point (clientX, clientY) fixed on screen.
  const zoomAt = useCallback((factor: number, clientX: number, clientY: number, rect: DOMRect) => {
    setView((prev) => {
      const scale = clampScale(prev.scale * factor)
      const k = scale / prev.scale
      const ox = clientX - rect.left
      const oy = clientY - rect.top
      return { scale, x: ox - k * (ox - prev.x), y: oy - k * (oy - prev.y) }
    })
  }, [])

  const onPointerDown = useCallback((e: React.PointerEvent) => {
    if (e.button !== 0) return
    const v = viewRef.current
    drag.current = { px: e.clientX, py: e.clientY, ox: v.x, oy: v.y }
    setDragging(true)
    try {
      ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
    } catch {
      /* capture is best-effort */
    }
  }, [])

  const onPointerMove = useCallback((e: React.PointerEvent) => {
    const d = drag.current
    if (!d) return
    setView((prev) => ({ ...prev, x: d.ox + (e.clientX - d.px), y: d.oy + (e.clientY - d.py) }))
  }, [])

  const onPointerUp = useCallback((e: React.PointerEvent) => {
    drag.current = null
    setDragging(false)
    try {
      ;(e.currentTarget as HTMLElement).releasePointerCapture(e.pointerId)
    } catch {
      /* may already be released */
    }
  }, [])

  return { view, setView, zoomAt, onPointerDown, onPointerMove, onPointerUp, dragging }
}

// ----- export helpers -------------------------------------------------------

function getSvgEl(container: HTMLElement | null): SVGSVGElement | null {
  return container?.querySelector('svg') ?? null
}

function svgNaturalSize(svg: SVGSVGElement): { w: number; h: number } {
  const vb = svg.viewBox?.baseVal
  if (vb && vb.width > 0 && vb.height > 0) return { w: vb.width, h: vb.height }
  const r = svg.getBoundingClientRect()
  return { w: Math.max(1, r.width), h: Math.max(1, r.height) }
}

function serializeSvg(svg: SVGSVGElement): string {
  const clone = svg.cloneNode(true) as SVGSVGElement
  if (!clone.getAttribute('xmlns')) clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg')
  const { w, h } = svgNaturalSize(svg)
  clone.setAttribute('width', String(Math.round(w)))
  clone.setAttribute('height', String(Math.round(h)))
  clone.style.removeProperty('max-width')
  return new XMLSerializer().serializeToString(clone)
}

function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 2000)
}

function exportSvgFile(svg: SVGSVGElement) {
  const xml = serializeSvg(svg)
  downloadBlob(new Blob([xml], { type: 'image/svg+xml;charset=utf-8' }), `diagram-${Date.now()}.svg`)
}

async function exportPngFile(svg: SVGSVGElement, bg: string) {
  const { w, h } = svgNaturalSize(svg)
  const xml = serializeSvg(svg)
  const src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(xml)
  const img = new Image()
  img.decoding = 'async'
  await new Promise<void>((resolve, reject) => {
    img.onload = () => resolve()
    img.onerror = () => reject(new Error('svg rasterisation failed'))
    img.src = src
  })
  // Render at >1× device pixels so the PNG stays crisp when opened/zoomed.
  const scale = Math.min(3, Math.max(1.5, window.devicePixelRatio || 1))
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(w * scale))
  canvas.height = Math.max(1, Math.round(h * scale))
  const ctx = canvas.getContext('2d')
  if (!ctx) return
  if (bg) {
    ctx.fillStyle = bg
    ctx.fillRect(0, 0, canvas.width, canvas.height)
  }
  ctx.drawImage(img, 0, 0, canvas.width, canvas.height)
  await new Promise<void>((resolve) => {
    canvas.toBlob((blob) => {
      if (blob) downloadBlob(blob, `diagram-${Date.now()}.png`)
      resolve()
    }, 'image/png')
  })
}

// ----- viewport -------------------------------------------------------------

interface DiagramViewportProps {
  svg: string
  /** Zoom on a plain wheel (fullscreen). Inline only zooms on ctrl/⌘+wheel so
   *  the page can still scroll over the diagram. */
  wheelZoom?: boolean
  /** Fill the parent's height (fullscreen) instead of the inline clamp. */
  fullscreen?: boolean
  /** Inline only: open the fullscreen lightbox. */
  onFullscreen?: () => void
  /** Fullscreen only: close the lightbox. */
  onClose?: () => void
  className?: string
}

const toolBtnCls = cn(
  'inline-flex items-center justify-center size-7 rounded-[7px] text-[var(--color-fg-muted)]',
  'transition-colors duration-150 hover:bg-[var(--color-bg-muted)] hover:text-[var(--color-fg)]',
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
)

function DiagramViewport({ svg, wheelZoom = false, fullscreen = false, onFullscreen, onClose, className }: DiagramViewportProps) {
  const { t } = useTranslation('chat')
  const theme = useTheme((s) => s.resolved)
  const containerRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  // Natural (viewBox) size of the injected SVG; drives the scaled wrapper.
  const [nat, setNat] = useState<{ w: number; h: number } | null>(null)
  const { view, setView, zoomAt, onPointerDown, onPointerMove, onPointerUp, dragging } = usePanZoom()

  // Center + scale the diagram to fill the viewport. Small diagrams are
  // upscaled (up to FIT_MAX_SCALE) instead of being left tiny; large ones
  // shrink to fit. Zoom is applied by resizing the SVG (see render), so a
  // fitted-up diagram stays crisp.
  const fit = useCallback(() => {
    const c = containerRef.current
    if (!c || !nat) return
    const cw = c.clientWidth
    const ch = c.clientHeight
    const { w, h } = nat
    if (w <= 0 || h <= 0 || cw <= 0 || ch <= 0) return
    const avail = Math.min((cw - FIT_PADDING) / w, (ch - FIT_PADDING) / h)
    const scale = clampScale(Math.min(avail, FIT_MAX_SCALE))
    setView({ scale, x: (cw - w * scale) / 2, y: (ch - h * scale) / 2 })
  }, [nat, setView])

  // Measure the injected SVG's natural (viewBox) size and strip mermaid's
  // inline width/height/max-width caps, then fit — all in a layout effect so
  // the sized wrapper is correct before the first paint (no zero-size flash).
  // The SVG then fills its scale-driven wrapper as a vector, so zooming
  // re-rasterises crisply instead of upscaling a cached bitmap.
  useLayoutEffect(() => {
    const c = containerRef.current
    const svgEl = getSvgEl(c)
    if (!c || !svgEl) {
      setNat(null)
      return
    }
    const { w, h } = svgNaturalSize(svgEl)
    svgEl.style.maxWidth = 'none'
    svgEl.style.removeProperty('width')
    svgEl.style.removeProperty('height')
    setNat({ w, h })
    const cw = c.clientWidth
    const ch = c.clientHeight
    if (w > 0 && h > 0 && cw > 0 && ch > 0) {
      const avail = Math.min((cw - FIT_PADDING) / w, (ch - FIT_PADDING) / h)
      const scale = clampScale(Math.min(avail, FIT_MAX_SCALE))
      setView({ scale, x: (cw - w * scale) / 2, y: (ch - h * scale) / 2 })
    }
  }, [svg, setView])

  // Wheel zoom via a non-passive native listener (React's onWheel is passive,
  // so preventDefault there is ignored).
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (!wheelZoom && !(e.ctrlKey || e.metaKey)) return
      e.preventDefault()
      const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12
      zoomAt(factor, e.clientX, e.clientY, el.getBoundingClientRect())
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [wheelZoom, zoomAt])

  const zoomCenter = useCallback(
    (factor: number) => {
      const c = containerRef.current
      if (!c) return
      const r = c.getBoundingClientRect()
      zoomAt(factor, r.left + r.width / 2, r.top + r.height / 2, r)
    },
    [zoomAt],
  )

  const backgroundColor = useCallback((): string => {
    const c = containerRef.current
    if (c) {
      const bg = getComputedStyle(c).backgroundColor
      if (bg && bg !== 'transparent' && bg !== 'rgba(0, 0, 0, 0)') return bg
    }
    return theme === 'dark' ? '#16181c' : '#ffffff'
  }, [theme])

  const onExportPng = useCallback(() => {
    const el = getSvgEl(containerRef.current)
    if (el) void exportPngFile(el, backgroundColor())
  }, [backgroundColor])

  const onExportSvg = useCallback(() => {
    const el = getSvgEl(containerRef.current)
    if (el) exportSvgFile(el)
  }, [])

  return (
    <div className={cn('relative', className)}>
      <div
        ref={containerRef}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerLeave={onPointerUp}
        onDoubleClick={fit}
        className={cn(
          'relative overflow-hidden rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)]',
          dragging ? 'cursor-grabbing' : 'cursor-grab',
          // Inline: let touch scroll the page; fullscreen: capture touch for pan.
          fullscreen ? 'h-full touch-none' : 'h-[clamp(220px,42vh,460px)]',
        )}
      >
        <div
          ref={contentRef}
          style={{
            // Pan only — a pure translate never rasterises, so it stays crisp.
            // Zoom lives in the wrapper's size below, not in this transform.
            transform: `translate(${view.x}px, ${view.y}px)`,
            transformOrigin: '0 0',
          }}
          className={cn('absolute left-0 top-0', dragging && 'will-change-transform')}
        >
          <div
            // Size the SVG box to natural × scale; the SVG fills it as a vector,
            // so the browser re-rasterises crisply at every zoom level.
            style={nat ? { width: nat.w * view.scale, height: nat.h * view.scale } : undefined}
            className="[&_svg]:block [&_svg]:!h-full [&_svg]:!w-full [&_svg]:!max-w-none"
            // SVG is sanitised by mermaid's securityLevel:'strict'.
            dangerouslySetInnerHTML={{ __html: svg }}
          />
        </div>
      </div>

      <div className="absolute right-2 top-2 flex items-center gap-0.5 rounded-[9px] border border-[var(--color-border)] bg-[var(--color-surface-raised)] p-0.5 shadow-[var(--shadow-md)]">
        <ToolbarButton label={t('diagram.zoomOut', { defaultValue: 'Zoom out' })} onClick={() => zoomCenter(1 / 1.2)}>
          <ZoomOut size={15} aria-hidden />
        </ToolbarButton>
        <span className="min-w-[2.75rem] text-center font-mono text-[11px] tabular-nums text-[var(--color-fg-subtle)] select-none">
          {Math.round(view.scale * 100)}%
        </span>
        <ToolbarButton label={t('diagram.zoomIn', { defaultValue: 'Zoom in' })} onClick={() => zoomCenter(1.2)}>
          <ZoomIn size={15} aria-hidden />
        </ToolbarButton>
        <ToolbarButton label={t('diagram.reset', { defaultValue: 'Fit to view' })} onClick={fit}>
          <RotateCcw size={14} aria-hidden />
        </ToolbarButton>
        <DropdownMenu>
          <DropdownMenuTrigger
            className={toolBtnCls}
            aria-label={t('diagram.download', { defaultValue: 'Download' })}
            title={t('diagram.download', { defaultValue: 'Download' })}
          >
            <Download size={15} aria-hidden />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={onExportPng}>
              {t('diagram.downloadPng', { defaultValue: 'Download PNG' })}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={onExportSvg}>
              {t('diagram.downloadSvg', { defaultValue: 'Download SVG' })}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        {onFullscreen ? (
          <ToolbarButton label={t('diagram.fullscreen', { defaultValue: 'Fullscreen' })} onClick={onFullscreen}>
            <Maximize2 size={14} aria-hidden />
          </ToolbarButton>
        ) : null}
        {onClose ? (
          <ToolbarButton label={t('diagram.close', { defaultValue: 'Close' })} onClick={onClose}>
            <X size={15} aria-hidden />
          </ToolbarButton>
        ) : null}
      </div>
    </div>
  )
}

interface ToolbarButtonProps {
  label: string
  onClick: () => void
  children: ReactNode
}

function ToolbarButton({ label, onClick, children }: ToolbarButtonProps) {
  return (
    <button type="button" onClick={onClick} aria-label={label} title={label} className={toolBtnCls}>
      {children}
    </button>
  )
}
