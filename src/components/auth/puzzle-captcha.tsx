import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronsRight, RefreshCw, Check, X, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'

/** The slider-puzzle challenge returned by GET /api/public/captcha. */
export interface PuzzleData {
  id: string
  background: string
  piece: string
  w: number
  h: number
  piece_size: number
  piece_y: number
}

/** idle → user is sliding; verifying → server is checking; success/error → result. */
export type PuzzleStatus = 'idle' | 'verifying' | 'success' | 'error'

export type PuzzleCaptchaPurpose = 'login' | 'register'

export interface PuzzleTracePoint {
  /** Normalized slider position in the 0..1 range. */
  x: number
  /** Milliseconds elapsed since the interaction began. */
  t: number
}

export interface PuzzleSolution {
  fraction: number
  interaction: {
    mode: 'pointer' | 'keyboard'
    points: PuzzleTracePoint[]
  }
}

interface PuzzleCaptchaProps {
  data: PuzzleData | null
  loading: boolean
  status: PuzzleStatus
  /** Fired with the final position and bounded interaction trace. */
  onChange: (solution: PuzzleSolution | null) => void
  onRefresh: () => void
}

const HANDLE_W = 44 // px — keep in sync with the size-11 handle below

/**
 * PuzzleCaptcha — drag the piece into the gap. The handle's position along its
 * track IS the answer fraction (0–1), resolution-independent: the piece renders
 * at `fraction × track` and we submit the same fraction; the server holds the
 * true gap fraction. Modern slider chrome: a coloured fill trails the handle and
 * the track reflects the verify result (green / red).
 */
