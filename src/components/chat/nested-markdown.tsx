import { useMemo, type ReactNode } from 'react'
import { marked, type Token, type Tokens } from 'marked'
import { blockMarkdownToHtml, inlineMarkdownToHtml, type CiteRef, type MathCopyLabels } from '@/lib/markdown'
import { GenerativeUI } from './generative-ui'
import { generativeLanguage } from '@/lib/generative-ui'
import { PlainCodeBlock } from './plain-code-block'

interface NestedMarkdownProps {
  allowGenerativeUI?: boolean
  live?: boolean
  content: string
  pathPrefix: string
  cites: CiteRef[]
  breaks: boolean
  mathCopyLabels: MathCopyLabels
}

function renderTokens(tokens: Token[], options: NestedMarkdownProps, path: string, depth = 0): ReactNode {
  if (depth > 24) return null
  return tokens.map((token, index) => {
    const key = `${path}/${index}`
    switch (token.type) {
      case 'space': return null
      case 'code': if (options.allowGenerativeUI && generativeLanguage(token.lang)) return <GenerativeUI key={key} code={token.text} lang={generativeLanguage(token.lang)!} live={options.live} />; return <PlainCodeBlock key={key} code={token.text} lang={token.lang?.split(/\s+/)[0]} />
      case 'list': {
        const list = token as Tokens.List
        const items = list.items.map((item, itemIndex) => (
          <li key={`${key}/${itemIndex}`}>
            {item.task ? <input type="checkbox" checked={Boolean(item.checked)} readOnly disabled className="mr-2" /> : null}
            {renderTokens(item.tokens, options, `${key}/${itemIndex}`, depth + 1)}
          </li>
        ))
        return list.ordered
          ? <ol key={key} start={Number(list.start) || 1}>{items}</ol>
          : <ul key={key}>{items}</ul>
      }
      case 'blockquote': return (
        <blockquote key={key} className="rounded-[8px] bg-[var(--color-bg-muted)] px-4 py-3 text-[var(--color-fg-muted)]">
          {renderTokens(token.tokens ?? [], options, key, depth + 1)}
        </blockquote>
      )
      case 'paragraph': return <p key={key} dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(token.text, options.cites, options.breaks, options.mathCopyLabels) }} />
      case 'text': return <span key={key} dangerouslySetInnerHTML={{ __html: inlineMarkdownToHtml(token.text, options.cites, options.breaks, options.mathCopyLabels) }} />
      default: return <div key={key} dangerouslySetInnerHTML={{ __html: blockMarkdownToHtml(token.raw, options.cites, options.breaks, options.mathCopyLabels) }} />
    }
  })
}

export function NestedMarkdown(options: NestedMarkdownProps) {
  const tokens = useMemo(() => marked.lexer(options.content, { gfm: true, breaks: options.breaks }), [options.content, options.breaks])
  return <>{renderTokens(tokens, options, options.pathPrefix)}</>
}
