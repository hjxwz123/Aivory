import {
  useEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from 'react'
import {
  clampSidebarWidth,
  SIDEBAR_WIDTH_DEFAULT,
  SIDEBAR_WIDTH_MAX,
  SIDEBAR_WIDTH_MIN,
  sidebarWidthForKey,
} from '@/lib/sidebar-width'

interface SidebarResizeHandleProps {
  label: string
  controlsId: string
  targetRef: RefObject<HTMLElement | null>
  width: number
  onCommit: (width: number) => void
}

export function SidebarResizeHandle({ label, controlsId, targetRef, width, onCommit }: SidebarResizeHandleProps) {
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

  function applyWidth(nextWidth: number) {
    const target = targetRef.current
    const handle = handleRef.current
    if (target) target.style.width = `${nextWidth}px`
    if (handle) handle.setAttribute('aria-valuenow', String(nextWidth))
  }

  function handlePointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    if (!event.isPrimary || event.button !== 0) return
    const target = targetRef.current
    if (!target) return

    event.preventDefault()
    finishRef.current()

    const handle = event.currentTarget
    const pointerId = event.pointerId
    const startX = event.clientX
    const startWidth = clampSidebarWidth(target.getBoundingClientRect().width || widthRef.current)
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

    try {
      handle.setPointerCapture(pointerId)
    } catch {
      // Window listeners below still keep the drag functional.
    }

    const handlePointerMove = (moveEvent: PointerEvent) => {
      if (moveEvent.pointerId !== pointerId) return
      currentWidth = clampSidebarWidth(startWidth + moveEvent.clientX - startX)
      applyWidth(currentWidth)
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
    const nextWidth = sidebarWidthForKey(widthRef.current, event.key)
    if (nextWidth === null) return
    event.preventDefault()
    applyWidth(nextWidth)
    onCommitRef.current(nextWidth)
  }

  function restoreDefault() {
    applyWidth(SIDEBAR_WIDTH_DEFAULT)
    onCommitRef.current(SIDEBAR_WIDTH_DEFAULT)
  }

  return (
    <div
      ref={handleRef}
      role="separator"
      aria-label={label}
      aria-controls={controlsId}
      aria-orientation="vertical"
      aria-valuemin={SIDEBAR_WIDTH_MIN}
      aria-valuemax={SIDEBAR_WIDTH_MAX}
      aria-valuenow={width}
      tabIndex={0}
      title={label}
      onPointerDown={handlePointerDown}
      onLostPointerCapture={() => finishRef.current()}
      onKeyDown={handleKeyDown}
      onDoubleClick={restoreDefault}
      className="group absolute inset-y-0 left-full z-[var(--z-raised)] w-2 cursor-col-resize touch-none select-none focus-visible:outline-none"
    >
      <span
        aria-hidden
        className="pointer-events-none absolute inset-y-0 left-0 w-px bg-transparent transition-colors duration-[var(--duration-fast)] group-hover:bg-[var(--color-border-strong)] group-focus-visible:bg-[var(--color-accent)] group-data-[resizing=true]:bg-[var(--color-accent)]"
      />
    </div>
  )
}
