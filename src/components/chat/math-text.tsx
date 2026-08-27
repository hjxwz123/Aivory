import { memo, useMemo } from 'react'
import { splitMathContent } from '@/lib/math-content'
import { renderMathToHtml } from '@/lib/markdown'
import { cn } from '@/lib/utils'
import { useMathCopy } from '@/hooks/use-math-copy'

interface MathTextProps {
  content: string
  className?: string
}

export const MathText = memo(function MathText({ content, className }: MathTextProps) {
  const { announcement, handleMathCopy, labels } = useMathCopy()
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
        return {
          ...segment,
          html: renderMathToHtml(segment.value, segment.type === 'block-math', labels),
        }
      })
    },
    [content, labels],
  )

  return (
    <div
      className={cn('min-w-0 whitespace-pre-wrap break-words', className)}
      onClick={handleMathCopy}
    >
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
      <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">
        {announcement}
      </span>
    </div>
  )
})
