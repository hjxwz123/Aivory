import katex from 'katex'
import { memo, useMemo } from 'react'
import { splitMathContent } from '@/lib/math-content'
import { cn } from '@/lib/utils'

interface MathTextProps {
  content: string
  className?: string
}

export const MathText = memo(function MathText({ content, className }: MathTextProps) {
  const segments = useMemo(
    () => {
      const parsed = splitMathContent(content)
      return parsed.map((segment, index) => {
        if (segment.type === 'text') {
          const blockBefore = parsed[index - 1]?.type === 'block-math'
          const blockAfter = parsed[index + 1]?.type === 'block-math'
          const onlyLineBreaks = /^(?:\r?\n)+$/.test(segment.value)
          let value = segment.value

          // A block element already supplies the line break represented by the
          // serialized separator. Consume only that break, preserving extras.
          if (blockBefore) value = value.replace(/^\r?\n/, '')
          if (blockAfter && !(blockBefore && onlyLineBreaks)) {
            value = value.replace(/\r?\n$/, '')
          }
          return { ...segment, value }
        }
        try {
          return {
            ...segment,
            html: katex.renderToString(segment.value, {
              displayMode: segment.type === 'block-math',
              throwOnError: false,
              strict: false,
            }),
          }
        } catch {
          // KaTeX can throw non-parse errors (for example a RangeError from an
          // adversarially deep expression). Keep the message renderable.
          return { ...segment, html: null }
        }
      })
    },
    [content],
  )

  return (
    <div className={cn('min-w-0 whitespace-pre-wrap break-words', className)}>
      {segments.map((segment, index) => {
        if (segment.type === 'text') {
          return segment.value ? <span key={index}>{segment.value}</span> : null
        }
        if (!segment.html) return <span key={index}>{segment.raw}</span>
        if (segment.type === 'block-math') {
          return (
            <div
              key={index}
              className="math-text-block"
              dangerouslySetInnerHTML={{ __html: segment.html }}
            />
          )
        }
        return (
          <span
            key={index}
            className="inline-block max-w-full overflow-x-auto align-middle"
            dangerouslySetInnerHTML={{ __html: segment.html }}
          />
        )
      })}
    </div>
  )
})
