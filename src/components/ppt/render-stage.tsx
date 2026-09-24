import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Presentation } from 'lucide-react'
import { aipptApi } from '@/api'
import type { ApiAiPPTTemplate } from '@/api/types'

interface RenderStageProps {
  template: ApiAiPPTTemplate | null
  subject: string
}

export function RenderStage({ template, subject }: RenderStageProps) {
  const { t } = useTranslation('ppt')
  const [brokenCover, setBrokenCover] = useState(false)

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex min-h-full max-w-md flex-col items-center justify-center py-8 text-center">
        <div className="mb-7 aspect-video w-full max-w-xs overflow-hidden rounded-[10px] border border-[var(--color-border)] bg-[var(--color-bg-muted)]">
          {template?.coverUrl && !brokenCover ? (
            <img
              src={aipptApi.resourceUrl(template.coverUrl)}
              alt={template.name}
              onError={() => setBrokenCover(true)}
              className="h-full w-full object-contain"
            />
          ) : (
            <div className="flex h-full items-center justify-center text-[var(--color-fg-muted)]">
              <Presentation size={32} strokeWidth={1.5} aria-hidden />
            </div>
          )}
        </div>
        <div role="status" aria-live="polite" className="w-full">
          <h2 className="break-words text-lg font-semibold leading-snug text-[var(--color-fg)]">
            {t('render.title', { subject: subject || t('result.untitled') })}
          </h2>
          <p className="mt-3 text-sm leading-6 text-[var(--color-fg-muted)]">{t('render.lead')}</p>
          <div className="mt-5 inline-flex items-center gap-2 text-xs text-[var(--color-fg-muted)]">
            <span aria-hidden className="size-2 rounded-full bg-[var(--color-secondary)] motion-safe:animate-pulse" />
            {t('render.waiting')}
          </div>
        </div>
      </div>
    </div>
  )
}
