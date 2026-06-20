import { memo, useDeferredValue, useEffect, useMemo, useState } from 'react'
import { tokenizeMarkdown, inlineMarkdownToHtml, blockMarkdownToHtml, type CiteRef } from '@/lib/markdown'
import { CodeBlock } from './code-block'
import { MermaidDiagram } from './mermaid-diagram'
import { cn, safeHref } from '@/lib/utils'
import type { Citation } from '@/types/chat'

interface MarkdownProps {
  content: string
  className?: string
  /** True while the owning message is still streaming (drives live HTML preview). */
  live?: boolean
  /** Stable prefix (message id) so code blocks keep their identity across remounts. */
  blockKeyPrefix?: string
  /** Citations for this turn — inline `[n]` markers become source links. */
  citations?: Citation[]
}

function HeadingTag({ depth, html, className }: { depth: number; html: string; className?: string }) {
  const base = 'font-sans font-semibold tracking-tight text-[var(--color-fg)]'
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

export const Markdown = memo(function Markdown({ content, className, live = false, blockKeyPrefix, citations }: MarkdownProps) {
  const throttled = useThrottledContent(content)
  const blocks = useMemo(() => tokenizeMarkdown(throttled), [throttled])
  // Map citations to the lib's CiteRef shape once; `inline`/`block` helpers thread
  // them into the HTML so `[n]` markers become source links.
  const cites = useMemo<CiteRef[]>(
    () =>
      (citations ?? []).map((c) => ({
        index: c.index,
        url: c.url,
        title: c.title,
        domain: c.domain,
        isDoc: c.source === 'kb' || c.url.trim().toLowerCase().startsWith('doc:') || !safeHref(c.url),
      })),
    [citations],
  )
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
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content, cites) }}
              />
            )
          case 'list':
          case 'ordered-list':
            // Block-parse so `- item` / `1. item` become real <ul>/<ol><li>
            // markup with line breaks — parseInline would leave the dashes as
            // literal inline text on one line. The sanitizer strips classes off
            // the list elements, so bullets/spacing are applied via the wrapper.
            return (
              <div
                key={i}
                className={cn(
                  'my-3 text-[var(--color-fg)] leading-relaxed',
                  '[&_ul]:list-disc [&_ol]:list-decimal [&_ul]:pl-5 [&_ol]:pl-5',
                  '[&_ul]:my-1 [&_ol]:my-1 [&_li]:my-1 [&_li]:pl-0.5',
                  '[&_ul_ul]:list-[circle] [&_ul_ul]:my-0.5 [&_ol_ol]:my-0.5 [&_li_p]:my-0',
                  blockAnim,
                )}
                dangerouslySetInnerHTML={{ __html: blockMarkdownToHtml(b.content, cites) }}
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
                dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(b.content, cites) }}
              />
            )
          case 'math':
            // Display math, pre-rendered by KaTeX in tokenizeMarkdown (trusted
            // output). Scrolls horizontally on small screens for wide formulas.
            return (
              <div
                key={i}
                className={cn('my-3 overflow-x-auto', blockAnim)}
                dangerouslySetInnerHTML={{ __html: b.content }}
              />
            )
          case 'hr':
            return <hr key={i} className={cn('my-6 border-[var(--color-divider)]', blockAnim)} />
          case 'table':
            // Block-parse so GFM pipe tables become real <table> markup. The
            // sanitizer strips classes off table elements, so styling is applied
            // from the wrapper via arbitrary child selectors.
            return (
              <div
                key={i}
                className={cn(
                  'my-4 overflow-x-auto rounded-[10px] border border-[var(--color-border)]',
                  '[&_table]:w-full [&_table]:border-collapse [&_table]:text-sm',
                  '[&_thead]:bg-[var(--color-bg-muted)]',
                  '[&_th]:px-3 [&_th]:py-2 [&_th]:text-left [&_th]:font-semibold [&_th]:text-[var(--color-fg)] [&_th]:border-b [&_th]:border-[var(--color-border)]',
                  '[&_td]:px-3 [&_td]:py-2 [&_td]:align-top [&_td]:text-[var(--color-fg-muted)] [&_td]:border-b [&_td]:border-[var(--color-divider)]',
                  '[&_tr:last-child_td]:border-b-0',
                  blockAnim,
                )}
                dangerouslySetInnerHTML={{ __html: blockMarkdownToHtml(b.content, cites) }}
              />
            )
          default:
            return null
        }
      })}
    </div>
  )
})
