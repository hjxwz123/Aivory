import { Fragment, useMemo, type ReactNode } from 'react'
import { marked, type Token, type Tokens } from 'marked'

function renderTokens(tokens: Token[], depth = 0): ReactNode {
  if (depth > 24) return null
  return tokens.map((token, index) => {
    const children = 'tokens' in token && Array.isArray(token.tokens)
      ? renderTokens(token.tokens, depth + 1)
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
      case 'code': return <pre key={index} className="overflow-x-auto rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)] p-4 text-sm"><code>{token.text}</code></pre>
      case 'blockquote': return <blockquote key={index} className="space-y-3 border-l-2 border-[var(--color-border-strong)] pl-4 text-[var(--color-fg-muted)]">{children}</blockquote>
      case 'list': {
        const items = (token as Tokens.List).items.map((item, itemIndex) => <li key={itemIndex}>{renderTokens(item.tokens, depth + 1)}</li>)
        return token.ordered
          ? <ol key={index} start={token.start || 1} className="list-decimal space-y-2 pl-6">{items}</ol>
          : <ul key={index} className="list-disc space-y-2 pl-6">{items}</ul>
      }
      case 'table': return (
        <div key={index} className="overflow-x-auto rounded-[10px] border border-[var(--color-border)]">
          <table className="w-full border-collapse text-sm">
            <thead className="bg-[var(--color-bg-muted)]"><tr>{(token as Tokens.Table).header.map((cell, cellIndex) => <th key={cellIndex} className="border-b border-[var(--color-border)] px-3 py-2 text-left">{renderTokens(cell.tokens, depth + 1)}</th>)}</tr></thead>
            <tbody>{(token as Tokens.Table).rows.map((row, rowIndex) => <tr key={rowIndex}>{row.map((cell, cellIndex) => <td key={cellIndex} className="border-b border-[var(--color-border-subtle)] px-3 py-2 align-top">{renderTokens(cell.tokens, depth + 1)}</td>)}</tr>)}</tbody>
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

export function PrivateMarkdown({ text }: { text: string }) {
  const content = useMemo(() => renderTokens(marked.lexer(text)), [text])
  return <div className="min-w-0 space-y-3 break-words text-[0.9375rem] [overflow-wrap:anywhere]">{content}</div>
}
