import { Fragment, useMemo, type ReactNode } from 'react'
import { marked, type Token, type Tokens } from 'marked'
import { GenerativeUI } from './generative-ui'
import { generativeLanguage } from '@/lib/generative-ui'
import { CodeBlockFrame } from './code-block-frame'

function renderTokens(tokens: Token[], allowGenerativeUI = false, live = false, depth = 0): ReactNode {
  if (depth > 24) return null
  return tokens.map((token, index) => {
    const children = 'tokens' in token && Array.isArray(token.tokens)
      ? renderTokens(token.tokens, allowGenerativeUI, live, depth + 1)
      : 'text' in token ? String(token.text) : ''
    switch (token.type) {
      case 'space': return null
      case 'text': return <Fragment key={index}>{children}</Fragment>
      case 'paragraph': return <p key={index} className="leading-relaxed">{children}</p>
      case 'heading': return <div key={index} role="heading" aria-level={Math.min(token.depth + 1, 6)} className="mt-5 text-lg font-semibold">{children}</div>
      case 'strong': return <strong key={index}>{children}</strong>
      case 'em': return <em key={index}>{children}</em>
      case 'del': return <del key={index}>{children}</del>
      case 'br': return <br key={index} />
      case 'hr': return <hr key={index} className="border-[var(--color-divider)]" />
      case 'codespan': return <code key={index} className="rounded bg-[var(--color-bg-muted)] px-1 py-0.5 font-mono text-[0.9em]">{token.text}</code>
      case 'code': if (allowGenerativeUI && generativeLanguage(token.lang)) return <GenerativeUI key={index} code={token.text} lang={generativeLanguage(token.lang)!} live={live} />; return <CodeBlockFrame key={index} code={token.text} lang={token.lang?.split(/\s+/)[0]} />
      case 'blockquote': return <blockquote key={index} className="space-y-3 rounded-[8px] bg-[var(--color-bg-muted)] px-4 py-3 text-[var(--color-fg-muted)]">{children}</blockquote>
      case 'list': {
        const items = (token as Tokens.List).items.map((item, itemIndex) => <li key={itemIndex}>{renderTokens(item.tokens, allowGenerativeUI, live, depth + 1)}</li>)
        return token.ordered
          ? <ol key={index} start={token.start || 1} className="list-decimal space-y-2 pl-6">{items}</ol>
          : <ul key={index} className="list-disc space-y-2 pl-6">{items}</ul>
      }
      case 'table': return (
        <div key={index} className="overflow-x-auto rounded-[10px]">
          <table className="quiet-table w-full border-collapse text-sm">
            <thead className="bg-[var(--color-bg-muted)]"><tr>{(token as Tokens.Table).header.map((cell, cellIndex) => <th key={cellIndex} className="px-3 py-2 text-left">{renderTokens(cell.tokens, allowGenerativeUI, live, depth + 1)}</th>)}</tr></thead>
            <tbody>{(token as Tokens.Table).rows.map((row, rowIndex) => <tr key={rowIndex}>{row.map((cell, cellIndex) => <td key={cellIndex} className="px-3 py-2 align-top">{renderTokens(cell.tokens, allowGenerativeUI, live, depth + 1)}</td>)}</tr>)}</tbody>
          </table>
        </div>
      )
      case 'link': return /^https?:\/\//i.test(token.href)
        ? <a key={index} href={token.href} target="_blank" rel="noreferrer noopener" className="text-[var(--color-accent)] underline underline-offset-4">{children}</a>
        : <span key={index}>{children}</span>
      case 'image': return <span key={index} className="text-[var(--color-fg-muted)]">[{token.text}]</span>
      default: return <span key={index}>{children}</span>
    }
  })
}

export function PrivateMarkdown({ text, allowGenerativeUI = false, live = false }: { text: string; allowGenerativeUI?: boolean; live?: boolean }) {
  const content = useMemo(() => renderTokens(marked.lexer(text), allowGenerativeUI, live), [text, allowGenerativeUI, live])
  return <div className="min-w-0 space-y-3 break-words text-[0.9375rem] [overflow-wrap:anywhere]">{content}</div>
}
