import { Check, Copy } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useCopy } from '@/hooks/use-clipboard'
import { CodeAction } from './code-action'
import { CodeBlockFrame } from './code-block-frame'

export function PlainCodeBlock({ code, lang }: { code: string; lang?: string }) {
  const { t } = useTranslation('chat')
  const { copied, copy } = useCopy()
  return (
    <CodeBlockFrame code={code} lang={lang} copyAction={
      <CodeAction onClick={() => void copy(code)} label={copied ? t('actions.copied') : t('actions.copy')}>
        {copied ? <Check size={13} aria-hidden /> : <Copy size={13} aria-hidden />}
      </CodeAction>
    } />
  )
}
