import { memo, useDeferredValue, useEffect, useMemo, useState } from 'react'
import { tokenizeMarkdown, inlineMarkdownToHtml } from '@/lib/markdown'
import { CodeBlock } from './code-block'
import { MermaidDiagram } from './mermaid-diagram'
import { cn } from '@/lib/utils'

interface MarkdownProps {
  content: string
  className?: string
  /** True while the owning message is still streaming (drives live HTML preview). */
  live?: boolean
  /** Stable prefix (message id) so code blocks keep their identity across remounts. */
  blockKeyPrefix?: string
}

function HeadingTag({ depth, html, className }: { depth: number; html: string; className?: string }) {
  const base = 'font-serif tracking-tight text-[var(--color-fg)]'
  const inner = <span dangerouslySetInnerHTML={{ __html: html }} />
  switch (depth) {
    case 1:
      return <h1 className={cn(base, 'text-2xl mt-6', className)}>{inner}</h1>
    case 2:
      return <h2 className={cn(base, 'text-xl mt-6', className)}>{inner}</h2>
    case 3:
      return <h3 className={cn(base, 'text-lg mt-5', className)}>{inner}</h3>
    case 4:
      return <h4 className={cn('font-sans font-semibold text-base mt-4 text-[var(--color-fg)]', className)}>{inner}</h4>
    default:
      return <h5 className={cn('font-sans font-semibold text-sm mt-3 text-[var(--color-fg)]', className)}>{inner}</h5>
  }
}

/**
 * useThrottledContent caps how often `content` is recomputed during a stream.
 * Streaming token-by-token re-renders trigger a full markdown re-parse on
 * every keystroke (16ms cadence with a fast model) which dominates CPU. We
 * use React's `useDeferredValue` PLUS a 50ms wall-clock floor so the rendered
 * value stops trying to keep up with the slot at sub-frame granularity.
 *
 * Final value (when the stream ends) is always flushed verbatim.
 */
function useThrottledContent(content: string, intervalMs = 50): string {
  const deferred = useDeferredValue(content)
  const [snap, setSnap] = useState(deferred)
  useEffect(() => {
    if (deferred === snap) return
    const t = setTimeout(() => setSnap(deferred), intervalMs)
    return () => clearTimeout(t)
  }, [deferred, snap, intervalMs])
  return snap
}

export const Markdown = memo(function Markdown({ content, className, live = false, blockKeyPrefix }: MarkdownProps) {
  const throttled = useThrottledContent(content)
  const blocks = useMemo(() => tokenizeMarkdown(throttled), [throttled])
  if (!content) return null

  // While streaming, each block fades in ONCE when it first appears, so a reply
  // arrives as a calm block-by-block reveal instead of a per-token jitter.
  // React reuses the DOM node for an already-rendered block (stable key), so
  // growing the last block's text does NOT replay the animation — only a
  // genuinely new block animates. `backwards` holds opacity:0 before the first
  // frame so a block never flashes fully-opaque then fades. Honors
  // prefers-reduced-motion via the global media query in globals.css.
  const blockAnim = live ? 'animate-[fade-in_500ms_var(--ease-out)_backwards]' : undefined

  return (
    <div className={cn('prose-aurelia', className)}>
      {blocks.map((b, i) => {
        switch (b.type) {
          case 'heading':
            return <HeadingTag key={i} depth={b.depth ?? 2} html={inlineMarkdownToHtml(b.content)} className={blockAnim} />
          case 'paragraph':
            return (
              <p
                key={i}
                className={cn('leading-relaxed text-[var(--color-fg)]', blockAnim)}
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content) }}
              />
            )
          case 'list':
          case 'ordered-list':
            return (
              <div
                key={i}
                className={cn(
                  'space-y-1.5 text-[var(--color-fg)]',
                  b.type === 'ordered-list' ? 'list-decimal' : 'list-disc',
                  blockAnim,
                )}
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content) }}
              />
            )
          case 'code':
            if ((b.lang ?? '').toLowerCase() === 'mermaid') {
              return <MermaidDiagram key={i} code={b.content} live={live} className={blockAnim} />
            }
            return (
              <CodeBlock
                key={i}
                code={b.content}
                lang={b.lang}
                live={live}
                className={blockAnim}
                previewKey={blockKeyPrefix ? `${blockKeyPrefix}#${i}` : undefined}
              />
            )
          case 'blockquote':
            return (
              <blockquote
                key={i}
                className={cn(
                  'border-l-2 border-[var(--color-border-strong)] pl-4 text-[var(--color-fg-muted)] italic',
                  blockAnim,
                )}
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content) }}
              />
            )
          case 'hr':
            return <hr key={i} className={cn('my-6 border-[var(--color-divider)]', blockAnim)} />
          case 'table':
            return (
              <div
                key={i}
                className={cn('overflow-x-auto', blockAnim)}
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content) }}
              />
            )
          default:
            return null
        }
      })}
    </div>
  )
})
