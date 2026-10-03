import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { CodeXml, RotateCcw, WrapText } from 'lucide-react'
import { useCodeWrap } from '@/hooks/use-code-wrap'
import { codeLanguageLabel } from '@/lib/code-language-label'
import { cn } from '@/lib/utils'
import { CodeAction } from './code-action'

interface CodeBlockFrameProps {
  code: string
  lang?: string
  html?: string
  className?: string
  actions?: ReactNode
  copyAction?: ReactNode
  footer?: ReactNode
}

export function CodeBlockFrame({ code, lang, html, className, actions, copyAction, footer }: CodeBlockFrameProps) {
  const { t } = useTranslation('chat')
  const wrap = useCodeWrap()
  return (
    <div className={cn(
      'group/code relative isolate my-3.5 min-w-0 max-w-full overflow-clip not-italic',
      'rounded-[14px] border border-[var(--color-border)]',
      'bg-[var(--color-code-bg)] text-[var(--color-code-fg)]',
      className,
    )}>
      <div
        data-code-toolbar
        className={cn(
          'sticky z-[var(--z-sticky)] flex h-10 min-w-0 items-center justify-between gap-2 px-4',
          'border-b border-[var(--color-border-subtle)] bg-[var(--color-code-bg)]',
          'max-sm:h-[var(--tap-min)] max-sm:px-3',
        )}
        style={{ top: 'var(--code-toolbar-sticky-top, 0px)' }}
      >
        <span className="inline-flex min-w-0 items-center gap-1.5 text-[12.5px] font-medium text-[var(--color-fg-muted)]">
          <CodeXml size={14} strokeWidth={1.5} aria-hidden className="shrink-0 text-[var(--color-fg-subtle)]" />
          <span className="truncate">{codeLanguageLabel(lang)}</span>
        </span>
        <div className="flex shrink-0 items-center gap-0.5">
          {actions}
          <CodeAction onClick={wrap.toggle} label={t('code.wrap')} tooltip={t(wrap.enabled ? 'code.disableWrap' : 'code.enableWrap')} pressed={wrap.enabled}>
            <WrapText size={14} aria-hidden />
          </CodeAction>
          {wrap.overridden ? (
            <CodeAction onClick={wrap.reset} label={t('code.resetWrap')}>
              <RotateCcw size={13} aria-hidden />
            </CodeAction>
          ) : null}
          {copyAction}
        </div>
      </div>
      <pre data-code-body data-wrap={wrap.enabled ? 'true' : 'false'} className="overflow-x-auto px-4 pb-4 pt-1 text-[13px] leading-[1.65]">
        {html === undefined
          ? <code className="font-mono">{code}</code>
          : <code className="font-mono" dangerouslySetInnerHTML={{ __html: html }} />}
      </pre>
      {footer}
    </div>
  )
}
