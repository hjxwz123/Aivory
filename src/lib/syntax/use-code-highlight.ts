import { useEffect, useMemo, useState } from 'react'
import { fallbackHighlight } from './fallback-highlight'
import { highlightCode, type HighlightEngine } from './shiki-client'
import { normalizeLanguage } from './shiki-languages'

export interface UseCodeHighlightOptions {
  code: string
  lang?: string
  live: boolean
  theme: 'light' | 'dark'
}

export interface UseCodeHighlightResult {
  html: string
  pending: boolean
  engine: HighlightEngine
}

export function useCodeHighlight({ code, lang, live, theme }: UseCodeHighlightOptions): UseCodeHighlightResult {
  const normalized = useMemo(() => normalizeLanguage(lang) ?? lang, [lang])
  const fallback = useMemo(() => fallbackHighlight(code, normalized), [code, normalized])
  const [state, setState] = useState<UseCodeHighlightResult>({
    html: fallback,
    pending: false,
    engine: 'fallback',
  })

  useEffect(() => {
    setState({ html: fallback, pending: !live, engine: 'fallback' })
    if (live) return

    const ctrl = new AbortController()
    void highlightCode({ code, lang: normalized, theme, signal: ctrl.signal }).then((result) => {
      if (ctrl.signal.aborted) return
      setState({
        html: result.html,
        pending: false,
        engine: result.engine,
      })
    })

    return () => ctrl.abort()
  }, [code, fallback, live, normalized, theme])

  return state
}
