import { useMemo, useRef } from 'react'
import { gsap } from 'gsap'
import { useGSAP } from '@gsap/react'

gsap.registerPlugin(useGSAP)

/**
 * FlowField — the landing hero's signature graphic: a generative field of smooth
 * contour lines drawn in token ink. On load each line self-draws
 * (stroke-dashoffset); then they breathe out of phase while the whole field
 * drifts — a calm, cinematic, living backdrop rather than stacked text.
 *
 * Token-only colours, no neon/blur/3D. `prefers-reduced-motion` renders the
 * lines fully drawn and still (the useGSAP matchMedia branch).
 */
const W = 1200
const H = 520
const LINE_COUNT = 9

interface Line {
  d: string
  stroke: string
  width: number
}

// A smooth-ish sine contour across the full width, sampled densely enough that a
// thin stroke reads as a continuous curve.
function wavePath(y: number, amp: number, periods: number, phase: number): string {
  const steps = 28
  let d = ''
  for (let i = 0; i <= steps; i++) {
    const x = (i / steps) * W
    const yy = y + Math.sin((i / steps) * Math.PI * 2 * periods + phase) * amp
    d += `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${yy.toFixed(1)} `
  }
  return d.trim()
}

const FAINT = 'color-mix(in oklch, var(--color-fg) 13%, transparent)'
const ACCENT = 'color-mix(in oklch, var(--color-accent) 38%, transparent)'
const SECONDARY = 'color-mix(in oklch, var(--color-secondary) 30%, transparent)'

export function FlowField() {
  const root = useRef<SVGSVGElement>(null)

  const lines = useMemo<Line[]>(() => {
    const out: Line[] = []
    for (let i = 0; i < LINE_COUNT; i++) {
      const y = 50 + (i / (LINE_COUNT - 1)) * (H - 100)
      const amp = 16 + (i % 3) * 11
      const periods = 1.25 + (i % 4) * 0.22
      const phase = i * 0.7
      const accent = i === 2 || i === 6
      const secondary = i === 4
      out.push({
        d: wavePath(y, amp, periods, phase),
        stroke: accent ? ACCENT : secondary ? SECONDARY : FAINT,
        width: accent ? 1.6 : 1.1,
      })
    }
    return out
  }, [])

  useGSAP(
    () => {
      const mm = gsap.matchMedia()
      mm.add('(prefers-reduced-motion: no-preference)', () => {
        const paths = gsap.utils.toArray<SVGPathElement>('.ff-line')
        // Self-draw on load.
        paths.forEach((p) => {
          const len = p.getTotalLength()
          gsap.set(p, { strokeDasharray: len, strokeDashoffset: len })
        })
        gsap.to('.ff-line', { strokeDashoffset: 0, duration: 1.8, ease: 'power2.out', stagger: 0.12 })
        // Each line breathes vertically, out of phase → a flowing field.
        paths.forEach((p, i) => {
          gsap.to(p, {
            y: (i % 2 ? 1 : -1) * (7 + (i % 3) * 4),
            duration: 9 + i * 1.4,
            ease: 'sine.inOut',
            repeat: -1,
            yoyo: true,
            delay: i * 0.25,
          })
        })
        // The whole field drifts laterally — slow, cinematic.
        gsap.to('.ff-group', { x: 24, duration: 26, ease: 'sine.inOut', repeat: -1, yoyo: true })
      })
      mm.add('(prefers-reduced-motion: reduce)', () => {
        gsap.set('.ff-line', { strokeDashoffset: 0 })
      })
    },
    { scope: root },
  )

  return (
    <svg
      ref={root}
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="xMidYMid slice"
      className="absolute inset-0 size-full"
      aria-hidden
    >
      <g className="ff-group">
        {lines.map((l, i) => (
          <path
            key={i}
            className="ff-line"
            d={l.d}
            fill="none"
            stroke={l.stroke}
            strokeWidth={l.width}
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        ))}
      </g>
    </svg>
  )
}
