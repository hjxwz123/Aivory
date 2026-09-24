import { useEffect, useState } from 'react'

const QUERY = '(prefers-reduced-motion: reduce)'

function current(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia(QUERY).matches
}

/**
 * Track the user's reduced-motion preference so decorative animation (the AI PPT
 * rendering stage, hover lifts) can be skipped instead of forced.
 */
export function usePrefersReducedMotion(): boolean {
  const [reduce, setReduce] = useState(current)

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const media = window.matchMedia(QUERY)
    const handler = () => setReduce(media.matches)
    handler()
    media.addEventListener('change', handler)
    return () => media.removeEventListener('change', handler)
  }, [])

  return reduce
}
