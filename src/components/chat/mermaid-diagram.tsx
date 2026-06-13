import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useTheme } from '@/store/theme'
import { CodeBlock } from './code-block'
import { cn } from '@/lib/utils'

interface MermaidDiagramProps {
  code: string
  /** True while the owning message is still streaming (source incomplete). */
  live?: boolean
  className?: string
}

// One shared, lazily-loaded mermaid instance — keeps the ~500KB engine out of
// the main bundle (loaded only when a diagram actually appears).
let mermaidPromise: Promise<typeof import('mermaid').default> | null = null
function loadMermaid() {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then((m) => m.default)
  }
  return mermaidPromise
}

// Monotonic id for mermaid.render() targets (avoids Math.random; SSR-safe).
let renderSeq = 0

/**
 * MermaidDiagram renders a ```mermaid code block as an SVG diagram.
 *
 * - Streams safely: while the message is still streaming the source is
 *   incomplete and would fail to parse, so we show the source code block until
 *   it settles, then render.
 * - Theme-aware: re-renders with mermaid's dark/default theme to match the app.
 * - Hostile-input safe: mermaid runs at securityLevel 'strict' (DOMPurify-
 *   sanitised SVG, no scripts/click handlers) — model output is untrusted.
 * - Degrades gracefully: a syntax error falls back to the source, never crashes
 *   the message.
 */
export function MermaidDiagram({ code, live = false, className }: MermaidDiagramProps) {
  const { t } = useTranslation('chat')
  const theme = useTheme((s) => s.resolved)
  const [svg, setSvg] = useState('')
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (live || !code.trim()) {
      setSvg('')
      setFailed(false)
      return
    }
    let cancelled = false
    void (async () => {
      try {
        const mermaid = await loadMermaid()
        mermaid.initialize({
          startOnLoad: false,
          theme: theme === 'dark' ? 'dark' : 'default',
          securityLevel: 'strict',
        })
        renderSeq += 1
        const { svg: out } = await mermaid.render(`mermaid-${renderSeq}`, code)
        if (!cancelled) {
          setSvg(out)
          setFailed(false)
        }
      } catch {
        if (!cancelled) {
          setSvg('')
          setFailed(true)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [code, live, theme])

  // Streaming, render failure, or not yet rendered → show the source block.
  if (live || failed || !svg) {
    return (
      <div className={className}>
        <CodeBlock code={code} lang="mermaid" />
        {failed ? (
          <p className="mt-1 px-1 text-[11px] text-[var(--color-fg-subtle)]">
            {t('code.mermaidFailed', { defaultValue: 'Could not render this diagram — showing source.' })}
          </p>
        ) : null}
      </div>
    )
  }

  return (
    <div
      role="img"
      className={cn(
        'my-3.5 overflow-x-auto rounded-[14px] border border-[var(--color-border)] bg-[var(--color-surface)] p-4',
        '[&_svg]:mx-auto [&_svg]:h-auto [&_svg]:max-w-full',
        className,
      )}
      // SVG is sanitised by mermaid's securityLevel:'strict'.
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  )
}
