/**
 * StarBorder — an orbiting glow that rings a CTA: two soft radial bands circle
 * the wrapper's top and bottom edges in counter-phase, so a warm highlight
 * appears to travel around whatever sits inside. Pure CSS keyframes — no rAF,
 * no animation lib. The component is a bare wrapper: it renders `children`
 * untouched and only paints the glow behind them. Under
 * `prefers-reduced-motion` the bands are dropped entirely and the static
 * children render alone.
 */
import {
  useInsertionEffect,
  type ComponentPropsWithoutRef,
  type CSSProperties,
  type ElementType,
  type ReactNode,
} from 'react'

import { useMediaQuery } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'

const STYLE_ID = 'aivory-star-border-style'
const LAYER_CLASS = 'aivory-star-border-layer'

// Band geometry (intrinsic to the effect): each band is 3× the wrapper's width
// and half its height, sunk almost fully past the clip so only a sliver of the
// radial glow rides the edge as it orbits.
const BAND_WIDTH = '300%'
const BAND_HEIGHT = '50%'
const BAND_START = '-250%'
const BOTTOM_SINK = -11
const TOP_SINK = -10

// One shared stylesheet for all instances: the two orbit keyframes (bands
// travel in opposite directions, fading as they exit) plus a CSS-level
// reduced-motion kill switch — covers the first paint before React hydrates,
// on top of the matchMedia gate below.
function useStarBorderStyles() {
  useInsertionEffect(() => {
    if (document.getElementById(STYLE_ID)) return
    const style = document.createElement('style')
    style.id = STYLE_ID
    style.textContent = `
@keyframes aivory-star-border-bottom {
  0% { transform: translate(0%, 0%); opacity: 1; }
  100% { transform: translate(-100%, 0%); opacity: 0; }
}
@keyframes aivory-star-border-top {
  0% { transform: translate(0%, 0%); opacity: 1; }
  100% { transform: translate(100%, 0%); opacity: 0; }
}
@media (prefers-reduced-motion: reduce) {
  .${LAYER_CLASS} { display: none !important; }
}
`
    document.head.appendChild(style)
  }, [])
}

interface StarBorderProps<T extends ElementType = 'div'> {
  /** Element or component the wrapper renders as. */
  as?: T
  /** Glow colour of the orbiting bands — keep it token-derived. */
  color?: string
  /** Duration of one edge-to-edge sweep (CSS time, e.g. '6s'). */
  speed?: CSSProperties['animationDuration']
  /** Vertical inset in px that lets the glow peek past the children. */
  thickness?: number
  children?: ReactNode
  className?: string
  style?: CSSProperties
}

export function StarBorder<T extends ElementType = 'div'>({
  as,
  color = 'var(--color-accent)',
  speed = '6s',
  thickness = 1,
  children,
  className,
  style,
  ...rest
}: StarBorderProps<T> & Omit<ComponentPropsWithoutRef<T>, keyof StarBorderProps<T>>) {
  const reduced = useMediaQuery('(prefers-reduced-motion: reduce)')
  const Component: ElementType = as ?? 'div'

  useStarBorderStyles()

  const band: CSSProperties = {
    width: BAND_WIDTH,
    height: BAND_HEIGHT,
    background: `radial-gradient(circle, ${color}, transparent 10%)`,
    animationDuration: speed,
    animationTimingFunction: 'linear',
    animationIterationCount: 'infinite',
    animationDirection: 'alternate',
  }

  return (
    <Component
      className={cn('relative inline-block overflow-hidden rounded-full', className)}
      style={{ padding: `${thickness}px 0`, ...style }}
      {...rest}
    >
      {!reduced && (
        <>
          <div
            aria-hidden
            className={cn(LAYER_CLASS, 'absolute z-0 rounded-full opacity-70')}
            style={{ ...band, bottom: BOTTOM_SINK, right: BAND_START, animationName: 'aivory-star-border-bottom' }}
          />
          <div
            aria-hidden
            className={cn(LAYER_CLASS, 'absolute z-0 rounded-full opacity-70')}
            style={{ ...band, top: TOP_SINK, left: BAND_START, animationName: 'aivory-star-border-top' }}
          />
        </>
      )}
      {/* h-full lets a stretched wrapper (e.g. an equal-height card grid) pass
          its height through to the child; content-sized wrappers are unaffected. */}
      <div className="relative z-10 h-full">{children}</div>
    </Component>
  )
}