export function PuzzleCaptcha({ data, loading, status, onChange, onRefresh }: PuzzleCaptchaProps) {
  const { t } = useTranslation('auth')
  const trackRef = useRef<HTMLDivElement>(null)
  const [fraction, setFraction] = useState(0)
  const [dragging, setDragging] = useState(false)
  const draggingRef = useRef(false)
  const interactionStartedRef = useRef<number | null>(null)
  const interactionModeRef = useRef<'pointer' | 'keyboard' | null>(null)
  const traceRef = useRef<PuzzleTracePoint[]>([])

  // Reset whenever a fresh puzzle arrives.
  useEffect(() => {
    setFraction(0)
    setDragging(false)
    draggingRef.current = false
    interactionStartedRef.current = null
    interactionModeRef.current = null
    traceRef.current = []
  }, [data?.id])

  const locked = loading || status !== 'idle'
  const pieceWidthPct = data ? (data.piece_size / data.w) * 100 : 0
  const pieceTopPct = data ? (data.piece_y / data.h) * 100 : 0
  const pieceLeftPct = fraction * (100 - pieceWidthPct)

  function fractionFromClientX(clientX: number): number {
    const track = trackRef.current
    if (!track) return 0
    const rect = track.getBoundingClientRect()
    const usable = rect.width - HANDLE_W
    if (usable <= 0) return 0
    return Math.min(1, Math.max(0, (clientX - rect.left - HANDLE_W / 2) / usable))
  }

  function beginInteraction(mode: 'pointer' | 'keyboard', x: number) {
    interactionModeRef.current = mode
    interactionStartedRef.current = performance.now()
    traceRef.current = [{ x, t: 0 }]
  }

  function recordTracePoint(x: number, force = false) {
    const started = interactionStartedRef.current
    if (started == null) return
    const t = Math.max(0, Math.round(performance.now() - started))
    const points = traceRef.current
    const previous = points[points.length - 1]
    if (!force && previous && t - previous.t < 12 && Math.abs(x - previous.x) < 0.004) return
    const point = { x, t }
    // Keep the request small even if a device emits very dense pointer events.
    // The final sample always replaces the tail when the buffer is full.
    if (points.length >= 64) points[points.length - 1] = point
    else points.push(point)
  }

  function finishInteraction(x: number) {
    const mode = interactionModeRef.current
    if (!mode) return
    recordTracePoint(x, true)
    onChange({ fraction: x, interaction: { mode, points: traceRef.current.slice() } })
  }

  function onPointerDown(e: React.PointerEvent) {
    if (!data || locked) return
    e.preventDefault()
    e.currentTarget.setPointerCapture(e.pointerId)
    const next = fractionFromClientX(e.clientX)
    beginInteraction('pointer', next)
    draggingRef.current = true
    setDragging(true)
    onChange(null)
    setFraction(next)
  }
  function onPointerMove(e: React.PointerEvent) {
    if (!draggingRef.current) return
    const next = fractionFromClientX(e.clientX)
    recordTracePoint(next)
    setFraction(next)
  }
  function onPointerUp(e: React.PointerEvent) {
    if (!draggingRef.current) return
    draggingRef.current = false
    setDragging(false)
    // Commit the release position (state can lag the final pointermove a frame).
    const f = fractionFromClientX(e.clientX)
    setFraction(f)
    finishInteraction(f)
  }
  function onPointerCancel() {
    if (!draggingRef.current) return
    draggingRef.current = false
    setDragging(false)
    setFraction(0)
    interactionStartedRef.current = null
    interactionModeRef.current = null
    traceRef.current = []
    onChange(null)
  }
  function nudge(delta: number) {
    if (locked) return
    if (interactionModeRef.current !== 'keyboard') beginInteraction('keyboard', fraction)
    const next = Math.min(1, Math.max(0, fraction + delta))
    setFraction(next)
    recordTracePoint(next, true)
    onChange(null)
  }
  function submitKeyboard() {
    if (locked || interactionModeRef.current !== 'keyboard') return
    finishInteraction(fraction)
  }

  const fillColor =
    status === 'success'
      ? 'bg-[var(--color-success-soft)]'
      : status === 'error'
        ? 'bg-[var(--color-danger-soft)]'
        : 'bg-[var(--color-accent-soft)]'

  const hint =
    status === 'verifying'
      ? t('register.captchaLoading')
      : status === 'success'
        ? t('register.captchaSuccess')
        : status === 'error'
          ? t('register.captchaWrong')
          : t('register.captchaSlide')

  return (
    <div className="flex flex-col gap-3">
      {/* Puzzle surface */}
      <div
        className={cn(
          'relative w-full overflow-hidden rounded-[12px] border bg-[var(--color-bg-muted)]',
          status === 'success'
            ? 'border-[var(--color-success)]'
            : status === 'error'
              ? 'border-[var(--color-danger)]'
              : 'border-[var(--color-border)]',
        )}
        style={{ aspectRatio: data ? `${data.w} / ${data.h}` : '280 / 160' }}
      >
        {data ? (
          <>
            <img src={data.background} alt="" className="block size-full select-none" draggable={false} />
            <img
              src={data.piece}
              alt=""
              draggable={false}
              className={cn(
                'absolute select-none drop-shadow-[0_2px_6px_rgba(0,0,0,0.45)]',
                !dragging && 'transition-[left] duration-150',
              )}
              style={{ width: `${pieceWidthPct}%`, top: `${pieceTopPct}%`, left: `${pieceLeftPct}%`, height: 'auto' }}
            />
          </>
        ) : (
          <div className="grid size-full place-items-center text-[12px] text-[var(--color-fg-subtle)]">
            <Loader2 size={18} aria-hidden className="animate-[spin_0.8s_linear_infinite]" />
          </div>
        )}
        {/* Refresh — dark glass chip, top-right (like a native slider captcha). */}
        <button
          type="button"
          onClick={onRefresh}
          disabled={loading || status === 'verifying'}
          aria-label={t('register.captchaRefresh')}
          className="absolute right-2 top-2 inline-flex size-9 items-center justify-center rounded-[10px] bg-black/45 text-white backdrop-blur-sm hover:bg-black/60 interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/60 disabled:opacity-60"
        >
          <RefreshCw size={15} aria-hidden className={cn(loading && 'animate-[spin_0.8s_linear_infinite]')} />
        </button>
      </div>

      {/* Slider track */}
      <div
        ref={trackRef}
        className={cn(
          'relative h-12 w-full select-none overflow-hidden rounded-[10px] border bg-[var(--color-bg-muted)]',
          status === 'error' ? 'border-[var(--color-danger)]' : 'border-[var(--color-border)]',
        )}
      >
        {/* Coloured fill trailing the handle. */}
        <span
          aria-hidden
          className={cn('pointer-events-none absolute inset-y-0 left-0 rounded-l-[10px]', fillColor)}
          style={{ width: `calc(${fraction} * (100% - ${HANDLE_W}px) + ${HANDLE_W}px)` }}
        />
        <span
          className={cn(
            'pointer-events-none absolute inset-0 grid place-items-center text-[13px]',
            status === 'success'
              ? 'text-[var(--color-success)]'
              : status === 'error'
                ? 'text-[var(--color-danger)]'
                : 'text-[var(--color-fg-subtle)]',
          )}
        >
          {hint}
        </span>
        <button
          type="button"
          role="slider"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(fraction * 100)}
          aria-label={t('register.captchaSlide')}
          disabled={!data || locked}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={onPointerCancel}
          onKeyDown={(e) => {
            if (e.key === 'ArrowRight') { e.preventDefault(); nudge(0.02) }
            if (e.key === 'ArrowLeft') { e.preventDefault(); nudge(-0.02) }
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); submitKeyboard() }
          }}
          className={cn(
            'absolute top-1/2 flex size-11 -translate-y-1/2 touch-none items-center justify-center rounded-[9px]',
            'shadow-[var(--shadow-md)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            !dragging && 'transition-[left] duration-150',
            status === 'success'
              ? 'bg-[var(--color-success)] text-white'
              : status === 'error'
                ? 'bg-[var(--color-danger)] text-white'
                : 'bg-[var(--color-surface)] text-[var(--color-fg-muted)]',
          )}
          style={{ left: `calc(${fraction} * (100% - ${HANDLE_W}px))` }}
        >
          {status === 'verifying' ? (
            <Loader2 size={17} aria-hidden className="animate-[spin_0.8s_linear_infinite]" />
          ) : status === 'success' ? (
            <Check size={17} aria-hidden />
          ) : status === 'error' ? (
            <X size={17} aria-hidden />
          ) : (
            <ChevronsRight size={18} aria-hidden />
          )}
        </button>
      </div>
    </div>
  )
}
