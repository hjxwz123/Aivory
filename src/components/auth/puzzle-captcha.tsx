import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronsRight, RefreshCw, Check } from 'lucide-react'
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

interface PuzzleCaptchaProps {
  data: PuzzleData | null
  loading: boolean
  /** Fired with the final drop position as a fraction of the track (0–1). */
  onChange: (fraction: number | null) => void
  onRefresh: () => void
  invalid?: boolean
}

/**
 * PuzzleCaptcha — drag the piece into the gap. The handle's position along its
 * track IS the answer fraction (0–1), so it's resolution-independent: the piece
 * renders at `fraction × (track width)` and we submit the same fraction. The
 * server holds the true gap fraction and checks it on register.
 */
export function PuzzleCaptcha({ data, loading, onChange, onRefresh, invalid }: PuzzleCaptchaProps) {
  const { t } = useTranslation('auth')
  const trackRef = useRef<HTMLDivElement>(null)
  const [fraction, setFraction] = useState(0)
  const [dragging, setDragging] = useState(false)
  const [dropped, setDropped] = useState(false)

  // Reset whenever a fresh puzzle arrives.
  useEffect(() => {
    setFraction(0)
    setDropped(false)
  }, [data?.id])

  const pieceWidthPct = data ? (data.piece_size / data.w) * 100 : 0
  const pieceTopPct = data ? (data.piece_y / data.h) * 100 : 0
  const pieceLeftPct = fraction * (100 - pieceWidthPct)

  function fractionFromClientX(clientX: number): number {
    const track = trackRef.current
    if (!track) return 0
    const rect = track.getBoundingClientRect()
    const handleW = 40
    const usable = rect.width - handleW
    if (usable <= 0) return 0
    return Math.min(1, Math.max(0, (clientX - rect.left - handleW / 2) / usable))
  }

  function onPointerDown(e: React.PointerEvent) {
    if (!data) return
    e.preventDefault()
    ;(e.target as HTMLElement).setPointerCapture(e.pointerId)
    setDragging(true)
    setDropped(false)
    onChange(null)
    setFraction(fractionFromClientX(e.clientX))
  }
  function onPointerMove(e: React.PointerEvent) {
    if (!dragging) return
    setFraction(fractionFromClientX(e.clientX))
  }
  function onPointerUp(e: React.PointerEvent) {
    if (!dragging) return
    setDragging(false)
    setDropped(true)
    // Commit the value at the release position rather than the `fraction` state,
    // which can lag the final pointermove by a frame (stale-closure).
    const f = fractionFromClientX(e.clientX)
    setFraction(f)
    onChange(f)
  }

  function nudge(delta: number) {
    const next = Math.min(1, Math.max(0, fraction + delta))
    setFraction(next)
    setDropped(true)
    onChange(next)
  }

  return (
    <div className="flex flex-col gap-2">
      {/* Puzzle surface */}
      <div
        className={cn(
          'relative w-full overflow-hidden rounded-[10px] border bg-[var(--color-bg-muted)]',
          invalid ? 'border-[var(--color-danger)]' : 'border-[var(--color-border)]',
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
              className="absolute select-none drop-shadow-[0_1px_3px_rgba(0,0,0,0.35)]"
              style={{ width: `${pieceWidthPct}%`, top: `${pieceTopPct}%`, left: `${pieceLeftPct}%`, height: 'auto' }}
            />
          </>
        ) : (
          <div className="grid size-full place-items-center text-[12px] text-[var(--color-fg-subtle)]">
            {t('register.captchaLoading', { defaultValue: 'Loading…' })}
          </div>
        )}
        <button
          type="button"
          onClick={onRefresh}
          disabled={loading}
          aria-label={t('register.captchaRefresh')}
          className="absolute right-1.5 top-1.5 inline-flex size-7 items-center justify-center rounded-[8px] bg-[var(--color-surface)]/85 text-[var(--color-fg-muted)] backdrop-blur-sm hover:text-[var(--color-fg)] interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]"
        >
          <RefreshCw size={13} aria-hidden className={cn(loading && 'animate-[spin_0.8s_linear_infinite]')} />
        </button>
      </div>

      {/* Slider track */}
      <div
        ref={trackRef}
        className="relative h-10 w-full select-none rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)]"
      >
        <span className="pointer-events-none absolute inset-0 grid place-items-center text-[12px] text-[var(--color-fg-subtle)]">
          {dropped ? t('register.captchaDropped', { defaultValue: 'Release to verify on submit' }) : t('register.captchaSlide', { defaultValue: 'Slide to fit the piece' })}
        </span>
        <button
          type="button"
          role="slider"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(fraction * 100)}
          aria-label={t('register.captchaSlide', { defaultValue: 'Slide to fit the piece' })}
          disabled={!data}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onKeyDown={(e) => {
            if (e.key === 'ArrowRight') { e.preventDefault(); nudge(0.02) }
            if (e.key === 'ArrowLeft') { e.preventDefault(); nudge(-0.02) }
          }}
          className={cn(
            'absolute top-1/2 flex h-9 w-10 -translate-y-1/2 touch-none items-center justify-center rounded-[8px]',
            'border interactive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-ring)]',
            dropped
              ? 'border-[var(--color-accent)] bg-[var(--color-accent)] text-[var(--color-accent-fg)]'
              : 'border-[var(--color-border)] bg-[var(--color-surface)] text-[var(--color-fg-muted)] shadow-[var(--shadow-sm)]',
          )}
          style={{ left: `calc(${fraction} * (100% - 40px))` }}
        >
          {dropped ? <Check size={15} aria-hidden /> : <ChevronsRight size={15} aria-hidden />}
        </button>
      </div>
    </div>
  )
}
