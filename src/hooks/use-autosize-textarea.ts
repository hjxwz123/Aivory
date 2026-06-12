import { useEffect, type RefObject } from 'react'

/**
 * Resize a textarea to fit its content, capped at `maxRows` worth of line-height.
 */
export function useAutosizeTextarea(
  ref: RefObject<HTMLTextAreaElement | null>,
  value: string,
  maxRows = 12,
) {
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    const computed = getComputedStyle(el)
    const lineHeight = parseFloat(computed.lineHeight || '20')
    const max = lineHeight * maxRows
    const next = Math.min(el.scrollHeight, max)
    el.style.height = `${next}px`
    el.style.overflowY = el.scrollHeight > max ? 'auto' : 'hidden'
  }, [ref, value, maxRows])
}
