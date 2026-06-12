import { useEffect, useRef } from 'react'
import { isMac } from '@/lib/utils'

type Combo = string

interface Handler {
  combo: Combo
  handler: (e: KeyboardEvent) => void
  /** If true, fire even when an input/textarea/contenteditable is focused. */
  whenInputFocused?: boolean
  /** If true, prevent default & stop propagation when the combo fires. */
  preventDefault?: boolean
}

function matches(combo: Combo, e: KeyboardEvent): boolean {
  const parts = combo.toLowerCase().split('+').map((p) => p.trim())
  const wantsMod = parts.includes('mod') || parts.includes('cmd') || parts.includes('ctrl')
  const wantsShift = parts.includes('shift')
  const wantsAlt = parts.includes('alt') || parts.includes('option')
  const key = parts.filter((p) => !['mod', 'cmd', 'ctrl', 'shift', 'alt', 'option', 'meta'].includes(p))[0]
  const modPressed = isMac() ? e.metaKey : e.ctrlKey
  const modOk = wantsMod ? modPressed : !modPressed
  const shiftOk = wantsShift ? e.shiftKey : !e.shiftKey
  const altOk = wantsAlt ? e.altKey : !e.altKey
  const k = (e.key || '').toLowerCase()
  return modOk && shiftOk && altOk && k === key
}

function isEditableTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false
  const tag = t.tagName
  return (
    tag === 'INPUT' ||
    tag === 'TEXTAREA' ||
    tag === 'SELECT' ||
    t.isContentEditable
  )
}

/**
 * Register a set of keyboard shortcuts. Handlers are read from a ref so the
 * effect can stay armed on a stable listener while consumers pass fresh
 * closures every render — no stale-closure bugs.
 */
export function useHotkeys(handlers: Handler[]) {
  const ref = useRef<Handler[]>(handlers)
  ref.current = handlers

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      for (const h of ref.current) {
        if (!h.whenInputFocused && isEditableTarget(e.target)) continue
        if (matches(h.combo, e)) {
          if (h.preventDefault !== false) {
            e.preventDefault()
            e.stopPropagation()
          }
          h.handler(e)
          return
        }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
}
