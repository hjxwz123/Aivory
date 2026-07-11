import { useInsertionEffect, type CSSProperties, type ReactNode } from 'react'

import { cn } from '@/lib/utils'

/**
 * GradientText — headline treatment: the text is filled with a slow clay→sage
 * gradient that pans back and forth (background-position yoyo), optionally
 * wrapped in a 1px gradient ring. Pure CSS keyframes — no rAF loop, no
 * animation lib. `prefers-reduced-motion` freezes the pan at its rest
 * position; the gradient fill itself stays visible.
 */

const STYLE_ID = 'aivory-gradient-text-style'
const PAN_CLASS = 'aivory-gradient-pan'

// One shared stylesheet for all instances: the yoyo keyframes plus the
// reduced-motion kill switch (!important so it beats the inline animation,
// leaving the inline rest position `0% 50%` as the static state).
function useGradientTextStyles() {
  useInsertionEffect(() => {
    if (document.getElementById(STYLE_ID)) return
    const style = document.createElement('style')
    style.id = STYLE_ID
    style.textContent = `
@keyframes aivory-gradient-pan {
  0%, 100% { background-position: 0% 50%; }
  50% { background-position: 100% 50%; }
}
@media (prefers-reduced-motion: reduce) {
  .${PAN_CLASS} { animation: none !important; }
}
`
    document.head.appendChild(style)
  }, [])
}

interface GradientTextProps {
  children: ReactNode
  /** Gradient stops — any CSS color strings; default sweeps clay → sage → clay. */
  colors?: string[]
  /** Seconds for one sweep in one direction (a full yoyo cycle is 2×). */
  animationSpeed?: number
  /** Draw a 1px gradient ring around the text (pill). */
  showBorder?: boolean
  className?: string
}

export function GradientText({
  children,
  colors = ['var(--color-accent)', 'var(--color-secondary)', 'var(--color-accent)'],
  animationSpeed = 8,
  showBorder = false,
  className,
}: GradientTextProps) {
  useGradientTextStyles()

  // A 3×-wide gradient pans behind the clipped glyphs; the default stops start
  // and end on clay so the yoyo turnarounds read as one continuous sweep.
  const gradient: CSSProperties = {
    backgroundImage: `linear-gradient(to right, ${colors.join(', ')})`,
    backgroundSize: '300% 100%',
    backgroundPosition: '0% 50%',
    animation: `aivory-gradient-pan ${animationSpeed * 2}s linear infinite`,
  }

  return (
    <div
      className={cn(
        'relative mx-auto flex w-fit items-center justify-center overflow-hidden rounded-full font-medium',
        showBorder && 'px-3 py-1',
        className,
      )}
    >
      {showBorder && (
        <div
          className={cn('pointer-events-none absolute inset-0 rounded-full', PAN_CLASS)}
          style={gradient}
          aria-hidden
        >
          {/* Inner plate in the page background, leaving a 1px gradient ring. */}
          <div className="absolute inset-px rounded-full bg-bg" />
        </div>
      )}
      <span
        className={cn('relative z-10 inline-block bg-clip-text text-transparent', PAN_CLASS)}
        style={{ ...gradient, WebkitBackgroundClip: 'text' }}
      >
        {children}
      </span>
    </div>
  )
}
