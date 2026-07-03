import { useCallback, useEffect, useRef, type ReactNode } from 'react'

import { cn } from '@/lib/utils'

/**
 * ClickSpark — a page-level wrapper that fires a small radial burst from every
 * click: `sparkCount` ink strokes shoot outward from the pointer and shrink as
 * they travel. Drawn on a pointer-events-none canvas overlay, so everything
 * underneath keeps full interactivity; the wrapper is a plain `relative` block
 * that sizes to its content and adds no stacking side effects.
 *
 * The burst is driven by a document-level `click` listener, NOT an `onClick`
 * on the wrapper div. That matters on iOS Safari: a click handler on a large
 * <div> that wraps form fields makes the div a "clickable" tap target, which
 * can steal the first tap from a nested <input> — the input never focuses and
 * the soft keyboard never opens. A passive document listener touches no
 * element's clickability, so nested inputs focus (and raise the keyboard)
 * exactly as if the wrapper weren't there.
 *
 * `sparkColor` defaults to the clay accent token and is resolved from CSS
 * variables at burst time, so theme switches apply on the very next click.
 * Under `prefers-reduced-motion: reduce` clicks draw nothing — the effect is
 * pure ornament.
 */

interface ClickSparkProps {
  sparkColor?: string
  sparkSize?: number
  sparkRadius?: number
  sparkCount?: number
  /** Spark lifetime in ms. */
  duration?: number
  easing?: 'linear' | 'ease-in' | 'ease-out' | 'ease-in-out'
  extraScale?: number
  className?: string
  children?: ReactNode
}

interface Spark {
  x: number
  y: number
  angle: number
  startTime: number
}

export function ClickSpark({
  sparkColor = 'var(--color-accent)',
  sparkSize = 10,
  sparkRadius = 15,
  sparkCount = 8,
  duration = 400,
  easing = 'ease-out',
  extraScale = 1.0,
  className,
  children,
}: ClickSparkProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const sparksRef = useRef<Spark[]>([])
  // Concrete rgb() for the canvas, resolved from `sparkColor` per burst.
  const strokeRef = useRef('')
  const rafRef = useRef<number | null>(null)

  // Keep the canvas bitmap matched to the wrapper; debounced so continuous
  // resizes (window drags, layout animations) don't thrash the buffer.
  useEffect(() => {
    const canvas = canvasRef.current
    const parent = canvas?.parentElement
    if (!canvas || !parent) return

    let resizeTimeout: ReturnType<typeof setTimeout>

    const resizeCanvas = () => {
      const { width, height } = parent.getBoundingClientRect()
      if (canvas.width !== width || canvas.height !== height) {
        canvas.width = width
        canvas.height = height
      }
    }

    const ro = new ResizeObserver(() => {
      clearTimeout(resizeTimeout)
      resizeTimeout = setTimeout(resizeCanvas, 100)
    })
    ro.observe(parent)
    resizeCanvas()

    return () => {
      ro.disconnect()
      clearTimeout(resizeTimeout)
    }
  }, [])

  const easeFunc = useCallback(
    (t: number) => {
      switch (easing) {
        case 'linear':
          return t
        case 'ease-in':
          return t * t
        case 'ease-in-out':
          return t < 0.5 ? 2 * t * t : -1 + (4 - 2 * t) * t
        default:
          return t * (2 - t)
      }
    },
    [easing],
  )

  // One frame of the burst animation. Held in a ref so the rAF loop always
  // sees fresh props without re-subscribing; `tick` is the stable callable.
  const frameRef = useRef<(timestamp: number) => void>(() => {})
  const tick = useCallback((timestamp: number) => frameRef.current(timestamp), [])

  frameRef.current = (timestamp: number) => {
    const canvas = canvasRef.current
    const ctx = canvas?.getContext('2d')
    if (!canvas || !ctx) {
      rafRef.current = null
      return
    }

    ctx.clearRect(0, 0, canvas.width, canvas.height)
    ctx.strokeStyle = strokeRef.current
    ctx.lineWidth = 2

    sparksRef.current = sparksRef.current.filter((spark) => {
      const elapsed = timestamp - spark.startTime
      if (elapsed >= duration) return false

      const eased = easeFunc(elapsed / duration)
      // Each spark flies outward while its tail shrinks to nothing.
      const distance = eased * sparkRadius * extraScale
      const lineLength = sparkSize * (1 - eased)

      ctx.beginPath()
      ctx.moveTo(spark.x + distance * Math.cos(spark.angle), spark.y + distance * Math.sin(spark.angle))
      ctx.lineTo(
        spark.x + (distance + lineLength) * Math.cos(spark.angle),
        spark.y + (distance + lineLength) * Math.sin(spark.angle),
      )
      ctx.stroke()
      return true
    })

    // Self-stopping loop — no idle rAF burning frames between clicks.
    rafRef.current = sparksRef.current.length > 0 ? requestAnimationFrame(tick) : null
  }

  useEffect(
    () => () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current)
    },
    [],
  )

  // The burst logic lives in a ref so the once-registered document listener
  // always sees fresh props without re-subscribing (mirrors the rAF pattern).
  const burstRef = useRef<(clientX: number, clientY: number) => void>(() => {})
  burstRef.current = (clientX: number, clientY: number) => {
    // Reduced motion: the burst is pure ornament, so clicks draw nothing.
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const canvas = canvasRef.current
    if (!canvas) return
    const rect = canvas.getBoundingClientRect()
    // Ignore clicks outside this wrapper (e.g. a portalled dialog above it).
    if (clientX < rect.left || clientX > rect.right || clientY < rect.top || clientY > rect.bottom) return

    // Canvas strokeStyle can't take var(); route the prop through the canvas's
    // own `color` so getComputedStyle resolves vars / color-mix to concrete
    // rgb. Re-resolved every burst, which keeps theme flips one click fresh.
    canvas.style.color = sparkColor
    strokeRef.current = getComputedStyle(canvas).color

    const x = clientX - rect.left
    const y = clientY - rect.top
    const now = performance.now()
    for (let i = 0; i < sparkCount; i++) {
      sparksRef.current.push({ x, y, angle: (2 * Math.PI * i) / sparkCount, startTime: now })
    }
    if (rafRef.current === null) rafRef.current = requestAnimationFrame(tick)
  }

  // Document-level (not wrapper-onClick) so the wrapper never becomes a
  // clickable tap target — see the component doc: nested <input> keyboards on
  // iOS Safari depend on this.
  useEffect(() => {
    const onDocClick = (e: globalThis.MouseEvent) => burstRef.current(e.clientX, e.clientY)
    document.addEventListener('click', onDocClick)
    return () => document.removeEventListener('click', onDocClick)
  }, [])

  return (
    <div className={cn('relative', className)}>
      {children}
      {/* After children in DOM order so bursts paint above positioned page content. */}
      <canvas ref={canvasRef} className="absolute inset-0 pointer-events-none" aria-hidden />
    </div>
  )
}
