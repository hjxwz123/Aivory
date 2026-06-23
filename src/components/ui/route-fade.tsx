import { useEffect, useRef, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface RouteFadeProps {
  /** Replays the enter animation whenever this value changes (e.g. the route
   *  path, or a coarser section key so within-section navigation doesn't flash). */
  dep: string
  /** Layout classes for the wrapper — pass the filling classes when it sits in a
   *  flex column (e.g. `flex-1 min-h-0 flex flex-col`) so it doesn't collapse. */
  className?: string
  children: ReactNode
}

/**
 * RouteFade — a soft page-transition wrapper. On `dep` change it restarts a
 * fade-up CSS animation WITHOUT remounting its children, so route content keeps
 * its state (scroll, in-flight requests) while the swap feels less abrupt.
 * `prefers-reduced-motion` zeroes the animation globally (see globals.css).
 */
export function RouteFade({ dep, className, children }: RouteFadeProps) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.classList.remove('page-enter')
    void el.offsetWidth // force reflow so the animation restarts from frame 0
    el.classList.add('page-enter')
  }, [dep])
  return (
    <div ref={ref} className={cn('page-enter', className)}>
      {children}
    </div>
  )
}
