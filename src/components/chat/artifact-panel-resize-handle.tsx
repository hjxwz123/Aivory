import {
  useCallback,
  useEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react'
import {
  ARTIFACT_PANEL_DEFAULT_WIDTH,
  ARTIFACT_PANEL_MAX_WIDTH,
  ARTIFACT_PANEL_MIN_WIDTH,
  artifactPanelWidthForKey,
  clampArtifactPanelWidth,
} from '@/lib/artifact-panel-width'

interface ArtifactPanelResizeHandleProps {
  label: string
  /** id of the panel the separator sizes, for `aria-controls`. */
  controlsId: string
  /** The `<aside>` whose width tracks the drag. */
  targetRef: RefObject<HTMLElement | null>
  /** Committed width (px) — the value the store holds between drags. */
  width: number
  onCommit: (width: number) => void
  onReset: () => void
}

/**
 * Drag handle on the artifact panel's left edge.
 *
 * This is the "middle divider" of the split layout: dragging it left grows the
 * panel (HTML preview, Word/Excel/PowerPoint document, or the editor beside its
 * live preview), dragging right gives the space back to the conversation.
 *
 * Two details differ from the sidebar's handle on purpose:
 *
 * 1. The width is applied through the `--chat-side-panel-width` custom property
 *    rather than `target.style.width`, so the panel's open/close keyframe
 *    animation stays authoritative while it runs and the drag cannot fight it.
 * 2. `max-width` is clamped against the live window on every frame, so a
 *    restored preference can never squeeze the conversation out of the layout.
 */
export function ArtifactPanelResizeHandle({
  label,
  controlsId,
  targetRef,
  width,
  onCommit,
  onReset,
}: ArtifactPanelResizeHandleProps) {
  const handleRef = useRef<HTMLDivElement>(null)
  const widthRef = useRef(width)
  const onCommitRef = useRef(onCommit)
  const cleanupRef = useRef<() => void>(() => undefined)
  const finishRef = useRef<() => void>(() => undefined)

  widthRef.current = width
  onCommitRef.current = onCommit

  useEffect(() => {
    return () => cleanupRef.current()
  }, [])

  /** Mirrors the width into the custom property so the panel tracks the drag. */
  const applyWidth = useCallback(
    (nextWidth: number) => {
      const handle = handleRef.current
      const clamped = clampArtifactPanelWidth(nextWidth)
      if (typeof document !== 'undefined') {
        document.documentElement.style.setProperty('--chat-side-panel-width', `${clamped}px`)
      }
      if (handle) handle.setAttribute('aria-valuenow', String(clamped))
      return clamped
    },
    [],
  )

  // A viewport that shrinks under a wide saved panel must not push the
  // conversation column off screen — re-clamp and, when the clamp moved the
  // value, persist the new one so the next load is already correct.
  useEffect(() => {
    if (typeof window === 'undefined') return
    const onResize = () => {
      const clamped = clampArtifactPanelWidth(widthRef.current)
      document.documentElement.style.setProperty('--chat-side-panel-width', `${clamped}px`)
      if (clamped !== widthRef.current) onCommitRef.current(clamped)
    }
    onResize()
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  function handlePointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    if (!event.isPrimary || event.button !== 0) return
    const target = targetRef.current
    if (!target) return

    event.preventDefault()
    finishRef.current()

    const handle = event.currentTarget
    // The outer frame owns the width transition; flagging it keeps the drag
    // frame-exact instead of easing behind the pointer.
    const frame = handle.parentElement
    const pointerId = event.pointerId
    const startX = event.clientX
    const startWidth = clampArtifactPanelWidth(target.getBoundingClientRect().width || widthRef.current)
    let currentWidth = startWidth
    let finished = false

    const root = document.documentElement
    const body = document.body
    const previousUserSelect = root.style.userSelect
    const previousCursor = body.style.cursor

    root.style.userSelect = 'none'
    body.style.cursor = 'col-resize'
    target.dataset.resizing = 'true'
    handle.dataset.resizing = 'true'
    if (frame) frame.dataset.resizing = 'true'

    try {
      handle.setPointerCapture(pointerId)
    } catch {
      // Window listeners below still keep the drag functional.
    }

    const handlePointerMove = (moveEvent: PointerEvent) => {
      if (moveEvent.pointerId !== pointerId) return
      // The handle is on the panel's LEFT edge, so dragging left is wider.
      currentWidth = applyWidth(startWidth + (startX - moveEvent.clientX))
    }

    const cleanup = () => {
      if (finished) return
      finished = true
      window.removeEventListener('pointermove', handlePointerMove)
      window.removeEventListener('pointerup', handlePointerEnd)
      window.removeEventListener('pointercancel', handlePointerEnd)
      cleanupRef.current = () => undefined
      finishRef.current = () => undefined
      root.style.userSelect = previousUserSelect
      body.style.cursor = previousCursor
      delete target.dataset.resizing
      delete handle.dataset.resizing
      if (frame) delete frame.dataset.resizing
      try {
        if (handle.hasPointerCapture(pointerId)) handle.releasePointerCapture(pointerId)
      } catch {
        // The browser may already have released capture after cancellation.
      }
    }

    function handlePointerEnd(endEvent: PointerEvent) {
      if (endEvent.pointerId !== pointerId) return
      cleanup()
      onCommitRef.current(currentWidth)
    }

    const finish = () => {
      if (finished) return
      cleanup()
      onCommitRef.current(currentWidth)
    }

    cleanupRef.current = cleanup
    finishRef.current = finish
    window.addEventListener('pointermove', handlePointerMove)
    window.addEventListener('pointerup', handlePointerEnd)
    window.addEventListener('pointercancel', handlePointerEnd)
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Enter') {
      event.preventDefault()
      restoreDefault()
      return
    }
    const nextWidth = artifactPanelWidthForKey(widthRef.current, event.key)
    if (nextWidth === null) return
    event.preventDefault()
    applyWidth(nextWidth)
    onCommitRef.current(nextWidth)
  }

  function restoreDefault() {
    const next = applyWidth(ARTIFACT_PANEL_DEFAULT_WIDTH)
    onReset()
    onCommitRef.current(next)
  }

  return (
    <div
      ref={handleRef}
      role="separator"
      aria-label={label}
      aria-controls={controlsId}
      aria-orientation="vertical"
      aria-valuemin={ARTIFACT_PANEL_MIN_WIDTH}
      aria-valuemax={ARTIFACT_PANEL_MAX_WIDTH}
      aria-valuenow={width}
      aria-valuetext={`${Math.round(width)}px`}
      tabIndex={0}
      title={label}
      onPointerDown={handlePointerDown}
      onLostPointerCapture={() => finishRef.current()}
      onKeyDown={handleKeyDown}
      onDoubleClick={restoreDefault}
      className="group relative z-[var(--z-raised)] hidden h-full w-2 shrink-0 cursor-col-resize touch-none select-none focus-visible:outline-none lg:block"
    >
      <span
        aria-hidden
        className="pointer-events-none absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-[var(--color-divider)] transition-colors duration-[var(--duration-fast)] group-hover:bg-[var(--color-border-strong)] group-focus-visible:bg-[var(--color-accent)] group-data-[resizing=true]:bg-[var(--color-accent)]"
      />
    </div>
  )
}
