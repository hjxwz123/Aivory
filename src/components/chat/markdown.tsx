import { memo, useDeferredValue, useEffect, useMemo, useState } from 'react'
import { tokenizeMarkdown, inlineMarkdownToHtml } from '@/lib/markdown'
import { CodeBlock } from './code-block'
import { cn } from '@/lib/utils'

interface MarkdownProps {
  content: string
  className?: string
}

function HeadingTag({ depth, html }: { depth: number; html: string }) {
  const props = { className: 'font-serif tracking-tight text-[var(--color-fg)]' as string }
  const inner = <span dangerouslySetInnerHTML={{ __html: html }} />
  switch (depth) {
    case 1:
      return <h1 className={cn(props.className, 'text-2xl mt-6')}>{inner}</h1>
    case 2:
      return <h2 className={cn(props.className, 'text-xl mt-6')}>{inner}</h2>
    case 3:
      return <h3 className={cn(props.className, 'text-lg mt-5')}>{inner}</h3>
    case 4:
      return <h4 className="font-sans font-semibold text-base mt-4 text-[var(--color-fg)]">{inner}</h4>
    default:
      return <h5 className="font-sans font-semibold text-sm mt-3 text-[var(--color-fg)]">{inner}</h5>
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

export const Markdown = memo(function Markdown({ content, className }: MarkdownProps) {
  const throttled = useThrottledContent(content)
  const blocks = useMemo(() => tokenizeMarkdown(throttled), [throttled])
  if (!content) return null

  return (
    <div className={cn('prose-aurelia', className)}>
      {blocks.map((b, i) => {
        switch (b.type) {
          case 'heading':
            return <HeadingTag key={i} depth={b.depth ?? 2} html={inlineMarkdownToHtml(b.content)} />
          case 'paragraph':
            return (
              <p
                key={i}
                className="leading-relaxed text-[var(--color-fg)]"
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
                )}
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content) }}
              />
            )
          case 'code':
            return <CodeBlock key={i} code={b.content} lang={b.lang} />
          case 'blockquote':
            return (
              <blockquote
                key={i}
                className="border-l-2 border-[var(--color-border-strong)] pl-4 text-[var(--color-fg-muted)] italic"
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content) }}
              />
            )
          case 'hr':
            return <hr key={i} className="my-6 border-[var(--color-divider)]" />
          case 'table':
            return (
              <div
                key={i}
                className="overflow-x-auto"
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
